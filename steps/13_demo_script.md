# Step 13 — Live Traffic Demo

> See the circuit breakers trip, protect, and recover in real time — no test harness, just curl and a shell script driving the server you built.

---

## Goal

By the end of this step you have:

- A `scripts/demo.sh` that drives the full outage → protection → recovery cycle against a running server
- A `cmd/loadgen/main.go` Go load generator that fires concurrent goroutines and prints a live counter board to the terminal
- Clear output that shows isolation: payment CB trips while reco and user stay CLOSED

No new application logic is added. Everything here exercises the server wired in Step 9 through the debug endpoints added in Step 9 §4.

---

## 1. What we're demonstrating

```
Phase 1 — Healthy baseline
  → All three services OK. Payments succeed. CBs: CLOSED/CLOSED/CLOSED.

Phase 2 — Payment service breaks
  → PaymentService.Break() called via /debug/payment/break
  → Failures accumulate. After FailureThreshold (3), payment CB trips: OPEN.
  → Calls fast-fail with 503 CIRCUIT_OPEN. No goroutines held. Reco + user unaffected.

Phase 3 — Traffic keeps flowing (isolation proof)
  → /recommendations and /users still return 200.
  → Payment requests get 503 instantly — no timeout wait.

Phase 4 — Service repairs
  → PaymentService.Repair() called via /debug/payment/repair
  → After OpenTimeout (5s), next payment probes: HALF-OPEN.
  → One success → CLOSED. Normal traffic resumes.
```

---

## 2. Prerequisites

The server from Step 9 must be running with the debug endpoints mounted:

```bash
go run .
# → {"msg":"server_listening","addr":":8080"}
```

The debug routes must be registered in `main.go` (Step 9 §4):

```
POST /debug/payment/break
POST /debug/payment/repair
POST /debug/user/break
POST /debug/user/repair
POST /debug/reco/fail-rate
```

---

## 3. Shell demo script

Create `scripts/demo.sh`:

