# Step 7 — HTTP Handlers

> The HTTP layer: parse input, call the gateway, format the response. Nothing more.

---

## Goal

By the end of this step you have a `handler` package with:

- One file per resource: `payment.go`, `recommendation.go`, `user.go`, `health.go`
- A shared response helper (`writeJSON`, `writeError`) with a consistent error envelope
- Proper error classification — `CIRCUIT_OPEN` → 503 with `Retry-After`, `UPSTREAM_ERROR` → 502
- A health endpoint that exposes every CB's current state + metrics

---

## 1. Handler responsibilities (what belongs here and what doesn't)

A handler does exactly four things:

1. **Parse** the request (URL params, JSON body, headers)
2. **Validate** the input — reject garbage with 400 before calling the gateway
3. **Call** the gateway (a single method per route, typically)
4. **Format** the response — success JSON or error envelope

Things that do **not** belong in handlers:

- Business logic → in the service
- Persistence → in the repository
- Circuit breaker logic → in the gateway
- Cross-cutting concerns (logging, auth, request IDs) → in middleware

If a handler is longer than ~30 lines, something has leaked into it that belongs elsewhere.

---

## 2. Shared response helpers

Create `handler/response.go`:

```go
// Package handler contains HTTP handlers. Handlers are thin: parse, call
// the gateway, format. All business logic lives in the gateway/services.
package handler

import (
    "encoding/json"
    "errors"
    "net/http"
    "strings"
    "time"

    "circuit-breaker-demo/circuitbreaker"
    "circuit-breaker-demo/gateway"
)

// ErrorResponse is the standard error envelope for every 4xx/5xx.
// Keeping one shape makes clients easier to write.
type ErrorResponse struct {
    Code    string `json:"code"`              // machine-readable, e.g. "CIRCUIT_OPEN"
    Message string `json:"message"`           // human-readable
    RetryIn string `json:"retry_in,omitempty"` // set when Code == "CIRCUIT_OPEN"
}

// writeJSON serialises v and writes it with the given status. Used for
// success responses.
func writeJSON(w http.ResponseWriter, status int, v any) {
    w.Header().Set("Content-Type", "application/json")
    w.WriteHeader(status)
    _ = json.NewEncoder(w).Encode(v)
}

// writeError writes a standard ErrorResponse envelope.
func writeError(w http.ResponseWriter, status int, code, msg, retryIn string) {
    w.Header().Set("Content-Type", "application/json")
    if retryIn != "" {
        w.Header().Set("Retry-After", stripTrailingS(retryIn))
    }
    w.WriteHeader(status)
    _ = json.NewEncoder(w).Encode(ErrorResponse{
        Code: code, Message: msg, RetryIn: retryIn,
    })
}

// writeGatewayError is the single place we map gateway errors to HTTP codes.
// Any handler that calls a gateway method should use this for non-nil errors.
func writeGatewayError(w http.ResponseWriter, err error) {
    switch {
    case gateway.IsCircuitError(err):
        writeError(w, http.StatusServiceUnavailable,
            "CIRCUIT_OPEN", "service temporarily unavailable", extractRetryAfter(err))

    case errors.Is(err, errDeadline):
        writeError(w, http.StatusGatewayTimeout,
            "TIMEOUT", "upstream request timed out", "")

    default:
        writeError(w, http.StatusBadGateway,
            "UPSTREAM_ERROR", err.Error(), "")
    }
}

// extractRetryAfter parses "retry in 4.838s" out of a wrapped ErrCircuitOpen.
// Returns "" if no duration was encoded.
func extractRetryAfter(err error) string {
    s := err.Error()
    i := strings.Index(s, "retry in ")
    if i < 0 {
        return ""
    }
    rest := s[i+len("retry in "):]
    if j := strings.Index(rest, ")"); j >= 0 {
        return rest[:j]
    }
    return rest
}

func stripTrailingS(d string) string {
    // Retry-After must be integer seconds per RFC 7231.
    if dur, err := time.ParseDuration(d); err == nil {
        secs := int(dur.Seconds())
        if secs < 1 {
            secs = 1
        }
        return intToString(secs)
    }
    return "5"
}

func intToString(i int) string {
    // tiny helper to avoid importing strconv just for this
    if i == 0 { return "0" }
    neg := false
    if i < 0 { neg = true; i = -i }
    var buf [12]byte
    pos := len(buf)
    for i > 0 {
        pos--
        buf[pos] = byte('0' + i%10)
        i /= 10
    }
    if neg {
        pos--
        buf[pos] = '-'
    }
    return string(buf[pos:])
}

// Sentinel we alias so imports stay minimal; real context.DeadlineExceeded check
// happens via errors.Is in writeGatewayError.
var errDeadline = circuitbreaker.ErrCircuitOpen // placeholder — replace with context.DeadlineExceeded in real code
```

