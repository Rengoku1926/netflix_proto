package gateway

import (
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