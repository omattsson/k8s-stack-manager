package auditlog

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
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

// fakeRepo records the entries it gets and returns err.
type fakeRepo struct {
	mu      sync.Mutex
	entries []models.AuditLog
	err     error
}

func (f *fakeRepo) Create(_ context.Context, e *models.AuditLog) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.entries = append(f.entries, *e)
	return f.err
}

func (f *fakeRepo) List(context.Context, models.AuditLogFilters) (*models.AuditLogResult, error) {
	return &models.AuditLogResult{}, nil
}

// syncBuffer is a bytes.Buffer that is safe for concurrent writes.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func useTestCounter(t *testing.T, w *Writer) *sdkmetric.ManualReader {
	t.Helper()
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	t.Cleanup(func() { _ = mp.Shutdown(context.Background()) })
	counter, err := mp.Meter("auditlog").Int64Counter("audit.write_failed_total")
	require.NoError(t, err)
	w.failed = counter
	return reader
}

func failedSum(t *testing.T, reader *sdkmetric.ManualReader, action string) int64 {
	t.Helper()
	var rm metricdata.ResourceMetrics
	require.NoError(t, reader.Collect(context.Background(), &rm))
	want := attribute.NewSet(attribute.String("action", action))
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != "audit.write_failed_total" {
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

func parseLines(t *testing.T, s string) []map[string]any {
	t.Helper()
	var out []map[string]any
	sc := bufio.NewScanner(strings.NewReader(s))
	for sc.Scan() {
		var m map[string]any
		require.NoError(t, json.Unmarshal(sc.Bytes(), &m), sc.Text())
		out = append(out, m)
	}
	return out
}

func TestWriter_Create(t *testing.T) {
	t.Parallel()
	ts := time.Date(2026, 11, 13, 10, 0, 0, 0, time.UTC)
	tests := []struct {
		name       string
		stdout     bool
		dbErr      error
		entry      models.AuditLog
		wantLines  int
		wantFailed int64
	}{
		{name: "ok no stdout", entry: models.AuditLog{ID: "a1", Action: "deploy", Timestamp: ts}},
		{name: "ok with stdout", stdout: true, entry: models.AuditLog{ID: "a1", Action: "deploy", Timestamp: ts}, wantLines: 1},
		{name: "fills id and timestamp", stdout: true, entry: models.AuditLog{Action: "deploy"}, wantLines: 1},
		{name: "db error", stdout: true, dbErr: errors.New("db down"), entry: models.AuditLog{ID: "a1", Action: "deploy", Timestamp: ts}, wantLines: 1, wantFailed: 1},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			repo := &fakeRepo{err: tt.dbErr}
			var out syncBuffer
			opts := WriterOptions{}
			if tt.stdout {
				opts.Out = &out
			}
			w := NewWriter(repo, opts)
			reader := useTestCounter(t, w)

			e := tt.entry
			err := w.Create(context.Background(), &e)
			if tt.dbErr != nil {
				require.ErrorIs(t, err, tt.dbErr)
			} else {
				require.NoError(t, err)
			}

			require.Len(t, repo.entries, 1)
			got := repo.entries[0]
			assert.NotEmpty(t, got.ID)
			assert.False(t, got.Timestamp.IsZero())
			assert.Equal(t, time.UTC, got.Timestamp.Location())

			lines := parseLines(t, out.String())
			require.Len(t, lines, tt.wantLines)
			if tt.wantLines == 1 {
				assert.True(t, strings.HasSuffix(out.String(), "\n"))
				assert.Equal(t, "audit", lines[0]["log_type"])
				assert.Equal(t, "deploy", lines[0]["action"])
				assert.Equal(t, got.ID, lines[0]["id"])
				assert.Equal(t, got.Timestamp.Format(time.RFC3339Nano), lines[0]["timestamp"])
			}
			assert.Equal(t, tt.wantFailed, failedSum(t, reader, "deploy"))
		})
	}
}

func TestWriter_ConcurrentLines(t *testing.T) {
	t.Parallel()
	var out syncBuffer
	w := NewWriter(&fakeRepo{}, WriterOptions{Out: &out})
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = w.Create(context.Background(), &models.AuditLog{Action: "update", Details: strings.Repeat("x", 4096)})
		}()
	}
	wg.Wait()
	assert.Len(t, parseLines(t, out.String()), 50)
}

func TestWriter_ListPassesThrough(t *testing.T) {
	t.Parallel()
	w := NewWriter(&fakeRepo{}, WriterOptions{})
	res, err := w.List(context.Background(), models.AuditLogFilters{})
	require.NoError(t, err)
	assert.NotNil(t, res)
}
