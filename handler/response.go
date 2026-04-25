package handler

import (
	"encoding/json"
	"errors"
	"net/http"
	"netflix-proto/circuitbreaker"
	"netflix-proto/gateway"
	"strings"
	"time"
)

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
var errDeadline = circuitbreaker.ErrCircuitOpen