```bash
#!/usr/bin/env bash
# demo.sh — drives a full circuit breaker outage/recovery cycle.
# Usage: ./scripts/demo.sh [BASE_URL]
# Default BASE_URL: http://localhost:8080

set -euo pipefail

BASE="${1:-http://localhost:8080}"
SEP="────────────────────────────────────────"

# ANSI colours
RED='\033[0;31m'; GRN='\033[0;32m'; YLW='\033[0;33m'
BLU='\033[0;34m'; BLD='\033[1m'; RST='\033[0m'

info()    { echo -e "${BLU}${BLD}▶ $*${RST}"; }
ok()      { echo -e "${GRN}  ✓ $*${RST}"; }
warn()    { echo -e "${YLW}  ⚡ $*${RST}"; }
fail()    { echo -e "${RED}  ✗ $*${RST}"; }
section() { echo -e "\n${BLD}${SEP}\n  $*\n${SEP}${RST}"; }

payment_payload='{"user_id":1,"amount":9.99,"currency":"USD"}'

# Helper: POST /payments and print status
try_payment() {
    local label="$1"
    local status
    status=$(curl -s -o /dev/null -w "%{http_code}" \
        -X POST "$BASE/payments" \
        -H 'Content-Type: application/json' \
        -d "$payment_payload")

    case "$status" in
        2*) ok  "$label → $status (success)" ;;
        503) warn "$label → $status (CIRCUIT_OPEN — fast-fail)" ;;
        502) fail "$label → $status (UPSTREAM_ERROR — counted as failure)" ;;
        *)   fail "$label → $status" ;;
    esac
}

# Helper: print CB state summary
cb_status() {
    echo ""
    curl -s "$BASE/health/circuit-breakers" | \
        python3 -c "
import sys, json
data = json.load(sys.stdin)
for b in data['breakers']:
    state = b['state']
    colour = '\033[0;31m' if state == 'OPEN' else ('\033[0;33m' if state == 'HALF-OPEN' else '\033[0;32m')
    print(f\"  {colour}{b['name']:30s}  {state:10s}  reqs={b['total_requests']:4d}  rej={b['rejections']:3d}\033[0m\")
"
    echo ""
}

# ── Phase 1: Healthy baseline ─────────────────────────────────────────────────
section "Phase 1 — Healthy baseline"
info "Firing 3 payments — all should succeed"
try_payment "payment #1"
try_payment "payment #2"
try_payment "payment #3"
info "CB states:"
cb_status

# ── Phase 2: Break the payment service ───────────────────────────────────────
section "Phase 2 — Breaking the payment service"
info "Calling /debug/payment/break"
curl -s -X POST "$BASE/debug/payment/break" | python3 -c "import sys,json; d=json.load(sys.stdin); print(f'  status: {d[\"status\"]}')"

echo ""
info "Firing 6 payments — first 3 should fail (upstream error), next 3 fast-fail (circuit open)"
for i in {1..6}; do
    try_payment "payment #$((i+3))"
done

info "CB states after failures:"
cb_status

# ── Phase 3: Isolation proof ──────────────────────────────────────────────────
section "Phase 3 — Isolation (reco + user should still work)"
info "Recommendations for user 1:"
reco_status=$(curl -s -o /dev/null -w "%{http_code}" "$BASE/recommendations/1")
if [[ "$reco_status" == 2* ]]; then
    ok "GET /recommendations/1 → $reco_status"
else
    fail "GET /recommendations/1 → $reco_status (unexpected!)"
fi

info "User profile for user 1:"
user_status=$(curl -s -o /dev/null -w "%{http_code}" "$BASE/users/1")
if [[ "$user_status" == 2* ]]; then
    ok "GET /users/1 → $user_status"
else
    fail "GET /users/1 → $user_status (unexpected!)"
fi

info "Payment attempt while CB is OPEN (should fast-fail instantly):"
time_ms=$(( SECONDS * 1000 ))
try_payment "payment (while OPEN)"

# ── Phase 4: Repair and recovery ─────────────────────────────────────────────
section "Phase 4 — Repair and recovery"
info "Calling /debug/payment/repair"
curl -s -X POST "$BASE/debug/payment/repair" | python3 -c "import sys,json; d=json.load(sys.stdin); print(f'  status: {d[\"status\"]}')"

info "Waiting 6s for OpenTimeout to elapse..."
sleep 6

info "First probe — should transition CB to HALF-OPEN then CLOSED"
try_payment "payment probe"

info "Follow-up payment — should succeed (CLOSED):"
try_payment "payment post-recovery"

info "Final CB states:"
cb_status

section "Demo complete"
echo -e "${GRN}${BLD}Circuit breaker cycle demonstrated:${RST}"
echo -e "  1. Service healthy      → CLOSED, requests pass"
echo -e "  2. Service broken       → OPEN after 3 failures, fast-fail"
echo -e "  3. Other CBs isolated   → reco + user unaffected"
echo -e "  4. Service repaired     → HALF-OPEN probe → CLOSED, traffic resumes"
```

Make it executable and run it:

```bash
chmod +x scripts/demo.sh
./scripts/demo.sh
```

Sample output:

```
────────────────────────────────────────
  Phase 1 — Healthy baseline
────────────────────────────────────────
▶ Firing 3 payments — all should succeed
  ✓ payment #1 → 201 (success)
  ✓ payment #2 → 201 (success)
  ✓ payment #3 → 201 (success)
▶ CB states:
  payment-service                CLOSED      reqs=   3  rej=  0
  recommendation-service         CLOSED      reqs=   0  rej=  0
  user-service                   CLOSED      reqs=   0  rej=  0
...
────────────────────────────────────────
  Phase 2 — Breaking the payment service
────────────────────────────────────────
  status: broken
▶ Firing 6 payments...
  ✗ payment #4 → 502 (UPSTREAM_ERROR — counted as failure)
  ✗ payment #5 → 502 (UPSTREAM_ERROR — counted as failure)
  ✗ payment #6 → 502 (UPSTREAM_ERROR — counted as failure)
  ⚡ payment #7 → 503 (CIRCUIT_OPEN — fast-fail)
  ⚡ payment #8 → 503 (CIRCUIT_OPEN — fast-fail)
  ⚡ payment #9 → 503 (CIRCUIT_OPEN — fast-fail)
▶ CB states after failures:
  payment-service                OPEN        reqs=   6  rej=  3
  recommendation-service         CLOSED      reqs=   0  rej=  0
  user-service                   CLOSED      reqs=   0  rej=  0
...
```

---

## 4. Go load generator (concurrent traffic)

The shell script is sequential — one request at a time. To actually stress the server and show isolation under concurrent load, use this Go load generator.

