# Step 1 — Project Setup

> Lay the foundation. No logic yet — just the skeleton that every later step fills in.

---

## Goal

By the end of this step you have:

- An initialized Go module
- The full folder scaffold matching `ARCHITECTURE.md §3`
- A working `go run .` that prints "hello" (proves toolchain works)
- Docker installed and verified (we'll need it soon — **never run Redis/Postgres locally, always via Docker**)

---

## 1. Prerequisites

| Tool              | Version    | Why                                                                                  |
| ----------------- | ---------- | ------------------------------------------------------------------------------------ |
| Go                | 1.22+      | We use `http.ServeMux` method-style routes (`"POST /payments"`) which landed in 1.22 |
| Docker Desktop    | any recent | For dockerized Redis/Postgres in later steps                                         |
| `make` (optional) | any        | Convenience for repeated commands                                                    |

Verify:

```bash
go version          # should print go1.22+
docker --version    # should print Docker version 24+
```

---

## 2. Initialize the module

```bash
cd ~/Development/personal/blogs/netflix_proto
go mod init circuit-breaker-demo
```

This creates `go.mod`. The module name `circuit-breaker-demo` is what we import in our internal packages (e.g. `import "circuit-breaker-demo/circuitbreaker"`).

---

## 3. Folder scaffold

Create the full directory tree up front. Empty folders won't compile — add a `.gitkeep` or a stub file to each so the structure survives `git add`.

```bash
mkdir -p circuitbreaker services gateway config handler middleware \
         repository/memory observability
```

Expected layout:

```
netflix_proto/
├── ARCHITECTURE.md
├── go.mod
├── main.go                   # we'll create this below
│
├── circuitbreaker/           # core state machine
├── services/                 # mock downstream services
├── gateway/                  # wires services ↔ circuit breakers
├── config/                   # env-var loader
├── handler/                  # HTTP handlers
├── middleware/               # HTTP middleware chain
├── repository/
│   └── memory/               # in-memory repos (dev/test)
└── observability/            # logging + metrics hooks
```

### Why this layout?

- **Flat package tree, not deeply nested.** Go idioms prefer `circuitbreaker/breaker.go` over `internal/pkg/circuitbreaker/...`. Easier to import, easier to reason about.
- **One concept per package.** `circuitbreaker` has nothing to do with HTTP. `handler` has nothing to do with persistence. This separation is what lets us unit-test the state machine without spinning up a server.
- **`repository/memory` as a sub-package.** Later we'll add `repository/postgres` (dockerized). The parent `repository/` package owns the interfaces; children are implementations.

---

## 4. A stub main.go (proves the module compiles)

Create `main.go` at the project root:

```go
// main.go
//
// Entry point. Right now it's a placeholder — in Step 9 we'll wire the
// full server here (config load → services → gateway → HTTP mux → listen).
package main

import "fmt"

func main() {
    fmt.Println("circuit-breaker-demo: scaffold ready")
}
```

Run it:

```bash
go run .
# → circuit-breaker-demo: scaffold ready
```

If that prints, the toolchain is healthy and we're ready to build.

---

## 5. Docker baseline

Later steps will need Redis (for the distributed-CB extension) and Postgres (for the real repository implementation). **We will always run these in Docker — never install them on the host.**

Reason: host installs drift (different Postgres versions per dev machine), clutter the system, and can't be torn down cleanly. A `docker-compose.yml` pins exact versions and resets cleanly with `docker compose down -v`.

For now, just verify Docker works:

```bash
docker run --rm hello-world
```

We'll write the actual `docker-compose.yml` in **Step 12 — Dockerization**.

---

## 6. .gitignore

Even if this isn't yet a git repo, add a `.gitignore` so it's ready:

```gitignore
# Go build artifacts
/circuit-breaker-demo
*.test
*.out

# IDE / editor
.idea/
.vscode/
*.swp

# Env files (secrets live here)
.env
.env.local

# Docker volumes mounted into the project
/pgdata/
/redisdata/
```

---

## 7. Sanity checklist before moving on

- [ ] `go version` reports 1.22 or newer
- [ ] `go mod init` created `go.mod` with module `circuit-breaker-demo`
- [ ] All package folders exist (empty is fine)
- [ ] `go run .` prints the stub message
- [ ] `docker run --rm hello-world` succeeds

---

## What's next

**Step 2 — Configuration.** Before we write the state machine, we set up the config loader. Every knob (failure threshold, timeouts, ports) will flow through one typed `Config` struct loaded from environment variables. This avoids hard-coded magic numbers buried in the circuit breaker code.
