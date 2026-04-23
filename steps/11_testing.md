# Step 11 — Testing Strategy

> Unit tests pin down the state machine. Race tests prove the concurrency is real. Scenario tests mimic outages end-to-end.

---

## Goal

By the end of this step you have:

- Unit tests for every transition in `circuitbreaker/breaker_test.go`
- A concurrent stress test that must pass with `-race`
- Scenario tests that drive the full stack (services → gateway → handlers)
- A coverage baseline (`go test -coverprofile`) + a command to view it

---

## 1. The testing pyramid

```
            ▲
           /│\
          / │ \   Scenario / integration (few, slow, high signal)
         /──┼──\
        /   │   \   Gateway + handler (medium)
       /────┼────\
      /     │     \  Unit tests (many, fast, granular)
     /──────┴──────\
```

- **Unit tests** prove each layer in isolation. Fast, table-driven, many.
- **Gateway tests** verify the wiring between services and CBs. Use real CBs + in-memory services.
- **Scenario tests** simulate real outage flows (service breaks → breaker trips → traffic routes around → service repairs → breaker closes). Slow, few, most valuable for detecting regressions.

---

## 2. Unit tests — circuit breaker state machine

Create `circuitbreaker/breaker_test.go`:

```go
package circuitbreaker

import (
    "errors"
    "sync"
    "testing"
    "time"
)

// --- Helpers -----------------------------------------------------------------

func smallConfig() Config {
    return Config{
        Name: "test", FailureThreshold: 3,
        SuccessThreshold: 2, OpenTimeout: 100 * time.Millisecond,
        MaxHalfOpenProbes: 1,
    }
}

var errBoom = errors.New("boom")

func fail() error    { return errBoom }
func succeed() error { return nil }

// tripBreaker forces the CB to OPEN by driving FailureThreshold failures.
func tripBreaker(t *testing.T, cb *CircuitBreaker) {
    t.Helper()
    for i := 0; i < cb.config.FailureThreshold; i++ {
        _ = cb.Execute(fail)
    }
    if cb.State() != StateOpen {
        t.Fatalf("expected OPEN after %d failures, got %v", cb.config.FailureThreshold, cb.State())
    }
}

// --- Transition tests --------------------------------------------------------

func TestClosedToOpenOnThreshold(t *testing.T) {
    cb := New(smallConfig())

    for i := 0; i < 2; i++ {
        if err := cb.Execute(fail); !errors.Is(err, errBoom) {
            t.Fatalf("want errBoom, got %v", err)
        }
        if cb.State() != StateClosed {
            t.Fatalf("want CLOSED after %d failures, got %v", i+1, cb.State())
        }
    }

    _ = cb.Execute(fail) // 3rd failure — should trip
    if cb.State() != StateOpen {
        t.Fatalf("want OPEN after 3rd failure, got %v", cb.State())
    }
}

func TestOpenFastFails(t *testing.T) {
    cb := New(smallConfig())
    tripBreaker(t, cb)

    called := false
    err := cb.Execute(func() error { called = true; return nil })
    if called {
        t.Fatal("fn should NOT be called when OPEN")
    }
    if !errors.Is(err, ErrCircuitOpen) {
        t.Fatalf("want ErrCircuitOpen, got %v", err)
    }
}

func TestOpenToHalfOpenAfterTimeout(t *testing.T) {
    cb := New(smallConfig())
    tripBreaker(t, cb)

    time.Sleep(smallConfig().OpenTimeout + 20*time.Millisecond)

    called := false
    _ = cb.Execute(func() error { called = true; return nil })
    if !called {
        t.Fatal("fn should be called after timeout (HALF-OPEN probe)")
    }
}

func TestHalfOpenToClosedOnSuccesses(t *testing.T) {
    cb := New(smallConfig())
    tripBreaker(t, cb)
    time.Sleep(smallConfig().OpenTimeout + 20*time.Millisecond)

    // SuccessThreshold=2; two successes should close the circuit.
    if err := cb.Execute(succeed); err != nil {
        t.Fatalf("first probe: %v", err)
    }
    if err := cb.Execute(succeed); err != nil {
        t.Fatalf("second probe: %v", err)
    }
    if cb.State() != StateClosed {
        t.Fatalf("want CLOSED after 2 successful probes, got %v", cb.State())
    }
}

func TestHalfOpenToOpenOnFailure(t *testing.T) {
    cb := New(smallConfig())
    tripBreaker(t, cb)
    time.Sleep(smallConfig().OpenTimeout + 20*time.Millisecond)

    _ = cb.Execute(fail) // any failure in HALF-OPEN → OPEN
    if cb.State() != StateOpen {
        t.Fatalf("want OPEN after HALF-OPEN failure, got %v", cb.State())
    }
}

func TestHalfOpenRejectsExcessProbes(t *testing.T) {
    cfg := smallConfig()
    cfg.MaxHalfOpenProbes = 1
    cb := New(cfg)
    tripBreaker(t, cb)
    time.Sleep(cfg.OpenTimeout + 20*time.Millisecond)

    // Occupy the probe slot with a slow function to hold it open.
    start := make(chan struct{})
    done := make(chan struct{})
    go func() {
        _ = cb.Execute(func() error {
            close(start)
            <-done
            return nil
        })
    }()
    <-start

    err := cb.Execute(succeed)
    close(done)
    if !errors.Is(err, ErrTooManyProbes) {
        t.Fatalf("want ErrTooManyProbes, got %v", err)
    }
}
```

