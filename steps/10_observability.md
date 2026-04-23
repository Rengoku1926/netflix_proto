# Step 10 — Observability

> You can't tune what you can't see. Structured logs + Prometheus metrics for every circuit breaker.

---

## Goal

By the end of this step you have an `observability` package that:

- Provides a **slog-based** state-change callback that logs every CB transition with level-based severity
- Exports **Prometheus metrics** for state, requests, and transitions
- Exposes `/metrics` over HTTP for scraping
- Hooks everything into the existing `OnStateChange` callback from Step 9

Prometheus will run in **Docker** when we want to actually scrape — never installed on the host.

---

## 1. What to measure

Four pillars of circuit-breaker observability:

| Signal              | Type    | Why                                                        |
| ------------------- | ------- | ---------------------------------------------------------- |
| Current state       | Gauge   | Dashboards show CLOSED=green, OPEN=red instantly           |
| Requests by outcome | Counter | Failure rate = `rate(failure) / rate(total)`               |
| State transitions   | Counter | Alert on "breaker flapping" (many transitions in a window) |
| Rejections          | Counter | Directly measures saved downstream load                    |

Prometheus counters are monotonic integers; gauges can go up or down. State is a gauge because it changes both ways; request counts only go up.

---

## 2. The logger hook

Create `observability/logger.go`:

```go
// Package observability wires circuit breakers to structured logs and
// Prometheus metrics. A single onStateChange callback composition feeds both.
package observability

import (
    "context"
    "log/slog"

    "circuit-breaker-demo/circuitbreaker"
)

// CBLogger returns an OnStateChange callback that writes one structured log
// line per transition. Severity escalates with danger:
//   CLOSED    → INFO
//   HALF-OPEN → INFO
//   OPEN      → WARN (you want alerts on these)
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
```

---

## 3. Prometheus metrics

Add the dependency:

```bash
go get github.com/prometheus/client_golang/prometheus
go get github.com/prometheus/client_golang/prometheus/promhttp
```

Create `observability/metrics.go`:

```go
package observability

import (
    "circuit-breaker-demo/circuitbreaker"

    "github.com/prometheus/client_golang/prometheus"
    "github.com/prometheus/client_golang/prometheus/promhttp"
    "net/http"
)

// Metrics holds every Prometheus collector. Build once at startup, pass
// around, register with the default registry.
type Metrics struct {
    State        *prometheus.GaugeVec
    Requests     *prometheus.CounterVec
    Transitions  *prometheus.CounterVec
    Rejections   *prometheus.CounterVec
}

// NewMetrics constructs all collectors and registers them with the default
// Prometheus registry. Call once at startup.
func NewMetrics() *Metrics {
    m := &Metrics{
        State: prometheus.NewGaugeVec(prometheus.GaugeOpts{
            Namespace: "cb",
            Name:      "state",
            Help:      "Current circuit breaker state: 0=CLOSED 1=OPEN 2=HALF-OPEN",
        }, []string{"breaker"}),

        Requests: prometheus.NewCounterVec(prometheus.CounterOpts{
            Namespace: "cb",
            Name:      "requests_total",
            Help:      "Total requests through the circuit breaker, by outcome",
        }, []string{"breaker", "outcome"}), // outcome: success|failure|rejected

        Transitions: prometheus.NewCounterVec(prometheus.CounterOpts{
            Namespace: "cb",
            Name:      "transitions_total",
            Help:      "Circuit breaker state transitions",
        }, []string{"breaker", "from", "to"}),

        Rejections: prometheus.NewCounterVec(prometheus.CounterOpts{
            Namespace: "cb",
            Name:      "rejections_total",
            Help:      "Requests fast-failed because the breaker was OPEN or HALF-OPEN full",
        }, []string{"breaker"}),
    }

    prometheus.MustRegister(m.State, m.Requests, m.Transitions, m.Rejections)
    return m
}

// Hook returns an OnStateChange callback that updates the state gauge and
// transition counter. Must be combined with manual counter bumps elsewhere
// — Prometheus can't observe Execute() directly, so we poll metrics from
// the breaker (see ScrapeFromBreakers below).
func (m *Metrics) Hook() func(*circuitbreaker.CircuitBreaker, circuitbreaker.State, circuitbreaker.State) {
    return func(cb *circuitbreaker.CircuitBreaker, from, to circuitbreaker.State) {
        m.State.WithLabelValues(cb.Name()).Set(float64(to))
        m.Transitions.WithLabelValues(cb.Name(), from.String(), to.String()).Inc()
    }
}

// Handler returns an http.Handler that serves Prometheus text format at
// /metrics. Mount it on your mux.
func Handler() http.Handler {
    return promhttp.Handler()
}
```

---

## 4. Bridging CB metrics → Prometheus counters

The CB's internal `Metrics()` returns cumulative counters. Prometheus scrapes are pull-based, so we need to report current cumulative values on each scrape. The cleanest pattern is a **custom collector** that reads breaker state lazily when Prometheus asks.

Create `observability/collector.go`:

