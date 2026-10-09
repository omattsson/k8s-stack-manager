package websocket

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"testing"
	"time"

	"backend/internal/database"
	"backend/internal/models"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// fakeEventRepo is an in-memory models.WSEventRepository. reserve allocates
// an ID for a row that is not visible yet (an insert that has not
// committed); commit makes it visible. This simulates MySQL auto-increment
// IDs that commit out of order.
type fakeEventRepo struct {
	rows      map[int64]models.WSEvent
	hidden    map[int64]bool
	insertErr error
	listErr   error
	maxErr    error
	block     chan struct{} // non-nil: Insert waits until it is closed
	nextID    int64
	inserts   int
	mu        sync.Mutex
}

var _ models.WSEventRepository = (*fakeEventRepo)(nil)

func newFakeEventRepo() *fakeEventRepo {
	return &fakeEventRepo{rows: map[int64]models.WSEvent{}, hidden: map[int64]bool{}}
}

func (r *fakeEventRepo) Insert(_ context.Context, events []*models.WSEvent) error {
	r.mu.Lock()
	block := r.block
	r.mu.Unlock()
	if block != nil {
		<-block
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.inserts++
	if r.insertErr != nil {
		return r.insertErr
	}
	for _, ev := range events {
		r.nextID++
		ev.ID = r.nextID
		r.rows[ev.ID] = *ev
	}
	return nil
}

// reserve stores a row that is not visible yet and returns its ID.
func (r *fakeEventRepo) reserve(ev models.WSEvent) int64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.nextID++
	ev.ID = r.nextID
	r.rows[ev.ID] = ev
	r.hidden[ev.ID] = true
	return ev.ID
}

// add stores a visible row and returns its ID.
func (r *fakeEventRepo) add(ev models.WSEvent) int64 {
	id := r.reserve(ev)
	r.commit(id)
	return id
}

func (r *fakeEventRepo) commit(id int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.hidden, id)
}

func (r *fakeEventRepo) rollback(id int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.hidden, id)
	delete(r.rows, id)
}

func (r *fakeEventRepo) setErrors(list, maxID, insert error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.listErr, r.maxErr, r.insertErr = list, maxID, insert
}

func (r *fakeEventRepo) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.rows)
}

func (r *fakeEventRepo) insertCalls() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.inserts
}

func (r *fakeEventRepo) MaxID(context.Context) (int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.maxErr != nil {
		return 0, r.maxErr
	}
	var maxID int64
	for id := range r.rows {
		if !r.hidden[id] && id > maxID {
			maxID = id
		}
	}
	return maxID, nil
}

