package handler

import (
	"net/http"
	"netflix-proto/gateway"
	"time"
)

type HealthHandler struct {
    gw *gateway.Gateway
}

func NewHealthHandler(gw *gateway.Gateway) *HealthHandler {
    return &HealthHandler{gw: gw}
}

// Liveness — GET /health. Returns 200 as long as the process is alive.
// Kubernetes hits this every periodSeconds for the livenessProbe.
func (h *HealthHandler) Liveness(w http.ResponseWriter, r *http.Request) {
    writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// breakerSnapshot is the per-breaker shape inside /health/circuit-breakers.
type breakerSnapshot struct {
    Name          string `json:"name"`
    State         string `json:"state"`
    TotalRequests int64  `json:"total_requests"`
    Successes     int64  `json:"successes"`
    Failures      int64  `json:"failures"`
    Rejections    int64  `json:"rejections"`
    StateChanges  int64  `json:"state_changes"`
}

type healthResponse struct {
    Timestamp time.Time         `json:"timestamp"`
    Breakers  []breakerSnapshot `json:"breakers"`
}

// CircuitBreakers — GET /health/circuit-breakers. Used by dashboards,
// alerting, and the load balancer to route around pods with tripped CBs.
func (h *HealthHandler) CircuitBreakers(w http.ResponseWriter, r *http.Request) {
    cbs := h.gw.Breakers()
    out := make([]breakerSnapshot, 0, len(cbs))
    for _, cb := range cbs {
        m := cb.Metrics()
        out = append(out, breakerSnapshot{
            Name:          cb.Name(),
            State:         cb.State().String(),
            TotalRequests: m.TotalRequests,
            Successes:     m.Successes,
            Failures:      m.Failures,
            Rejections:    m.Rejections,
            StateChanges:  m.StateChanges,
        })
    }
    writeJSON(w, http.StatusOK, healthResponse{
        Timestamp: time.Now().UTC(),
        Breakers:  out,
    })
}