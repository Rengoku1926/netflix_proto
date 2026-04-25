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