Create `cmd/loadgen/main.go`:

```go
// cmd/loadgen/main.go — concurrent load generator with a live terminal display.
//
// Usage:
//   go run ./cmd/loadgen --base http://localhost:8080 --rps 50 --duration 60s
//
// It fires payment, reco, and user requests concurrently, polls the CB health
// endpoint every second, and prints a live dashboard to the terminal.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

var (
	base     = flag.String("base", "http://localhost:8080", "server base URL")
	rps      = flag.Int("rps", 20, "requests per second (split across all endpoints)")
	duration = flag.Duration("duration", 60*time.Second, "how long to run")
)

// counters tracks per-endpoint outcomes.
type counters struct {
	success   int64
	circuitOp int64 // 503 CIRCUIT_OPEN
	upstream  int64 // 502 UPSTREAM_ERROR
	other     int64
}

func (c *counters) record(status int) {
	switch {
	case status >= 200 && status < 300:
		atomic.AddInt64(&c.success, 1)
	case status == 503:
		atomic.AddInt64(&c.circuitOp, 1)
	case status == 502:
		atomic.AddInt64(&c.upstream, 1)
	default:
		atomic.AddInt64(&c.other, 1)
	}
}

var (
	paymentC = &counters{}
	recoC    = &counters{}
	userC    = &counters{}
)

type breakerSnapshot struct {
	Name          string `json:"name"`
	State         string `json:"state"`
	TotalRequests int64  `json:"total_requests"`
	Rejections    int64  `json:"rejections"`
	Failures      int64  `json:"failures"`
}

type healthResp struct {
	Breakers []breakerSnapshot `json:"breakers"`
}

func fetchCBState(base string) []breakerSnapshot {
	resp, err := http.Get(base + "/health/circuit-breakers")
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	var h healthResp
	json.Unmarshal(body, &h)
	return h.Breakers
}

func stateColour(state string) string {
	switch state {
	case "OPEN":
		return "\033[0;31m" // red
	case "HALF-OPEN":
		return "\033[0;33m" // yellow
	default:
		return "\033[0;32m" // green
	}
}

func printDashboard(breakers []breakerSnapshot, elapsed time.Duration) {
	// Move cursor up to overwrite previous dashboard (11 lines).
	fmt.Print("\033[11A\033[J")

	fmt.Printf("\033[1m── Circuit Breaker Load Generator  elapsed: %s \033[0m\n", elapsed.Round(time.Second))
	fmt.Println(strings.Repeat("─", 70))

	// CB state table
	fmt.Printf("  %-32s %-10s %8s %8s\n", "breaker", "state", "reqs", "rejected")
	for _, b := range breakers {
		col := stateColour(b.State)
		fmt.Printf("  %-32s %s%-10s\033[0m %8d %8d\n",
			b.Name, col, b.State, b.TotalRequests, b.Rejections)
	}

	fmt.Println(strings.Repeat("─", 70))

	// Per-endpoint counters
	fmt.Printf("  %-20s %8s %12s %10s\n", "endpoint", "success", "circuit_open", "upstream_err")
	fmt.Printf("  %-20s %8d %12d %10d\n", "POST /payments",
		atomic.LoadInt64(&paymentC.success),
		atomic.LoadInt64(&paymentC.circuitOp),
		atomic.LoadInt64(&paymentC.upstream))
	fmt.Printf("  %-20s %8d %12d %10d\n", "GET /reco",
		atomic.LoadInt64(&recoC.success),
		atomic.LoadInt64(&recoC.circuitOp),
		atomic.LoadInt64(&recoC.upstream))
	fmt.Printf("  %-20s %8d %12d %10d\n", "GET /users",
		atomic.LoadInt64(&userC.success),
		atomic.LoadInt64(&userC.circuitOp),
		atomic.LoadInt64(&userC.upstream))

	fmt.Println(strings.Repeat("─", 70))
	fmt.Println("  Ctrl-C to stop")
}

func sendPayment(ctx context.Context, base string) {
	req, _ := http.NewRequestWithContext(ctx, "POST", base+"/payments",
		strings.NewReader(`{"user_id":1,"amount":9.99,"currency":"USD"}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return
	}
	resp.Body.Close()
	paymentC.record(resp.StatusCode)
}