func (r *fakeEventRepo) visible(match func(id int64) bool, skipPayloadOrigin string) []models.WSEvent {
	var out []models.WSEvent
	for id, ev := range r.rows {
		if r.hidden[id] || !match(id) {
			continue
		}
		if ev.Origin == skipPayloadOrigin {
			ev.Payload = ""
		}
		out = append(out, ev)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func (r *fakeEventRepo) ListAfter(_ context.Context, afterID int64, limit int, skip string) ([]models.WSEvent, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.listErr != nil {
		return nil, r.listErr
	}
	out := r.visible(func(id int64) bool { return id > afterID }, skip)
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (r *fakeEventRepo) ListByIDs(_ context.Context, ids []int64, skip string) ([]models.WSEvent, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.listErr != nil {
		return nil, r.listErr
	}
	want := make(map[int64]bool, len(ids))
	for _, id := range ids {
		want[id] = true
	}
	return r.visible(func(id int64) bool { return want[id] }, skip), nil
}

func (r *fakeEventRepo) IDsAfter(_ context.Context, afterID int64, limit int) ([]int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.listErr != nil {
		return nil, r.listErr
	}
	var ids []int64
	for _, ev := range r.visible(func(id int64) bool { return id > afterID }, "") {
		ids = append(ids, ev.ID)
	}
	if len(ids) > limit {
		ids = ids[:limit]
	}
	return ids, nil
}

// restartIDs deletes all rows and restarts the IDs at 1.
func (r *fakeEventRepo) restartIDs() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.rows = map[int64]models.WSEvent{}
	r.hidden = map[int64]bool{}
	r.nextID = 0
}

// deleteAll deletes all rows; the IDs continue.
func (r *fakeEventRepo) deleteAll() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.rows = map[int64]models.WSEvent{}
	r.hidden = map[int64]bool{}
}

func (r *fakeEventRepo) DeleteOlderThan(_ context.Context, t time.Time, _ int) (int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var n int64
	for id, ev := range r.rows {
		if ev.CreatedAt.Before(t) {
			delete(r.rows, id)
			n++
		}
	}
	return n, nil
}

// newTestClient registers a client of userID on the hub and subscribes it to
// the instances.
func newTestClient(t *testing.T, hub *Hub, userID string, instances ...string) *Client {
	t.Helper()
	c := &Client{hub: hub, send: make(chan []byte, sendBufferSize), identity: ClientIdentity{UserID: userID}}
	require.NoError(t, hub.Register(c))
	for _, id := range instances {
		hub.Subscribe(c, id)
	}
	return c
}

// drain returns the messages queued for the client.
func drain(c *Client) []string {
	var out []string
	for {
		select {
		case m, ok := <-c.send:
			if !ok {
				return out
			}
			out = append(out, string(m))
		default:
			return out
		}
	}
}

// collect reads messages of the client until it has want messages or the
// timeout expires.
func collect(t *testing.T, c *Client, want int, timeout time.Duration) []string {
	t.Helper()
	var out []string
	deadline := time.After(timeout)
	for len(out) < want {
		select {
		case m := <-c.send:
			out = append(out, string(m))
		case <-deadline:
			return out
		}
	}
	return out
}

func newSQLiteEventRepo(t *testing.T) models.WSEventRepository {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	// One connection: SQLite ":memory:" is one database per connection.
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	require.NoError(t, db.AutoMigrate(&models.WSEvent{}))
	return database.NewGORMWSEventRepository(db)
}

// startReplica starts a hub with a started fan-out on repo and waits until
// the poller has read its start position.
func startReplica(t *testing.T, repo models.WSEventRepository, origin string) (*Hub, *Fanout) {
	t.Helper()
	hub := NewHub()
	go hub.Run()
	f := NewFanout(hub, repo, FanoutConfig{Origin: origin, PollInterval: 10 * time.Millisecond})
	require.NotNil(t, f)
	f.Start()
	t.Cleanup(func() {
		f.Stop()
		hub.Shutdown()
	})
	select {
	case <-f.ready:
	case <-time.After(2 * time.Second):
		t.Fatal("fan-out poller did not start")
	}
	return hub, f
}

// TestFanout_TwoHubsOneDatabase checks that a send on replica A reaches the
// right clients of replica B through one database, and that A delivers to
// its own clients once. A leader-only producer (k8s status watcher, cluster
// health poller) calls Broadcast on the leader hub, as replica A does here.
func TestFanout_TwoHubsOneDatabase(t *testing.T) {
	t.Parallel()

	tests := []struct {
		send func(h *Hub, msg []byte)
		name string
		// wantB lists the clients of replica B that get the message.
		wantAll, wantInst, wantUser, wantOrigin bool
	}{
		{
			name:    "broadcast reaches all clients",
			send:    func(h *Hub, msg []byte) { h.Broadcast(msg) },
			wantAll: true, wantInst: true, wantUser: true, wantOrigin: true,
		},
		{
			name:     "instance message reaches subscribed clients only",
			send:     func(h *Hub, msg []byte) { h.BroadcastToInstance("inst-1", msg) },
			wantInst: true, wantOrigin: true,
		},
		{
			name:     "user message reaches the clients of the user only",
			send:     func(h *Hub, msg []byte) { h.BroadcastToUser("u3", msg) },
			wantUser: true, wantOrigin: true,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			repo := newSQLiteEventRepo(t)
			hubA, fA := startReplica(t, repo, "replica-a")
			hubB, fB := startReplica(t, repo, "replica-b")

			// Replica A: one client of user u3, subscribed to inst-1.
			origin := newTestClient(t, hubA, "u3", "inst-1")
			// Replica B: a client per target kind.
			bAll := newTestClient(t, hubB, "u1")
			bInst := newTestClient(t, hubB, "u2", "inst-1")
			bUser := newTestClient(t, hubB, "u3")

			msg := []byte(`{"type":"test","payload":{"n":1}}`)
			tt.send(hubA, msg)

			// Wait for B to deliver, then for a few more polls of both
			// replicas, so a duplicate or a wrong target would show up.
			require.Eventually(t, func() bool { return fB.Stats().Delivered == 1 }, 2*time.Second, 5*time.Millisecond)
			time.Sleep(100 * time.Millisecond)

			check := func(name string, c *Client, want bool) {
				got := drain(c)
				if want {
					assert.Equal(t, []string{string(msg)}, got, "%s gets the message once", name)
				} else {
					assert.Empty(t, got, "%s must not get the message", name)
				}
			}
			check("origin client on A", origin, tt.wantOrigin)
			check("B client (all)", bAll, tt.wantAll)
			check("B client (instance)", bInst, tt.wantInst)
			check("B client (user)", bUser, tt.wantUser)

			assert.Equal(t, int64(1), fA.Stats().Written)
			assert.Equal(t, int64(0), fA.Stats().Delivered, "A skips its own row")
			assert.Equal(t, int64(1), fB.Stats().Delivered)
		})
	}
}