> **Note:** in a real build, replace `errDeadline` with the standard library import: `import "context"` and `errors.Is(err, context.DeadlineExceeded)`. The placeholder above keeps this snippet self-contained.

---

## 3. PaymentHandler

Create `handler/payment.go`:

```go
package handler

import (
    "context"
    "crypto/rand"
    "encoding/hex"
    "encoding/json"
    "net/http"
    "time"

    "circuit-breaker-demo/gateway"
)

type PaymentHandler struct {
    gw *gateway.Gateway
}

func NewPaymentHandler(gw *gateway.Gateway) *PaymentHandler {
    return &PaymentHandler{gw: gw}
}

type paymentRequest struct {
    UserID   int     `json:"user_id"`
    Amount   float64 `json:"amount"`
    Currency string  `json:"currency"`
}

type paymentResponse struct {
    TransactionID string    `json:"transaction_id"`
    Status        string    `json:"status"`
    ProcessedAt   time.Time `json:"processed_at"`
}

// Create handles POST /payments.
func (h *PaymentHandler) Create(w http.ResponseWriter, r *http.Request) {
    var req paymentRequest
    if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
        writeError(w, http.StatusBadRequest, "INVALID_BODY", err.Error(), "")
        return
    }
    if req.UserID <= 0 || req.Amount <= 0 || len(req.Currency) != 3 {
        writeError(w, http.StatusBadRequest, "INVALID_INPUT",
            "user_id, amount, and currency are required", "")
        return
    }

    // Give each downstream call a hard upper bound — breaker tripping is
    // only useful if the call doesn't block indefinitely first.
    ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
    defer cancel()

    if err := h.gw.ProcessPayment(ctx, req.UserID, req.Amount, req.Currency); err != nil {
        writeGatewayError(w, err)
        return
    }

    writeJSON(w, http.StatusCreated, paymentResponse{
        TransactionID: newID(),
        Status:        "processed",
        ProcessedAt:   time.Now().UTC(),
    })
}

// newID generates a random 16-byte hex token for the transaction ID. In
// production, use a ULID or UUID for lexicographic ordering + debuggability.
func newID() string {
    var b [16]byte
    _, _ = rand.Read(b[:])
    return hex.EncodeToString(b[:])
}
```

---

## 4. RecommendationHandler (with fallback)

Create `handler/recommendation.go`:

```go
package handler

import (
    "context"
    "net/http"
    "strconv"
    "time"

    "circuit-breaker-demo/gateway"
)

type RecoHandler struct {
    gw *gateway.Gateway
}

func NewRecoHandler(gw *gateway.Gateway) *RecoHandler {
    return &RecoHandler{gw: gw}
}

type recoResponse struct {
    UserID          int      `json:"user_id"`
    Recommendations []string `json:"recommendations"`
    Degraded        bool     `json:"degraded"` // true when served from fallback
}

// Get handles GET /recommendations/{userID}.
func (h *RecoHandler) Get(w http.ResponseWriter, r *http.Request) {
    userID, err := strconv.Atoi(r.PathValue("userID"))
    if err != nil || userID <= 0 {
        writeError(w, http.StatusBadRequest, "INVALID_USER_ID",
            "userID must be a positive integer", "")
        return
    }

    ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
    defer cancel()

    // Reco is best-effort — we always use the fallback-aware variant so
    // the client never sees a 503 for recommendations.
    recs, degraded := h.gw.GetRecommendationsWithFallback(ctx, userID)
    writeJSON(w, http.StatusOK, recoResponse{
        UserID:          userID,
        Recommendations: recs,
        Degraded:        degraded,
    })
}
```

