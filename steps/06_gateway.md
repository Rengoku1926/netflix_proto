# Step 6 — API Gateway

> The single wiring point: one circuit breaker per downstream, exposed through one struct.

---

## Goal

By the end of this step you have a `gateway` package that:

- Owns one `*circuitbreaker.CircuitBreaker` per service, each with tuned per-service config
- Wraps every service call in `cb.Execute(...)` before returning it to handlers
- Registers a shared `OnStateChange` callback for observability
- Implements **fallback variants** for graceful degradation (e.g. return cached recs when reco is down)
- Exposes `Breakers()` for the health handler to introspect

---

## 1. Why does "gateway" exist?

The gateway is **not** the HTTP server. It's the API surface that HTTP handlers call. Think of it as the facade that stitches together:

- Services (what to do)
- Circuit breakers (whether it's safe to do)
- Fallbacks (what to return if not)

Keeping this in its own package means:

- HTTP handlers stay thin (parse → call gateway → format response)
- Tests exercise real business flows without HTTP
- The same gateway could be reused from a gRPC server, a CLI tool, or a queue consumer

```
┌───────────┐   HTTP body   ┌─────────┐   Go call   ┌───────────┐
│  Handler  ├──────────────►│ Gateway ├────────────►│  Service  │
└───────────┘   JSON resp   └─────┬───┘             └───────────┘
                                  │
                                  ▼
                            CircuitBreaker
                            (guards the call)
```

---

## 2. Gateway struct

Create `gateway/gateway.go`:

```go
// Package gateway wires downstream services to their circuit breakers and
// exposes business operations to the handler layer. It is the only place
// where "service X" and "CB for service X" know about each other.
package gateway

import (
    "context"
    "errors"

    "circuit-breaker-demo/circuitbreaker"
    "circuit-breaker-demo/config"
    "circuit-breaker-demo/services"
)

// Gateway owns one CircuitBreaker per downstream service.
//
// The struct is immutable after construction: no fields are ever written
// outside New(). This makes it safe to share across any number of goroutines
// without synchronisation.
type Gateway struct {
    paymentSvc services.PaymentServicer
    recoSvc    services.RecommendationServicer
    userSvc    services.UserServicer

    paymentCB *circuitbreaker.CircuitBreaker
    recoCB    *circuitbreaker.CircuitBreaker
    userCB    *circuitbreaker.CircuitBreaker
}

// New builds a Gateway from the services and config. The onStateChange hook
// is attached to every CB so a single observability sink sees all transitions.
func New(
    paymentSvc services.PaymentServicer,
    recoSvc services.RecommendationServicer,
    userSvc services.UserServicer,
    cfg config.BreakersConfig,
    onStateChange func(*circuitbreaker.CircuitBreaker, circuitbreaker.State, circuitbreaker.State),
) *Gateway {
    paymentCB := circuitbreaker.New(circuitbreaker.Config{
        Name:              "payment-service",
        FailureThreshold:  cfg.Payment.FailureThreshold,
        SuccessThreshold:  cfg.Payment.SuccessThreshold,
        OpenTimeout:       cfg.Payment.OpenTimeout,
        MaxHalfOpenProbes: cfg.Payment.MaxHalfOpenProbes,
    })
    recoCB := circuitbreaker.New(circuitbreaker.Config{
        Name:              "recommendation-service",
        FailureThreshold:  cfg.Recommendation.FailureThreshold,
        SuccessThreshold:  cfg.Recommendation.SuccessThreshold,
        OpenTimeout:       cfg.Recommendation.OpenTimeout,
        MaxHalfOpenProbes: cfg.Recommendation.MaxHalfOpenProbes,
    })
    userCB := circuitbreaker.New(circuitbreaker.Config{
        Name:              "user-service",
        FailureThreshold:  cfg.User.FailureThreshold,
        SuccessThreshold:  cfg.User.SuccessThreshold,
        OpenTimeout:       cfg.User.OpenTimeout,
        MaxHalfOpenProbes: cfg.User.MaxHalfOpenProbes,
    })

    if onStateChange != nil {
        paymentCB.OnStateChange(onStateChange)
        recoCB.OnStateChange(onStateChange)
        userCB.OnStateChange(onStateChange)
    }

    return &Gateway{
        paymentSvc: paymentSvc,
        recoSvc:    recoSvc,
        userSvc:    userSvc,
        paymentCB:  paymentCB,
        recoCB:     recoCB,
        userCB:     userCB,
    }
}
```

---

## 3. Protected calls

Each gateway method wraps its service call in the matching breaker's `Execute`. The wrapping is the only place errors from the breaker (e.g. `ErrCircuitOpen`) enter the call graph.

```go
// ProcessPayment runs the payment through the payment CB.
func (gw *Gateway) ProcessPayment(ctx context.Context, userID int, amount float64, currency string) error {
    return gw.paymentCB.Execute(func() error {
        return gw.paymentSvc.ProcessPayment(ctx, userID, amount, currency)
    })
}

// GetRecommendations runs the reco lookup through the reco CB.
// Returns (nil, ErrCircuitOpen) when the breaker is OPEN.
func (gw *Gateway) GetRecommendations(ctx context.Context, userID int) ([]string, error) {
    var recs []string
    err := gw.recoCB.Execute(func() error {
        r, err := gw.recoSvc.GetRecommendations(ctx, userID)
        if err != nil {
            return err
        }
        recs = r
        return nil
    })
    return recs, err
}

// GetUser looks up a user profile via the user CB.
func (gw *Gateway) GetUser(ctx context.Context, userID int) (*services.UserProfile, error) {
    var profile *services.UserProfile
    err := gw.userCB.Execute(func() error {
        p, err := gw.userSvc.GetUser(ctx, userID)
        if err != nil {
            return err
        }
        profile = p
        return nil
    })
    return profile, err
}
```

### Why the closure-and-outer-var pattern?

`cb.Execute(fn func() error)` takes a `func() error` — but we need the function to also return a `[]string` or `*UserProfile`. The closure captures the outer `recs` / `profile` variable and writes to it only on success. If `Execute` short-circuits (ErrCircuitOpen), the outer var stays nil and we return `(nil, err)` — clean.

---

## 4. Fallback variants (graceful degradation)

For services where a degraded response beats no response, provide a fallback wrapper. The fallback lives in the gateway because it's business policy — "when the reco service is down, return the popular-titles list instead."

```go
// GetRecommendationsWithFallback returns a degraded-but-useful response
// when the reco CB is OPEN. Used for user-facing endpoints where any result
// is better than an error page.
func (gw *Gateway) GetRecommendationsWithFallback(ctx context.Context, userID int) ([]string, bool) {
    recs, err := gw.GetRecommendations(ctx, userID)
    if err == nil {
        return recs, false // degraded=false — served from live service
    }
    // Either CB is OPEN, or the service errored. Either way, serve the fallback.
    _ = err // could log here; caller may prefer to know it's degraded
    return popularTitlesFallback(), true
}

// popularTitlesFallback returns a static list of popular shows. In production
// this would be a cached, periodically-refreshed list stored in Redis.
func popularTitlesFallback() []string {
    return []string{
        "Stranger Things",
        "Breaking Bad",
        "The Crown",
    }
}
```

**Payment has no fallback.** You cannot "gracefully degrade" a charge — taking half someone's money is worse than taking none. When the payment CB trips, the handler returns 503 and the client retries later.

---

## 5. Inspection for the health handler

```go
// Breakers returns all CBs for health/metrics endpoints. Order is stable.
func (gw *Gateway) Breakers() []*circuitbreaker.CircuitBreaker {
    return []*circuitbreaker.CircuitBreaker{gw.paymentCB, gw.recoCB, gw.userCB}
}

// Individual accessors — occasionally useful for middleware that needs a
// specific CB (e.g. CircuitBreakerGuard around the /payments route).
func (gw *Gateway) PaymentCB() *circuitbreaker.CircuitBreaker { return gw.paymentCB }
func (gw *Gateway) RecoCB() *circuitbreaker.CircuitBreaker    { return gw.recoCB }
func (gw *Gateway) UserCB() *circuitbreaker.CircuitBreaker    { return gw.userCB }
```

---

## 6. Why per-service breakers, not one global?

This is the **bulkhead principle**: failure in one service should not impact others.

```
           ┌─ paymentCB ─── payment (OPEN)
Gateway ───┼─ recoCB    ─── reco    (CLOSED — still serving!)
           └─ userCB    ─── user    (CLOSED — still serving!)
```

A single global CB would trip on _any_ downstream failure — a flaky reco model would block payments. Absurd. One CB per service means each failure is contained.

Each service also gets its own tuning (see `ARCHITECTURE.md §9`):

| Service | Fail Thr | Reason                                   |
| ------- | -------- | ---------------------------------------- |
| payment | 3        | Money — trip fast                        |
| reco    | 5        | Best-effort — be forgiving               |
| user    | 3        | Auth — recover fast (SuccessThreshold=1) |

---
 
## 7. Helper for error classification

Callers will want to know _why_ a gateway call failed. A small helper makes this explicit:

```go
// IsCircuitError reports whether err came from a tripped circuit breaker.
// Handlers use this to return 503 with Retry-After instead of 502.
func IsCircuitError(err error) bool {
    return errors.Is(err, circuitbreaker.ErrCircuitOpen) ||
        errors.Is(err, circuitbreaker.ErrTooManyProbes)
}
```

---

## 8. Sanity checklist

- [ ] `go build ./gateway/...` compiles
- [ ] `gateway.New(...)` returns a non-nil Gateway with all three CBs initialized
- [ ] Breaking the payment service + calling `ProcessPayment` N times trips the payment CB — verify with `gw.PaymentCB().State() == StateOpen`
- [ ] The other two CBs stay CLOSED while payment is OPEN (isolation check)

---

## What's next

**Step 7 — HTTP Handlers.** With the gateway ready, we build the HTTP layer: one handler per route, each parsing input, calling the gateway, and formatting the response with proper error codes (`CIRCUIT_OPEN` vs `UPSTREAM_ERROR`).
