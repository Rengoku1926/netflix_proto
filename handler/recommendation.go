package handler

import (
	"context"
	"net/http"
	"netflix-proto/gateway"
	"strconv"
	"time"
)

type RecoHandler struct {
    gw *gateway.Gateway
}

func NewRecoHandler(gw *gateway.Gateway) *RecoHandler {
    return &RecoHandler{gw: gw}
}

type recoResponse struct {
    UserID          int      `json:"user_id"`
    Recommendations []string `json:"recommendations"`
    Degraded        bool     `json:"degraded"` // true when served from fallback
}

// Get handles GET /recommendations/{userID}.
func (h *RecoHandler) Get(w http.ResponseWriter, r *http.Request) {
    userID, err := strconv.Atoi(r.PathValue("userID"))
    if err != nil || userID <= 0 {
        writeError(w, http.StatusBadRequest, "INVALID_USER_ID",
            "userID must be a positive integer", "")
        return
    }

    ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
    defer cancel()

    // Reco is best-effort — we always use the fallback-aware variant so
    // the client never sees a 503 for recommendations.
    recs, degraded := h.gw.GetRecommendationsWithFallback(ctx, userID)
    writeJSON(w, http.StatusOK, recoResponse{
        UserID:          userID,
        Recommendations: recs,
        Degraded:        degraded,
    })
}