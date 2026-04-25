package middleware

import (
	"net/http"
	"netflix-proto/circuitbreaker"
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