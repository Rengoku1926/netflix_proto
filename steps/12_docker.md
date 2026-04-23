# Step 12 — Dockerization

> Everything runs in Docker. Nothing is installed on the host except Docker itself.

---

## Goal

By the end of this step you have:

- A **multi-stage `Dockerfile`** that produces a minimal production image for the Go app
- A **`docker-compose.yml`** that wires the app, Postgres, and Prometheus together
- A single `docker compose up` that starts the full stack — no manual `go run`, no local DB, no local Prometheus
- Health checks so dependent services wait for their upstreams to be ready

---

## 1. Why multi-stage

A plain `FROM golang` image weighs ~800 MB. The Go toolchain is needed only to compile; it must not ship to production. A two-stage build solves this:

```
Stage 1 (builder)     Stage 2 (runtime)
──────────────────    ─────────────────────────────
golang:1.23-alpine    gcr.io/distroless/static-debian12
  go mod download       COPY --from=builder /app/server .
  go build -o server    ENTRYPOINT ["/server"]
```

The final image contains only the statically-linked binary. It has no shell, no package manager, and no attack surface beyond the binary itself.

---

## 2. Dockerfile

Create `Dockerfile` at the project root:

```dockerfile
# ── Stage 1: build ────────────────────────────────────────────────────────────
FROM golang:1.23-alpine AS builder

WORKDIR /src

# Cache dependency downloads separately from source code.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 GOOS=linux go build \
    -ldflags="-s -w" \
    -o /app/server \
    ./cmd/server

# ── Stage 2: runtime ──────────────────────────────────────────────────────────
FROM gcr.io/distroless/static-debian12

COPY --from=builder /app/server /server

EXPOSE 8080

ENTRYPOINT ["/server"]
```

Key flags:
- `CGO_ENABLED=0` — produces a fully static binary with no libc dependency
- `-ldflags="-s -w"` — strips debug info and DWARF tables, shaving ~30% off binary size
- `distroless/static` — no shell, no apt, no libc; smallest possible attack surface

---

## 3. docker-compose.yml

Create `docker-compose.yml` at the project root:

```yaml
version: "3.9"

services:

  # ── PostgreSQL ──────────────────────────────────────────────────────────────
  db:
    image: postgres:16-alpine
    environment:
      POSTGRES_DB:       circuit_demo
      POSTGRES_USER:     demo
      POSTGRES_PASSWORD: demo
    ports:
      - "5432:5432"
    volumes:
      - pg_data:/var/lib/postgresql/data
    healthcheck:
      test: ["CMD-SHELL", "pg_isready -U demo -d circuit_demo"]
      interval: 5s
      timeout: 3s
      retries: 10

  # ── App ─────────────────────────────────────────────────────────────────────
  app:
    build: .
    ports:
      - "8080:8080"
    environment:
      DB_DSN:      "postgres://demo:demo@db:5432/circuit_demo?sslmode=disable"
      LOG_LEVEL:   "info"
      PORT:        "8080"
    depends_on:
      db:
        condition: service_healthy
    healthcheck:
      test: ["CMD-SHELL", "wget -qO- http://localhost:8080/health || exit 1"]
      interval: 10s
      timeout: 3s
      retries: 5

  # ── Prometheus ──────────────────────────────────────────────────────────────
  prometheus:
    image: prom/prometheus:v2.52.0
    volumes:
      - ./prometheus.yml:/etc/prometheus/prometheus.yml:ro
    ports:
      - "9090:9090"
    depends_on:
      app:
        condition: service_healthy

volumes:
  pg_data:
```

`condition: service_healthy` makes each service wait for its upstream's health check to pass before starting — no more "app crashes on startup because Postgres isn't ready" races.

---

## 4. Prometheus scrape config

Create `prometheus.yml` at the project root:

```yaml
global:
  scrape_interval: 5s

scrape_configs:
  - job_name: circuit_breaker_demo
    static_configs:
      - targets: ["app:8080"]
    metrics_path: /metrics
```

Prometheus reaches the app over Docker's internal network using the service name `app`, not `localhost`.

---

## 5. Environment variable wiring

The app must read configuration from environment variables so Docker can inject them at runtime without rebuilding the image. In `config/config.go`, ensure you load from the environment:

```go
package config

import (
    "os"
    "time"
)

type AppConfig struct {
    Port     string
    DBDSN    string
    LogLevel string
    Breakers BreakersConfig
}

func Load() AppConfig {
    return AppConfig{
        Port:     getEnv("PORT", "8080"),
        DBDSN:    getEnv("DB_DSN", "postgres://demo:demo@localhost:5432/circuit_demo?sslmode=disable"),
        LogLevel: getEnv("LOG_LEVEL", "info"),
        Breakers: defaultBreakers(),
    }
}

func getEnv(key, fallback string) string {
    if v, ok := os.LookupEnv(key); ok {
        return v
    }
    return fallback
}

func defaultBreakers() BreakersConfig {
    cb := BreakerConfig{
        FailureThreshold:  5,
        SuccessThreshold:  2,
        OpenTimeout:       10 * time.Second,
        MaxHalfOpenProbes: 2,
    }
    return BreakersConfig{Payment: cb, Recommendation: cb, User: cb}
}
```

---

## 6. `.dockerignore`

Create `.dockerignore` to keep the build context small and avoid leaking secrets:

```
.git
*.md
coverage.out
*.test
.env
.env.*
docker-compose*.yml
prometheus.yml
```

---

## 7. Running the stack

```bash
# Build and start everything (detached)
docker compose up --build -d

# Follow app logs
docker compose logs -f app

# Tail all services
docker compose logs -f

# Stop and remove containers (keeps the pg_data volume)
docker compose down

# Full teardown including the volume
docker compose down -v
```

After `docker compose up`, the three endpoints are:

| Endpoint                        | What                        |
| ------------------------------- | --------------------------- |
| `http://localhost:8080/health`  | App liveness check          |
| `http://localhost:8080/metrics` | Prometheus scrape target    |
| `http://localhost:9090`         | Prometheus UI               |

---

## 8. Verifying the build

```bash
# Check the final image size
docker images circuit-breaker-demo_app

# Confirm there is no shell in the image
docker run --rm --entrypoint sh circuit-breaker-demo_app -c "echo hi" 2>&1
# Expected: exec: "sh": executable file not found
```

A healthy build produces an image under 15 MB.

---

## 9. Sanity checklist

- [ ] `docker compose up --build` completes without errors
- [ ] `curl http://localhost:8080/health` returns `200 OK`
- [ ] `curl http://localhost:8080/metrics` returns Prometheus exposition text
- [ ] Prometheus UI at `http://localhost:9090` shows `circuit_breaker_demo` as a target with state `UP`
- [ ] `docker images` shows the app image under 15 MB
- [ ] `docker compose down -v && docker compose up --build` (cold start) still works

---

## What's next

**Step 13 — Load testing with k6.** Drive realistic traffic through the stack inside Docker, watch the circuit breakers trip and recover in real time on the Prometheus dashboard, and tune thresholds based on observed latency percentiles.
