package middleware

import (
	"sync"
	"time"
)

const defaultCleanupInterval = time.Minute

// TokenBlocklist maintains an in-memory set of revoked JWT token IDs (jti).
// Entries auto-expire after the access token TTL to bound memory usage.
// This is used to immediately invalidate access tokens on logout without
// waiting for natural expiry.
type TokenBlocklist struct {
	mu       sync.RWMutex
	entries  map[string]time.Time // jti → expiry time
	stopCh   chan struct{}
	stopOnce sync.Once
}

// NewTokenBlocklist creates a blocklist that cleans up expired entries
// at the given interval. Call Stop() when no longer needed.
func NewTokenBlocklist(cleanupInterval time.Duration) *TokenBlocklist {
	if cleanupInterval <= 0 {
		cleanupInterval = defaultCleanupInterval
	}
	bl := &TokenBlocklist{
		entries: make(map[string]time.Time),
		stopCh:  make(chan struct{}),
	}
	go bl.cleanupLoop(cleanupInterval)
	return bl
}

// Add adds a token to the blocklist. The token will be automatically removed
// after its expiry time (no need to block longer than the token's lifetime).
func (bl *TokenBlocklist) Add(tokenID string, expiresAt time.Time) {
	bl.mu.Lock()
	bl.entries[tokenID] = expiresAt
	bl.mu.Unlock()
}

// IsBlocked returns true if the given token ID is in the blocklist.
func (bl *TokenBlocklist) IsBlocked(tokenID string) bool {
	bl.mu.RLock()
	_, ok := bl.entries[tokenID]
	bl.mu.RUnlock()
	return ok
}

// Stop halts the background cleanup goroutine. Safe to call multiple times.
func (bl *TokenBlocklist) Stop() {
	bl.stopOnce.Do(func() {
		close(bl.stopCh)
	})
}

func (bl *TokenBlocklist) cleanupLoop(interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-bl.stopCh:
			return
		case now := <-ticker.C:
			bl.mu.Lock()
			for id, exp := range bl.entries {
				if now.After(exp) {
					delete(bl.entries, id)
				}
			}
			bl.mu.Unlock()
		}
	}
}
