package auth

import (
	"context"
	"errors"
	"sync"
	"time"
)

var ErrUserNotFound = errors.New("user not found")

type UserStore interface {
	GetUserBySub(ctx context.Context, sub string) (*User, error)
	CreateUser(ctx context.Context, user *User) error
	UpdateUser(ctx context.Context, user *User) error
}

type MemoryUserStore struct {
	mu    sync.RWMutex
	users map[string]*User
}

func NewMemoryUserStore() *MemoryUserStore {
	return &MemoryUserStore{
		users: make(map[string]*User),
	}
}

func (s *MemoryUserStore) GetUserBySub(_ context.Context, sub string) (*User, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	u, exists := s.users[sub]
	if !exists {
		return nil, ErrUserNotFound
	}
	return u, nil
}

func (s *MemoryUserStore) CreateUser(_ context.Context, user *User) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.users[user.AuthentikSub] = user
	return nil
}

func (s *MemoryUserStore) UpdateUser(_ context.Context, user *User) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.users[user.AuthentikSub] = user
	return nil
}

type JITProvisioner struct {
	store UserStore
}

func NewJITProvisioner(store UserStore) *JITProvisioner {
	return &JITProvisioner{store: store}
}

func (p *JITProvisioner) EnsureUser(ctx context.Context, claims *Claims) (*User, error) {
	existing, err := p.store.GetUserBySub(ctx, claims.Subject)
	if err == nil {
		existing.Email = claims.Email
		existing.Username = claims.PreferredUsername
		existing.Groups = claims.Groups
		existing.UpdatedAt = time.Now()
		if updateErr := p.store.UpdateUser(ctx, existing); updateErr != nil {
			return nil, updateErr
		}
		return existing, nil
	}

	newUser := &User{
		ID:           generateUUID(),
		AuthentikSub: claims.Subject,
		Username:     claims.PreferredUsername,
		Email:        claims.Email,
		Groups:       claims.Groups,
		CreatedAt:    time.Now(),
		UpdatedAt:    time.Now(),
	}

	if createErr := p.store.CreateUser(ctx, newUser); createErr != nil {
		return nil, createErr
	}

	return newUser, nil
}
