package services

import (
	"context"
	"errors"
	"math/rand"
	"sync"
	"time"
)

type RecommendationService struct {
    mu       sync.RWMutex
    failRate float64 // 0.0 = always succeed, 1.0 = always fail
    latency  time.Duration
    rng      *rand.Rand
}

func NewRecommendationService() *RecommendationService {
    return &RecommendationService{
        failRate: 0.0,
        latency:  80 * time.Millisecond,
        rng:      rand.New(rand.NewSource(time.Now().UnixNano())),
    }
}

func (s *RecommendationService) SetFailRate(r float64) {
    if r < 0 {
        r = 0
    }
    if r > 1 {
        r = 1
    }
    s.mu.Lock()
    s.failRate = r
    s.mu.Unlock()
}

func (s *RecommendationService) GetRecommendations(ctx context.Context, userID int) ([]string, error) {
    select {
    case <-ctx.Done():
        return nil, ctx.Err()
    case <-time.After(s.latency):
    }

    s.mu.RLock()
    shouldFail := s.rng.Float64() < s.failRate
    s.mu.RUnlock()

    if shouldFail {
        return nil, errors.New("recommendation service: model timeout")
    }

    return []string{
        "Stranger Things",
        "Breaking Bad",
        "The Crown",
        "Black Mirror",
    }, nil
}

var _ RecommendationServicer = (*RecommendationService)(nil)
