package services

import "context"

type UserProfile struct {
	ID int
	Name string
	Email string 
	Plan string // "free" || "premium" | "enterprise"
}

// PaymentServicer — anything that can process a payment.
type PaymentServicer interface {
    ProcessPayment(ctx context.Context, userID int, amount float64, currency string) error
}

// RecommendationServicer — anything that can serve personalised recs.
type RecommendationServicer interface {
    GetRecommendations(ctx context.Context, userID int) ([]string, error)
}

// UserServicer — anything that can look up a user profile.
type UserServicer interface {
    GetUser(ctx context.Context, userID int) (*UserProfile, error)
}