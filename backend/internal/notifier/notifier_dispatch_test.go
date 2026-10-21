package notifier

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"backend/internal/models"
	"backend/internal/notifier/channel"
	"backend/pkg/dberrors"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// channelRepo is the channel repository of the dispatch tests. Only
// FindChannelsByEvent, GetChannel and CreateDeliveryLog are used.
type channelRepo struct {
	models.NotificationChannelRepository
	channels []models.NotificationChannel
	// lookupStarted gets one value per lookup while it has room. With
	// holdLookup, the lookup waits until its context ends.
	lookupStarted chan struct{}
	holdLookup    bool
	lookups       atomic.Int64
	mu            sync.Mutex
	deliveries    []models.NotificationDeliveryLog
}

func (r *channelRepo) FindChannelsByEvent(ctx context.Context, _ string) ([]models.NotificationChannel, error) {
	r.lookups.Add(1)
	select {
	case r.lookupStarted <- struct{}{}:
	default:
	}
	if r.holdLookup {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return r.channels, nil
}

func (r *channelRepo) GetChannel(_ context.Context, id string) (*models.NotificationChannel, error) {
	for _, ch := range r.channels {
		if ch.ID == id {
			return &ch, nil
		}
	}
	return nil, dberrors.NewDatabaseError("get_channel", dberrors.ErrNotFound)
}

func (r *channelRepo) CreateDeliveryLog(_ context.Context, l *models.NotificationDeliveryLog) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.deliveries = append(r.deliveries, *l)
	return nil
}

func (r *channelRepo) deliveryCount(channelID string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, l := range r.deliveries {
		if l.ChannelID == channelID {
			n++
		}
	}
	return n
}

// TestNotifier_StopEndsAllWorkers checks that Stop cancels the work in
// progress (a channel lookup or a delivery to a channel that does not
// answer) and returns after all workers have ended, and that a healthy
// channel gets its events in under 1 s next to a slow one (#649).
func TestNotifier_StopEndsAllWorkers(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		holdLookup bool
	}{
		{name: "delivery to a channel that does not answer"},
		{name: "channel lookup in progress", holdLookup: true},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			slowStarted := make(chan struct{}, 10)
			var slowCancelled atomic.Int32
			release := make(chan struct{})
			slow := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
				// The server sees a closed connection only after the body is read.
				_, _ = io.Copy(io.Discard, r.Body)
				slowStarted <- struct{}{}
				select {
				case <-r.Context().Done():
					slowCancelled.Add(1)
				case <-release:
				}
			}))
			t.Cleanup(func() { close(release); slow.Close() })
			var healthyHits atomic.Int32
			healthy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				healthyHits.Add(1)
				w.WriteHeader(http.StatusOK)
			}))
			t.Cleanup(healthy.Close)

			repo := &channelRepo{
				channels: []models.NotificationChannel{
					{ID: "slow", Name: "slow", WebhookURL: slow.URL, Enabled: true},
					{ID: "healthy", Name: "healthy", WebhookURL: healthy.URL, Enabled: true},
				},
				lookupStarted: make(chan struct{}, 10),
				holdLookup:    tt.holdLookup,
			}
			n := NewNotifier(newMockNotificationRepo(), nil, nil).WithChannelDispatcher(channel.NewDispatcher(repo))
			n.stopDrain = 20 * time.Millisecond
			t.Cleanup(n.Stop)
			assert.Equal(t, 1000, cap(n.dispatchQueue))

			if tt.holdLookup {
				n.enqueueDispatch(channel.EventPayload{EventType: "deployment.success"})
				waitSignal(t, repo.lookupStarted, "channel lookup")
			} else {
				start := time.Now()
				for i := 0; i < 3; i++ {
					n.enqueueDispatch(channel.EventPayload{EventType: "deployment.success"})
				}
				require.Eventually(t, func() bool { return repo.deliveryCount("healthy") == 3 },
					time.Second, 5*time.Millisecond, "the healthy channel gets its events in under 1 s")
				assert.Less(t, time.Since(start), time.Second)
				assert.Equal(t, int32(3), healthyHits.Load())
				waitSignal(t, slowStarted, "slow request")
			}

			stopped := make(chan struct{})
			go func() {
				n.Stop()
				close(stopped)
			}()
			waitSignal(t, stopped, "Stop (drain 20 ms)")

			select {
			case <-n.workerExited:
			default:
				t.Fatal("the intake worker still runs after Stop")
			}
			if !tt.holdLookup {
				require.Eventually(t, func() bool { return slowCancelled.Load() >= 1 },
					5*time.Second, 5*time.Millisecond, "Stop cancels the slow request")
				assert.Equal(t, 1, repo.deliveryCount("slow"), "the cancelled request has a delivery log")
			}
			assert.NotPanics(t, func() {
				n.enqueueDispatch(channel.EventPayload{EventType: "deployment.success"})
				n.Stop()
			}, "events after Stop are dropped")
		})
	}
}

