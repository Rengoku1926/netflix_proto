# Step 3 — Circuit Breaker Core Engine

> The single most important file in the project: the state machine that protects every downstream call.

---

## Goal

By the end of this step you have `circuitbreaker/breaker.go` with:

- Three states: `StateClosed`, `StateOpen`, `StateHalfOpen` (encoded as `int32` for atomic ops)
- A `Config` struct per instance (thresholds, timeouts)
- An `Execute(fn func() error) error` entry point that implements the full state machine
- Atomic counters for metrics (`totalRequests`, `successes`, `failures`, `rejections`, `stateChanges`)
- A mutex-guarded transition block for state changes
- An asynchronous `OnStateChange` hook
- No background goroutines — recovery is **lazy** (triggered by the next request after timeout)

---

## 1. The mental model

A circuit breaker is a stateful guard between a caller and a downstream dependency.

```
CLOSED     ─── requests flow normally ──▶  downstream
  │
  │ N consecutive failures
  ▼
OPEN       ─── requests fast-fail ─────▶  ErrCircuitOpen (downstream NOT called)
  │
  │ OpenTimeout elapsed, a request arrives
  ▼
HALF-OPEN  ─── limited probes allowed ─▶  downstream
  │
  ├── probe succeeds M times → CLOSED
  └── probe fails           → OPEN
```

**Why bother?** Without a breaker, when a downstream slows to 5s latency, every caller goroutine blocks for 5s. Under 1000 RPS you accumulate 5000 blocked goroutines in a second → thread pool exhaustion → your service dies too. Cascading failure.

With a breaker: after 3 failures we stop calling the downstream at all. Our goroutines return in microseconds with `ErrCircuitOpen`. We survive.

---

## 2. Design decisions worth understanding

### Atomic state, mutex counters

```
state (int32)                — atomic read, atomic write
counters (consecutiveFails…) — read+written under sync.Mutex
metrics (totalRequests…)     — atomic add, atomic read (eventual consistency)
```

Why split? **The hot path is `cb.State()` — it runs on every single request.** Making it lock-free (atomic) means 10,000 concurrent goroutines can all check state in parallel with zero contention. Only the rare _transition_ (a few times per outage) needs the mutex.

### Lazy recovery (no background goroutine)

We do **not** use `time.AfterFunc` to transition OPEN → HALF-OPEN. Instead, `beforeExec` checks `time.Since(lastFailureTime) >= OpenTimeout` on every call. The first request to arrive after the timeout triggers the transition.

Benefits:

- Zero goroutine leak risk (nothing to stop, nothing to `<-done`)
- Works correctly under zero traffic (no phantom transitions when nobody is calling)
- No wakeup cost when the service is healthy

### fn() runs outside the lock

The mutex is held only for the tiny `beforeExec` and `afterExec` bookkeeping. The actual `fn()` call — which may take seconds — runs with **no lock held**. This is what lets thousands of concurrent requests actually execute in parallel.

---

## 3. The type definitions

Create `circuitbreaker/breaker.go`:

```go
// Package circuitbreaker implements the three-state circuit breaker pattern.
//
// A circuit breaker wraps calls to an unreliable downstream dependency and
// monitors the failure rate. When failures exceed a threshold it "trips"
// open, rejecting subsequent calls immediately without invoking the
// downstream. After a timeout it allows a limited number of probes; if
// those succeed it closes again and traffic resumes.
//
// This package has no dependencies outside the standard library.
package circuitbreaker

import (
    "errors"
    "fmt"
    "sync"
    "sync/atomic"
    "time"
)

// State encodes the three circuit breaker states as int32 so the current
// value can be read/written with sync/atomic on the hot path.
type State int32

const (
    StateClosed   State = iota // 0 — normal operation
    StateOpen                  // 1 — tripped, fast-fail
    StateHalfOpen              // 2 — probing recovery
)

// String makes State printable for logs and health endpoints.
func (s State) String() string {
    switch s {
    case StateClosed:
        return "CLOSED"
    case StateOpen:
        return "OPEN"
    case StateHalfOpen:
        return "HALF-OPEN"
    default:
        return "UNKNOWN"
    }
}

// Config is the full set of tuning knobs for one CircuitBreaker instance.
// One Config per downstream service — do not share configs across breakers.
type Config struct {
    Name              string        // used in logs, metrics, callbacks
    FailureThreshold  int           // consecutive failures before OPEN
    SuccessThreshold  int           // consecutive successes in HALF-OPEN before CLOSED
    OpenTimeout       time.Duration // how long to stay OPEN before probing
    MaxHalfOpenProbes int           // max concurrent probes during HALF-OPEN
}

// Metrics is a point-in-time snapshot of all counters. Fields are atomically
// consistent with each other at the moment Metrics() was called; counters
// may have moved forward by the time the caller reads individual fields.
type Metrics struct {
    TotalRequests int64
    Successes     int64
    Failures      int64
    Rejections    int64
    StateChanges  int64
}

// Sentinel errors. Callers distinguish circuit-open from downstream errors
// with errors.Is(err, ErrCircuitOpen).
var (
    ErrCircuitOpen   = errors.New("circuit breaker OPEN: fast-failing request")
    ErrTooManyProbes = errors.New("circuit breaker HALF-OPEN: probe slot occupied")
)

// CircuitBreaker is the state machine. Zero value is invalid — always
// construct via New(cfg).
type CircuitBreaker struct {
    config Config

    // Atomic — read without lock on the hot path.
    state int32

    // Mutex-guarded bookkeeping. These fields must change together.
    mu                sync.Mutex
    consecutiveFails  int
    consecutivePasses int
    lastFailureTime   time.Time
    activeProbes      int

    // Atomic metric counters. Written with atomic.AddInt64, read with
    // atomic.LoadInt64. No lock needed.
    totalRequests int64
    successes     int64
    failures      int64
    rejections    int64
    stateChanges  int64

    // Called in a goroutine on every state transition. Safe to be nil.
    onStateChange func(cb *CircuitBreaker, from, to State)
}
```

