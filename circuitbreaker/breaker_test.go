package circuitbreaker

import (
	"errors"
	"testing"
	"time"
)

func smallConfig() Config {
	return Config {
		Name : "test", 
		FailureThreshold: 3,
		SuccessThreshold: 2,
		OpenTimeout: 100 * time.Millisecond,
		MaxHalfOpenProbes: 1,
	}
}

var errBoom = errors.New("boom")

func fail() error {
	return errBoom
}

func succeed() error {
	return nil
}

// tripBreaker forces the CB to OPEN by driving FailureThreshold failures
func tripBreaker(t *testing.T, cb *CircuitBreaker){
	t.Helper()
	for i := 0; i <cb.config.FailureThreshold; i++ {
		_ = cb.Execute(fail)
	}
	if cb.State() != StateOpen {
		t.Fatalf("expected OPEN after %d failures, got %v", cb.config.FailureThreshold, cb.State())
	}
}

// this test verifies that the CB transitions to OPEN state after meeting the failure threshold
func TestClosedToOpenOnThreshold(t *testing.T) {
	cb := New(smallConfig())

	for i := 0; i < 2; i++ {
        if err := cb.Execute(fail); !errors.Is(err, errBoom) {
            t.Fatalf("want errBoom, got %v", err)
        }
        if cb.State() != StateClosed {
            t.Fatalf("want CLOSED after %d failures, got %v", i+1, cb.State())
        }
    }

    _ = cb.Execute(fail)
    if cb.State() != StateOpen {
         t.Fatalf("want OPEN after 3rd failure, got %v", cb.State())
    }
}

// this test verifies that the CB state remains OPEN after exceeding failure threshold
func TestOpenFastFails(t *testing.T) {
	cb := New(smallConfig())
    tripBreaker(t, cb)

    called := false
    err := cb.Execute(func() error { called = true; return nil })
    if called {
        t.Fatal("fn should NOT be called when OPEN")
    }
    if !errors.Is(err, ErrCircuitOpen) {
        t.Fatalf("want ErrCircuitOpen, got %v", err)
    }
}

// this test varifies that CB state transitions from OPEN to HALF-OPEN after timeout
func TestOpenToHalfOpenTimeout(t *testing.T) {
	cb := New(smallConfig())
	tripBreaker(t, cb)

	time.Sleep(smallConfig().OpenTimeout + 20*time.Millisecond)

	called := false
	_ = cb.Execute(func() error {called = true; return nil})
	if !called {
		t.Fatalf("fn should be called after timeout (HALF-OPEN probe)")
	}
}

func TestHalfOpenToClosedOnSuccesses(t *testing.T) {
	cb := New(smallConfig())
	tripBreaker(t, cb)
	time.Sleep(smallConfig().OpenTimeout + 20*time.Millisecond)

    // SuccessThreshold=2; two successes should close the circuit.
    if err := cb.Execute(succeed); err != nil {
        t.Fatalf("first probe: %v", err)
    }
    if  err := cb.Execute(succeed); err != nil {
        t.Fatalf("second probe: %v", err)
    }
    if cb.State() != StateClosed {
        t.Fatalf("want CLOSED after 2 successful probes, got %v", cb.State())
    }
}