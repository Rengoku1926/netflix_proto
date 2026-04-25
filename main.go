package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"netflix-proto/circuitbreaker"
	"netflix-proto/config"
	"netflix-proto/gateway"
	"netflix-proto/handler"
	"netflix-proto/middleware"
	"netflix-proto/services"
	"os"
	"os/signal"
	"syscall"
	"time"
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