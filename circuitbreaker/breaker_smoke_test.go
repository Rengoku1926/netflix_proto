package circuitbreaker

import (
	"errors"
	"testing"
	"time"
)

func TestTripsAfterThreshold(t *testing.T) {
	cb := New(Config{
		Name: "smoke", 
		FailureThreshold: 3, 
		SuccessThreshold: 1,
        OpenTimeout: time.Hour, 
		MaxHalfOpenProbes: 1,
	})

	boom := func() error { return errors.New("boom") }

    for i := 0; i < 2; i++ {
        _ = cb.Execute(boom)
        if cb.State() != StateClosed {
            t.Fatalf("expected CLOSED after %d failures, got %v", i+1, cb.State())
        }
    }

    _ = cb.Execute(boom) // 3rd — should trip
    if cb.State() != StateOpen {
        t.Fatalf("expected OPEN after 3rd failure, got %v", cb.State())
    }

    // Now it must fast-fail without calling fn.
    called := false
    err := cb.Execute(func() error { called = true; return nil })
    if called {
        t.Fatal("fn should NOT be called when OPEN")
    }
    if !errors.Is(err, ErrCircuitOpen) {
        t.Fatalf("expected ErrCircuitOpen, got %v", err)
    }
}