func waitSignal(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatalf("timeout: %s", what)
	}
}

// TestNotifier_IntakeQueueDropLog checks that a full intake queue drops
// events without blocking, logs one warning per minute with the count
// (channel_id empty), and that Stop counts the queued events as dropped.
func TestNotifier_IntakeQueueDropLog(t *testing.T) {
	// Not parallel: this test changes the global slog default logger.
	var mu sync.Mutex
	var buf bytes.Buffer
	orig := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(writerFunc(func(p []byte) (int, error) {
		mu.Lock()
		defer mu.Unlock()
		return buf.Write(p)
	}), nil)))
	defer slog.SetDefault(orig)
	dropLines := func() []string {
		mu.Lock()
		defer mu.Unlock()
		var out []string
		for _, l := range strings.Split(buf.String(), "\n") {
			if strings.Contains(l, "events dropped") {
				out = append(out, l)
			}
		}
		return out
	}

	repo := &channelRepo{lookupStarted: make(chan struct{}, 1), holdLookup: true}
	n := NewNotifier(newMockNotificationRepo(), nil, nil).WithChannelDispatcher(channel.NewDispatcher(repo))
	n.stopDrain = 20 * time.Millisecond
	defer n.Stop()

	// The intake worker holds the first event in the lookup.
	n.enqueueDispatch(channel.EventPayload{EventType: "deployment.success"})
	waitSignal(t, repo.lookupStarted, "channel lookup")

	start := time.Now()
	for i := 0; i < dispatchQueueSize+5; i++ {
		n.enqueueDispatch(channel.EventPayload{EventType: "deployment.success"})
	}
	assert.Less(t, time.Since(start), time.Second, "a full queue does not block")
	lines := dropLines()
	require.Len(t, lines, 1, "one warning for 5 drops")
	assert.Contains(t, lines[0], `channel_id=""`)
	assert.Contains(t, lines[0], "reason=queue_full")
	assert.Contains(t, lines[0], "count=1")

	n.Stop()
	lines = dropLines()[1:]
	require.Len(t, lines, 3, "%v", lines)
	assert.Contains(t, lines[0], "reason=shutdown", "the cancelled lookup")
	assert.Contains(t, lines[0], "count=1")
	rest := strings.Join(lines[1:], "\n")
	assert.Contains(t, rest, "reason=queue_full count=4")
	assert.Contains(t, rest, "reason=shutdown count=1000", "the queued events")
}

// TestNotifier_StopDrainsQueuedEvents checks that events queued right before
// Stop still reach a healthy channel (the drain of Stop, #649).
func TestNotifier_StopDrainsQueuedEvents(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		events int
	}{
		{name: "one event", events: 1},
		// At most the channel queue (100), so no event is dropped.
		{name: "burst", events: 100},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			healthy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusOK)
			}))
			t.Cleanup(healthy.Close)
			repo := &channelRepo{channels: []models.NotificationChannel{
				{ID: "healthy", Name: "healthy", WebhookURL: healthy.URL, Enabled: true},
			}}
			n := NewNotifier(newMockNotificationRepo(), nil, nil).WithChannelDispatcher(channel.NewDispatcher(repo))
			// Long enough for any runner: Stop returns when the queues are empty.
			n.stopDrain = time.Minute

			for i := 0; i < tt.events; i++ {
				n.enqueueDispatch(channel.EventPayload{EventType: "deployment.success"})
			}
			n.Stop()
			assert.Equal(t, tt.events, repo.deliveryCount("healthy"))
		})
	}
}

// TestNotifier_EnqueueDuringStop checks that no event stays in the intake
// queue when enqueueDispatch runs at the same time as Stop: each event is
// passed to the dispatcher or counted as dropped.
func TestNotifier_EnqueueDuringStop(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		producers int
		events    int
	}{
		{name: "few producers", producers: 2, events: 500},
		{name: "many producers", producers: 16, events: 200},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			repo := &channelRepo{}
			n := NewNotifier(newMockNotificationRepo(), nil, nil).WithChannelDispatcher(channel.NewDispatcher(repo))
			n.stopDrain = time.Minute

			start := make(chan struct{})
			var wg sync.WaitGroup
			for p := 0; p < tt.producers; p++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					<-start
					for i := 0; i < tt.events; i++ {
						n.enqueueDispatch(channel.EventPayload{EventType: "deployment.success"})
					}
				}()
			}
			close(start)
			n.Stop()
			wg.Wait()

			assert.Zero(t, len(n.dispatchQueue), "an event entered the queue after Stop")
			assert.LessOrEqual(t, repo.lookups.Load(), int64(tt.producers*tt.events))
		})
	}
}

type writerFunc func(p []byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }
