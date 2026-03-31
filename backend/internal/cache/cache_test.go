package cache

import (
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestSetAndGet(t *testing.T) {
	t.Parallel()
	c := New[string](time.Minute, time.Minute)
	defer c.Stop()

	c.Set("key1", "value1")
	v, ok := c.Get("key1")
	assert.True(t, ok)
	assert.Equal(t, "value1", v)
}

func TestGetMiss(t *testing.T) {
	t.Parallel()
	c := New[int](time.Minute, time.Minute)
	defer c.Stop()

	v, ok := c.Get("nonexistent")
	assert.False(t, ok)
	assert.Zero(t, v)
}

func TestTTLExpiry(t *testing.T) {
	t.Parallel()
	c := New[string](50*time.Millisecond, 10*time.Millisecond)
	defer c.Stop()

	c.Set("ephemeral", "gone-soon")
	v, ok := c.Get("ephemeral")
	assert.True(t, ok)
	assert.Equal(t, "gone-soon", v)

	time.Sleep(80 * time.Millisecond)
	_, ok = c.Get("ephemeral")
	assert.False(t, ok, "entry should have expired")
}

func TestDelete(t *testing.T) {
	t.Parallel()
	c := New[string](time.Minute, time.Minute)
	defer c.Stop()

	c.Set("key", "val")
	c.Delete("key")
	_, ok := c.Get("key")
	assert.False(t, ok)
}

func TestDeleteNonexistent(t *testing.T) {
	t.Parallel()
	c := New[string](time.Minute, time.Minute)
	defer c.Stop()
	// Should not panic.
	c.Delete("nope")
}

func TestConcurrentAccess(t *testing.T) {
	t.Parallel()
	c := New[int](time.Minute, time.Minute)
	defer c.Stop()

	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			c.Set("counter", n)
			c.Get("counter")
		}(i)
	}
	wg.Wait()

	_, ok := c.Get("counter")
	assert.True(t, ok)
}

func TestCleanupEvictsExpired(t *testing.T) {
	t.Parallel()
	c := New[string](30*time.Millisecond, 20*time.Millisecond)
	defer c.Stop()

	c.Set("a", "1")
	c.Set("b", "2")
	time.Sleep(60 * time.Millisecond)

	// After cleanup runs, items map should be empty.
	c.mu.RLock()
	count := len(c.items)
	c.mu.RUnlock()
	assert.Equal(t, 0, count, "cleanup should have evicted expired entries")
}

func TestOverwrite(t *testing.T) {
	t.Parallel()
	c := New[string](time.Minute, time.Minute)
	defer c.Stop()

	c.Set("k", "v1")
	c.Set("k", "v2")
	v, ok := c.Get("k")
	assert.True(t, ok)
	assert.Equal(t, "v2", v)
}
