package websocket

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeRevocable struct {
	id       ClientIdentity
	instance string
	mu       sync.Mutex
	calls    []revokeCall
}

type revokeCall struct {
	code   int
	reason string
}

func (f *fakeRevocable) Identity() ClientIdentity { return f.id }
func (f *fakeRevocable) InstanceID() string       { return f.instance }
func (f *fakeRevocable) Revoke(code int, reason string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, revokeCall{code, reason})
}
func (f *fakeRevocable) got() []revokeCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]revokeCall(nil), f.calls...)
}

func TestHub_RevocableDisconnect(t *testing.T) {
	t.Parallel()
	past := time.Now().Add(-time.Hour)
	future := time.Now().Add(time.Hour)

	tests := []struct {
		name       string
		disconnect func(h *Hub) int
		wantRevoke []bool // per revocable below
	}{
		{"user", func(h *Hub) int { return h.DisconnectUser("u1") }, []bool{true, true, false, true}},
		{"token", func(h *Hub) int { return h.DisconnectToken("jti-b") }, []bool{false, true, false, false}},
		{"api key", func(h *Hub) int { return h.DisconnectAPIKey("key-1") }, []bool{false, false, false, true}},
		{"user local", func(h *Hub) int { return h.disconnectUserLocal("u2") }, []bool{false, false, true, false}},
		{"fan-out user, issued before the row", func(h *Hub) int { return h.disconnectUserIssuedBefore("u1", future) }, []bool{true, true, false, true}},
		{"fan-out user, issued after the row", func(h *Hub) int { return h.disconnectUserIssuedBefore("u1", past) }, []bool{false, false, false, true}},
		{"unknown user", func(h *Hub) int { return h.DisconnectUser("nobody") }, []bool{false, false, false, false}},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			hub := NewHub()
			go hub.Run()
			defer hub.Shutdown()

			now := time.Now()
			rs := []*fakeRevocable{
				{id: ClientIdentity{UserID: "u1", TokenID: "jti-a", IssuedAt: now.Add(-time.Minute)}},
				{id: ClientIdentity{UserID: "u1", TokenID: "jti-b", IssuedAt: now.Add(-time.Minute)}},
				{id: ClientIdentity{UserID: "u2", TokenID: "jti-c", IssuedAt: now}},
				{id: ClientIdentity{UserID: "u1", APIKeyID: "key-1"}},
			}
			// The key revocable has a zero IssuedAt: it counts as issued before.
			for _, r := range rs {
				_, err := hub.RegisterRevocable(r)
				require.NoError(t, err)
			}
			assert.Equal(t, 0, hub.ClientCount(), "revocables are not clients")
			assert.Equal(t, 4, hub.RevocableCount())

			n := tt.disconnect(hub)
			want := 0
			for i, r := range rs {
				if tt.wantRevoke[i] {
					want++
					assert.Equal(t, []revokeCall{{CloseCodePolicyViolation, closeReasonRevoked}}, r.got(), "revocable %d", i)
				} else {
					assert.Empty(t, r.got(), "revocable %d", i)
				}
			}
			assert.Equal(t, want, n)
			assert.Equal(t, 4-want, hub.RevocableCount(), "a revoked revocable leaves the set")
		})
	}
}

func TestHub_DisconnectMixedClientsAndRevocables(t *testing.T) {
	t.Parallel()
	hub := NewHub()
	go hub.Run()
	defer hub.Shutdown()

	c := &Client{hub: hub, identity: ClientIdentity{UserID: "u1"}, send: make(chan []byte, 1)}
	require.NoError(t, hub.Register(c))
	r := &fakeRevocable{id: ClientIdentity{UserID: "u1"}}
	_, err := hub.RegisterRevocable(r)
	require.NoError(t, err)

	assert.Equal(t, 2, hub.DisconnectUser("u1"))
	assert.True(t, isClosed(c.send))
	assert.Len(t, r.got(), 1)
}

