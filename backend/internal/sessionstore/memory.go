package sessionstore

import (
	"context"
	"sync"
	"time"
)

type memBlockEntry struct {
	expiresAt time.Time
}

type memOIDCEntry struct {
	data      OIDCStateData
	expiresAt time.Time
}

type MemoryStore struct {
	mu         sync.Mutex
	blocked    map[string]memBlockEntry
	oidcStates map[string]memOIDCEntry
	done       chan struct{}
	stopOnce   sync.Once
}

func NewMemoryStore() *MemoryStore {
	s := &MemoryStore{
		blocked:    make(map[string]memBlockEntry),
		oidcStates: make(map[string]memOIDCEntry),
		done:       make(chan struct{}),
	}
	go s.cleanupLoop()
	return s
}

func (s *MemoryStore) BlockToken(_ context.Context, jti string, expiresAt time.Time) error {
	s.mu.Lock()
	s.blocked[jti] = memBlockEntry{expiresAt: expiresAt}
	s.mu.Unlock()
	return nil
}

func (s *MemoryStore) IsTokenBlocked(_ context.Context, jti string) (bool, error) {
	s.mu.Lock()
	entry, ok := s.blocked[jti]
	s.mu.Unlock()
	if !ok {
		return false, nil
	}
	return time.Now().Before(entry.expiresAt), nil
}

func (s *MemoryStore) SaveOIDCState(_ context.Context, state string, data OIDCStateData, ttl time.Duration) error {
	s.mu.Lock()
	s.oidcStates[state] = memOIDCEntry{
		data:      data,
		expiresAt: time.Now().Add(ttl),
	}
	s.mu.Unlock()
	return nil
}

func (s *MemoryStore) ConsumeOIDCState(_ context.Context, state string) (*OIDCStateData, error) {
	s.mu.Lock()
	entry, ok := s.oidcStates[state]
	if ok {
		delete(s.oidcStates, state)
	}
	s.mu.Unlock()

	if !ok || time.Now().After(entry.expiresAt) {
		return nil, nil
	}
	return &entry.data, nil
}

func (s *MemoryStore) Cleanup(_ context.Context) error {
	now := time.Now()
	s.mu.Lock()
	for k, v := range s.blocked {
		if now.After(v.expiresAt) {
			delete(s.blocked, k)
		}
	}
	for k, v := range s.oidcStates {
		if now.After(v.expiresAt) {
			delete(s.oidcStates, k)
		}
	}
	s.mu.Unlock()
	return nil
}

func (s *MemoryStore) Stop() {
	s.stopOnce.Do(func() { close(s.done) })
}

func (s *MemoryStore) cleanupLoop() {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-s.done:
			return
		case <-ticker.C:
			_ = s.Cleanup(context.Background())
		}
	}
}