func sendReco(ctx context.Context, base string) {
	req, _ := http.NewRequestWithContext(ctx, "GET", base+"/recommendations/1", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return
	}
	resp.Body.Close()
	recoC.record(resp.StatusCode)
}

func sendUser(ctx context.Context, base string) {
	req, _ := http.NewRequestWithContext(ctx, "GET", base+"/users/1", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return
	}
	resp.Body.Close()
	userC.record(resp.StatusCode)
}

func main() {
	flag.Parse()

	ctx, cancel := context.WithTimeout(context.Background(), *duration)
	defer cancel()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	go func() { <-stop; cancel() }()

	interval := time.Second / time.Duration(*rps)
	senders := []func(context.Context, string){sendPayment, sendReco, sendUser}

	// Print blank lines so the first overwrite has space.
	fmt.Print(strings.Repeat("\n", 11))

	var wg sync.WaitGroup
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	start := time.Now()
	idx := 0

	// Dashboard refresh goroutine.
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Second):
				bs := fetchCBState(*base)
				printDashboard(bs, time.Since(start))
			}
		}
	}()

	for {
		select {
		case <-ctx.Done():
			wg.Wait()
			fmt.Println("\nLoad generator stopped.")
			os.Exit(0)
		case <-ticker.C:
			fn := senders[idx%len(senders)]
			idx++
			wg.Add(1)
			go func(f func(context.Context, string)) {
				defer wg.Done()
				f(ctx, *base)
			}(fn)
		}
	}
}
```

Run it:

```bash
go run ./cmd/loadgen --rps 30 --duration 120s
```

---

## 5. Fault injection during load

While `loadgen` is running, open a second terminal and drive the faults manually:

```bash
# Break payment — watch circuit_open count climb in the dashboard
curl -sX POST localhost:8080/debug/payment/break

# Let it run for ~10s, then repair
sleep 10
curl -sX POST localhost:8080/debug/payment/repair

# After 5s (OpenTimeout), the CB probes and closes
# Watch the success column recover while reco + user never dipped

# Try reco degradation — probabilistic failure at 80%
curl -sX POST "localhost:8080/debug/reco/fail-rate?rate=0.8"
sleep 10
curl -sX POST "localhost:8080/debug/reco/fail-rate?rate=0.0"
```

What you'll see in the `loadgen` dashboard:

```
── Circuit Breaker Load Generator  elapsed: 23s
──────────────────────────────────────────────────────────────────────
  breaker                          state      reqs  rejected
  payment-service                  OPEN        183        97
  recommendation-service           CLOSED      183         0
  user-service                     CLOSED      183         0
──────────────────────────────────────────────────────────────────────
  endpoint              success  circuit_open  upstream_err
  POST /payments             62            97            24
  GET /reco                 183             0             0
  GET /users                183             0             0
──────────────────────────────────────────────────────────────────────
  Ctrl-C to stop
```

Key observations:
- `payment-service` is OPEN, `rejected=97` — those 97 calls returned instantly
- `reco` and `user` success counters keep climbing — **isolation is working**
- The `upstream_err=24` for payments are the failures that *tripped* the breaker (3 per trip × 8 trip cycles)

---

## 6. What each number proves

| Observation | What it proves |
|---|---|
| `rejected` jumps after 3 upstream errors | FailureThreshold=3 is respected |
| `reco` and `user` success unaffected | Per-service CB isolation works |
| `circuit_open` returns appear in < 1ms | Fast-fail: no goroutine held for the timeout |
| After `sleep 5` + repair, `success` climbs again | HALF-OPEN probe → CLOSED recovery works |
| `upstream_err` stops after CB opens | Service is no longer called when CB is OPEN |

---

## 7. Sanity checklist

- [ ] `./scripts/demo.sh` runs end-to-end, green output in phases 1, 3, 4; yellow in phase 2
- [ ] `go run ./cmd/loadgen` shows a live refreshing dashboard
- [ ] Breaking payment during loadgen shows `OPEN` state + rising `circuit_open` count
- [ ] Reco and user counters never show `circuit_open` while only payment is broken
- [ ] After repair + OpenTimeout, payment `success` resumes and `circuit_open` drops to zero

---

## What's next

**Step 14 — Kubernetes manifests.** Deploy the containerized app (Step 12) to a local k3d cluster. Add a `HorizontalPodAutoscaler` and show that the CB protects each pod independently while the HPA scales under load.