func TestHub_RegisterRevocable(t *testing.T) {
	t.Parallel()

	t.Run("unregister removes and is idempotent", func(t *testing.T) {
		t.Parallel()
		hub := NewHub()
		go hub.Run()
		defer hub.Shutdown()
		r := &fakeRevocable{id: ClientIdentity{UserID: "u1"}}
		unregister, err := hub.RegisterRevocable(r)
		require.NoError(t, err)
		unregister()
		unregister()
		assert.Equal(t, 0, hub.RevocableCount())
		assert.Equal(t, 0, hub.DisconnectUser("u1"))
		assert.Empty(t, r.got())
	})

	t.Run("closed hub refuses", func(t *testing.T) {
		t.Parallel()
		hub := NewHub()
		hub.Shutdown()
		_, err := hub.RegisterRevocable(&fakeRevocable{})
		assert.ErrorIs(t, err, ErrHubClosed)
	})
}

func TestHub_ShutdownRevokesRevocables(t *testing.T) {
	t.Parallel()
	hub := NewHub()
	done := make(chan struct{})
	go func() { hub.Run(); close(done) }()
	r := &fakeRevocable{id: ClientIdentity{UserID: "u1"}}
	_, err := hub.RegisterRevocable(r)
	require.NoError(t, err)

	hub.Shutdown()
	<-done
	assert.Equal(t, []revokeCall{{CloseCodeGoingAway, CloseReasonShutdown}}, r.got())
	assert.Equal(t, 0, hub.RevocableCount())
}

func TestHub_DisconnectInstance(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		instance string
		want     []bool
	}{
		{"match", "i1", []bool{true, false, true}},
		{"no match", "nope", []bool{false, false, false}},
		{"empty", "", []bool{false, false, false}},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			hub := NewHub()
			go hub.Run()
			defer hub.Shutdown()
			rs := []*fakeRevocable{{instance: "i1"}, {instance: "i2"}, {instance: "i1"}}
			for _, r := range rs {
				_, err := hub.RegisterRevocable(r)
				require.NoError(t, err)
			}
			n := hub.DisconnectInstance(tt.instance)
			want := 0
			for i, r := range rs {
				if tt.want[i] {
					want++
					assert.Equal(t, []revokeCall{{CloseCodePolicyViolation, CloseReasonAccessLost}}, r.got())
				} else {
					assert.Empty(t, r.got())
				}
			}
			assert.Equal(t, want, n)
		})
	}
	var nilHub *Hub
	assert.Equal(t, 0, nilHub.DisconnectInstance("i1"))
}

func TestHub_RevalidateRevocables(t *testing.T) {
	t.Parallel()

	t.Run("identity check closes revoked revocables and clients", func(t *testing.T) {
		t.Parallel()
		hub := NewHub()
		go hub.Run()
		defer hub.Shutdown()
		good := &fakeRevocable{id: ClientIdentity{UserID: "u1", TokenID: "ok"}}
		bad := &fakeRevocable{id: ClientIdentity{UserID: "u2", TokenID: "bad"}}
		c := &Client{hub: hub, identity: ClientIdentity{UserID: "u2", TokenID: "bad"}, send: make(chan []byte, 1)}
		for _, r := range []*fakeRevocable{good, bad} {
			_, err := hub.RegisterRevocable(r)
			require.NoError(t, err)
		}
		require.NoError(t, hub.Register(c))

		var seen []ClientIdentity
		closed := hub.revalidate(func(_ context.Context, ids []ClientIdentity) []bool {
			seen = ids
			out := make([]bool, len(ids))
			for i, id := range ids {
				out[i] = id.TokenID == "bad"
			}
			return out
		})
		assert.Equal(t, 2, closed)
		assert.Len(t, seen, 2, "the same identity of a client and a revocable is checked once")
		assert.Empty(t, good.got())
		assert.Equal(t, []revokeCall{{CloseCodePolicyViolation, closeReasonRevoked}}, bad.got())
		assert.True(t, isClosed(c.send))
	})

	t.Run("access checker", func(t *testing.T) {
		t.Parallel()
		tests := []struct {
			name     string
			checker  RevocableAccessChecker
			wantLost []bool
		}{
			{"lost access closes", func(_ context.Context, rs []Revocable) []bool {
				out := make([]bool, len(rs))
				for i, r := range rs {
					out[i] = r.InstanceID() == "gone"
				}
				return out
			}, []bool{false, true}},
			{"all fail open", func(_ context.Context, rs []Revocable) []bool { return make([]bool, len(rs)) }, []bool{false, false}},
			{"wrong result size is ignored", func(context.Context, []Revocable) []bool { return []bool{true} }, []bool{false, false}},
			{"no checker", nil, []bool{false, false}},
		}
		for _, tt := range tests {
			tt := tt
			t.Run(tt.name, func(t *testing.T) {
				t.Parallel()
				hub := NewHub()
				go hub.Run()
				defer hub.Shutdown()
				hub.SetRevocableAccessChecker(tt.checker)
				rs := []*fakeRevocable{
					{id: ClientIdentity{UserID: "u1", TokenID: "a"}, instance: "i1"},
					{id: ClientIdentity{UserID: "u1", TokenID: "b"}, instance: "gone"},
				}
				for _, r := range rs {
					_, err := hub.RegisterRevocable(r)
					require.NoError(t, err)
				}
				// No identity checker hit: nothing is revoked by identity.
				hub.revalidate(func(_ context.Context, ids []ClientIdentity) []bool { return make([]bool, len(ids)) })
				for i, r := range rs {
					if tt.wantLost[i] {
						assert.Equal(t, []revokeCall{{CloseCodePolicyViolation, CloseReasonAccessLost}}, r.got())
					} else {
						assert.Empty(t, r.got())
					}
				}
			})
		}
	})

	t.Run("access checker runs without identities", func(t *testing.T) {
		t.Parallel()
		hub := NewHub()
		go hub.Run()
		defer hub.Shutdown()
		hub.SetRevocableAccessChecker(func(_ context.Context, rs []Revocable) []bool {
			out := make([]bool, len(rs))
			for i := range out {
				out[i] = true
			}
			return out
		})
		r := &fakeRevocable{} // no credential identity
		_, err := hub.RegisterRevocable(r)
		require.NoError(t, err)
		assert.Equal(t, 1, hub.revalidate(func(_ context.Context, ids []ClientIdentity) []bool { return make([]bool, len(ids)) }))
		assert.Len(t, r.got(), 1)
	})
}