// newPollFanout creates a Fanout that the test polls by hand (no goroutines)
// with a fake clock.
func newPollFanout(t *testing.T, repo models.WSEventRepository, origin string, now *time.Time) (*Hub, *Fanout) {
	t.Helper()
	hub := NewHub()
	f := NewFanout(hub, repo, FanoutConfig{Origin: origin, GapTimeout: 10 * time.Second, PollBatch: 2})
	require.NotNil(t, f)
	f.now = func() time.Time { return *now }
	return hub, f
}

func row(origin, target, payload string) models.WSEvent {
	return models.WSEvent{CreatedAt: time.Now().UTC(), Origin: origin, Target: target, Payload: payload}
}

// TestFanout_OutOfOrderCommit checks the gap handling: a row with a lower ID
// that becomes visible after a higher ID was read is still delivered, once;
// a gap that never fills (rolled-back insert) expires.
func TestFanout_OutOfOrderCommit(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repo := newFakeEventRepo()
	now := time.Now()
	hub, f := newPollFanout(t, repo, "b", &now)
	c := newTestClient(t, hub, "u1", "inst-1")

	f.poll(ctx) // start position: empty table
	require.True(t, f.started)

	// Replica a allocates id 1 but commits late; replica c commits id 2.
	late := repo.reserve(row("a", "instance:inst-1", "late"))
	repo.add(row("c", "instance:inst-1", "early"))

	f.poll(ctx)
	assert.Equal(t, []string{"early"}, drain(c))
	assert.Contains(t, f.missing, late, "the skipped ID is tracked")

	repo.commit(late)
	f.poll(ctx)
	assert.Equal(t, []string{"late"}, drain(c), "the late row is delivered")
	assert.Empty(t, f.missing)

	f.poll(ctx)
	assert.Empty(t, drain(c), "no second delivery")

	// A rolled-back insert leaves a gap that never fills.
	gone := repo.reserve(row("a", "instance:inst-1", "rolled back"))
	repo.add(row("a", "instance:inst-1", "after gap"))
	repo.rollback(gone)
	f.poll(ctx)
	assert.Equal(t, []string{"after gap"}, drain(c))
	assert.Contains(t, f.missing, gone)

	now = now.Add(11 * time.Second)
	f.poll(ctx)
	assert.NotContains(t, f.missing, gone, "the gap expires after GapTimeout")
	assert.Empty(t, drain(c))
	assert.Equal(t, int64(3), f.Stats().Delivered)
}

func TestFanout_PollReadsAllPagesAndSkipsOwnRows(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repo := newFakeEventRepo()
	now := time.Now()

	// Rows before the start are history and are not replayed.
	repo.add(row("a", models.WSEventTargetAll, "history"))

	hub, f := newPollFanout(t, repo, "b", &now)
	c := newTestClient(t, hub, "u1")
	f.poll(ctx)
	assert.Empty(t, drain(c), "no replay of rows before the start")

	// PollBatch is 2: five rows need three pages in one poll.
	for i := 1; i <= 5; i++ {
		origin := "a"
		if i == 3 {
			origin = "b" // own row: skipped, but its ID is no gap
		}
		repo.add(row(origin, "user:u1", fmt.Sprintf("m%d", i)))
	}
	f.poll(ctx)
	assert.Equal(t, []string{"m1", "m2", "m4", "m5"}, drain(c))
	assert.Empty(t, f.missing, "own rows are read, so they are no gap")

	// Unknown target and other users are not delivered.
	repo.add(row("a", "user:u2", "other user"))
	repo.add(row("a", "bogus", "unknown target"))
	f.poll(ctx)
	assert.Empty(t, drain(c))
}

