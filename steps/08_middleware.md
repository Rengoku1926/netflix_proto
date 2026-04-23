# Step 8 — Middleware

> Cross-cutting concerns wrap every handler: request IDs, logging, panic recovery, and an HTTP-layer circuit-breaker guard.

---

## Goal

By the end of this step you have a `middleware` package with:

- `RequestID` — injects `X-Request-ID` into context + response headers
- `Logger` — structured request/response logging (slog)
- `Recovery` — catches panics and returns 500 instead of crashing
- `CircuitBreakerGuard` — rejects with 503 *before* parsing the body when a given CB is OPEN
- (Optional) `RateLimiter` — token-bucket per client

---

## 1. The middleware pattern in Go

A middleware is any function with the signature:

```go
func(next http.Handler) http.Handler
```

It receives the next handler in the chain and returns a new handler that does its own work before (or after) calling `next.ServeHTTP`. Chaining is just function composition:

```go
chain := RequestID(Logger(logger)(Recovery(logger)(mux)))
```

Order matters: the outermost wrapper runs first on the request, last on the response.

### The recommended ordering

```
Request  → RequestID → Logger → Recovery → CircuitBreakerGuard → handler
Response ← RequestID ← Logger ← Recovery ← CircuitBreakerGuard ← handler
```

Why this order?

1. **RequestID first** — everything downstream should have an ID to correlate with
2. **Logger next** — it needs the RequestID to be in context
3. **Recovery next** — catches panics from *everything below it*, including the CB guard and the handler
4. **CircuitBreakerGuard last** — by the time we're here, we've already logged + have a panic net

---

## 2. RequestID

Create `middleware/request_id.go`:

```go
// Package middleware holds HTTP middleware building blocks. Each middleware
// is a func(http.Handler) http.Handler for easy chaining.
package middleware

import (
    "context"
    "net/http"

    "github.com/google/uuid"
)

type ctxKey string

// RequestIDKey is the context key that holds the current request ID.
// Handlers can read it via r.Context().Value(middleware.RequestIDKey).
const RequestIDKey ctxKey = "request_id"

// RequestID injects a unique ID into the context and mirrors it on the
// response as X-Request-ID. If the client supplies their own X-Request-ID
// we honour it — useful for correlating logs across microservices.
func RequestID(next http.Handler) http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        id := r.Header.Get("X-Request-ID")
        if id == "" {
            id = uuid.New().String()
        }
        w.Header().Set("X-Request-ID", id)
        ctx := context.WithValue(r.Context(), RequestIDKey, id)
        next.ServeHTTP(w, r.WithContext(ctx))
    })
}
```

Add the uuid dependency:

```bash
go get github.com/google/uuid
```

---

## 3. Logger

Create `middleware/logger.go`:

```go
package middleware

import (
    "log/slog"
    "net/http"
    "time"
)

// Logger emits one structured log line per request with method, path,
// status, latency, and the request ID.
//
// It returns a func to match the standard middleware signature even though
// it takes a *slog.Logger parameter — double call: `Logger(logger)(next)`.
func Logger(logger *slog.Logger) func(http.Handler) http.Handler {
    return func(next http.Handler) http.Handler {
        return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
            start := time.Now()
            rw := &responseWriter{ResponseWriter: w, status: http.StatusOK}

            next.ServeHTTP(rw, r)

            logger.Info("http_request",
                "method", r.Method,
                "path", r.URL.Path,
                "status", rw.status,
                "bytes", rw.bytesWritten,
                "latency_ms", time.Since(start).Milliseconds(),
                "request_id", r.Context().Value(RequestIDKey),
                "remote", r.RemoteAddr,
            )
        })
    }
}

// responseWriter wraps http.ResponseWriter to capture the status code +
// bytes written, which the bare interface does not expose.
type responseWriter struct {
    http.ResponseWriter
    status       int
    bytesWritten int
}

func (rw *responseWriter) WriteHeader(code int) {
    rw.status = code
    rw.ResponseWriter.WriteHeader(code)
}

func (rw *responseWriter) Write(b []byte) (int, error) {
    n, err := rw.ResponseWriter.Write(b)
    rw.bytesWritten += n
    return n, err
}
```

### Why wrap ResponseWriter?

`http.ResponseWriter` is an interface — you can't observe the status code after the handler runs without wrapping. The wrapper intercepts `WriteHeader` / `Write` calls so the logger can report them.

---

## 4. Recovery

Create `middleware/recovery.go`:

```go
package middleware

import (
    "log/slog"
    "net/http"
    "runtime/debug"
)

// Recovery catches panics from downstream handlers and returns 500 instead
// of letting the goroutine crash the process (the net/http server recovers
// from panics by default, but silently — we want structured logs + a
// proper error envelope).
func Recovery(logger *slog.Logger) func(http.Handler) http.Handler {
    return func(next http.Handler) http.Handler {
        return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
            defer func() {
                if rec := recover(); rec != nil {
                    logger.Error("panic_recovered",
                        "panic", rec,
                        "stack", string(debug.Stack()),
                        "request_id", r.Context().Value(RequestIDKey),
                    )
                    http.Error(w,
                        `{"code":"INTERNAL_ERROR","message":"server error"}`,
                        http.StatusInternalServerError)
                }
            }()
            next.ServeHTTP(w, r)
        })
    }
}
```

