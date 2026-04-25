package services

import (
    "context"
    "testing"
)

func TestPaymentBreakRepair(t *testing.T) {
    s := NewPaymentService()
    ctx := context.Background()

    if err := s.ProcessPayment(ctx, 1, 9.99, "USD"); err != nil {
        t.Fatalf("healthy service should succeed, got %v", err)
    }

    s.Break()
    if err := s.ProcessPayment(ctx, 1, 9.99, "USD"); err == nil {
        t.Fatal("broken service should fail")
    }

    s.Repair()
    if err := s.ProcessPayment(ctx, 1, 9.99, "USD"); err != nil {
        t.Fatalf("repaired service should succeed, got %v", err)
    }
}