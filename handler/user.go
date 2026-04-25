package handler

import (
	"context"
	"net/http"
	"netflix-proto/gateway"
	"strconv"
	"time"
)

type UserHandler struct {
    gw *gateway.Gateway
}

func NewUserHandler(gw *gateway.Gateway) *UserHandler {
    return &UserHandler{gw: gw}
}

func (h *UserHandler) Get(w http.ResponseWriter, r *http.Request) {
    userID, err := strconv.Atoi(r.PathValue("userID"))
    if err != nil || userID <= 0 {
        writeError(w, http.StatusBadRequest, "INVALID_USER_ID",
            "userID must be a positive integer", "")
        return
    }

    ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
    defer cancel()

    profile, err := h.gw.GetUser(ctx, userID)
    if err != nil {
        writeGatewayError(w, err)
        return
    }
    writeJSON(w, http.StatusOK, profile)
}