func TestHub_RevokeOutsideLock(t *testing.T) {
	t.Parallel()
	hub := NewHub()
	go hub.Run()
	defer hub.Shutdown()
	// A Revoke that calls back into the hub would deadlock under the lock.
	r := &reentrantRevocable{hub: hub}
	_, err := hub.RegisterRevocable(r)
	require.NoError(t, err)
	done := make(chan struct{})
	go func() { hub.DisconnectInstance("i1"); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("deadlock: Revoke ran under the hub lock")
	}
	assert.Equal(t, 1, r.n)
}

type reentrantRevocable struct {
	hub *Hub
	n   int
}

func (r *reentrantRevocable) Identity() ClientIdentity { return ClientIdentity{} }
func (r *reentrantRevocable) InstanceID() string       { return "i1" }
func (r *reentrantRevocable) Revoke(int, string) {
	r.n++
	_ = r.hub.ClientCount()
	_ = r.hub.RevocableCount()
}

func TestHub_PanicInRevokeIsContained(t *testing.T) {
	t.Parallel()
	hub := NewHub()
	go hub.Run()
	defer hub.Shutdown()
	ok := &fakeRevocable{instance: "i1"}
	_, _ = hub.RegisterRevocable(&panicRevocable{})
	_, _ = hub.RegisterRevocable(ok)
	assert.Equal(t, 2, hub.DisconnectInstance("i1"))
	assert.Len(t, ok.got(), 1)
}

type panicRevocable struct{}

func (*panicRevocable) Identity() ClientIdentity { return ClientIdentity{} }
func (*panicRevocable) InstanceID() string       { return "i1" }
func (*panicRevocable) Revoke(int, string)       { panic("boom") }

func TestExportedLogLimiter(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		interval time.Duration
		wantSeq  []bool
	}{
		{"once per interval", time.Hour, []bool{true, false, false}},
		{"zero interval allows all", 0, []bool{true, true, true}},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			l := NewLogLimiter(tt.interval)
			for i, want := range tt.wantSeq {
				assert.Equal(t, want, l.Allow(), "call %d", i)
			}
		})
	}
	var nilLimiter *LogLimiter
	assert.True(t, nilLimiter.Allow())

	l := NewLogLimiter(20 * time.Millisecond)
	assert.True(t, l.Allow())
	time.Sleep(40 * time.Millisecond)
	assert.True(t, l.Allow())
}