func TestFanout_PollErrorsRetryOnNextTick(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repo := newFakeEventRepo()
	now := time.Now()
	hub, f := newPollFanout(t, repo, "b", &now)
	c := newTestClient(t, hub, "u1")

	// The start position cannot be read: no delivery, no crash.
	repo.setErrors(nil, errors.New("db down"), nil)
	f.poll(ctx)
	assert.False(t, f.started)

	repo.setErrors(nil, nil, nil)
	f.poll(ctx)
	require.True(t, f.started)

	repo.add(row("a", models.WSEventTargetAll, "m1"))
	repo.setErrors(errors.New("db down"), nil, nil)
	f.poll(ctx)
	f.poll(ctx)
	assert.Equal(t, int64(0), f.Stats().Delivered)

	repo.setErrors(nil, nil, nil)
	go hub.Run()
	defer hub.Shutdown()
	f.poll(ctx)
	assert.Equal(t, []string{"m1"}, collect(t, c, 1, time.Second), "the row is delivered after the error")
}

func TestFanout_WriteDropsWithoutBlocking(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		cfg         FanoutConfig
		messages    []string
		wantDropped int64
		wantQueued  int
	}{
		{
			name:        "buffer full drops the rest",
			cfg:         FanoutConfig{Origin: "a", BufferSize: 2},
			messages:    []string{"m1", "m2", "m3", "m4", "m5"},
			wantDropped: 3,
			wantQueued:  2,
		},
		{
			name:        "message larger than the cap is not written",
			cfg:         FanoutConfig{Origin: "a", MaxPayloadBytes: 4},
			messages:    []string{"tiny", "too large"},
			wantDropped: 1,
			wantQueued:  1,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			hub := NewHub()
			go hub.Run()
			defer hub.Shutdown()
			// Writer not started: the queue fills up.
			f := NewFanout(hub, newFakeEventRepo(), tt.cfg)
			c := newTestClient(t, hub, "u1", "inst-1")

			done := make(chan struct{})
			go func() {
				defer close(done)
				for _, m := range tt.messages {
					hub.BroadcastToInstance("inst-1", []byte(m))
				}
			}()
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("hub send blocked on a full fan-out buffer")
			}

			assert.Equal(t, tt.messages, drain(c), "local clients get every message")
			assert.Equal(t, tt.wantDropped, f.Stats().Dropped)
			assert.Len(t, f.queue, tt.wantQueued)
		})
	}
}

func TestFanout_WriterBatchesAndFlushesOnStop(t *testing.T) {
	t.Parallel()
	repo := newFakeEventRepo()
	block := make(chan struct{})
	repo.block = block
	hub := NewHub()
	go hub.Run()
	defer hub.Shutdown()
	f := NewFanout(hub, repo, FanoutConfig{Origin: "a", PollInterval: time.Hour})
	f.Start()

	// The first insert blocks; the next messages queue up and go in one batch.
	hub.BroadcastToUser("u1", []byte("m0"))
	require.Eventually(t, func() bool { return len(f.queue) == 0 }, time.Second, time.Millisecond)
	for i := 1; i <= 10; i++ {
		hub.BroadcastToUser("u1", []byte(fmt.Sprintf("m%d", i)))
	}
	close(block)
	f.Stop()

	assert.Equal(t, 11, repo.count())
	assert.Equal(t, int64(11), f.Stats().Written)
	assert.LessOrEqual(t, repo.insertCalls(), 3, "queued rows are written in batches")

	// After Stop the hub writes nothing more.
	hub.Broadcast([]byte("after stop"))
	assert.Equal(t, 11, repo.count())
	assert.Nil(t, hub.fanout.Load(), "Stop detaches the fan-out")
}

func TestFanout_WriteErrorDropsBatch(t *testing.T) {
	t.Parallel()
	repo := newFakeEventRepo()
	repo.setErrors(nil, nil, errors.New("insert failed"))
	hub := NewHub()
	go hub.Run()
	defer hub.Shutdown()
	f := NewFanout(hub, repo, FanoutConfig{Origin: "a", PollInterval: time.Hour})
	f.Start()
	hub.Broadcast([]byte("m1"))
	require.Eventually(t, func() bool { return f.Stats().Dropped == 1 }, time.Second, time.Millisecond)
	f.Stop()
	assert.Equal(t, int64(0), f.Stats().Written)
	assert.Equal(t, 0, repo.count())
}

func TestNewFanout_RequiresHubRepoOrigin(t *testing.T) {
	t.Parallel()
	hub := NewHub()
	assert.Nil(t, NewFanout(nil, newFakeEventRepo(), FanoutConfig{Origin: "a"}))
	assert.Nil(t, NewFanout(hub, nil, FanoutConfig{Origin: "a"}))
	assert.Nil(t, NewFanout(hub, newFakeEventRepo(), FanoutConfig{}))
	assert.Nil(t, hub.fanout.Load(), "no fan-out attached")

	// A nil Fanout is safe to start and stop (fan-out disabled).
	var f *Fanout
	f.Start()
	f.Stop()
}

