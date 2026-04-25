package services

import (
	"context"
	"errors"
	"sync/atomic"
	"time"
)

type PaymentService struct {
    healthy int32         // 1 = up, 0 = down; atomic, no lock needed
    latency time.Duration // simulated I/O
}

func NewPaymentService() *PaymentService {
    return &PaymentService{
        healthy: 1,
        latency: 50 * time.Millisecond,
    }
}

func (s *PaymentService) Break() {atomic.StoreInt32(&s.healthy, 0)}
func (s *PaymentService) Repair() {atomic.StoreInt32(&s.healthy, 1)}

func (s *PaymentService) IsHealthy() bool {
    return atomic.LoadInt32(&s.healthy) == 1
}

func (s *PaymentService) ProcessPayment(ctx context.Context, userID int, amount float64, currency string) error {
	select {
    case <-ctx.Done():
        return ctx.Err()
    case <-time.After(s.latency):
    }

    if !s.IsHealthy() {
        return errors.New("payment service: database unavailable")
    }
    return nil
}

var _ PaymentServicer = (*PaymentService)(nil)
