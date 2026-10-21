package channel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"backend/internal/models"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// blockingServer is a channel that does not answer until the request is
// cancelled. started gets one value per request.
type blockingServer struct {
	*httptest.Server
	started   chan struct{}
	cancelled atomic.Int32
}

func newBlockingServer(t *testing.T) *blockingServer {
	t.Helper()
	s := &blockingServer{started: make(chan struct{}, 100)}
	release := make(chan struct{})
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The server sees a closed connection only after the body is read.
		_, _ = io.Copy(io.Discard, r.Body)
		s.started <- struct{}{}
		select {
		case <-r.Context().Done():
			s.cancelled.Add(1)
		case <-release:
		}
	}))
	// Cleanups run last in, first out: the dispatcher of the test stops
	// before the server closes.
	t.Cleanup(func() {
		close(release)
		s.Close()
	})
	return s
}

// waitStarted waits until the server has n requests in progress.
func (s *blockingServer) waitStarted(t *testing.T, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		select {
		case <-s.started:
		case <-time.After(5 * time.Second):
			t.Fatalf("request %d did not reach the blocking channel", i+1)
		}
	}
}

// recordingServer answers 200 and records the event header of each request.
type recordingServer struct {
	*httptest.Server
	mu     sync.Mutex
	events []string
	at     []time.Time
}

func newRecordingServer(t *testing.T) *recordingServer {
	t.Helper()
	s := &recordingServer{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.events = append(s.events, r.Header.Get("X-StackManager-Event"))
		s.at = append(s.at, time.Now())
		s.mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *recordingServer) received() ([]string, []time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.events...), append([]time.Time(nil), s.at...)
}

// TestDispatch_SlowChannelDoesNotDelayOthers checks #649: a channel that
// does not answer, or answers 503 (retry after 2 s), does not delay the
// delivery to a healthy channel. Stop then cancels the slow request and
// drops the queued events of the slow channel.
func TestDispatch_SlowChannelDoesNotDelayOthers(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		// slowURL returns the URL of the slow channel and a function that
		// waits until its first request is in progress.
		slowURL     func(t *testing.T) (string, func())
		wantErrPart string
	}{
		{
			name: "channel does not answer",
			slowURL: func(t *testing.T) (string, func()) {
				s := newBlockingServer(t)
				return s.URL, func() { s.waitStarted(t, 1) }
			},
			wantErrPart: "context cancel",
		},
		{
			name: "channel answers 503 and is retried",
			slowURL: func(t *testing.T) (string, func()) {
				var hits atomic.Int32
				s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					hits.Add(1)
					w.WriteHeader(http.StatusServiceUnavailable)
				}))
				t.Cleanup(s.Close)
				return s.URL, func() {
					require.Eventually(t, func() bool { return hits.Load() >= 1 }, 5*time.Second, 5*time.Millisecond)
				}
			},
			wantErrPart: "context cancelled",
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			healthy := newRecordingServer(t)
			slowURL, waitSlow := tt.slowURL(t)

			repo := &mockChannelRepo{channels: []models.NotificationChannel{
				{ID: "slow", Name: "slow", WebhookURL: slowURL, Enabled: true},
				{ID: "healthy", Name: "healthy", WebhookURL: healthy.URL, Secret: "s", Enabled: true},
			}}
			d := newTestDispatcher(t, repo)
			// Stop always comes during the retry wait, also on a slow runner.
			d.retryDelay = time.Minute
			reader := useTestCounter(t, d)

			const events = 5
			start := time.Now()
			for i := 0; i < events; i++ {
				d.Dispatch(context.Background(), EventPayload{EventType: "deployment.success", Title: fmt.Sprint(i)})
			}
			require.Eventually(t, func() bool {
				n := 0
				for _, l := range repo.Deliveries() {
					if l.ChannelID == "healthy" {
						n++
					}
				}
				return n == events
			}, time.Second, 5*time.Millisecond, "the healthy channel gets all events in under 1 s")
			_, at := healthy.received()
			require.Len(t, at, events)
			assert.Less(t, at[events-1].Sub(start), time.Second)
			waitSlow()

			stopStart := time.Now()
			d.Stop()
			assert.Less(t, time.Since(stopStart), requestTimeout, "Stop cancels the slow request")
			assert.Zero(t, d.workers.running.Load(), "no channel worker runs after Stop")
			assert.Zero(t, d.workers.workerCount())

			var slowLogs, healthyLogs []models.NotificationDeliveryLog
			for _, l := range repo.Deliveries() {
				if l.ChannelID == "slow" {
					slowLogs = append(slowLogs, l)
				} else {
					healthyLogs = append(healthyLogs, l)
				}
			}
			assert.Len(t, healthyLogs, events)
			for _, l := range healthyLogs {
				assert.Equal(t, "success", l.Status)
			}
			require.Len(t, slowLogs, 1, "only the cancelled event of the slow channel has a delivery log")
			assert.Equal(t, "failed", slowLogs[0].Status)
			assert.Contains(t, slowLogs[0].ErrorMessage, tt.wantErrPart)
			assert.NotContains(t, slowLogs[0].ErrorMessage, "127.0.0.1", "no URL in the delivery log")
			assert.Equal(t, int64(events-1), droppedSum(t, reader, "deployment.success", "slow", DropReasonShutdown))
		})
	}
}

// TestChannelWorker_Order checks that each channel gets its events in the
// order of Dispatch, also with more than one channel.
func TestChannelWorker_Order(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		channels int
		events   int
	}{
		{name: "one channel", channels: 1, events: 40},
		{name: "three channels", channels: 3, events: 40},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			servers := make([]*recordingServer, tt.channels)
			repo := &mockChannelRepo{}
			for i := range servers {
				servers[i] = newRecordingServer(t)
				repo.channels = append(repo.channels, models.NotificationChannel{
					ID: fmt.Sprintf("ch-%d", i), Name: fmt.Sprintf("ch-%d", i), WebhookURL: servers[i].URL, Enabled: true,
				})
			}
			d := newTestDispatcher(t, repo)

			want := make([]string, tt.events)
			for i := range want {
				want[i] = fmt.Sprintf("test.%03d", i)
				d.Dispatch(context.Background(), EventPayload{EventType: want[i]})
			}
			logs := waitDeliveries(t, repo, tt.events*tt.channels)

			for i, s := range servers {
				got, _ := s.received()
				assert.Equal(t, want, got, "requests of channel %d", i)
				var logOrder []string
				for _, l := range logs {
					if l.ChannelID == fmt.Sprintf("ch-%d", i) {
						logOrder = append(logOrder, l.EventType)
					}
				}
				assert.Equal(t, want, logOrder, "delivery logs of channel %d", i)
			}
		})
	}
}

// TestChannelWorker_IdleExitAndRestart checks that a worker without events
// exits after the idle time, and that the next event starts a new one.
func TestChannelWorker_IdleExitAndRestart(t *testing.T) {
	t.Parallel()

	healthy := newRecordingServer(t)
	repo := &mockChannelRepo{channels: []models.NotificationChannel{
		{ID: "ch-1", Name: "chat", WebhookURL: healthy.URL, Enabled: true},
	}}
	d := newTestDispatcher(t, repo)
	d.workers.idleTimeout = 30 * time.Millisecond

	for round := 1; round <= 2; round++ {
		d.Dispatch(context.Background(), EventPayload{EventType: fmt.Sprintf("test.%d", round)})
		logs := waitDeliveries(t, repo, round)
		assert.Equal(t, "success", logs[round-1].Status)
		require.Eventually(t, func() bool {
			return d.workers.workerCount() == 0 && d.workers.running.Load() == 0
		}, 5*time.Second, 5*time.Millisecond, "round %d: the idle worker exits", round)
	}
	got, _ := healthy.received()
	assert.Equal(t, []string{"test.1", "test.2"}, got)
}