func TestHub_WithoutFanoutWritesNothing(t *testing.T) {
	t.Parallel()
	hub := NewHub()
	go hub.Run()
	defer hub.Shutdown()
	c := newTestClient(t, hub, "u1", "inst-1")

	hub.Broadcast([]byte("all"))
	hub.BroadcastToInstance("inst-1", []byte("inst"))
	hub.BroadcastToUser("u1", []byte("user"))
	hub.BroadcastToUser("", []byte("nobody"))

	got := collect(t, c, 3, time.Second)
	sort.Strings(got)
	assert.Equal(t, []string{"all", "inst", "user"}, got)
	assert.Nil(t, hub.fanout.Load())
}

func TestHub_BroadcastToUser(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		userID string
		want   map[string]int // user -> messages
	}{
		{name: "only the clients of the user", userID: "u1", want: map[string]int{"u1": 1, "u2": 0}},
		{name: "unknown user reaches nobody", userID: "u9", want: map[string]int{"u1": 0, "u2": 0}},
		{name: "empty user ID reaches nobody", userID: "", want: map[string]int{"u1": 0, "u2": 0}},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			hub := NewHub()
			clients := map[string][]*Client{}
			for _, u := range []string{"u1", "u1", "u2"} {
				clients[u] = append(clients[u], newTestClient(t, hub, u))
			}
			hub.BroadcastToUser(tt.userID, []byte("hello"))
			for u, cs := range clients {
				for _, c := range cs {
					assert.Len(t, drain(c), tt.want[u], "user %s", u)
				}
			}
		})
	}
}

func TestEventCleaner_DeletesOldRows(t *testing.T) {
	t.Parallel()
	repo := newFakeEventRepo()
	now := time.Now().UTC()
	old := row("a", models.WSEventTargetAll, "old")
	old.CreatedAt = now.Add(-6 * time.Minute)
	repo.add(old)
	repo.add(row("a", models.WSEventTargetAll, "fresh"))

	cleaner := NewEventCleaner(repo, 5*time.Minute, time.Hour)
	// Run cleans at once, stops on cancel, and can run again (leader worker).
	for term := 0; term < 2; term++ {
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() { defer close(done); cleaner.Run(ctx) }()
		require.Eventually(t, func() bool { return repo.count() == 1 }, time.Second, time.Millisecond)
		cancel()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("cleaner did not stop")
		}
	}

	defaults := NewEventCleaner(repo, 0, 0)
	assert.Equal(t, DefaultFanoutRetention, defaults.retention)
	assert.Equal(t, DefaultFanoutCleanupInterval, defaults.interval)
}

func TestLogLimiter(t *testing.T) {
	t.Parallel()
	l := logLimiter{every: 10 * time.Second}
	t0 := time.Now()

	ok, n := l.allow(t0)
	assert.True(t, ok)
	assert.Zero(t, n)
	ok, _ = l.allow(t0.Add(time.Second))
	assert.False(t, ok)
	ok, _ = l.allow(t0.Add(2 * time.Second))
	assert.False(t, ok)
	ok, n = l.allow(t0.Add(11 * time.Second))
	assert.True(t, ok)
	assert.Equal(t, 2, n, "suppressed lines are counted")
}