---

## 3. Metrics + hook tests

```go
func TestMetricsCounters(t *testing.T) {
    cb := New(smallConfig())
    _ = cb.Execute(succeed)
    _ = cb.Execute(fail)
    _ = cb.Execute(succeed)

    m := cb.Metrics()
    if m.TotalRequests != 3 {
        t.Fatalf("total: want 3, got %d", m.TotalRequests)
    }
    if m.Successes != 2 || m.Failures != 1 {
        t.Fatalf("want 2s/1f, got %d/%d", m.Successes, m.Failures)
    }
}

func TestOnStateChangeFiresAsync(t *testing.T) {
    cb := New(smallConfig())

    called := make(chan [2]State, 10)
    cb.OnStateChange(func(_ *CircuitBreaker, from, to State) {
        called <- [2]State{from, to}
    })

    tripBreaker(t, cb)

    select {
    case ft := <-called:
        if ft != [2]State{StateClosed, StateOpen} {
            t.Fatalf("want CLOSED→OPEN, got %v→%v", ft[0], ft[1])
        }
    case <-time.After(time.Second):
        t.Fatal("hook not fired within 1s")
    }
}
```

---

## 4. Concurrent / race test

```go
// TestConcurrentExecuteIsRaceFree MUST pass under go test -race.
// It doesn't assert much semantically — the value is that the race detector
// has zero tolerance for unsynchronised access.
func TestConcurrentExecuteIsRaceFree(t *testing.T) {
    cb := New(Config{
        Name: "race", FailureThreshold: 50,
        SuccessThreshold: 5, OpenTimeout: 10 * time.Millisecond,
        MaxHalfOpenProbes: 3,
    })

    const goroutines = 200
    const iters = 100

    var wg sync.WaitGroup
    wg.Add(goroutines)
    for i := 0; i < goroutines; i++ {
        go func(id int) {
            defer wg.Done()
            for j := 0; j < iters; j++ {
                // 90% succeed, 10% fail — realistic mix
                if (id+j)%10 == 0 {
                    _ = cb.Execute(fail)
                } else {
                    _ = cb.Execute(succeed)
                }
                _ = cb.State()
                _ = cb.Metrics()
            }
        }(i)
    }
    wg.Wait()
}
```

Run with the race detector:

```bash
go test -race ./circuitbreaker/...
```

Any warning about unsynchronised access is a bug — fix it before merging.

---

## 5. Gateway tests with a fake service

Create `gateway/gateway_test.go`:

```go
package gateway

import (
    "context"
    "errors"
    "testing"
    "time"

    "circuit-breaker-demo/circuitbreaker"
    "circuit-breaker-demo/config"
    "circuit-breaker-demo/services"
)

// fakePayment is a controllable PaymentServicer for gateway tests.
type fakePayment struct{ err error }

func (f *fakePayment) ProcessPayment(ctx context.Context, userID int, amount float64, currency string) error {
    return f.err
}

func testCfg() config.BreakersConfig {
    cb := config.BreakerConfig{
        FailureThreshold:  2,
        SuccessThreshold:  1,
        OpenTimeout:       50 * time.Millisecond,
        MaxHalfOpenProbes: 1,
    }
    return config.BreakersConfig{Payment: cb, Recommendation: cb, User: cb}
}

func TestGatewayTripsPaymentCBOnRepeatedFailures(t *testing.T) {
    fp := &fakePayment{err: errors.New("db down")}
    gw := New(fp, nil /* reco */, nil /* user */, testCfg(), nil)

    ctx := context.Background()

    // 2 failures → CB trips
    for i := 0; i < 2; i++ {
        _ = gw.ProcessPayment(ctx, 1, 1.0, "USD")
    }
    if gw.PaymentCB().State() != circuitbreaker.StateOpen {
        t.Fatal("want payment CB OPEN after 2 failures")
    }

    // Next call must be rejected without hitting the service
    fp.err = nil // even if the service is "healthy" now
    err := gw.ProcessPayment(ctx, 1, 1.0, "USD")
    if !IsCircuitError(err) {
        t.Fatalf("want circuit error, got %v", err)
    }
}

func TestIsolation_OneCBTripDoesNotAffectOthers(t *testing.T) {
    fp := &fakePayment{err: errors.New("db down")}
    gw := New(fp, services.NewRecommendationService(), services.NewUserService(), testCfg(), nil)

    for i := 0; i < 2; i++ {
        _ = gw.ProcessPayment(context.Background(), 1, 1.0, "USD")
    }

    if gw.RecoCB().State() != circuitbreaker.StateClosed {
        t.Fatal("reco CB must remain CLOSED when payment trips")
    }
    if gw.UserCB().State() != circuitbreaker.StateClosed {
        t.Fatal("user CB must remain CLOSED when payment trips")
    }
}
```

