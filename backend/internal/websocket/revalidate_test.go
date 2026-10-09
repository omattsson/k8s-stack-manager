package websocket

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHub_Revalidate(t *testing.T) {
	t.Parallel()

	iat := time.Now().Add(-time.Minute).Truncate(time.Second)
	identities := []ClientIdentity{
		{UserID: "u1", TokenID: "jti-a", IssuedAt: iat},
		{UserID: "u1", TokenID: "jti-a", IssuedAt: iat}, // second tab, same token
		{UserID: "u1", TokenID: "jti-b", IssuedAt: iat},
		{UserID: "u2", TokenID: "jti-c", IssuedAt: iat},
		{}, // socket without identity: never checked
	}

	tests := []struct {
		name       string
		revoke     map[string]bool // token IDs the checker revokes
		badSize    bool
		panics     bool
		wantClosed []int
	}{
		{name: "nothing revoked"},
		{name: "one token revoked closes both tabs of it", revoke: map[string]bool{"jti-a": true}, wantClosed: []int{0, 1}},
		{name: "two tokens revoked", revoke: map[string]bool{"jti-b": true, "jti-c": true}, wantClosed: []int{2, 3}},
		{name: "wrong result size closes nothing", revoke: map[string]bool{"jti-a": true}, badSize: true},
		{name: "panic in checker closes nothing", panics: true},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			hub := NewHub()
			go hub.Run()
			defer hub.Shutdown()

			clients := make([]*Client, len(identities))
			for i, id := range identities {
				clients[i] = &Client{hub: hub, identity: id, send: make(chan []byte, 1)}
				require.NoError(t, hub.Register(clients[i]))
			}

			var calls int
			var checked []ClientIdentity
			check := func(_ context.Context, ids []ClientIdentity) []bool {
				calls++
				checked = ids
				if tt.panics {
					panic("checker bug")
				}
				out := make([]bool, len(ids))
				for i, id := range ids {
					out[i] = tt.revoke[id.TokenID]
				}
				if tt.badSize {
					return out[:1]
				}
				return out
			}

			closed := hub.revalidate(check)

			assert.Equal(t, 1, calls)
			assert.Len(t, checked, 3, "distinct identities only, without the empty one")
			assert.Equal(t, len(tt.wantClosed), closed)
			want := map[int]bool{}
			for _, i := range tt.wantClosed {
				want[i] = true
			}
			for i, c := range clients {
				assert.Equal(t, want[i], isClosed(c.send), "client %d send closed", i)
			}
			assert.Equal(t, len(clients)-len(tt.wantClosed), hub.ClientCount())
		})
	}
}

func TestHub_RevalidateNoClients(t *testing.T) {
	t.Parallel()
	hub := NewHub()
	called := false
	assert.Equal(t, 0, hub.revalidate(func(context.Context, []ClientIdentity) []bool { called = true; return nil }))
	assert.False(t, called, "no sockets: no check")
}

func TestHub_StartRevalidation(t *testing.T) {
	t.Parallel()

	hub := NewHub()
	go hub.Run()

	c := &Client{hub: hub, identity: ClientIdentity{UserID: "u1", TokenID: "jti-1"}, send: make(chan []byte, 1)}
	require.NoError(t, hub.Register(c))

	var mu sync.Mutex
	runs := 0
	check := func(_ context.Context, ids []ClientIdentity) []bool {
		mu.Lock()
		runs++
		mu.Unlock()
		return []bool{true}
	}
	hub.StartRevalidation(10*time.Millisecond, check)
	hub.StartRevalidation(10*time.Millisecond, check) // second call starts nothing

	waitForClientCount(t, hub, 0)
	assert.True(t, isClosed(c.send))

	hub.Shutdown()
	select {
	case <-hub.revalidateDone:
	case <-time.After(2 * time.Second):
		t.Fatal("revalidation loop did not stop on shutdown")
	}
}

func TestHub_StartRevalidationNoop(t *testing.T) {
	t.Parallel()

	var nilHub *Hub
	nilHub.StartRevalidation(time.Millisecond, func(context.Context, []ClientIdentity) []bool { return nil })

	hub := NewHub()
	hub.StartRevalidation(time.Millisecond, nil)
	hub.StartRevalidation(0, func(context.Context, []ClientIdentity) []bool { return nil })
	select {
	case <-hub.revalidateDone:
		t.Fatal("no loop must start")
	case <-time.After(20 * time.Millisecond):
	}
}

func TestHub_SubscribeRemovedClient(t *testing.T) {
	t.Parallel()

	hub := NewHub()
	go hub.Run()
	defer hub.Shutdown()

	c := &Client{hub: hub, identity: ClientIdentity{UserID: "u1"}, send: make(chan []byte, 1)}
	require.NoError(t, hub.Register(c))
	assert.True(t, hub.DisconnectClient(c))
	assert.False(t, hub.DisconnectClient(c), "second disconnect is a no-op")

	// A subscribe message that the read pump handles after the removal.
	hub.Subscribe(c, "inst-1")
	hub.mu.RLock()
	assert.Empty(t, hub.instanceSubs["inst-1"])
	hub.mu.RUnlock()
	assert.NotPanics(t, func() { hub.BroadcastToInstance("inst-1", []byte("log")) })
}

func TestHub_BroadcastToInstanceSkipsRemovedClient(t *testing.T) {
	t.Parallel()

	hub := NewHub()
	c := &Client{hub: hub, send: make(chan []byte, 1)}
	require.NoError(t, hub.Register(c))
	hub.Subscribe(c, "inst-1")
	// Simulate a stale subscription of a removed client.
	hub.mu.Lock()
	delete(hub.clients, c)
	close(c.send)
	hub.mu.Unlock()

	assert.NotPanics(t, func() { hub.BroadcastToInstance("inst-1", []byte("log")) })
}
