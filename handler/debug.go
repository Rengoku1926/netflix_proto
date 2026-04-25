package handler

import (
	"net/http"
	"netflix-proto/services"
	"strconv"
)

// DebugHandler exposes service-injection controls for demos.
// DO NOT MOUNT THIS IN PRODUCTION without auth.
type DebugHandler struct {
    payment *services.PaymentService
    user    *services.UserService
    reco    *services.RecommendationService
}

func NewDebugHandler(p *services.PaymentService, u *services.UserService, r *services.RecommendationService) *DebugHandler {
    return &DebugHandler{payment: p, user: u, reco: r}
}

func (h *DebugHandler) BreakPayment(w http.ResponseWriter, r *http.Request)  { h.payment.Break();  writeJSON(w, 200, map[string]string{"status": "broken"}) }
func (h *DebugHandler) RepairPayment(w http.ResponseWriter, r *http.Request) { h.payment.Repair(); writeJSON(w, 200, map[string]string{"status": "healthy"}) }
func (h *DebugHandler) BreakUser(w http.ResponseWriter, r *http.Request)     { h.user.Break();    writeJSON(w, 200, map[string]string{"status": "broken"}) }
func (h *DebugHandler) RepairUser(w http.ResponseWriter, r *http.Request)    { h.user.Repair();   writeJSON(w, 200, map[string]string{"status": "healthy"}) }

func (h *DebugHandler) SetRecoFailRate(w http.ResponseWriter, r *http.Request) {
    rate, err := strconv.ParseFloat(r.URL.Query().Get("rate"), 64)
    if err != nil {
        writeError(w, 400, "BAD_RATE", "?rate=0.5", "")
        return
    }
    h.reco.SetFailRate(rate)
    writeJSON(w, 200, map[string]any{"fail_rate": rate})
}