---

## 5. CircuitBreakerGuard

Create `middleware/circuit_breaker.go`:

```go
package middleware

import (
    "net/http"

    "circuit-breaker-demo/circuitbreaker"
)

// CircuitBreakerGuard rejects requests with 503 when the given CB is OPEN.
// Useful for expensive routes where we want to fast-fail before body parsing.
//
// Note: this is redundant with the CB wrapping in the gateway — but wrapping
// both layers gives you:
//   1) no wasted CPU parsing a JSON body we're going to reject anyway
//   2) a consistent 503 response at the HTTP edge (before auth, validation, etc.)
//
// Use it for public-facing high-QPS routes; skip it for internal endpoints.
func CircuitBreakerGuard(cb *circuitbreaker.CircuitBreaker) func(http.Handler) http.Handler {
    return func(next http.Handler) http.Handler {
        return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
            if cb.State() == circuitbreaker.StateOpen {
                w.Header().Set("Content-Type", "application/json")
                w.Header().Set("Retry-After", "5")
                w.WriteHeader(http.StatusServiceUnavailable)
                _, _ = w.Write([]byte(`{"code":"CIRCUIT_OPEN","message":"service temporarily unavailable"}`))
                return
            }
            next.ServeHTTP(w, r)
        })
    }
}
```

---

## 6. (Optional) RateLimiter

A tiny token-bucket per client IP. Skip in v1 if you want; add when you need it.

Create `middleware/rate_limit.go`:

```go
package middleware

import (
    "net/http"
    "sync"
    "time"

    "golang.org/x/time/rate"
)

// RateLimiter applies a per-client rate limit. Uses IP as the key — in
// production behind a proxy, read X-Forwarded-For / X-Real-IP instead.
//
// Each client gets their own *rate.Limiter with the given rps + burst. A
// janitor trims idle clients every 10 minutes to bound memory.
type RateLimiter struct {
    mu       sync.Mutex
    visitors map[string]*visitor
    rps      rate.Limit
    burst    int
}

type visitor struct {
    limiter  *rate.Limiter
    lastSeen time.Time
}

func NewRateLimiter(rps float64, burst int) *RateLimiter {
    rl := &RateLimiter{
        visitors: make(map[string]*visitor),
        rps:      rate.Limit(rps),
        burst:    burst,
    }
    go rl.cleanup()
    return rl
}

func (rl *RateLimiter) getLimiter(ip string) *rate.Limiter {
    rl.mu.Lock()
    defer rl.mu.Unlock()
    v, ok := rl.visitors[ip]
    if !ok {
        l := rate.NewLimiter(rl.rps, rl.burst)
        rl.visitors[ip] = &visitor{limiter: l, lastSeen: time.Now()}
        return l
    }
    v.lastSeen = time.Now()
    return v.limiter
}

func (rl *RateLimiter) cleanup() {
    for {
        time.Sleep(10 * time.Minute)
        rl.mu.Lock()
        for ip, v := range rl.visitors {
            if time.Since(v.lastSeen) > 15*time.Minute {
                delete(rl.visitors, ip)
            }
        }
        rl.mu.Unlock()
    }
}

func (rl *RateLimiter) Middleware(next http.Handler) http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        ip := clientIP(r)
        if !rl.getLimiter(ip).Allow() {
            w.Header().Set("Retry-After", "1")
            http.Error(w,
                `{"code":"RATE_LIMITED","message":"too many requests"}`,
                http.StatusTooManyRequests)
            return
        }
        next.ServeHTTP(w, r)
    })
}

func clientIP(r *http.Request) string {
    if fwd := r.Header.Get("X-Forwarded-For"); fwd != "" {
        return fwd
    }
    return r.RemoteAddr
}
```

Add the dep:

```bash
go get golang.org/x/time/rate
```

---

## 7. Chaining helper (avoids deep nesting)

Rather than `A(B(C(D(mux))))`, a small helper makes the chain readable:

```go
// middleware/chain.go
package middleware

import "net/http"

// Chain composes middleware in the order given. The first argument is the
// outermost wrapper — it runs first on the request, last on the response.
//
//   handler := middleware.Chain(mux,
//       middleware.RequestID,
//       middleware.Logger(logger),
//       middleware.Recovery(logger),
//   )
func Chain(h http.Handler, mws ...func(http.Handler) http.Handler) http.Handler {
    // Apply in reverse so the first middleware ends up outermost.
    for i := len(mws) - 1; i >= 0; i-- {
        h = mws[i](h)
    }
    return h
}
```

---

## 8. Sanity checklist

- [ ] `go build ./middleware/...` compiles
- [ ] `RequestID` adds a header to every response (test with `curl -i`)
- [ ] `Logger` prints one line per request with latency + status
- [ ] `Recovery` returns 500 JSON when a handler panics (test with a panic handler)
- [ ] `CircuitBreakerGuard` returns 503 when the CB is OPEN, without calling the next handler

---

## What's next

**Step 9 — Main & Server Wiring.** Everything comes together: load config → build services → build gateway → register routes → wrap in middleware → `ListenAndServe` with graceful shutdown.
