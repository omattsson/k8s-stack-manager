package auth

import (
"fmt"
"sync"
"testing"
"time"

"github.com/stretchr/testify/assert"
"github.com/stretchr/testify/require"
)

func TestStateStore_StoreAndRetrieve(t *testing.T) {
t.Parallel()

s := NewStateStore(time.Minute)
defer s.Stop()

state := &AuthState{
State:        "abc123",
CodeVerifier: "verifier-xyz",
RedirectURL:  "/dashboard",
CreatedAt:    time.Now(),
}
s.Store(state)

got, ok := s.Retrieve("abc123")
require.True(t, ok)
assert.Equal(t, "verifier-xyz", got.CodeVerifier)
assert.Equal(t, "/dashboard", got.RedirectURL)
assert.Equal(t, "abc123", got.State)
}

func TestStateStore_OneTimeUse(t *testing.T) {
t.Parallel()

s := NewStateStore(time.Minute)
defer s.Stop()

s.Store(&AuthState{State: "once", CreatedAt: time.Now()})

_, ok := s.Retrieve("once")
require.True(t, ok, "first retrieve should succeed")

_, ok = s.Retrieve("once")
assert.False(t, ok, "second retrieve must return false (one-time use)")
}

func TestStateStore_UnknownState(t *testing.T) {
t.Parallel()

s := NewStateStore(time.Minute)
defer s.Stop()

_, ok := s.Retrieve("does-not-exist")
assert.False(t, ok)
}

func TestStateStore_EmptyStateParam(t *testing.T) {
t.Parallel()

s := NewStateStore(time.Minute)
defer s.Stop()

_, ok := s.Retrieve("")
assert.False(t, ok)
}

func TestStateStore_ExpiredEntry(t *testing.T) {
t.Parallel()

// Very short TTL so any entry with a past CreatedAt is already expired.
s := NewStateStore(time.Millisecond)
defer s.Stop()

s.Store(&AuthState{
State:     "stale",
CreatedAt: time.Now().Add(-time.Second),
})

_, ok := s.Retrieve("stale")
assert.False(t, ok, "expired entry must not be retrievable")
}

func TestStateStore_NotExpiredEntry(t *testing.T) {
t.Parallel()

s := NewStateStore(time.Minute)
defer s.Stop()

s.Store(&AuthState{
State:     "fresh",
CreatedAt: time.Now(),
})

_, ok := s.Retrieve("fresh")
assert.True(t, ok, "fresh entry must be retrievable")
}

func TestStateStore_ConcurrentAccess(t *testing.T) {
t.Parallel()

s := NewStateStore(time.Minute)
defer s.Stop()

const n = 100
var wg sync.WaitGroup

for i := 0; i < n; i++ {
wg.Add(1)
i := i
go func() {
defer wg.Done()
s.Store(&AuthState{
State:     fmt.Sprintf("key-%d", i),
CreatedAt: time.Now(),
})
}()
}
wg.Wait()

for i := 0; i < n; i++ {
wg.Add(1)
i := i
go func() {
defer wg.Done()
s.Retrieve(fmt.Sprintf("key-%d", i))
}()
}
wg.Wait()
}

func TestStateStore_ConcurrentStoreAndRetrieve(t *testing.T) {
t.Parallel()

s := NewStateStore(time.Minute)
defer s.Stop()

const n = 50
var wg sync.WaitGroup

for i := 0; i < n; i++ {
wg.Add(2)
i := i
go func() {
defer wg.Done()
s.Store(&AuthState{
State:     fmt.Sprintf("concurrent-%d", i),
CreatedAt: time.Now(),
})
}()
go func() {
defer wg.Done()
s.Retrieve(fmt.Sprintf("concurrent-%d", i))
}()
}
wg.Wait()
}

func TestStateStore_Stop(t *testing.T) {
t.Parallel()

s := NewStateStore(time.Minute)
s.Stop()
}

func TestStateStore_StoreMultipleEntries(t *testing.T) {
t.Parallel()

s := NewStateStore(time.Minute)
defer s.Stop()

entries := []struct {
state    string
verifier string
}{
{"state-1", "verifier-1"},
{"state-2", "verifier-2"},
{"state-3", "verifier-3"},
}

for _, e := range entries {
s.Store(&AuthState{State: e.state, CodeVerifier: e.verifier, CreatedAt: time.Now()})
}

for _, e := range entries {
got, ok := s.Retrieve(e.state)
require.True(t, ok, "state %q must be retrievable", e.state)
assert.Equal(t, e.verifier, got.CodeVerifier)
}
}
