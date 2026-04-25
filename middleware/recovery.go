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