// TestChannelWorker_QueueFull checks that a full channel queue drops the
// event with reason queue_full, logs one warning per minute and does not
// block Dispatch.
func TestChannelWorker_QueueFull(t *testing.T) {
	t.Parallel()

	slow := newBlockingServer(t)
	repo := &mockChannelRepo{channels: []models.NotificationChannel{
		{ID: "slow", Name: "slow", WebhookURL: slow.URL, Enabled: true},
	}}
	d := newTestDispatcher(t, repo)
	d.workers.queueSize = 2
	d.retryDelay = time.Minute
	reader := useTestCounter(t, d)
	logs := useTestLogger(t, &d.drops)

	d.Dispatch(context.Background(), EventPayload{EventType: "deployment.success"})
	slow.waitStarted(t, 1)

	start := time.Now()
	for i := 0; i < 2+5; i++ {
		d.Dispatch(context.Background(), EventPayload{EventType: "deployment.success"})
	}
	assert.Less(t, time.Since(start), time.Second, "Dispatch does not wait for the channel")
	assert.Equal(t, int64(5), droppedSum(t, reader, "deployment.success", "slow", DropReasonQueueFull))
	assertWarnings(t, logs.dropLines(), [][]string{{"channel_id=slow", "reason=queue_full", "count=1"}})

	d.Stop()
	assert.Equal(t, int64(2), droppedSum(t, reader, "deployment.success", "slow", DropReasonShutdown))
	assertWarnings(t, logs.dropLines()[1:], [][]string{
		{"reason=queue_full", "count=4"},
		{"reason=shutdown", "count=1"},
		{"reason=shutdown", "count=1"},
	})
}

// TestDispatcher_StopLeavesNoWorker checks that Stop cancels the requests
// in progress on all channels, waits for all workers, and that an event
// after Stop starts no worker.
func TestDispatcher_StopLeavesNoWorker(t *testing.T) {
	t.Parallel()

	slowA, slowB := newBlockingServer(t), newBlockingServer(t)
	healthy := newRecordingServer(t)
	repo := &mockChannelRepo{channels: []models.NotificationChannel{
		{ID: "a", Name: "a", WebhookURL: slowA.URL, Enabled: true},
		{ID: "b", Name: "b", WebhookURL: slowB.URL, Enabled: true},
		{ID: "h", Name: "h", WebhookURL: healthy.URL, Enabled: true},
	}}
	d := newTestDispatcher(t, repo)
	// A request timeout on a slow runner waits for the retry, not a second request.
	d.retryDelay = time.Minute
	reader := useTestCounter(t, d)

	d.Dispatch(context.Background(), EventPayload{EventType: "deployment.success"})
	slowA.waitStarted(t, 1)
	slowB.waitStarted(t, 1)
	assert.Equal(t, int64(3), d.workers.running.Load())

	done := make(chan struct{})
	go func() {
		d.Stop()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Stop did not return")
	}
	assert.Zero(t, d.workers.running.Load())
	assert.Zero(t, d.workers.workerCount())
	require.Eventually(t, func() bool { return slowA.cancelled.Load() >= 1 && slowB.cancelled.Load() >= 1 },
		5*time.Second, 5*time.Millisecond, "the channels see the cancelled requests")
	assert.Len(t, repo.Deliveries(), 3, "each event in progress has a delivery log")

	d.Dispatch(context.Background(), EventPayload{EventType: "deployment.error"})
	assert.Zero(t, d.workers.running.Load(), "an event after Stop starts no worker")
	for _, id := range []string{"a", "b", "h"} {
		assert.Equal(t, int64(1), droppedSum(t, reader, "deployment.error", id, DropReasonShutdown), id)
	}
	assert.NotPanics(t, d.Stop, "Stop can be called again")
}

// TestDispatch_EncodeOncePerFormat checks that the channels of one format
// get the same body (encoded once) and the signature of their own secret.
func TestDispatch_EncodeOncePerFormat(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex
	bodies := map[string]map[string]any{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		assert.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		mu.Lock()
		bodies[r.URL.Path] = body
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)

	repo := &mockChannelRepo{channels: []models.NotificationChannel{
		{ID: "a", Name: "a", WebhookURL: server.URL + "/a", Secret: "s1", Enabled: true},
		{ID: "b", Name: "b", WebhookURL: server.URL + "/b", Secret: "s2", Enabled: true},
	}}
	d := newTestDispatcher(t, repo)
	d.Dispatch(context.Background(), EventPayload{EventType: "deployment.success", Title: "t"})
	waitDeliveries(t, repo, 2)

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, bodies, 2)
	assert.Equal(t, bodies["/a"], bodies["/b"])
	assert.Equal(t, "t", bodies["/a"]["title"])
}

