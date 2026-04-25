package middleware

import (
	"net/http"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// RateLimiter applies a per-client rate limit. Uses IP as the key — in
// production behind a proxy, read X-Forwarded-For / X-Real-IP instead.
//
// Each client gets their own *rate.Limiter with the given rps + burst. A
// janitor trims idle clients every 10 minutes to bound memory.
type RateLimiter struct {
    mu       sync.Mutex
    visitors map[string]*visitor
    rps      rate.Limit
    burst    int
}

type visitor struct {
    limiter  *rate.Limiter
    lastSeen time.Time
}

func NewRateLimiter(rps float64, burst int) *RateLimiter {
    rl := &RateLimiter{
        visitors: make(map[string]*visitor),
        rps:      rate.Limit(rps),
        burst:    burst,
    }
    go rl.cleanup()
    return rl
}

func (rl *RateLimiter) getLimiter(ip string) *rate.Limiter {
    rl.mu.Lock()
    defer rl.mu.Unlock()
    v, ok := rl.visitors[ip]
    if !ok {
        l := rate.NewLimiter(rl.rps, rl.burst)
        rl.visitors[ip] = &visitor{limiter: l, lastSeen: time.Now()}
        return l
    }
    v.lastSeen = time.Now()
    return v.limiter
}

func (rl *RateLimiter) cleanup() {
    for {
        time.Sleep(10 * time.Minute)
        rl.mu.Lock()
        for ip, v := range rl.visitors {
            if time.Since(v.lastSeen) > 15*time.Minute {
                delete(rl.visitors, ip)
            }
        }
        rl.mu.Unlock()
    }
}

func (rl *RateLimiter) Middleware(next http.Handler) http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        ip := clientIP(r)
        if !rl.getLimiter(ip).Allow() {
            w.Header().Set("Retry-After", "1")
            http.Error(w,
                `{"code":"RATE_LIMITED","message":"too many requests"}`,
                http.StatusTooManyRequests)
            return
        }
        next.ServeHTTP(w, r)
    })
}

func clientIP(r *http.Request) string {
    if fwd := r.Header.Get("X-Forwarded-For"); fwd != "" {
        return fwd
    }
    return r.RemoteAddr
}