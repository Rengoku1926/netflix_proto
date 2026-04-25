package middleware

import (
    "context"
    "net/http"

    "github.com/google/uuid"
)

type ctxKey string

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