```go
package observability

import (
    "circuit-breaker-demo/circuitbreaker"

    "github.com/prometheus/client_golang/prometheus"
)

// breakerCollector implements prometheus.Collector. On each scrape it
// reads live metrics from every registered breaker — no background tick,
// no sampling, always current.
type breakerCollector struct {
    breakers []*circuitbreaker.CircuitBreaker

    totalDesc      *prometheus.Desc
    successDesc    *prometheus.Desc
    failureDesc    *prometheus.Desc
    rejectionDesc  *prometheus.Desc
    stateDesc      *prometheus.Desc
}

// RegisterBreakerCollector wires the given breakers as a Prometheus collector.
// Call once, after the gateway is built.
func RegisterBreakerCollector(breakers []*circuitbreaker.CircuitBreaker) {
    c := &breakerCollector{
        breakers: breakers,
        totalDesc:     prometheus.NewDesc("cb_total_requests",  "Total requests observed by the CB",    []string{"breaker"}, nil),
        successDesc:   prometheus.NewDesc("cb_successes_total", "Successful calls through the CB",      []string{"breaker"}, nil),
        failureDesc:   prometheus.NewDesc("cb_failures_total",  "Failed calls through the CB",          []string{"breaker"}, nil),
        rejectionDesc: prometheus.NewDesc("cb_rejections_total","Calls rejected by the CB (fast-fail)", []string{"breaker"}, nil),
        stateDesc:     prometheus.NewDesc("cb_current_state",   "Current state (0=CLOSED 1=OPEN 2=HALF-OPEN)", []string{"breaker"}, nil),
    }
    prometheus.MustRegister(c)
}

func (c *breakerCollector) Describe(ch chan<- *prometheus.Desc) {
    ch <- c.totalDesc
    ch <- c.successDesc
    ch <- c.failureDesc
    ch <- c.rejectionDesc
    ch <- c.stateDesc
}

func (c *breakerCollector) Collect(ch chan<- prometheus.Metric) {
    for _, cb := range c.breakers {
        m := cb.Metrics()
        name := cb.Name()
        ch <- prometheus.MustNewConstMetric(c.totalDesc,     prometheus.CounterValue, float64(m.TotalRequests), name)
        ch <- prometheus.MustNewConstMetric(c.successDesc,   prometheus.CounterValue, float64(m.Successes),     name)
        ch <- prometheus.MustNewConstMetric(c.failureDesc,   prometheus.CounterValue, float64(m.Failures),      name)
        ch <- prometheus.MustNewConstMetric(c.rejectionDesc, prometheus.CounterValue, float64(m.Rejections),    name)
        ch <- prometheus.MustNewConstMetric(c.stateDesc,     prometheus.GaugeValue,   float64(cb.State()),      name)
    }
}
```

Why a custom collector? The naive "inc a counter every call" approach requires passing a metrics object into every `Execute`. A custom collector reads the CB's existing atomic counters on scrape — one integration point, no changes to the breaker itself.

---

## 5. Wiring into main.go

Update `main.go` (around where the gateway is built):

```go
import (
    // ... existing imports ...
    "circuit-breaker-demo/observability"
)

// ... inside main() ...

// Build the state-change callback as the composition of logging + Prom hook.
metrics := observability.NewMetrics()
logHook := observability.CBLogger(logger)
promHook := metrics.Hook()

onStateChange := func(cb *circuitbreaker.CircuitBreaker, from, to circuitbreaker.State) {
    logHook(cb, from, to)
    promHook(cb, from, to)
}

gw := gateway.New(paymentSvc, recoSvc, userSvc, cfg.Breakers, onStateChange)

// Register the custom collector that reads cumulative counters on scrape.
observability.RegisterBreakerCollector(gw.Breakers())

// ... in the mux setup ...
mux.Handle("GET /metrics", observability.Handler())
```

---

## 6. Scraping with Prometheus (in Docker)

Create `deploy/prometheus.yml`:

```yaml
global:
  scrape_interval: 10s

scrape_configs:
  - job_name: circuit-breaker-demo
    static_configs:
      - targets: ["host.docker.internal:8080"]
```

Add a Prometheus service to the `docker-compose.yml` you'll build in Step 12:

```yaml
services:
  prometheus:
    image: prom/prometheus:v2.52.0
    volumes:
      - ./deploy/prometheus.yml:/etc/prometheus/prometheus.yml:ro
    ports:
      - "9090:9090"
```

Then:

```bash
docker compose up prometheus
# → Open http://localhost:9090/graph
# → Query: cb_current_state  /  rate(cb_failures_total[1m])
```

**No Prometheus binary on your Mac — ever.** Docker image, pinned tag.

---

## 7. Useful PromQL queries

```promql
# Current state per breaker
cb_current_state

# Failure rate over the last minute
rate(cb_failures_total[1m])

# Percentage of requests rejected (circuit protecting us)
100 * rate(cb_rejections_total[5m]) / rate(cb_total_requests[5m])

# Alert: breaker has been OPEN for > 2 minutes
cb_current_state == 1
```

Add these to Grafana later — but start with Prom's built-in graph view to verify metrics are flowing.

---

## 8. Sanity checklist

- [ ] `go build ./...` still compiles
- [ ] `curl localhost:8080/metrics | grep cb_` returns metrics for all three breakers
- [ ] Breaking a service triggers both a log line and a `cb_transitions_total` increment
- [ ] Prometheus (dockerized) scrapes the target successfully (Targets page shows UP)

---

## What's next

**Step 11 — Testing Strategy.** Unit tests for the state machine, race-detector-clean concurrent tests, scenario tests that mimic the demo flow, and a coverage target.
