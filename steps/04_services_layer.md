# Step 4 — Services Layer (Mock Downstreams)

> The circuit breaker is the *seatbelt*. These services are the *road* — sometimes smooth, sometimes full of potholes. We control the potholes to demo the breaker.

---

## Goal

By the end of this step you have a `services` package with three mock services that:

- Each implement a clean interface (so they can be swapped for real DB-backed versions later)
- Expose failure-injection helpers: `Break()` / `Repair()` / `SetFailRate()`
- Simulate realistic I/O latency
- Are goroutine-safe (atomic flags, no locks on the hot path)

---

## 1. Why an interface?

In Go, the consumer defines the interface. The `gateway` package only needs "something that processes a payment" — it doesn't care whether that's an HTTP call, a DB transaction, or a mock. Interfaces let us:

- **Test without real infrastructure** — the gateway can be tested with mock impls
- **Swap implementations** — today the mock, tomorrow a real Postgres-backed service (running in Docker, of course)
- **Fail realistically** — the mock can inject failures deterministically; a real service can't

```
┌──────────────┐         ┌──────────────────────────┐
│   Gateway    │────────►│  PaymentServicer (iface) │
└──────────────┘         └──────────────┬───────────┘
                                        │ implements
                                        ▼
                          ┌──────────────────────────┐
                          │  MockPaymentService      │   ← this step
                          └──────────────────────────┘
                          ┌──────────────────────────┐
                          │  PostgresPaymentService  │   ← later, in Step 5 / 12
                          └──────────────────────────┘
```

---

## 2. The interfaces

Create `services/interfaces.go`:

```go
// Package services defines the downstream service contracts and provides
// mock implementations with controllable failure modes. Real implementations
// (DB-backed) live in separate packages and satisfy these same interfaces.
package services

import "context"

// UserProfile is the read model returned by the user service.
type UserProfile struct {
    ID    int
    Name  string
    Email string
    Plan  string // "free" | "premium" | "enterprise"
}

// PaymentServicer — anything that can process a payment.
type PaymentServicer interface {
    ProcessPayment(ctx context.Context, userID int, amount float64, currency string) error
}

// RecommendationServicer — anything that can serve personalised recs.
type RecommendationServicer interface {
    GetRecommendations(ctx context.Context, userID int) ([]string, error)
}

// UserServicer — anything that can look up a user profile.
type UserServicer interface {
    GetUser(ctx context.Context, userID int) (*UserProfile, error)
}
```

---

## 3. PaymentService — hard failure mode

Create `services/payment.go`:

```go
package services

import (
    "context"
    "errors"
    "sync/atomic"
    "time"
)

// PaymentService is a mock payment processor.
//
// It has a single "healthy" flag controlled by Break()/Repair(). When broken,
// every ProcessPayment returns an error after a small latency — simulating
// a DB that is reachable but refusing writes. This is the cleanest failure
// mode to demo against: flip the switch, watch the breaker trip.
type PaymentService struct {
    healthy int32         // 1 = up, 0 = down; atomic, no lock needed
    latency time.Duration // simulated I/O
}

func NewPaymentService() *PaymentService {
    return &PaymentService{
        healthy: 1,
        latency: 50 * time.Millisecond,
    }
}

func (s *PaymentService) Break()  { atomic.StoreInt32(&s.healthy, 0) }
func (s *PaymentService) Repair() { atomic.StoreInt32(&s.healthy, 1) }

// IsHealthy is exposed for tests and debug endpoints. Not part of the
// PaymentServicer interface — the gateway never asks, it just calls.
func (s *PaymentService) IsHealthy() bool {
    return atomic.LoadInt32(&s.healthy) == 1
}

// ProcessPayment is the interface-satisfying method the gateway calls.
func (s *PaymentService) ProcessPayment(ctx context.Context, userID int, amount float64, currency string) error {
    // Respect context cancellation first — the breaker wraps this call
    // with its own timeouts, so ctx.Err() == the caller gave up.
    select {
    case <-ctx.Done():
        return ctx.Err()
    case <-time.After(s.latency):
    }

    if !s.IsHealthy() {
        return errors.New("payment service: database unavailable")
    }
    return nil
}
```

### Why `int32` + `atomic`?

Because `Break()` might be called from one goroutine (a test harness, an admin HTTP handler) while `ProcessPayment` runs in another. `atomic.StoreInt32` / `atomic.LoadInt32` gives us a lock-free flag with happens-before semantics. Cheaper than a mutex, and impossible to deadlock.

---

## 4. RecommendationService — probabilistic failure

The reco service models ML degradation. It doesn't go fully "down" — it just starts returning errors at some rate.

Create `services/recommendation.go`:

