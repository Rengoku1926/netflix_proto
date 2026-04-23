# Step 9 — Main & Server Wiring

> Assemble every piece built so far into a running HTTP server with graceful shutdown.

---

## Goal

By the end of this step `go run .` gives you a live server that:

- Loads config from env vars
- Constructs mock services
- Builds the gateway with per-service circuit breakers
- Registers all HTTP routes on `http.ServeMux`
- Wraps the mux in the middleware chain
- Listens on the configured port with proper timeouts
- Shuts down cleanly on SIGTERM/SIGINT

---

## 1. Why graceful shutdown matters

When Kubernetes sends SIGTERM to a pod (on a rolling deploy, for instance), the process has ~30 seconds to finish in-flight requests before SIGKILL arrives. If you just `os.Exit(0)` you'll drop requests mid-flight — users see 502s.

Go's `http.Server.Shutdown(ctx)` stops accepting new connections and waits for active ones to finish up to the context deadline. Combined with a signal listener, this is ~20 lines of code for a clean shutdown.

---

## 2. The full main.go

Replace the stub from Step 1:

```go
// main.go
//
// Bootstrap:
//   1. Load config from environment
//   2. Construct structured logger (slog)
//   3. Build mock services (later: swap for real DB-backed ones)
//   4. Build the gateway, which wires services to per-service circuit breakers
//   5. Register HTTP handlers on a ServeMux
//   6. Wrap the mux in the middleware chain
//   7. Start the server in a goroutine; block on a shutdown signal
//   8. On SIGTERM/SIGINT, gracefully drain in-flight requests
package main

import (
    "context"
    "errors"
    "log/slog"
    "net/http"
    "os"
    "os/signal"
    "syscall"
    "time"

    "circuit-breaker-demo/circuitbreaker"
    "circuit-breaker-demo/config"
    "circuit-breaker-demo/gateway"
    "circuit-breaker-demo/handler"
    "circuit-breaker-demo/middleware"
    "circuit-breaker-demo/services"
)

func main() {
    cfg := config.Load()

    // JSON logs on stdout — parse-friendly for container log collectors.
    logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
        Level: slog.LevelInfo,
    }))
    slog.SetDefault(logger)

    // --- Services ---------------------------------------------------------
    paymentSvc := services.NewPaymentService()
    recoSvc := services.NewRecommendationService()
    userSvc := services.NewUserService()

    // --- Gateway ----------------------------------------------------------
    onStateChange := func(cb *circuitbreaker.CircuitBreaker, from, to circuitbreaker.State) {
        level := slog.LevelInfo
        if to == circuitbreaker.StateOpen {
            level = slog.LevelWarn
        }
        logger.Log(context.Background(), level, "circuit_breaker_transition",
            "breaker", cb.Name(),
            "from", from.String(),
            "to", to.String(),
        )
    }
    gw := gateway.New(paymentSvc, recoSvc, userSvc, cfg.Breakers, onStateChange)

    // --- Handlers ---------------------------------------------------------
    paymentH := handler.NewPaymentHandler(gw)
    recoH := handler.NewRecoHandler(gw)
    userH := handler.NewUserHandler(gw)
    healthH := handler.NewHealthHandler(gw)

    // --- Routes -----------------------------------------------------------
    mux := http.NewServeMux()
    mux.HandleFunc("POST /payments", paymentH.Create)
    mux.HandleFunc("GET /recommendations/{userID}", recoH.Get)
    mux.HandleFunc("GET /users/{userID}", userH.Get)
    mux.HandleFunc("GET /health", healthH.Liveness)
    mux.HandleFunc("GET /health/circuit-breakers", healthH.CircuitBreakers)

    // --- Middleware chain -------------------------------------------------
    root := middleware.Chain(mux,
        middleware.RequestID,
        middleware.Logger(logger),
        middleware.Recovery(logger),
    )

    // --- Server -----------------------------------------------------------
    srv := &http.Server{
        Addr:         ":" + cfg.Server.Port,
        Handler:      root,
        ReadTimeout:  cfg.Server.ReadTimeout,
        WriteTimeout: cfg.Server.WriteTimeout,
        IdleTimeout:  cfg.Server.IdleTimeout,
    }

    // Start the server in a goroutine so main can block on shutdown signals.
    go func() {
        logger.Info("server_listening", "addr", srv.Addr)
        if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
            logger.Error("server_error", "err", err.Error())
            os.Exit(1)
        }
    }()

    // --- Graceful shutdown ------------------------------------------------
    stop := make(chan os.Signal, 1)
    signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
    <-stop

    logger.Info("shutdown_initiated")
    ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
    defer cancel()

    if err := srv.Shutdown(ctx); err != nil {
        logger.Error("shutdown_error", "err", err.Error())
    } else {
        logger.Info("shutdown_complete")
    }
}
```

### Key mechanics

- **`signal.Notify(stop, ...)`** blocks until SIGINT (Ctrl-C) or SIGTERM (Docker/K8s stop) arrives.
- **`srv.Shutdown(ctx)`** stops accepting new connections and waits for active ones. The 20s deadline must be ≤ your orchestrator's grace period (K8s default is 30s).
- **`http.ErrServerClosed`** is what `ListenAndServe` returns when `Shutdown` is called — it's not really an error, so we filter it.

