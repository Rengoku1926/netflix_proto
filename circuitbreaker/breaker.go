package circuitbreaker

import (
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

type State int32

const (
	StateClosed State = iota
	StateOpen
	StateHalfOpen
)

func (s State) String() string {
	switch s {
	case StateClosed:
		return "CLOSED"
	case StateOpen:
		return "OPEN"
	case StateHalfOpen:
		return "HALF_OPEN"
	default:
		return "UNKNOWN"
	}
}	

// Config is the full set of parameters needed to configure a circuit breaker instance. 
// One config per downstream service. 
type Config struct {
	Name string	//used in logs, metrics, and tracing to identify the service this breaker is for
	FailureThreshold int // number of consecutive failures before opening the circuit
	SuccessThreshold int // number of consecutive successes required to close the circuit from half-open state
	OpenTimeout time.Duration // how long to stay open before allowing a probe
	MaxHalfOpenProbes int //max concurrent probes when in half-open state
}

// Metrics is a point-in-time snapshot of all counters. Fields are atomically
// consistent with each other at the moment Metrics() was called; counters
// may have moved forward by the time the caller reads individual fields.
type Metrics struct {
    TotalRequests int64
    Successes     int64
    Failures      int64
    Rejections    int64
    StateChanges  int64
}

var (
	ErrCircuitOpen = errors.New("circuit breaker OPEN: fast-failing request")
	ErrTooManyProbes = errors.New("circuit breaker HALF-OPEN: probe slot occupied")
)

type CircuitBreaker struct {
	config Config
	state int32

	//Mutex guarded bookkeeping. 
	mu sync.Mutex
	consecutiveFails int
	consecutivePasses int
	lastFailureTime time.Time
	activeProbes int

	//Atomic counters for metrics. Updated under lock to ensure consistency with state changes.
	totalRequests int64
	successes int64
	failures int64
	rejections int64
	stateChanges int64

	// Optional callback for state changes, useful for logging or metrics.
	onStateChange func(cb *CircuitBreaker, from, to State)
}

func New(cfg Config) *CircuitBreaker {
	return &CircuitBreaker{
		config: cfg,
		state: int32(StateClosed),
	}
}

func (cb *CircuitBreaker) OnStateChange(fn func(cb *CircuitBreaker, from, to State)){
	cb.onStateChange = fn
}

// State returns the current state with a lock-free atomic read. 
// Safe to call at any frequency (e.g. for metrics) without blocking.
func (cb *CircuitBreaker) State() State {
	return State(atomic.LoadInt32(&cb.state))
}

// Name returns the breaker name in teh config, useful for logging and metrics
func (cb *CircuitBreaker) Name() string {
	return cb.config.Name
}

// Metrics returns an atomic snapshot of all counters. Safe to call at any frequency (e.g. for metrics) without blocking.
func (cb *CircuitBreaker) Metrics() Metrics {
    return Metrics{
        TotalRequests: atomic.LoadInt64(&cb.totalRequests),
        Successes:     atomic.LoadInt64(&cb.successes),
        Failures:      atomic.LoadInt64(&cb.failures),
        Rejections:    atomic.LoadInt64(&cb.rejections),
        StateChanges:  atomic.LoadInt64(&cb.stateChanges),
    }
}

// Reset returns the breaker to CLOSED with zeroed bookkeeping. Metrics
// counters are kept (they're cumulative). Intended for tests + admin ops.
func (cb *CircuitBreaker) Reset() {
    cb.mu.Lock()
    defer cb.mu.Unlock()
    atomic.StoreInt32(&cb.state, int32(StateClosed))
    cb.consecutiveFails = 0
    cb.consecutivePasses = 0
    cb.activeProbes = 0
    cb.lastFailureTime = time.Time{}
}

func (cb *CircuitBreaker) Execute(fn func() error) error {
	atomic.AddInt64(&cb.totalRequests, 1)

	if err := cb.beforeExec(); err != nil {
		atomic.AddInt64(&cb.rejections, 1)
		return err
	}
	err := fn()

	cb.afterExec(err)
	return err
}

// beforeExec checks the current state and decides whether to allow the call to proceed or reject it immediately.
func (cb *CircuitBreaker) beforeExec() error {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	switch State(atomic.LoadInt32(&cb.state)) {
    case StateClosed:
        return nil

    case StateOpen:
        remaining := cb.config.OpenTimeout - time.Since(cb.lastFailureTime)
        if remaining > 0 {
            // Still within the timeout window — reject.
            return fmt.Errorf("%w (retry in %s)",
                ErrCircuitOpen, remaining.Round(time.Millisecond))
        }
        // Timeout elapsed — transition to HALF-OPEN and let this probe through.
        cb.transitionToLocked(StateHalfOpen)
        cb.activeProbes = 1
        return nil

    case StateHalfOpen:
        if cb.activeProbes >= cb.config.MaxHalfOpenProbes {
            return ErrTooManyProbes
        }
        cb.activeProbes++
        return nil
    }
    return nil
}

// afterExec records the outcome of fn and updates state if thresholds are hit.
func (cb *CircuitBreaker) afterExec(err error) {
    cb.mu.Lock()
    defer cb.mu.Unlock()

    if err != nil {
        atomic.AddInt64(&cb.failures, 1)
        cb.lastFailureTime = time.Now()
        cb.consecutivePasses = 0

        switch State(atomic.LoadInt32(&cb.state)) {
        case StateClosed:
            cb.consecutiveFails++
            if cb.consecutiveFails >= cb.config.FailureThreshold {
                cb.transitionToLocked(StateOpen)
            }
        case StateHalfOpen:
            cb.activeProbes--
            cb.transitionToLocked(StateOpen)
        }
        return
    }

    atomic.AddInt64(&cb.successes, 1)
    cb.consecutiveFails = 0

    switch State(atomic.LoadInt32(&cb.state)) {
    case StateClosed:
        // nothing to do — stay CLOSED
    case StateHalfOpen:
        cb.activeProbes--
        cb.consecutivePasses++
        if cb.consecutivePasses >= cb.config.SuccessThreshold {
            cb.transitionToLocked(StateClosed)
        }
    }
}

// transitionToLocked changes state and fires the onStateChange hook.
// Caller MUST hold cb.mu.
func (cb *CircuitBreaker) transitionToLocked(to State) {
    from := State(atomic.LoadInt32(&cb.state))
    if from == to {
        return
    }

    atomic.StoreInt32(&cb.state, int32(to))
    atomic.AddInt64(&cb.stateChanges, 1)

    // Reset bookkeeping appropriate to the new state.
    switch to {
    case StateClosed:
        cb.consecutiveFails = 0
        cb.consecutivePasses = 0
        cb.activeProbes = 0
    case StateOpen:
        cb.consecutivePasses = 0
        cb.activeProbes = 0
    case StateHalfOpen:
        cb.consecutivePasses = 0
    }

    // Fire the hook in a new goroutine so it can never block the hot path.
    if cb.onStateChange != nil {
        go cb.onStateChange(cb, from, to)
    }
}