---

## 4. Constructor and accessors

```go
// New creates a CircuitBreaker. Always use this — the zero value is invalid
// because the mutex and state field need specific zero semantics.
func New(cfg Config) *CircuitBreaker {
    return &CircuitBreaker{
        config: cfg,
        state:  int32(StateClosed),
    }
}

// OnStateChange registers a hook fired (in a new goroutine) on every
// state transition. The hook must not panic — if it does, it will crash
// the whole process because it's invoked outside any recover().
//
// Register before any call to Execute to avoid a data race on the field.
func (cb *CircuitBreaker) OnStateChange(fn func(cb *CircuitBreaker, from, to State)) {
    cb.onStateChange = fn
}

// State returns the current state with a lock-free atomic read.
// Safe to call at any frequency (e.g. from middleware on every request).
func (cb *CircuitBreaker) State() State {
    return State(atomic.LoadInt32(&cb.state))
}

// Name returns the breaker name set in Config.
func (cb *CircuitBreaker) Name() string { return cb.config.Name }

// Metrics returns an atomic snapshot of all counters. No lock needed.
func (cb *CircuitBreaker) Metrics() Metrics {
    return Metrics{
        TotalRequests: atomic.LoadInt64(&cb.totalRequests),
        Successes:     atomic.LoadInt64(&cb.successes),
        Failures:      atomic.LoadInt64(&cb.failures),
        Rejections:    atomic.LoadInt64(&cb.rejections),
        StateChanges:  atomic.LoadInt64(&cb.stateChanges),
    }
}

// Reset returns the breaker to CLOSED with zeroed bookkeeping. Metrics
// counters are kept (they're cumulative). Intended for tests + admin ops.
func (cb *CircuitBreaker) Reset() {
    cb.mu.Lock()
    defer cb.mu.Unlock()
    atomic.StoreInt32(&cb.state, int32(StateClosed))
    cb.consecutiveFails = 0
    cb.consecutivePasses = 0
    cb.activeProbes = 0
    cb.lastFailureTime = time.Time{}
}
```

---

## 5. The Execute method (the heart of everything)

