package memory

import (
	"context"
	"fmt"
	"netflix-proto/repository"
	"sync"
)

type UserRepo struct {
    mu       sync.RWMutex
    byID     map[int]*repository.UserProfile
    byEmail  map[string]*repository.UserProfile
}

func NewUserRepo() *UserRepo {
    return &UserRepo{
        byID:    make(map[int]*repository.UserProfile),
        byEmail: make(map[string]*repository.UserProfile),
    }
}

// Seed inserts a user. Useful in tests / main.go bootstrap.
func (r *UserRepo) Seed(u *repository.UserProfile) {
    r.mu.Lock()
    defer r.mu.Unlock()
    cp := *u
    r.byID[u.ID] = &cp
    r.byEmail[u.Email] = &cp
}

func (r *UserRepo) GetByID(ctx context.Context, id int) (*repository.UserProfile, error) {
    if err := ctx.Err(); err != nil {
        return nil, err
    }
    r.mu.RLock()
    defer r.mu.RUnlock()
    u, ok := r.byID[id]
    if !ok {
        return nil, fmt.Errorf("user %d not found", id)
    }
    cp := *u
    return &cp, nil
}

func (r *UserRepo) GetByEmail(ctx context.Context, email string) (*repository.UserProfile, error) {
    if err := ctx.Err(); err != nil {
        return nil, err
    }
    r.mu.RLock()
    defer r.mu.RUnlock()
    u, ok := r.byEmail[email]
    if !ok {
        return nil, fmt.Errorf("user %s not found", email)
    }
    cp := *u
    return &cp, nil
}

var _ repository.UserRepository = (*UserRepo)(nil)