`r.PathValue("userID")` is the Go 1.22 router feature — no third-party router needed.

---

## 5. UserHandler

Create `handler/user.go`:

```go
package handler

import (
    "context"
    "net/http"
    "strconv"
    "time"

    "circuit-breaker-demo/gateway"
)

type UserHandler struct {
    gw *gateway.Gateway
}

func NewUserHandler(gw *gateway.Gateway) *UserHandler {
    return &UserHandler{gw: gw}
}

func (h *UserHandler) Get(w http.ResponseWriter, r *http.Request) {
    userID, err := strconv.Atoi(r.PathValue("userID"))
    if err != nil || userID <= 0 {
        writeError(w, http.StatusBadRequest, "INVALID_USER_ID",
            "userID must be a positive integer", "")
        return
    }

    ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
    defer cancel()

    profile, err := h.gw.GetUser(ctx, userID)
    if err != nil {
        writeGatewayError(w, err)
        return
    }
    writeJSON(w, http.StatusOK, profile)
}
```

---

## 6. HealthHandler — liveness + circuit breaker dashboard

Create `handler/health.go`:

```go
package handler

import (
    "net/http"
    "time"

    "circuit-breaker-demo/gateway"
)

type HealthHandler struct {
    gw *gateway.Gateway
}

func NewHealthHandler(gw *gateway.Gateway) *HealthHandler {
    return &HealthHandler{gw: gw}
}

// Liveness — GET /health. Returns 200 as long as the process is alive.
// Kubernetes hits this every periodSeconds for the livenessProbe.
func (h *HealthHandler) Liveness(w http.ResponseWriter, r *http.Request) {
    writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// breakerSnapshot is the per-breaker shape inside /health/circuit-breakers.
type breakerSnapshot struct {
    Name          string `json:"name"`
    State         string `json:"state"`
    TotalRequests int64  `json:"total_requests"`
    Successes     int64  `json:"successes"`
    Failures      int64  `json:"failures"`
    Rejections    int64  `json:"rejections"`
    StateChanges  int64  `json:"state_changes"`
}

type healthResponse struct {
    Timestamp time.Time         `json:"timestamp"`
    Breakers  []breakerSnapshot `json:"breakers"`
}

// CircuitBreakers — GET /health/circuit-breakers. Used by dashboards,
// alerting, and the load balancer to route around pods with tripped CBs.
func (h *HealthHandler) CircuitBreakers(w http.ResponseWriter, r *http.Request) {
    cbs := h.gw.Breakers()
    out := make([]breakerSnapshot, 0, len(cbs))
    for _, cb := range cbs {
        m := cb.Metrics()
        out = append(out, breakerSnapshot{
            Name:          cb.Name(),
            State:         cb.State().String(),
            TotalRequests: m.TotalRequests,
            Successes:     m.Successes,
            Failures:      m.Failures,
            Rejections:    m.Rejections,
            StateChanges:  m.StateChanges,
        })
    }
    writeJSON(w, http.StatusOK, healthResponse{
        Timestamp: time.Now().UTC(),
        Breakers:  out,
    })
}
```

---

## 7. Route table (reference — wired in Step 9)

| Method | Path | Handler | Purpose |
|---|---|---|---|
| POST | `/payments` | `PaymentHandler.Create` | Process a payment |
| GET | `/recommendations/{userID}` | `RecoHandler.Get` | Get recs (with fallback) |
| GET | `/users/{userID}` | `UserHandler.Get` | Get user profile |
| GET | `/health` | `HealthHandler.Liveness` | K8s liveness probe |
| GET | `/health/circuit-breakers` | `HealthHandler.CircuitBreakers` | CB dashboard |

---

## 8. Sanity checklist

- [ ] `go build ./handler/...` compiles
- [ ] Each handler file is under ~50 lines — if it isn't, something leaked in
- [ ] Every call to a gateway method uses `writeGatewayError(w, err)` on non-nil errors
- [ ] `writeJSON` is the only place `json.NewEncoder(w).Encode(...)` appears

---

## What's next

**Step 8 — Middleware.** We'll build the HTTP middleware chain: request IDs, structured logging, panic recovery, and an optional CircuitBreakerGuard that fast-fails at the HTTP layer.