```go
package services

import (
    "context"
    "errors"
    "math/rand"
    "sync"
    "time"
)

// RecommendationService simulates an ML model that becomes less reliable
// under load (say, the model server is OOM-ing on some shards). We control
// the failure rate with SetFailRate.
type RecommendationService struct {
    mu       sync.RWMutex
    failRate float64 // 0.0 = always succeed, 1.0 = always fail
    latency  time.Duration
    rng      *rand.Rand
}

func NewRecommendationService() *RecommendationService {
    return &RecommendationService{
        failRate: 0.0,
        latency:  80 * time.Millisecond,
        rng:      rand.New(rand.NewSource(time.Now().UnixNano())),
    }
}

func (s *RecommendationService) SetFailRate(r float64) {
    if r < 0 {
        r = 0
    }
    if r > 1 {
        r = 1
    }
    s.mu.Lock()
    s.failRate = r
    s.mu.Unlock()
}

func (s *RecommendationService) GetRecommendations(ctx context.Context, userID int) ([]string, error) {
    select {
    case <-ctx.Done():
        return nil, ctx.Err()
    case <-time.After(s.latency):
    }

    s.mu.RLock()
    shouldFail := s.rng.Float64() < s.failRate
    s.mu.RUnlock()

    if shouldFail {
        return nil, errors.New("recommendation service: model timeout")
    }

    return []string{
        "Stranger Things",
        "Breaking Bad",
        "The Crown",
        "Black Mirror",
    }, nil
}
```

### Why `sync.RWMutex` here but atomic in PaymentService?

The reco service stores a `float64` (the fail rate) which can't be atomically loaded without `atomic.LoadUint64` + bit casts. The rand `Source` is also not goroutine-safe. A read-write mutex with read-heavy access is clean and cheap.

---

## 5. UserService — simple pass-through with a break flag

Create `services/user.go`:

```go
package services

import (
    "context"
    "errors"
    "fmt"
    "sync/atomic"
    "time"
)

type UserService struct {
    healthy int32
    latency time.Duration
}

func NewUserService() *UserService {
    return &UserService{healthy: 1, latency: 30 * time.Millisecond}
}

func (s *UserService) Break()  { atomic.StoreInt32(&s.healthy, 0) }
func (s *UserService) Repair() { atomic.StoreInt32(&s.healthy, 1) }

func (s *UserService) GetUser(ctx context.Context, userID int) (*UserProfile, error) {
    select {
    case <-ctx.Done():
        return nil, ctx.Err()
    case <-time.After(s.latency):
    }

    if atomic.LoadInt32(&s.healthy) == 0 {
        return nil, errors.New("user service: auth provider unreachable")
    }

    return &UserProfile{
        ID:    userID,
        Name:  fmt.Sprintf("User %d", userID),
        Email: fmt.Sprintf("user%d@example.com", userID),
        Plan:  "premium",
    }, nil
}
```

---

## 6. Compile-time interface checks

At the bottom of each service file, add a blank assignment to guarantee the interface is satisfied. If someone renames a method, the build breaks instantly — not at runtime.

```go
// In payment.go
var _ PaymentServicer = (*PaymentService)(nil)

// In recommendation.go
var _ RecommendationServicer = (*RecommendationService)(nil)

// In user.go
var _ UserServicer = (*UserService)(nil)
```

This is a standard Go idiom. The `_` discards the value; the compiler still checks the assignment.

---

## 7. Quick smoke test

Create `services/services_smoke_test.go`:

```go
package services

import (
    "context"
    "testing"
)

func TestPaymentBreakRepair(t *testing.T) {
    s := NewPaymentService()
    ctx := context.Background()

    if err := s.ProcessPayment(ctx, 1, 9.99, "USD"); err != nil {
        t.Fatalf("healthy service should succeed, got %v", err)
    }

    s.Break()
    if err := s.ProcessPayment(ctx, 1, 9.99, "USD"); err == nil {
        t.Fatal("broken service should fail")
    }

    s.Repair()
    if err := s.ProcessPayment(ctx, 1, 9.99, "USD"); err != nil {
        t.Fatalf("repaired service should succeed, got %v", err)
    }
}
```

Run:

```bash
go test ./services/... -race
```

---

## 8. Sanity checklist

- [ ] `go build ./services/...` compiles
- [ ] The three `var _ …Servicer = …` compile-time checks pass
- [ ] `go test ./services/... -race` is green
- [ ] No service spawns a background goroutine in its constructor

---

## What's next

**Step 5 — Repository Layer.** Services handle *behaviour*; repositories handle *persistence*. We'll define repository interfaces + in-memory implementations (the Postgres-backed version comes in Step 12 with Docker).
