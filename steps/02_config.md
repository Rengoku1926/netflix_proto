# Step 2 — Configuration

> All knobs in one typed struct, loaded from environment variables, with sensible defaults.

---

## Goal

By the end of this step you have a `config` package that:

- Defines a typed `Config` struct covering server + per-breaker settings
- Reads values from environment variables (with defaults when unset)
- Parses durations (`"5s"`) and integers safely — bad values fall back to defaults, never panic
- Can be swapped for a YAML or flags loader later without changing any caller

---

## 1. Why config first, before any logic?

Two reasons:

1. **No magic numbers.** The circuit breaker code will reference `cfg.Breakers.Payment.FailureThreshold` — never a literal `3`. Every tuning knob is discoverable in one place.
2. **12-factor app friendliness.** Reading from env vars means the same binary runs in dev, staging, and prod — only the environment changes. No code rebuild to retune a threshold.

We use env vars as the **source of truth** and a YAML file purely as a reference/default dump. This is deliberate: env vars win in containers, CI, and K8s where config is injected at runtime.

---

## 2. The config struct

Create `config/config.go`:

```go
// Package config loads application settings from environment variables.
//
// Design: one typed Config struct, loaded once at process start, passed
// by value into the gateway + server. Never read os.Getenv outside this
// package — it keeps config access testable and centralised.
package config

import (
    "os"
    "strconv"
    "time"
)

// Config is the top-level config for the whole application.
type Config struct {
    Server   ServerConfig
    Breakers BreakersConfig
}

// ServerConfig — HTTP server tuning.
type ServerConfig struct {
    Port         string        // PORT, default "8080"
    ReadTimeout  time.Duration // READ_TIMEOUT, default "10s"
    WriteTimeout time.Duration // WRITE_TIMEOUT, default "30s"
    IdleTimeout  time.Duration // IDLE_TIMEOUT, default "120s"
}

// BreakersConfig groups per-service CB configs.
// Each downstream service gets independent tuning — see Gateway §9.
type BreakersConfig struct {
    Payment        BreakerConfig
    Recommendation BreakerConfig
    User           BreakerConfig
}

// BreakerConfig is the tuning for a single circuit breaker instance.
type BreakerConfig struct {
    FailureThreshold  int           // consecutive failures before OPEN
    SuccessThreshold  int           // consecutive successes in HALF-OPEN before CLOSED
    OpenTimeout       time.Duration // how long to stay OPEN before allowing a probe
    MaxHalfOpenProbes int           // max concurrent probes during HALF-OPEN
}
```

---

## 3. The loader

Still in `config/config.go`:

```go
// Load reads every config value from the environment, applying defaults
// when the variable is unset or malformed. Never returns an error — a
// misconfigured env var silently falls back to the default so the process
// can still boot. If you want strict validation, add a Validate() method.
func Load() Config {
    return Config{
        Server: ServerConfig{
            Port:         getEnv("PORT", "8080"),
            ReadTimeout:  getDuration("READ_TIMEOUT", 10*time.Second),
            WriteTimeout: getDuration("WRITE_TIMEOUT", 30*time.Second),
            IdleTimeout:  getDuration("IDLE_TIMEOUT", 120*time.Second),
        },
        Breakers: BreakersConfig{
            Payment: BreakerConfig{
                FailureThreshold:  getInt("PAYMENT_FAILURE_THRESHOLD", 3),
                SuccessThreshold:  getInt("PAYMENT_SUCCESS_THRESHOLD", 2),
                OpenTimeout:       getDuration("PAYMENT_OPEN_TIMEOUT", 5*time.Second),
                MaxHalfOpenProbes: getInt("PAYMENT_MAX_PROBES", 1),
            },
            Recommendation: BreakerConfig{
                FailureThreshold:  getInt("RECO_FAILURE_THRESHOLD", 5),
                SuccessThreshold:  getInt("RECO_SUCCESS_THRESHOLD", 2),
                OpenTimeout:       getDuration("RECO_OPEN_TIMEOUT", 8*time.Second),
                MaxHalfOpenProbes: getInt("RECO_MAX_PROBES", 2),
            },
            User: BreakerConfig{
                FailureThreshold:  getInt("USER_FAILURE_THRESHOLD", 3),
                SuccessThreshold:  getInt("USER_SUCCESS_THRESHOLD", 1),
                OpenTimeout:       getDuration("USER_OPEN_TIMEOUT", 6*time.Second),
                MaxHalfOpenProbes: getInt("USER_MAX_PROBES", 1),
            },
        },
    }
}

// --- tiny helpers ---------------------------------------------------------

func getEnv(key, def string) string {
    if v := os.Getenv(key); v != "" {
        return v
    }
    return def
}

func getInt(key string, def int) int {
    if v := os.Getenv(key); v != "" {
        if n, err := strconv.Atoi(v); err == nil {
            return n
        }
    }
    return def
}

func getDuration(key string, def time.Duration) time.Duration {
    if v := os.Getenv(key); v != "" {
        if d, err := time.ParseDuration(v); err == nil {
            return d
        }
    }
    return def
}
```

### Why these defaults?

| Service | Fail Thr | Succ Thr | Open | Reason |
|---|---|---|---|---|
| payment | 3 | 2 | 5s | Money is critical — trip fast, probe carefully |
| recommendation | 5 | 2 | 8s | Best-effort; stale recs are fine, so be forgiving |
| user (auth) | 3 | 1 | 6s | Close on first probe — auth must recover quickly |

See `ARCHITECTURE.md §9` for the full rationale.

---

## 4. A reference YAML (optional — human-readable defaults)

`config/config.yaml` — **not parsed by the loader**, purely a reference dump so a newcomer can see all defaults in one glance without reading Go code.

```yaml
server:
  port: "8080"
  read_timeout:  "10s"
  write_timeout: "30s"
  idle_timeout:  "120s"

breakers:
  payment:        { failure_threshold: 3, success_threshold: 2, open_timeout: "5s", max_half_open_probes: 1 }
  recommendation: { failure_threshold: 5, success_threshold: 2, open_timeout: "8s", max_half_open_probes: 2 }
  user:           { failure_threshold: 3, success_threshold: 1, open_timeout: "6s", max_half_open_probes: 1 }
```

---

## 5. Using the config in main.go

Update the stub `main.go`:

```go
package main

import (
    "fmt"

    "circuit-breaker-demo/config"
)

func main() {
    cfg := config.Load()
    fmt.Printf("config loaded — payment fail threshold = %d\n",
        cfg.Breakers.Payment.FailureThreshold)
}
```

Test both default and override paths:

```bash
go run .
# → config loaded — payment fail threshold = 3

PAYMENT_FAILURE_THRESHOLD=10 go run .
# → config loaded — payment fail threshold = 10
```

---

## 6. Sanity checklist

- [ ] `config/config.go` compiles with `go build ./config/...`
- [ ] Defaults apply when no env vars are set
- [ ] Env override works (`PAYMENT_FAILURE_THRESHOLD=10`)
- [ ] Malformed values (`PAYMENT_FAILURE_THRESHOLD=not-a-number`) fall back to defaults silently

---

## What's next

**Step 3 — Circuit Breaker Core Engine.** The heart of the project. We implement the three-state machine (CLOSED → OPEN → HALF-OPEN), the atomic/mutex concurrency model, and the `Execute(fn)` entry point. This is the single most important file in the codebase — everything else is glue around it.
