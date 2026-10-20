package channel

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"backend/internal/models"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

// syncBuffer is a bytes.Buffer for log output from more than one goroutine.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

// lines returns the log lines that contain part.
func (b *syncBuffer) lines(part string) []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []string
	for _, line := range strings.Split(b.buf.String(), "\n") {
		if strings.Contains(line, part) {
			out = append(out, line)
		}
	}
	return out
}

// dropLines returns the drop warnings in the log output.
func (b *syncBuffer) dropLines() []string { return b.lines("events dropped") }

// useTestLogger sends the warnings of l to the returned buffer.
func useTestLogger(t *testing.T, l *limitedLog) *syncBuffer {
	t.Helper()
	buf := &syncBuffer{}
	l.logger = slog.New(slog.NewTextHandler(buf, nil))
	return buf
}

// useTestCounter gives d a drop counter of its own meter provider, so
// parallel tests do not share the global meter.
func useTestCounter(t *testing.T, d *Dispatcher) *sdkmetric.ManualReader {
	t.Helper()
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	t.Cleanup(func() { _ = mp.Shutdown(context.Background()) })
	counter, err := mp.Meter("notifier").Int64Counter("notification.channel.dropped")
	require.NoError(t, err)
	d.dropCounter = counter
	return reader
}

// droppedSum returns the counter value of one label set.
func droppedSum(t *testing.T, reader *sdkmetric.ManualReader, eventType, channelID, reason string) int64 {
	t.Helper()
	var rm metricdata.ResourceMetrics
	require.NoError(t, reader.Collect(context.Background(), &rm))
	want := attribute.NewSet(
		attribute.String("event_type", eventType),
		attribute.String("channel_id", channelID),
		attribute.String("reason", reason),
	)
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != "notification.channel.dropped" {
				continue
			}
			sum, ok := m.Data.(metricdata.Sum[int64])
			require.True(t, ok)
			for _, dp := range sum.DataPoints {
				if dp.Attributes.Equals(&want) {
					return dp.Value
				}
			}
		}
	}
	return 0
}

// fakeClock is a clock that the test moves.
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// TestDropLog_RateLimit checks that each channel and reason gets at most one
// warning per minute, with the drops since the last one, and that the sweep
// of the other keys runs at most once per minute.
func TestDropLog_RateLimit(t *testing.T) {
	t.Parallel()

	type drop struct {
		event, channelID, name, reason string
		times                          int
	}
	type step struct {
		advance time.Duration
		drops   []drop
		// want holds the new warning lines of the step, each as substrings.
		want [][]string
	}
	a := drop{event: "deployment.success", channelID: "ch-1", name: "chat", reason: DropReasonQueueFull}
	a2 := drop{event: "deployment.error", channelID: "ch-1", name: "chat", reason: DropReasonQueueFull}
	aStop := drop{event: "deployment.success", channelID: "ch-1", name: "chat", reason: DropReasonShutdown}
	b := drop{event: "deployment.success", channelID: "ch-2", name: "ops", reason: DropReasonQueueFull}
	c := drop{event: "deployment.success", channelID: "ch-3", name: "dev", reason: DropReasonQueueFull}
	intake := drop{event: "deployment.error", reason: DropReasonQueueFull}
	times := func(d drop, n int) drop { d.times = n; return d }

	tests := []struct {
		name      string
		steps     []step
		wantFlush [][]string
	}{
		{
			name: "burst on one channel with more event types",
			steps: []step{
				{drops: []drop{times(a, 500), times(a2, 100)}, want: [][]string{{"channel=chat", "channel_id=ch-1", "count=1"}}},
				{advance: 30 * time.Second, drops: []drop{times(a, 300)}},
				{advance: 30 * time.Second, drops: []drop{times(a2, 1)}, want: [][]string{{"channel_id=ch-1", "count=900"}}},
				{advance: 59 * time.Second, drops: []drop{times(a, 9)}},
			},
			wantFlush: [][]string{{"channel_id=ch-1", "count=9"}},
		},
		{
			name: "keys are channel and reason",
			steps: []step{
				{drops: []drop{times(a, 3), times(a2, 2), times(b, 2), times(intake, 4), times(aStop, 1)}, want: [][]string{
					{"channel_id=ch-1", "reason=queue_full", "count=1"},
					{"channel=ops", "channel_id=ch-2", "count=1"},
					{`channel=""`, `channel_id=""`, "reason=queue_full", "count=1"},
					{"channel_id=ch-1", "reason=shutdown", "count=1"},
				}},
			},
			wantFlush: [][]string{
				{"channel_id=ch-1", "reason=queue_full", "count=4"},
				{"channel_id=ch-2", "count=1"},
				{`channel_id=""`, "count=3"},
			},
		},
		{
			name: "pending count comes with the next drop of another key",
			steps: []step{
				{drops: []drop{times(a, 4)}, want: [][]string{{"channel_id=ch-1", "count=1"}}},
				{advance: 2 * time.Minute, drops: []drop{times(b, 1)}, want: [][]string{
					{"channel_id=ch-1", "count=3"},
					{"channel_id=ch-2", "count=1"},
				}},
			},
		},
		{
			name: "quiet key warns again at once",
			steps: []step{
				{drops: []drop{times(a, 1)}, want: [][]string{{"channel_id=ch-1", "count=1"}}},
				{advance: 5 * time.Minute, drops: []drop{times(a, 1)}, want: [][]string{{"channel_id=ch-1", "count=1"}}},
			},
		},
		{
			name: "sweep runs at most once per minute",
			steps: []step{
				{drops: []drop{times(a, 1)}, want: [][]string{{"channel_id=ch-1", "count=1"}}},
				{advance: 10 * time.Second, drops: []drop{times(b, 2)}, want: [][]string{{"channel_id=ch-2", "count=1"}}},
				// The sweep at 65 s finds ch-2 inside its window.
				{advance: 55 * time.Second, drops: []drop{times(a, 1)}, want: [][]string{{"channel_id=ch-1", "count=1"}}},
				// ch-2 is past its window, but the next sweep is at 125 s.
				{advance: 10 * time.Second, drops: []drop{times(c, 1)}, want: [][]string{{"channel_id=ch-3", "count=1"}}},
				{advance: 50 * time.Second, drops: []drop{times(c, 1)}, want: [][]string{{"channel_id=ch-2", "count=1"}}},
			},
			wantFlush: [][]string{{"channel_id=ch-3", "count=1"}},
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			clock := &fakeClock{now: time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)}
			d := &Dispatcher{}
			d.drops.now = clock.Now
			buf := useTestLogger(t, &d.drops)
			reader := useTestCounter(t, d)

			seen := 0
			total := map[drop]int64{}
			for i, s := range tt.steps {
				clock.Advance(s.advance)
				for _, dr := range s.drops {
					for n := 0; n < dr.times; n++ {
						d.recordDrop(dr.event, dr.channelID, dr.name, dr.reason)
					}
					key := dr
					key.times = 0
					total[key] += int64(dr.times)
				}
				lines := buf.dropLines()
				assertWarnings(t, lines[seen:], s.want, "step %d", i)
				seen = len(lines)
			}

			d.drops.flush()
			lines := buf.dropLines()
			assertWarnings(t, lines[seen:], tt.wantFlush, "flush")

			for key, n := range total {
				assert.Equal(t, n, droppedSum(t, reader, key.event, key.channelID, key.reason), "metric of %+v", key)
			}
		})
	}
}

