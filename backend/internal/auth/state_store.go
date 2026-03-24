package auth

import (
	"sync"
	"time"
)

// AuthState holds PKCE and CSRF state for an in-flight OIDC authorization.
type AuthState struct {
	State        string
	CodeVerifier string
	RedirectURL  string
	CreatedAt    time.Time
}

// StateStore manages short-lived AuthState entries with automatic TTL cleanup.
type StateStore struct {
	mu      sync.RWMutex
	entries map[string]*AuthState
	ttl     time.Duration
	done    chan struct{}
}

// NewStateStore creates a new StateStore that expires entries after the given TTL.
// A background goroutine cleans up expired entries every minute.
func NewStateStore(ttl time.Duration) *StateStore {
	s := &StateStore{
		entries: make(map[string]*AuthState),
		ttl:     ttl,
		done:    make(chan struct{}),
	}
	go s.cleanup()
	return s
}

// Store saves an AuthState entry keyed by its State nonce.
func (s *StateStore) Store(state *AuthState) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries[state.State] = state
}

// Retrieve returns and deletes the AuthState for the given state parameter (one-time use).
// Returns nil and false if the state is not found or has expired.
func (s *StateStore) Retrieve(stateParam string) (*AuthState, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.entries[stateParam]
	if !ok {
		return nil, false
	}
	delete(s.entries, stateParam)
	if time.Since(entry.CreatedAt) > s.ttl {
		return nil, false
	}
	return entry, true
}

// Stop ends the background cleanup goroutine.
func (s *StateStore) Stop() {
	close(s.done)
}

func (s *StateStore) cleanup() {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-s.done:
			return
		case <-ticker.C:
			s.mu.Lock()
			now := time.Now()
			for k, v := range s.entries {
				if now.Sub(v.CreatedAt) > s.ttl {
					delete(s.entries, k)
				}
			}
			s.mu.Unlock()
		}
	}
}
