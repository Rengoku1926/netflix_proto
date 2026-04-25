package services

import (
    "context"
    "errors"
    "fmt"
    "sync/atomic"
    "time"
)

type UserService struct {
    healthy int32
    latency time.Duration
}

func NewUserService() *UserService {
    return &UserService{healthy: 1, latency: 30 * time.Millisecond}
}

func (s *UserService) Break()  { atomic.StoreInt32(&s.healthy, 0) }
func (s *UserService) Repair() { atomic.StoreInt32(&s.healthy, 1) }

func (s *UserService) GetUser(ctx context.Context, userID int) (*UserProfile, error) {
    select {
    case <-ctx.Done():
        return nil, ctx.Err()
    case <-time.After(s.latency):
    }

    if atomic.LoadInt32(&s.healthy) == 0 {
        return nil, errors.New("user service: auth provider unreachable")
    }

    return &UserProfile{
        ID:    userID,
        Name:  fmt.Sprintf("User %d", userID),
        Email: fmt.Sprintf("user%d@example.com", userID),
        Plan:  "premium",
    }, nil
}

var _ UserServicer = (*UserService)(nil)
