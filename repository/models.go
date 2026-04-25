package repository

import "time"

// PaymentRecord is the persisted form of a processed payment.
type PaymentRecord struct {
    ID        string    `db:"id"`
    UserID    int       `db:"user_id"`
    Amount    float64   `db:"amount"`
    Currency  string    `db:"currency"`
    Status    string    `db:"status"` // "pending" | "settled" | "failed"
    CreatedAt time.Time `db:"created_at"`
    UpdatedAt time.Time `db:"updated_at"`
}

// UserProfile is the read model for the user service's persistence layer.
// Note: services.UserProfile is the wire model; this one includes DB-only
// fields. Duplication is intentional — wire and persistence evolve at
// different rates.
type UserProfile struct {
    ID        int       `db:"id"`
    Name      string    `db:"name"`
    Email     string    `db:"email"`
    Plan      string    `db:"plan"`
    CreatedAt time.Time `db:"created_at"`
}