// gateServer holds the request of event "gate" until release, so the test
// can change the channel while the next event waits in the queue. It records
// all requests.
type gateServer struct {
	*httptest.Server
	started chan struct{}
	release chan struct{}
	mu      sync.Mutex
	reqs    []capturedRequest
}

func newGateServer(t *testing.T) *gateServer {
	t.Helper()
	s := &gateServer{started: make(chan struct{}, 10), release: make(chan struct{})}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		event := r.Header.Get("X-StackManager-Event")
		s.mu.Lock()
		s.reqs = append(s.reqs, capturedRequest{body: b, signature: r.Header.Get("X-StackManager-Signature"), event: event})
		s.mu.Unlock()
		if event == "gate" {
			s.started <- struct{}{}
			<-s.release
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *gateServer) requests() []capturedRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]capturedRequest(nil), s.reqs...)
}

// TestChannelWorker_ReadsChannelAgain checks that a queued event uses the
// stored channel at send time (#649): a deleted or disabled channel gets
// nothing (drop reason channel_removed, no delivery log), and a new URL,
// secret or format applies.
func TestChannelWorker_ReadsChannelAgain(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		// change edits the stored channel; nil deletes it.
		change      func(ch *models.NotificationChannel, otherURL string)
		wantRemoved bool
		// check gets the second request (on the first or the other server).
		check   func(t *testing.T, req capturedRequest)
		onOther bool
	}{
		{name: "deleted", wantRemoved: true},
		{name: "disabled", change: func(ch *models.NotificationChannel, _ string) { ch.Enabled = false }, wantRemoved: true},
		{
			name:    "new URL",
			change:  func(ch *models.NotificationChannel, otherURL string) { ch.WebhookURL = otherURL },
			onOther: true,
			check: func(t *testing.T, req capturedRequest) {
				assert.Equal(t, "deployment.success", req.event)
			},
		},
		{
			name:   "new secret",
			change: func(ch *models.NotificationChannel, _ string) { ch.Secret = "rotated" },
			check: func(t *testing.T, req capturedRequest) {
				assert.Equal(t, sign(req.body, "rotated"), req.signature)
			},
		},
		{
			name:   "new format",
			change: func(ch *models.NotificationChannel, _ string) { ch.Format = models.NotificationChannelFormatTeams },
			check: func(t *testing.T, req capturedRequest) {
				var body map[string]any
				require.NoError(t, json.Unmarshal(req.body, &body))
				assert.Contains(t, body, "attachments", "the body is encoded again as a Teams card")
				assert.Equal(t, sign(req.body, "s"), req.signature, "the signature covers the new body")
			},
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			gate, other := newGateServer(t), newGateServer(t)
			repo := &mockChannelRepo{channels: []models.NotificationChannel{
				{ID: "ch-1", Name: "chat", WebhookURL: gate.URL, Secret: "s", Enabled: true},
			}}
			d := newTestDispatcher(t, repo)
			reader := useTestCounter(t, d)

			d.Dispatch(context.Background(), EventPayload{EventType: "gate"})
			waitSignal(t, gate.started)
			d.Dispatch(context.Background(), EventPayload{EventType: "deployment.success", Title: "queued"})
			if tt.change == nil {
				repo.updateChannel("ch-1", nil)
			} else {
				repo.updateChannel("ch-1", func(ch *models.NotificationChannel) { tt.change(ch, other.URL) })
			}
			close(gate.release)

			if tt.wantRemoved {
				require.Eventually(t, func() bool {
					return droppedSum(t, reader, "deployment.success", "ch-1", DropReasonChannelRemoved) == 1
				}, 5*time.Second, 5*time.Millisecond)
				d.Stop()
				logs := repo.Deliveries()
				require.Len(t, logs, 1, "no delivery log for the removed channel")
				assert.Equal(t, "gate", logs[0].EventType)
				assert.Len(t, gate.requests(), 1)
				assert.Empty(t, other.requests())
				return
			}

			logs := waitDeliveries(t, repo, 2)
			assert.Equal(t, "success", logs[1].Status)
			assert.Empty(t, logs[1].ErrorMessage)
			reqs := gate.requests()[1:]
			if tt.onOther {
				reqs = other.requests()
			}
			require.Len(t, reqs, 1)
			tt.check(t, reqs[0])
			assert.Zero(t, droppedSum(t, reader, "deployment.success", "ch-1", DropReasonChannelRemoved))
		})
	}
}

