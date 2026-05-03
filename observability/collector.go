package observability

import (
	"net/http"
	"netflix-proto/circuitbreaker"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

type breakerCollector struct {
	breakers      []*circuitbreaker.CircuitBreaker
	totalDesc     *prometheus.Desc
	successDesc   *prometheus.Desc
	failureDesc   *prometheus.Desc
	rejectionDesc *prometheus.Desc
	stateDesc     *prometheus.Desc
	stateChanges  *prometheus.Desc
}

// RegisterBreakerCollector wires the given breakers as a Prometheus collector.
// Call once, after the gateway is built.
func RegisterBreakerCollector(breakers []*circuitbreaker.CircuitBreaker) {
	c := &breakerCollector{
		breakers:      breakers,
		totalDesc:     prometheus.NewDesc("cb_total_requests", "Total requests observed by the CB", []string{"breaker"}, nil),
		successDesc:   prometheus.NewDesc("cb_successes_total", "Successful calls through the CB", []string{"breaker"}, nil),
		failureDesc:   prometheus.NewDesc("cb_failures_total", "Failed calls through the CB", []string{"breaker"}, nil),
		rejectionDesc: prometheus.NewDesc("cb_rejections_total", "Calls rejected by the CB (fast-fail)", []string{"breaker"}, nil),
		stateDesc:     prometheus.NewDesc("cb_current_state", "Current state (0=CLOSED 1=OPEN 2=HALF-OPEN)", []string{"breaker"}, nil),
		stateChanges:  prometheus.NewDesc("cb_state_changes_total", "Total state transitions by the CB", []string{"breaker"}, nil),
	}
	prometheus.MustRegister(c)
}

func (c *breakerCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.totalDesc
	ch <- c.successDesc
	ch <- c.failureDesc
	ch <- c.rejectionDesc
	ch <- c.stateDesc
	ch <- c.stateChanges
}

func Handler() http.Handler {
	return promhttp.Handler()
}

func (c *breakerCollector) Collect(ch chan<- prometheus.Metric) {
	for _, cb := range c.breakers {
		m := cb.Metrics()
		name := cb.Name()
		ch <- prometheus.MustNewConstMetric(c.totalDesc, prometheus.CounterValue, float64(m.TotalRequests), name)
		ch <- prometheus.MustNewConstMetric(c.successDesc, prometheus.CounterValue, float64(m.Successes), name)
		ch <- prometheus.MustNewConstMetric(c.failureDesc, prometheus.CounterValue, float64(m.Failures), name)
		ch <- prometheus.MustNewConstMetric(c.rejectionDesc, prometheus.CounterValue, float64(m.Rejections), name)
		ch <- prometheus.MustNewConstMetric(c.stateDesc, prometheus.GaugeValue, float64(cb.State()), name)
		ch <- prometheus.MustNewConstMetric(c.stateChanges, prometheus.CounterValue, float64(m.StateChanges), name)
	}
}