```go
// Execute runs fn through the circuit breaker.
//
//   err := cb.Execute(func() error {
//       return downstream.Call(ctx, req)
//   })
//
// Returns:
//   nil              — fn succeeded
//   ErrCircuitOpen   — OPEN; fn was NOT called
//   ErrTooManyProbes — HALF-OPEN, probe slot full; fn was NOT called
//   err from fn()    — fn was called and returned an error
//
// Safe for concurrent use by any number of goroutines.
func (cb *CircuitBreaker) Execute(fn func() error) error {
    atomic.AddInt64(&cb.totalRequests, 1)

    if err := cb.beforeExec(); err != nil {
        atomic.AddInt64(&cb.rejections, 1)
        return err
    }

    // fn() runs with NO lock held — this is the key to parallelism.
    err := fn()

    cb.afterExec(err)
    return err
}

// beforeExec decides whether to let fn run based on current state.
// Holds mu for the duration.
func (cb *CircuitBreaker) beforeExec() error {
    cb.mu.Lock()
    defer cb.mu.Unlock()

    switch State(atomic.LoadInt32(&cb.state)) {
    case StateClosed:
        return nil

    case StateOpen:
        remaining := cb.config.OpenTimeout - time.Since(cb.lastFailureTime)
        if remaining > 0 {
            // Still within the timeout window — reject.
            return fmt.Errorf("%w (retry in %s)",
                ErrCircuitOpen, remaining.Round(time.Millisecond))
        }
        // Timeout elapsed — transition to HALF-OPEN and let this probe through.
        cb.transitionToLocked(StateHalfOpen)
        cb.activeProbes = 1
        return nil

    case StateHalfOpen:
        if cb.activeProbes >= cb.config.MaxHalfOpenProbes {
            return ErrTooManyProbes
        }
        cb.activeProbes++
        return nil
    }
    return nil
}

// afterExec records the outcome of fn and updates state if thresholds are hit.
func (cb *CircuitBreaker) afterExec(err error) {
    cb.mu.Lock()
    defer cb.mu.Unlock()

    if err != nil {
        atomic.AddInt64(&cb.failures, 1)
        cb.lastFailureTime = time.Now()
        cb.consecutivePasses = 0

        switch State(atomic.LoadInt32(&cb.state)) {
        case StateClosed:
            cb.consecutiveFails++
            if cb.consecutiveFails >= cb.config.FailureThreshold {
                cb.transitionToLocked(StateOpen)
            }
        case StateHalfOpen:
            cb.activeProbes--
            cb.transitionToLocked(StateOpen)
        }
        return
    }

    atomic.AddInt64(&cb.successes, 1)
    cb.consecutiveFails = 0

    switch State(atomic.LoadInt32(&cb.state)) {
    case StateClosed:
        // nothing to do — stay CLOSED
    case StateHalfOpen:
        cb.activeProbes--
        cb.consecutivePasses++
        if cb.consecutivePasses >= cb.config.SuccessThreshold {
            cb.transitionToLocked(StateClosed)
        }
    }
}

// transitionToLocked changes state and fires the onStateChange hook.
// Caller MUST hold cb.mu.
func (cb *CircuitBreaker) transitionToLocked(to State) {
    from := State(atomic.LoadInt32(&cb.state))
    if from == to {
        return
    }

    atomic.StoreInt32(&cb.state, int32(to))
    atomic.AddInt64(&cb.stateChanges, 1)

    // Reset bookkeeping appropriate to the new state.
    switch to {
    case StateClosed:
        cb.consecutiveFails = 0
        cb.consecutivePasses = 0
        cb.activeProbes = 0
    case StateOpen:
        cb.consecutivePasses = 0
        cb.activeProbes = 0
    case StateHalfOpen:
        cb.consecutivePasses = 0
    }

    // Fire the hook in a new goroutine so it can never block the hot path.
    if cb.onStateChange != nil {
        go cb.onStateChange(cb, from, to)
    }
}
```

---

## 6. Quick smoke test

Create `circuitbreaker/breaker_smoke_test.go` (full tests come in Step 11):

```go
package circuitbreaker

import (
    "errors"
    "testing"
    "time"
)

func TestTripsAfterThreshold(t *testing.T) {
    cb := New(Config{
        Name: "smoke", FailureThreshold: 3, SuccessThreshold: 1,
        OpenTimeout: time.Hour, MaxHalfOpenProbes: 1,
    })

    boom := func() error { return errors.New("boom") }

    for i := 0; i < 2; i++ {
        _ = cb.Execute(boom)
        if cb.State() != StateClosed {
            t.Fatalf("expected CLOSED after %d failures, got %v", i+1, cb.State())
        }
    }

    _ = cb.Execute(boom) // 3rd — should trip
    if cb.State() != StateOpen {
        t.Fatalf("expected OPEN after 3rd failure, got %v", cb.State())
    }

    // Now it must fast-fail without calling fn.
    called := false
    err := cb.Execute(func() error { called = true; return nil })
    if called {
        t.Fatal("fn should NOT be called when OPEN")
    }
    if !errors.Is(err, ErrCircuitOpen) {
        t.Fatalf("expected ErrCircuitOpen, got %v", err)
    }
}
```

Run it:

```bash
go test ./circuitbreaker/... -race
```

Must pass under `-race`. If it doesn't, something is wrong with the atomic/mutex design.

---

## 7. Sanity checklist

- [ ] `go build ./circuitbreaker/...` compiles
- [ ] Smoke test passes with `-race`
- [ ] No background goroutines started from `New()` — grep your file; there should be zero `go func()` except inside `transitionToLocked`
- [ ] `State()` uses `atomic.LoadInt32`, not mutex

---

## What's next

**Step 4 — Services Layer.** The breaker is useless without something to protect. We'll create mock downstream services with failure-injection helpers (`Break()`, `Repair()`, `SetFailRate()`) so we can simulate outages and watch the breaker react.
