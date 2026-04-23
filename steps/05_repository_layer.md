# Step 5 — Repository Layer

> Persistence is a separate concern from business logic. Repositories are the seam between them.

---

## Goal

By the end of this step you have a `repository` package with:

- Clean interfaces (`PaymentRepository`, `UserRepository`) that business logic depends on
- An in-memory implementation under `repository/memory/` for dev + tests
- A placeholder for a future Postgres implementation (Docker — we'll wire it in Step 12)
- Proper goroutine safety (`sync.RWMutex` for concurrent reads)

---

## 1. What is the repository pattern?

A **repository** is a narrow interface that hides whatever storage technology you use. The business layer (gateway / services) calls `paymentRepo.Create(...)` and has no idea whether that ended up in Postgres, in Redis, in memory, or in a log file.

Benefits:

- **Swap storage without touching business code.** Memory today, Postgres tomorrow, DynamoDB the day after. The gateway never changes.
- **Test without infrastructure.** Tests use the in-memory repo; CI stays fast.
- **Circuit breakers can wrap repositories too.** If your Postgres instance itself becomes unreliable, wrap `paymentRepo.Create` in a `dbCB.Execute(...)` call. The pattern composes cleanly.

---

## 2. Domain types

Create `repository/models.go`:

```go
// Package repository defines persistence contracts and domain models.
// Implementations live in subpackages: repository/memory, repository/postgres, etc.
package repository

import "time"

// PaymentRecord is the persisted form of a processed payment.
type PaymentRecord struct {
    ID        string    `db:"id"`
    UserID    int       `db:"user_id"`
    Amount    float64   `db:"amount"`
    Currency  string    `db:"currency"`
    Status    string    `db:"status"` // "pending" | "settled" | "failed"
    CreatedAt time.Time `db:"created_at"`
    UpdatedAt time.Time `db:"updated_at"`
}

// UserProfile is the read model for the user service's persistence layer.
// Note: services.UserProfile is the wire model; this one includes DB-only
// fields. Duplication is intentional — wire and persistence evolve at
// different rates.
type UserProfile struct {
    ID        int       `db:"id"`
    Name      string    `db:"name"`
    Email     string    `db:"email"`
    Plan      string    `db:"plan"`
    CreatedAt time.Time `db:"created_at"`
}
```

---

## 3. The interfaces

Create `repository/interface.go`:

```go
package repository

import "context"

// PaymentRepository — all persistence for payments.
type PaymentRepository interface {
    Create(ctx context.Context, rec *PaymentRecord) error
    GetByID(ctx context.Context, id string) (*PaymentRecord, error)
    ListByUser(ctx context.Context, userID int, limit int) ([]*PaymentRecord, error)
    UpdateStatus(ctx context.Context, id string, status string) error
}

// UserRepository — all persistence for user profiles.
type UserRepository interface {
    GetByID(ctx context.Context, id int) (*UserProfile, error)
    GetByEmail(ctx context.Context, email string) (*UserProfile, error)
}
```

### Keep interfaces small

Each interface lists only the methods the consumer actually uses. Don't define a 20-method `PaymentRepository` "just in case". If a future feature needs `GetByTransactionID`, add it then. Small interfaces make mocks trivial and prevent accidental coupling.

---

## 4. In-memory PaymentRepo

Create `repository/memory/payment_repo.go`:

```go
// Package memory provides in-memory implementations of the repository
// interfaces. Used for tests and local dev. Data is lost on process restart.
package memory

import (
    "context"
    "fmt"
    "sync"

    "circuit-breaker-demo/repository"
)

// PaymentRepo is a thread-safe map-backed PaymentRepository.
type PaymentRepo struct {
    mu      sync.RWMutex
    records map[string]*repository.PaymentRecord
}

func NewPaymentRepo() *PaymentRepo {
    return &PaymentRepo{records: make(map[string]*repository.PaymentRecord)}
}

func (r *PaymentRepo) Create(ctx context.Context, rec *repository.PaymentRecord) error {
    if err := ctx.Err(); err != nil {
        return err
    }
    r.mu.Lock()
    defer r.mu.Unlock()
    if _, exists := r.records[rec.ID]; exists {
        return fmt.Errorf("payment %s already exists", rec.ID)
    }
    // Store a copy to prevent external mutation of our state.
    cp := *rec
    r.records[rec.ID] = &cp
    return nil
}

func (r *PaymentRepo) GetByID(ctx context.Context, id string) (*repository.PaymentRecord, error) {
    if err := ctx.Err(); err != nil {
        return nil, err
    }
    r.mu.RLock()
    defer r.mu.RUnlock()
    rec, ok := r.records[id]
    if !ok {
        return nil, fmt.Errorf("payment %s not found", id)
    }
    cp := *rec
    return &cp, nil
}

func (r *PaymentRepo) ListByUser(ctx context.Context, userID int, limit int) ([]*repository.PaymentRecord, error) {
    if err := ctx.Err(); err != nil {
        return nil, err
    }
    r.mu.RLock()
    defer r.mu.RUnlock()

    out := make([]*repository.PaymentRecord, 0, limit)
    for _, rec := range r.records {
        if rec.UserID == userID {
            cp := *rec
            out = append(out, &cp)
            if len(out) >= limit {
                break
            }
        }
    }
    return out, nil
}

func (r *PaymentRepo) UpdateStatus(ctx context.Context, id string, status string) error {
    if err := ctx.Err(); err != nil {
        return err
    }
    r.mu.Lock()
    defer r.mu.Unlock()
    rec, ok := r.records[id]
    if !ok {
        return fmt.Errorf("payment %s not found", id)
    }
    rec.Status = status
    return nil
}

// Compile-time interface check.
var _ repository.PaymentRepository = (*PaymentRepo)(nil)
```

### Why copy on read AND on write?

Because Go maps return pointers to the stored values. Without copying, a caller could do `rec := repo.GetByID(...); rec.Amount = 0` and silently corrupt our internal state. Copying on the boundary is a small cost for a meaningful invariant.

Note: this is *only* needed for the in-memory impl. A Postgres impl marshals through SQL driver buffers, so state corruption isn't possible.

---

## 5. In-memory UserRepo

Create `repository/memory/user_repo.go`:

```go
package memory

import (
    "context"
    "fmt"
    "sync"

    "circuit-breaker-demo/repository"
)

type UserRepo struct {
    mu       sync.RWMutex
    byID     map[int]*repository.UserProfile
    byEmail  map[string]*repository.UserProfile
}

func NewUserRepo() *UserRepo {
    return &UserRepo{
        byID:    make(map[int]*repository.UserProfile),
        byEmail: make(map[string]*repository.UserProfile),
    }
}

// Seed inserts a user. Useful in tests / main.go bootstrap.
func (r *UserRepo) Seed(u *repository.UserProfile) {
    r.mu.Lock()
    defer r.mu.Unlock()
    cp := *u
    r.byID[u.ID] = &cp
    r.byEmail[u.Email] = &cp
}

func (r *UserRepo) GetByID(ctx context.Context, id int) (*repository.UserProfile, error) {
    if err := ctx.Err(); err != nil {
        return nil, err
    }
    r.mu.RLock()
    defer r.mu.RUnlock()
    u, ok := r.byID[id]
    if !ok {
        return nil, fmt.Errorf("user %d not found", id)
    }
    cp := *u
    return &cp, nil
}

func (r *UserRepo) GetByEmail(ctx context.Context, email string) (*repository.UserProfile, error) {
    if err := ctx.Err(); err != nil {
        return nil, err
    }
    r.mu.RLock()
    defer r.mu.RUnlock()
    u, ok := r.byEmail[email]
    if !ok {
        return nil, fmt.Errorf("user %s not found", email)
    }
    cp := *u
    return &cp, nil
}

var _ repository.UserRepository = (*UserRepo)(nil)
```

---

## 6. Future: Postgres implementation (dockerized)

We won't write this yet — that's Step 12, after the full in-memory flow works end-to-end. But the plan:

```
repository/postgres/
├── payment_repo.go   # implements repository.PaymentRepository
├── user_repo.go      # implements repository.UserRepository
├── migrations/
│   └── 001_init.sql  # CREATE TABLE statements
└── conn.go           # pgxpool wiring, reads DSN from env
```

**Postgres will run exclusively in Docker** — via `docker compose up postgres`. There is no "install Postgres on your Mac" step in this project. Ever. A `docker-compose.yml` pins the version and wires the DSN so every dev, CI, and prod environment sees the same schema.

### Wrapping the repo in its own circuit breaker

Once we have a real DB, the DB itself is a downstream dependency that can degrade. We'll wrap repo calls in a dedicated `dbCB`:

```go
func (gw *Gateway) createPayment(ctx context.Context, rec *repository.PaymentRecord) error {
    return gw.dbCB.Execute(func() error {
        return gw.paymentRepo.Create(ctx, rec)
    })
}
```

The pattern composes: per-service breakers + a DB breaker. If Postgres dies, the DB breaker trips, the payment service starts returning errors, and the payment breaker trips too. Every layer protects the next.

---

## 7. Sanity checklist

- [ ] `go build ./repository/...` compiles
- [ ] Compile-time interface checks pass
- [ ] Tests (write a few — see Step 11) run under `-race` without warnings
- [ ] No caller outside `repository/memory` imports `sync` for repo access

---

## What's next

**Step 6 — API Gateway.** The gateway is the single wiring point. It owns one `CircuitBreaker` per service, exposes `Execute`-wrapped methods to the HTTP layer, and implements fallback patterns for graceful degradation.