// TestFanout_RevocationReachesOtherReplicas checks that a revoke on replica A
// closes the matching sockets on replica B, and that B does not write a new
// row for it (no loop). A user revoke closes only sockets with a token issued
// at or before the revoke: a session opened after it stays open.
func TestFanout_RevocationReachesOtherReplicas(t *testing.T) {
	t.Parallel()

	tests := []struct {
		revoke   func(h *Hub)
		name     string
		wantOpen []string // B clients that stay open
	}{
		{name: "user", revoke: func(h *Hub) { h.DisconnectUser("u1") }, wantOpen: []string{"u1-new", "u2"}},
		{name: "token", revoke: func(h *Hub) { h.DisconnectToken("jti-2") }, wantOpen: []string{"u1-old", "u1-new"}},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			repo := newSQLiteEventRepo(t)
			hubA, fA := startReplica(t, repo, "replica-a")
			hubB, fB := startReplica(t, repo, "replica-b")

			now := time.Now()
			identities := map[string]ClientIdentity{
				"u1-old": {UserID: "u1", TokenID: "jti-1", IssuedAt: now.Add(-time.Minute)},
				"u1-new": {UserID: "u1", TokenID: "jti-3", IssuedAt: now.Add(time.Hour)}, // session after the revoke
				"u2":     {UserID: "u2", TokenID: "jti-2", IssuedAt: now.Add(-time.Minute)},
			}
			clients := map[string]*Client{}
			for name, id := range identities {
				c := &Client{hub: hubB, send: make(chan []byte, sendBufferSize), identity: id}
				require.NoError(t, hubB.Register(c))
				clients[name] = c
			}

			tt.revoke(hubA)
			require.Eventually(t, func() bool { return hubB.ClientCount() == len(tt.wantOpen) }, 2*time.Second, 5*time.Millisecond)
			time.Sleep(100 * time.Millisecond)

			for name, c := range clients {
				hubB.mu.RLock()
				open := hubB.clients[c]
				hubB.mu.RUnlock()
				assert.Equal(t, contains(tt.wantOpen, name), open, "client %s", name)
			}
			assert.Equal(t, int64(1), fA.Stats().Written)
			assert.Equal(t, int64(0), fB.Stats().Written, "a remote revoke must not write a new row")
			assert.Equal(t, int64(1), fB.Stats().Delivered)
		})
	}
}

func contains(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}

// TestFanout_StartWindow checks that an ID below MAX(id) that is not visible
// at start is delivered when it commits later, and that visible rows are
// not replayed.
func TestFanout_StartWindow(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repo := newFakeEventRepo()
	late := repo.reserve(row("a", models.WSEventTargetAll, "late"))
	repo.add(row("a", "user:u1", "history"))

	now := time.Now()
	hub, f := newPollFanout(t, repo, "b", &now)
	c := newTestClient(t, hub, "u1")
	f.poll(ctx)
	require.True(t, f.started)
	assert.Equal(t, int64(2), f.lastID)
	assert.Contains(t, f.missing, late)

	repo.commit(late)
	go hub.Run()
	defer hub.Shutdown()
	f.poll(ctx)
	assert.Equal(t, []string{"late"}, collect(t, c, 1, time.Second))
	time.Sleep(20 * time.Millisecond)
	assert.Empty(t, drain(c), "the visible row at start is not replayed")
}

func TestFanout_IDRestart(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repo := newFakeEventRepo()
	now := time.Now()
	hub, f := newPollFanout(t, repo, "b", &now)
	c := newTestClient(t, hub, "u1")
	f.poll(ctx)
	for i := 0; i < 5; i++ {
		repo.add(row("a", "user:u1", fmt.Sprintf("m%d", i)))
	}
	f.poll(ctx)
	require.Len(t, drain(c), 5)
	require.Equal(t, int64(5), f.lastID)

	// emptyPolls runs enough empty polls for one ID restart check.
	emptyPolls := func() {
		for i := 0; i < fanoutRestartCheckEvery; i++ {
			f.poll(ctx)
		}
	}

	// Cleanup emptied the table: the IDs continue, nothing changes.
	repo.deleteAll()
	emptyPolls()
	assert.Equal(t, int64(5), f.lastID)

	// The IDs restarted: rows 1 and 2 are below the last ID. The poller
	// reads from 0 again; the stale filter drops the old row.
	repo.restartIDs()
	old := row("a", "user:u1", "old")
	old.CreatedAt = now.Add(-time.Minute)
	repo.add(old)
	repo.add(row("a", "user:u1", "new"))
	emptyPolls() // the check resets the last ID; the next poll reads both rows
	assert.Equal(t, []string{"new"}, drain(c))
	assert.Equal(t, int64(2), f.lastID)
	assert.Equal(t, int64(1), f.Stats().Skipped, "the old row is stale")
	assert.Empty(t, f.missing)
}

func TestFanout_SkipsStaleRows(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repo := newFakeEventRepo()
	now := time.Now()
	hub, f := newPollFanout(t, repo, "b", &now)
	c := newTestClient(t, hub, "u1")
	f.poll(ctx)

	stale := row("a", "user:u1", "stale")
	stale.CreatedAt = now.Add(-31 * time.Second)
	repo.add(stale)
	repo.add(row("a", "user:u1", "fresh"))
	f.poll(ctx)
	assert.Equal(t, []string{"fresh"}, drain(c))
	assert.Equal(t, int64(1), f.Stats().Skipped)
	assert.Equal(t, int64(1), f.Stats().Delivered)
}
