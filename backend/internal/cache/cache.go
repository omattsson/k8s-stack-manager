// Package cache provides a generic, concurrent-safe, in-memory TTL cache.
package cache

import (
	"sync"
	"time"
)

type entry[V any] struct {
	value     V
	expiresAt time.Time
}

// TTLCache is a generic in-memory cache with per-entry expiration.
type TTLCache[V any] struct {
	mu              sync.RWMutex
	items           map[string]entry[V]
	ttl             time.Duration
	cleanupInterval time.Duration
	stopCh          chan struct{}
}

// New creates a TTLCache with the given default TTL and cleanup interval.
// A background goroutine evicts expired entries every cleanupInterval.
// Call Stop() when the cache is no longer needed.
func New[V any](ttl, cleanupInterval time.Duration) *TTLCache[V] {
	c := &TTLCache[V]{
		items:           make(map[string]entry[V]),
		ttl:             ttl,
		cleanupInterval: cleanupInterval,
		stopCh:          make(chan struct{}),
	}
	go c.cleanupLoop()
	return c
}

// Get returns the cached value and true if the key exists and has not expired.
func (c *TTLCache[V]) Get(key string) (V, bool) {
	c.mu.RLock()
	e, ok := c.items[key]
	c.mu.RUnlock()
	if !ok || time.Now().After(e.expiresAt) {
		var zero V
		return zero, false
	}
	return e.value, true
}

// Set stores a value with the cache's default TTL.
func (c *TTLCache[V]) Set(key string, value V) {
	c.mu.Lock()
	c.items[key] = entry[V]{value: value, expiresAt: time.Now().Add(c.ttl)}
	c.mu.Unlock()
}

// Delete removes a key from the cache.
func (c *TTLCache[V]) Delete(key string) {
	c.mu.Lock()
	delete(c.items, key)
	c.mu.Unlock()
}

// Stop halts the background cleanup goroutine.
func (c *TTLCache[V]) Stop() {
	close(c.stopCh)
}

func (c *TTLCache[V]) cleanupLoop() {
	ticker := time.NewTicker(c.cleanupInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			c.evictExpired()
		case <-c.stopCh:
			return
		}
	}
}

func (c *TTLCache[V]) evictExpired() {
	now := time.Now()
	c.mu.Lock()
	for k, e := range c.items {
		if now.After(e.expiresAt) {
			delete(c.items, k)
		}
	}
	c.mu.Unlock()
}
