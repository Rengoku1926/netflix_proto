package handler

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"netflix-proto/gateway"
	"time"
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