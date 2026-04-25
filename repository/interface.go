package repository

import "context"

// PaymentRepository — all persistence for payments.
type PaymentRepository interface {
    Create(ctx context.Context, rec *PaymentRecord) error
    GetByID(ctx context.Context, id string) (*PaymentRecord, error)
    ListByUser(ctx context.Context, userID int, limit int) ([]*PaymentRecord, error)
    UpdateStatus(ctx context.Context, id string, status string) error
}

// UserRepository — all persistence for user profiles.
type UserRepository interface {
    GetByID(ctx context.Context, id int) (*UserProfile, error)
    GetByEmail(ctx context.Context, email string) (*UserProfile, error)
}