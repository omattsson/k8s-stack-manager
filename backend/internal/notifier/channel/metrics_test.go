package channel

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

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

// dropLines returns the drop warnings in the log output.
func (b *syncBuffer) dropLines() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []string
	for _, line := range strings.Split(b.buf.String(), "\n") {
		if strings.Contains(line, "events dropped") {
			out = append(out, line)
		}
	}
	return out
}

// useTestLogger sends the drop warnings of d to the returned buffer.
func useTestLogger(t *testing.T, l *dropLog) *syncBuffer {
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
	d.drops.counter = counter
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

// TestDropLog_RateLimit checks that each key (event type, channel, reason)
// gets at most one warning per minute, with the drops since the last one.
func TestDropLog_RateLimit(t *testing.T) {
	t.Parallel()

	type drop struct {
		event, channelID, reason string
		times                    int
	}
	type step struct {
		advance time.Duration
		drops   []drop
		// want holds the new warning lines of the step, each as substrings.
		want [][]string
	}
	a := drop{event: "deployment.success", channelID: "ch-1", reason: DropReasonQueueFull}
	b := drop{event: "deployment.success", channelID: "ch-2", reason: DropReasonQueueFull}
	intake := drop{event: "deployment.error", channelID: "", reason: DropReasonQueueFull}
	times := func(d drop, n int) drop { d.times = n; return d }

	tests := []struct {
		name      string
		steps     []step
		wantFlush [][]string
	}{
		{
			name: "burst on one channel",
			steps: []step{
				{drops: []drop{times(a, 500)}, want: [][]string{{"channel_id=ch-1", "count=1"}}},
				{advance: 30 * time.Second, drops: []drop{times(a, 300)}},
				{advance: 30 * time.Second, drops: []drop{times(a, 1)}, want: [][]string{{"channel_id=ch-1", "count=800"}}},
				{advance: 59 * time.Second, drops: []drop{times(a, 9)}},
			},
			wantFlush: [][]string{{"channel_id=ch-1", "count=9"}},
		},
		{
			name: "keys are independent",
			steps: []step{
				{drops: []drop{times(a, 3), times(b, 2), times(intake, 4)}, want: [][]string{
					{"channel_id=ch-1", "count=1"},
					{"channel_id=ch-2", "count=1"},
					{`channel_id=""`, "event=deployment.error", "reason=queue_full", "count=1"},
				}},
			},
			wantFlush: [][]string{{"channel_id=ch-1", "count=2"}, {"channel_id=ch-2", "count=1"}, {`channel_id=""`, "count=3"}},
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
						d.RecordDrop(dr.event, dr.channelID, dr.reason)
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

// TestDropLog_ZeroDispatcher checks that RecordDrop works on a Dispatcher
// that NewDispatcher did not build (notifier tests use one).
func TestDropLog_ZeroDispatcher(t *testing.T) {
	t.Parallel()
	d := &Dispatcher{}
	buf := useTestLogger(t, &d.drops)
	assert.NotPanics(t, func() {
		d.RecordDrop("deployment.success", "", DropReasonShutdown)
		d.Stop()
	})
	require.Len(t, buf.dropLines(), 1)
	assert.Contains(t, buf.dropLines()[0], "reason=shutdown")
}
