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