// assertWarnings checks that each want entry matches exactly one line, in
// any order.
func assertWarnings(t *testing.T, lines []string, want [][]string, msgAndArgs ...any) {
	t.Helper()
	require.Len(t, lines, len(want), msgAndArgs...)
	used := make([]bool, len(lines))
	for _, parts := range want {
		found := false
		for i, line := range lines {
			if used[i] || !containsAll(line, parts) {
				continue
			}
			used[i], found = true, true
			break
		}
		assert.True(t, found, "no warning with %v in %v", parts, lines)
	}
}

func containsAll(s string, parts []string) bool {
	for _, p := range parts {
		if !strings.Contains(s, p) {
			return false
		}
	}
	return true
}

// TestDropLog_ZeroDispatcher checks that RecordIntakeDrop works on a
// Dispatcher that NewDispatcher did not build (notifier tests use one).
func TestDropLog_ZeroDispatcher(t *testing.T) {
	t.Parallel()
	d := &Dispatcher{}
	buf := useTestLogger(t, &d.drops)
	assert.NotPanics(t, func() {
		d.RecordIntakeDrop("deployment.success", DropReasonShutdown)
		d.Stop()
	})
	require.Len(t, buf.dropLines(), 1)
	assert.Contains(t, buf.dropLines()[0], "reason=shutdown")
}

// TestDeliveryFailed_RateLimit checks that a failing channel writes one
// delivery log per event but at most one "delivery failed" warning per
// minute, with the count, and no URL.
func TestDeliveryFailed_RateLimit(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		events int
		// wantNow and wantFlush are the counts of the warnings before and at Stop.
		wantNow   []string
		wantFlush []string
	}{
		{name: "one failure", events: 1, wantNow: []string{"count=1"}},
		{name: "many failures", events: 6, wantNow: []string{"count=1"}, wantFlush: []string{"count=5"}},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusInternalServerError)
			}))
			t.Cleanup(server.Close)
			repo := &mockChannelRepo{channels: []models.NotificationChannel{
				{ID: "ch-1", Name: "chat", WebhookURL: server.URL + "/hook?token=s3cret", Enabled: true},
			}}
			d := newTestDispatcher(t, repo)
			buf := useTestLogger(t, &d.failures)

			for i := 0; i < tt.events; i++ {
				d.Dispatch(context.Background(), EventPayload{EventType: "deployment.error"})
			}
			for _, l := range waitDeliveries(t, repo, tt.events) {
				assert.Equal(t, "failed", l.Status)
			}
			lines := buf.lines("delivery failed")
			require.Len(t, lines, len(tt.wantNow))
			for i, part := range tt.wantNow {
				assert.Contains(t, lines[i], part)
				assert.Contains(t, lines[i], "channel=chat")
				assert.Contains(t, lines[i], "status_code=500")
			}

			d.Stop()
			lines = buf.lines("delivery failed")[len(tt.wantNow):]
			require.Len(t, lines, len(tt.wantFlush))
			for i, part := range tt.wantFlush {
				assert.Contains(t, lines[i], part)
			}
			for _, line := range buf.lines("delivery failed") {
				assert.NotContains(t, line, "s3cret")
				assert.NotContains(t, line, "127.0.0.1")
			}
		})
	}
}