---

## 6. Scenario test (end-to-end)

Create `scenarios_test.go` at the project root — it exercises the full stack just like the demo script.

```go
package main

import (
    "context"
    "fmt"
    "testing"
    "time"

    "circuit-breaker-demo/circuitbreaker"
    "circuit-breaker-demo/config"
    "circuit-breaker-demo/gateway"
    "circuit-breaker-demo/services"
)

func tinyCfg() config.BreakersConfig {
    cb := config.BreakerConfig{
        FailureThreshold:  2,
        SuccessThreshold:  1,
        OpenTimeout:       80 * time.Millisecond,
        MaxHalfOpenProbes: 1,
    }
    return config.BreakersConfig{Payment: cb, Recommendation: cb, User: cb}
}

func TestScenario_OutageAndRecovery(t *testing.T) {
    paymentSvc := services.NewPaymentService()
    recoSvc := services.NewRecommendationService()
    userSvc := services.NewUserService()
    gw := gateway.New(paymentSvc, recoSvc, userSvc, tinyCfg(), nil)

    ctx := context.Background()

    // 1. Happy path
    if err := gw.ProcessPayment(ctx, 1, 1.0, "USD"); err != nil {
        t.Fatalf("step 1: %v", err)
    }

    // 2. Service goes down
    paymentSvc.Break()

    // 3. Two failures trip the breaker
    for i := 0; i < 2; i++ {
        _ = gw.ProcessPayment(ctx, 1, 1.0, "USD")
    }
    if gw.PaymentCB().State() != circuitbreaker.StateOpen {
        t.Fatalf("step 3: want OPEN, got %v", gw.PaymentCB().State())
    }

    // 4. Now calls fast-fail without hitting the (broken) service
    err := gw.ProcessPayment(ctx, 1, 1.0, "USD")
    if !gateway.IsCircuitError(err) {
        t.Fatalf("step 4: want circuit err, got %v", err)
    }

    // 5. Service recovers
    paymentSvc.Repair()

    // 6. Wait past OpenTimeout and probe
    time.Sleep(tinyCfg().Payment.OpenTimeout + 20*time.Millisecond)
    if err := gw.ProcessPayment(ctx, 1, 1.0, "USD"); err != nil {
        t.Fatalf("step 6: want success after recovery, got %v", err)
    }

    // 7. Breaker should now be CLOSED (SuccessThreshold=1 in tinyCfg)
    if s := gw.PaymentCB().State(); s != circuitbreaker.StateClosed {
        t.Fatalf("step 7: want CLOSED, got %v", s)
    }

    _ = fmt.Sprintf("") // keep fmt in imports if needed elsewhere
}
```

---

## 7. Coverage

```bash
go test -race -coverprofile=coverage.out ./...
go tool cover -html=coverage.out
```

Target: >80% for `circuitbreaker/` and `gateway/`. Handlers can be lower — they're mostly glue.

---

## 8. CI command

One line that runs in CI to catch everything:

```bash
go test -race -count=1 -coverprofile=coverage.out ./...
```

- `-race` — catches concurrency bugs
- `-count=1` — disables test caching
- `-coverprofile` — for coverage reporting

---

## 9. Sanity checklist

- [ ] `go test -race ./circuitbreaker/...` is green
- [ ] `go test -race ./gateway/...` is green
- [ ] `go test -race ./...` from the root is green
- [ ] Coverage report shows CB and gateway >80%
- [ ] The concurrent test completes in < 1 second

---

## What's next

**Step 12 — Dockerization.** Multi-stage Dockerfile for the app + `docker-compose.yml` for the app + Postgres + Redis + Prometheus. **Everything runs in Docker; nothing is installed on the host.**