---

## 3. Try it end-to-end

```bash
# Terminal 1
go run .
# → {"time":"...","level":"INFO","msg":"server_listening","addr":":8080"}
```

```bash
# Terminal 2 — successful payment
curl -sD- -X POST localhost:8080/payments \
  -H 'Content-Type: application/json' \
  -d '{"user_id":1,"amount":9.99,"currency":"USD"}'

# Recommendations (healthy)
curl -s localhost:8080/recommendations/1 | jq

# Health
curl -s localhost:8080/health/circuit-breakers | jq
```

Now simulate a payment outage. You can wire a debug endpoint (see next section) or simply rebuild with a hook that breaks the service after N seconds — whichever you prefer for demos.

---

## 4. Optional: debug endpoints for demoing

Add a handful of admin routes to manually break/repair services during demos. Keep them behind a feature flag or a different port in production.

Create `handler/debug.go`:

```go
package handler

import (
    "net/http"
    "strconv"

    "circuit-breaker-demo/services"
)

// DebugHandler exposes service-injection controls for demos.
// DO NOT MOUNT THIS IN PRODUCTION without auth.
type DebugHandler struct {
    payment *services.PaymentService
    user    *services.UserService
    reco    *services.RecommendationService
}

func NewDebugHandler(p *services.PaymentService, u *services.UserService, r *services.RecommendationService) *DebugHandler {
    return &DebugHandler{payment: p, user: u, reco: r}
}

func (h *DebugHandler) BreakPayment(w http.ResponseWriter, r *http.Request)  { h.payment.Break();  writeJSON(w, 200, map[string]string{"status": "broken"}) }
func (h *DebugHandler) RepairPayment(w http.ResponseWriter, r *http.Request) { h.payment.Repair(); writeJSON(w, 200, map[string]string{"status": "healthy"}) }
func (h *DebugHandler) BreakUser(w http.ResponseWriter, r *http.Request)     { h.user.Break();    writeJSON(w, 200, map[string]string{"status": "broken"}) }
func (h *DebugHandler) RepairUser(w http.ResponseWriter, r *http.Request)    { h.user.Repair();   writeJSON(w, 200, map[string]string{"status": "healthy"}) }

func (h *DebugHandler) SetRecoFailRate(w http.ResponseWriter, r *http.Request) {
    rate, err := strconv.ParseFloat(r.URL.Query().Get("rate"), 64)
    if err != nil {
        writeError(w, 400, "BAD_RATE", "?rate=0.5", "")
        return
    }
    h.reco.SetFailRate(rate)
    writeJSON(w, 200, map[string]any{"fail_rate": rate})
}
```

Register in `main.go` (concrete service pointers needed for the Break/Repair methods, so cast safely):

```go
debugH := handler.NewDebugHandler(paymentSvc, userSvc, recoSvc)
mux.HandleFunc("POST /debug/payment/break",  debugH.BreakPayment)
mux.HandleFunc("POST /debug/payment/repair", debugH.RepairPayment)
mux.HandleFunc("POST /debug/user/break",     debugH.BreakUser)
mux.HandleFunc("POST /debug/user/repair",    debugH.RepairUser)
mux.HandleFunc("POST /debug/reco/fail-rate", debugH.SetRecoFailRate)
```

---

## 5. Demo script (try this live)

```bash
# Start server
go run .

# 1. Healthy payment
curl -sX POST localhost:8080/payments -H 'Content-Type: application/json' \
  -d '{"user_id":1,"amount":9.99,"currency":"USD"}'
# → 201 created

# 2. Break the payment service
curl -sX POST localhost:8080/debug/payment/break
# → {"status":"broken"}

# 3. Hammer payments — watch the breaker trip after 3 failures
for i in {1..6}; do
  curl -sw '\n' -X POST localhost:8080/payments -H 'Content-Type: application/json' \
    -d '{"user_id":1,"amount":9.99,"currency":"USD"}'
done
# → first 3: 502 UPSTREAM_ERROR
# → next 3:  503 CIRCUIT_OPEN

# 4. Dashboard
curl -s localhost:8080/health/circuit-breakers | jq
# → payment-service: OPEN, others: CLOSED (isolation!)

# 5. Repair + wait 5s for OpenTimeout
curl -sX POST localhost:8080/debug/payment/repair
sleep 5

# 6. Next request probes and closes the circuit
curl -sX POST localhost:8080/payments -H 'Content-Type: application/json' \
  -d '{"user_id":1,"amount":9.99,"currency":"USD"}'
# → 201 — HALF-OPEN → CLOSED
```

---

## 6. Sanity checklist

- [ ] `go build .` and `go run .` succeed
- [ ] Server logs `server_listening` on startup
- [ ] SIGINT (Ctrl-C) triggers `shutdown_initiated` + `shutdown_complete` within 20s
- [ ] A broken service trips the matching CB; other CBs remain CLOSED
- [ ] `/health/circuit-breakers` reflects the current state accurately

---

## What's next

**Step 10 — Observability.** Wire structured log lines on every CB transition, and expose Prometheus metrics so dashboards can graph state + failure rate over time.
