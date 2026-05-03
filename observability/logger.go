package observability

import (
	"context"
	"log/slog"
	"netflix-proto/circuitbreaker"
)

func CBLogger(logger *slog.Logger) func(*circuitbreaker.CircuitBreaker, circuitbreaker.State, circuitbreaker.State) {
	return func(cb *circuitbreaker.CircuitBreaker, from, to circuitbreaker.State) {
		level := slog.LevelInfo
		if to == circuitbreaker.StateOpen {
			level = slog.LevelWarn
		}

		m := cb.Metrics()
        logger.Log(context.Background(), level, "circuit_breaker_transition",
            "breaker", cb.Name(),
            "from", from.String(),
            "to", to.String(),
            "total_requests", m.TotalRequests,
            "successes", m.Successes,
            "failures", m.Failures,
            "rejections", m.Rejections,
            "state_changes", m.StateChanges,
        )
	}
}