func waitSignal(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatal("timeout")
	}
}

// TestChannelWorker_LookupErrorUsesQueuedCopy checks that a failed channel
// read sends with the queued copy and warns at most once per minute.
func TestChannelWorker_LookupErrorUsesQueuedCopy(t *testing.T) {
	t.Parallel()

	healthy := newRecordingServer(t)
	repo := &mockChannelRepo{channels: []models.NotificationChannel{
		{ID: "ch-1", Name: "chat", WebhookURL: healthy.URL, Enabled: true},
	}}
	repo.setGetErr(errors.New("db down"))
	d := newTestDispatcher(t, repo)
	buf := useTestLogger(t, &d.lookups)

	for i := 0; i < 3; i++ {
		d.Dispatch(context.Background(), EventPayload{EventType: "deployment.success"})
	}
	for _, l := range waitDeliveries(t, repo, 3) {
		assert.Equal(t, "success", l.Status)
	}
	lines := buf.lines("channel lookup failed")
	require.Len(t, lines, 1)
	assert.Contains(t, lines[0], "channel=chat")
	assert.Contains(t, lines[0], "count=1")
}

// TestDispatcher_ShutdownDrain checks that Shutdown lets the queued events go
// out within the grace time, and cancels a channel that does not answer
// when the grace time ends.
func TestDispatcher_ShutdownDrain(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		slow  bool
		grace time.Duration
	}{
		// A long grace: Shutdown returns when the queues are empty.
		{name: "healthy channel gets all queued events", grace: time.Minute},
		{name: "slow channel is cancelled after the grace time", slow: true, grace: 50 * time.Millisecond},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			healthy := newRecordingServer(t)
			repo := &mockChannelRepo{channels: []models.NotificationChannel{
				{ID: "healthy", Name: "healthy", WebhookURL: healthy.URL, Enabled: true},
			}}
			var slow *blockingServer
			if tt.slow {
				slow = newBlockingServer(t)
				repo.channels = append(repo.channels, models.NotificationChannel{ID: "slow", Name: "slow", WebhookURL: slow.URL, Enabled: true})
			}
			d := newTestDispatcher(t, repo)
			d.retryDelay = time.Minute
			reader := useTestCounter(t, d)

			const events = 20
			for i := 0; i < events; i++ {
				d.Dispatch(context.Background(), EventPayload{EventType: "deployment.success"})
			}
			if tt.slow {
				slow.waitStarted(t, 1)
			}
			start := time.Now()
			d.Shutdown(tt.grace)
			assert.Less(t, time.Since(start), tt.grace+5*time.Second)
			assert.Zero(t, d.workers.running.Load())

			n := 0
			for _, l := range repo.Deliveries() {
				if l.ChannelID == "healthy" {
					n++
				}
			}
			if !tt.slow {
				assert.Equal(t, events, n, "the queued events go out before Shutdown returns")
				assert.Zero(t, droppedSum(t, reader, "deployment.success", "healthy", DropReasonShutdown))
				return
			}
			assert.Equal(t, int64(events-1), droppedSum(t, reader, "deployment.success", "slow", DropReasonShutdown))
			require.Eventually(t, func() bool { return slow.cancelled.Load() >= 1 }, 5*time.Second, 5*time.Millisecond)
		})
	}
}
