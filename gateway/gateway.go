package gateway

import (
	"context"
	"errors"
	"netflix-proto/circuitbreaker"
	"netflix-proto/config"
	"netflix-proto/services"
)

type Gateway struct {
	paymentSvc services.PaymentServicer
	recoSvc services.RecommendationServicer
	userSvc services.UserServicer

	paymentCB *circuitbreaker.CircuitBreaker
	recoCB *circuitbreaker.CircuitBreaker
	userCB *circuitbreaker.CircuitBreaker
}

func New(
	paymentSvc services.PaymentServicer,
	recoSvc services.RecommendationServicer,
	userSvc services.UserServicer,
	cfg config.BreakersConfig,
	onStateChange func(*circuitbreaker.CircuitBreaker, circuitbreaker.State, circuitbreaker.State),
) *Gateway {
	paymentCB := circuitbreaker.New(circuitbreaker.Config{
		Name:              "payment-service",
        FailureThreshold:  cfg.Payment.FailureThreshold,
        SuccessThreshold:  cfg.Payment.SuccessThreshold,
        OpenTimeout:       cfg.Payment.OpenTimeout,
        MaxHalfOpenProbes: cfg.Payment.MaxHalfOpenProbes,
	})
	recoCB := circuitbreaker.New(circuitbreaker.Config{
        Name:              "recommendation-service",
        FailureThreshold:  cfg.Recommendation.FailureThreshold,
        SuccessThreshold:  cfg.Recommendation.SuccessThreshold,
        OpenTimeout:       cfg.Recommendation.OpenTimeout,
        MaxHalfOpenProbes: cfg.Recommendation.MaxHalfOpenProbes,
    })
    userCB := circuitbreaker.New(circuitbreaker.Config{
        Name:              "user-service",
        FailureThreshold:  cfg.User.FailureThreshold,
        SuccessThreshold:  cfg.User.SuccessThreshold,
        OpenTimeout:       cfg.User.OpenTimeout,
        MaxHalfOpenProbes: cfg.User.MaxHalfOpenProbes,
    })

	if onStateChange != nil {
        paymentCB.OnStateChange(onStateChange)
        recoCB.OnStateChange(onStateChange)
        userCB.OnStateChange(onStateChange)
    }

    return &Gateway{
        paymentSvc: paymentSvc,
        recoSvc:    recoSvc,
        userSvc:    userSvc,
        paymentCB:  paymentCB,
        recoCB:     recoCB,
        userCB:     userCB,
    }
}

// ProcessPayment runs the payment through the payment CB.
func (gw *Gateway) ProcessPayment(ctx context.Context, userID int, amount float64, currency string) error {
    return gw.paymentCB.Execute(func() error {
        return gw.paymentSvc.ProcessPayment(ctx, userID, amount, currency)
    })
}

// GetRecommendations runs the reco lookup through the reco CB.
// Returns (nil, ErrCircuitOpen) when the breaker is OPEN.
func (gw *Gateway) GetRecommendations(ctx context.Context, userID int) ([]string, error) {
    var recs []string
    err := gw.recoCB.Execute(func() error {
        r, err := gw.recoSvc.GetRecommendations(ctx, userID)
        if err != nil {
            return err
        }
        recs = r
        return nil
    })
    return recs, err
}

// GetUser looks up a user profile via the user CB.
func (gw *Gateway) GetUser(ctx context.Context, userID int) (*services.UserProfile, error) {
    var profile *services.UserProfile
    err := gw.userCB.Execute(func() error {
        p, err := gw.userSvc.GetUser(ctx, userID)
        if err != nil {
            return err
        }
        profile = p
        return nil
    })
    return profile, err
}

// GetRecommendationsWithFallback returns a degraded-but-useful response
// when the reco CB is OPEN. Used for user-facing endpoints where any result
// is better than an error page.
func (gw *Gateway) GetRecommendationsWithFallback(ctx context.Context, userID int) ([]string, bool) {
    recs, err := gw.GetRecommendations(ctx, userID)
    if err == nil {
        return recs, false // degraded=false — served from live service
    }
    // Either CB is OPEN, or the service errored. Either way, serve the fallback.
    _ = err // could log here; caller may prefer to know it's degraded
    return popularTitlesFallback(), true
}

// popularTitlesFallback returns a static list of popular shows. In production
// this would be a cached, periodically-refreshed list stored in Redis.
func popularTitlesFallback() []string {
    return []string{
        "Stranger Things",
        "Breaking Bad",
        "The Crown",
    }
}

// Breakers returns all CBs for health/metrics endpoints. Order is stable.
func (gw *Gateway) Breakers() []*circuitbreaker.CircuitBreaker {
    return []*circuitbreaker.CircuitBreaker{gw.paymentCB, gw.recoCB, gw.userCB}
}

// Individual accessors — occasionally useful for middleware that needs a
// specific CB (e.g. CircuitBreakerGuard around the /payments route).
func (gw *Gateway) PaymentCB() *circuitbreaker.CircuitBreaker { return gw.paymentCB }
func (gw *Gateway) RecoCB() *circuitbreaker.CircuitBreaker    { return gw.recoCB }
func (gw *Gateway) UserCB() *circuitbreaker.CircuitBreaker    { return gw.userCB }

func IsCircuitError(err error) bool {
    return errors.Is(err, circuitbreaker.ErrCircuitOpen) ||
        errors.Is(err, circuitbreaker.ErrTooManyProbes)
}