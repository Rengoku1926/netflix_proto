package memory

import (
	"context"
	"fmt"
	"netflix-proto/repository"
	"sync"
)

// PaymentRepo is a thread-safe map-backed PaymentRepository.
type PaymentRepo struct {
    mu      sync.RWMutex
    records map[string]*repository.PaymentRecord
}

func NewPaymentRepo() *PaymentRepo {
    return &PaymentRepo{records: make(map[string]*repository.PaymentRecord)}
}

func (r *PaymentRepo) Create(ctx context.Context, rec *repository.PaymentRecord) error {
    if err := ctx.Err(); err != nil {
        return err
    }
    r.mu.Lock()
    defer r.mu.Unlock()
    if _, exists := r.records[rec.ID]; exists {
        return fmt.Errorf("payment %s already exists", rec.ID)
    }
    // Store a copy to prevent external mutation of our state.
    cp := *rec
    r.records[rec.ID] = &cp
    return nil
}

func (r *PaymentRepo) GetByID(ctx context.Context, id string) (*repository.PaymentRecord, error) {
    if err := ctx.Err(); err != nil {
        return nil, err
    }
    r.mu.RLock()
    defer r.mu.RUnlock()
    rec, ok := r.records[id]
    if !ok {
        return nil, fmt.Errorf("payment %s not found", id)
    }
    cp := *rec
    return &cp, nil
}

func (r *PaymentRepo) ListByUser(ctx context.Context, userID int, limit int) ([]*repository.PaymentRecord, error) {
    if err := ctx.Err(); err != nil {
        return nil, err
    }
    r.mu.RLock()
    defer r.mu.RUnlock()

    out := make([]*repository.PaymentRecord, 0, limit)
    for _, rec := range r.records {
        if rec.UserID == userID {
            cp := *rec
            out = append(out, &cp)
            if len(out) >= limit {
                break
            }
        }
    }
    return out, nil
}

func (r *PaymentRepo) UpdateStatus(ctx context.Context, id string, status string) error {
    if err := ctx.Err(); err != nil {
        return err
    }
    r.mu.Lock()
    defer r.mu.Unlock()
    rec, ok := r.records[id]
    if !ok {
        return fmt.Errorf("payment %s not found", id)
    }
    rec.Status = status
    return nil
}

// Compile-time interface check.
var _ repository.PaymentRepository = (*PaymentRepo)(nil)