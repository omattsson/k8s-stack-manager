package database

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"backend/internal/telemetry"

	mysqldriver "github.com/go-sql-driver/mysql"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	"go.opentelemetry.io/otel/trace"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// recordingHandler keeps the slog records it gets, with their context.
type recordingHandler struct {
	mu      sync.Mutex
	records []slog.Record
	ctxs    []context.Context
	level   slog.Level
}

func newRecordingLogger(level slog.Level) (*slog.Logger, *recordingHandler) {
	h := &recordingHandler{level: level}
	return slog.New(h), h
}

func (h *recordingHandler) Enabled(_ context.Context, l slog.Level) bool { return l >= h.level }

func (h *recordingHandler) Handle(ctx context.Context, r slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.records = append(h.records, r.Clone())
	h.ctxs = append(h.ctxs, ctx)
	return nil
}

func (h *recordingHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *recordingHandler) WithGroup(string) slog.Handler      { return h }

func (h *recordingHandler) all() []slog.Record {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]slog.Record(nil), h.records...)
}

func recordAttrs(r slog.Record) map[string]slog.Value {
	attrs := map[string]slog.Value{}
	r.Attrs(func(a slog.Attr) bool {
		attrs[a.Key] = a.Value
		return true
	})
	return attrs
}

func attrKeys(r slog.Record) []string {
	var keys []string
	r.Attrs(func(a slog.Attr) bool {
		keys = append(keys, a.Key)
		return true
	})
	return keys
}

func TestSlogGormLogger_Trace(t *testing.T) {
	t.Parallel()

	const query = "SELECT * FROM `stack_instances` WHERE namespace = ? LIMIT 1"
	errSQL := errors.New("Error 1213 (40001): Deadlock found when trying to get lock; try restarting transaction")

	tests := []struct {
		name         string
		debug        bool
		handlerLevel slog.Level
		err          error
		elapsed      time.Duration
		rows         int64
		wantLevel    slog.Level
		wantMsg      string // empty: nothing is logged and fc is not called
		wantKeys     []string
	}{
		{name: "release: record not found is not logged", err: gorm.ErrRecordNotFound},
		{name: "release: wrapped record not found is not logged", err: fmt.Errorf("find: %w", gorm.ErrRecordNotFound)},
		{name: "release: fast query is not logged", rows: 1},
		{name: "release: query under the threshold is not logged", elapsed: gormSlowQueryThreshold / 2},
		{
			name: "release: SQL error at level error", err: errSQL, rows: 0,
			wantLevel: slog.LevelError, wantMsg: "SQL error", wantKeys: []string{"sql", "rows", "duration", "error"},
		},
		{
			name: "release: slow query at level warn", elapsed: time.Second, rows: 3,
			wantLevel: slog.LevelWarn, wantMsg: "Slow SQL query", wantKeys: []string{"sql", "rows", "duration", "threshold"},
		},
		{
			name: "release: slow record not found is a slow query, not an error", err: gorm.ErrRecordNotFound, elapsed: time.Second,
			wantLevel: slog.LevelWarn, wantMsg: "Slow SQL query", wantKeys: []string{"sql", "rows", "duration", "threshold"},
		},
		{
			name: "release: slow SQL error is an error", err: errSQL, elapsed: time.Second,
			wantLevel: slog.LevelError, wantMsg: "SQL error", wantKeys: []string{"sql", "rows", "duration", "error"},
		},
		{
			name: "release: unknown row count has no rows attribute", err: errSQL, rows: -1,
			wantLevel: slog.LevelError, wantMsg: "SQL error", wantKeys: []string{"sql", "duration", "error"},
		},
		{
			name: "debug: statement at level debug", debug: true, handlerLevel: slog.LevelDebug, rows: 2,
			wantLevel: slog.LevelDebug, wantMsg: "SQL query", wantKeys: []string{"sql", "rows", "duration"},
		},
		{
			name: "debug: record not found is a statement, not an error", debug: true, handlerLevel: slog.LevelDebug, err: gorm.ErrRecordNotFound,
			wantLevel: slog.LevelDebug, wantMsg: "SQL query", wantKeys: []string{"sql", "rows", "duration"},
		},
		{
			name: "debug: SQL error at level error", debug: true, handlerLevel: slog.LevelDebug, err: errSQL,
			wantLevel: slog.LevelError, wantMsg: "SQL error", wantKeys: []string{"sql", "rows", "duration", "error"},
		},
		{
			name: "release: cancelled context at level debug", handlerLevel: slog.LevelDebug, err: context.Canceled,
			wantLevel: slog.LevelDebug, wantMsg: "SQL query cancelled", wantKeys: []string{"sql", "rows", "duration", "error"},
		},
		{
			name: "release: wrapped cancelled context at level debug", handlerLevel: slog.LevelDebug, err: fmt.Errorf("query: %w", context.Canceled),
			wantLevel: slog.LevelDebug, wantMsg: "SQL query cancelled", wantKeys: []string{"sql", "rows", "duration", "error"},
		},
		{name: "release with LOG_LEVEL info: cancelled context is not logged", err: context.Canceled},
		{
			name: "release: slow cancelled query keeps the slow line with the error", err: context.Canceled, elapsed: time.Second,
			wantLevel: slog.LevelWarn, wantMsg: "Slow SQL query", wantKeys: []string{"sql", "rows", "duration", "error", "threshold"},
		},
		{
			name: "release: deadline exceeded stays an error", err: context.DeadlineExceeded,
			wantLevel: slog.LevelError, wantMsg: "SQL error", wantKeys: []string{"sql", "rows", "duration", "error"},
		},
		{name: "debug with LOG_LEVEL info: statement is not logged", debug: true, handlerLevel: slog.LevelInfo},
		{name: "release with LOG_LEVEL error: slow query is not logged", handlerLevel: slog.LevelError, elapsed: time.Second},
		{
			name: "release with LOG_LEVEL error: SQL error is logged", handlerLevel: slog.LevelError, err: errSQL,
			wantLevel: slog.LevelError, wantMsg: "SQL error", wantKeys: []string{"sql", "rows", "duration", "error"},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			log, h := newRecordingLogger(tt.handlerLevel)
			l := newSlogGormLogger(log, tt.debug)

			fcCalls := 0
			l.Trace(context.Background(), time.Now().Add(-tt.elapsed), func() (string, int64) {
				fcCalls++
				return query, tt.rows
			}, tt.err)

			records := h.all()
			if tt.wantMsg == "" {
				assert.Empty(t, records)
				assert.Zero(t, fcCalls, "the SQL text is not built for a line that is not logged")
				return
			}
			require.Len(t, records, 1)
			assert.Equal(t, 1, fcCalls)
			r := records[0]
			assert.Equal(t, tt.wantLevel, r.Level)
			assert.Equal(t, tt.wantMsg, r.Message)
			assert.ElementsMatch(t, tt.wantKeys, attrKeys(r))

			attrs := recordAttrs(r)
			assert.Equal(t, query, attrs["sql"].String())
			if v, ok := attrs["rows"]; ok {
				assert.Equal(t, tt.rows, v.Int64())
			}
			assert.GreaterOrEqual(t, attrs["duration"].Duration(), tt.elapsed)
			if v, ok := attrs["error"]; ok {
				assert.Equal(t, tt.err.Error(), v.String())
			}
			if v, ok := attrs["threshold"]; ok {
				assert.Equal(t, gormSlowQueryThreshold, v.Duration())
			}
		})
	}
}

func TestSlogGormLogger_Messages(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		debug        bool
		handlerLevel slog.Level
		want         []string
	}{
		{name: "debug logs info, warn and error", debug: true, want: []string{"info 1", "warn 2", "error 3"}},
		{name: "release logs warn and error", want: []string{"warn 2", "error 3"}},
		{name: "LOG_LEVEL error keeps only errors", debug: true, handlerLevel: slog.LevelError, want: []string{"error 3"}},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			log, h := newRecordingLogger(tt.handlerLevel)
			l := newSlogGormLogger(log, tt.debug)
			ctx := context.Background()

			l.Info(ctx, "info %d", 1)
			l.Warn(ctx, "warn %d", 2)
			l.Error(ctx, "error %d", 3)

			var got []string
			for _, r := range h.all() {
				got = append(got, r.Message)
			}
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestSlogGormLogger_LogMode(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		mode    logger.LogLevel
		err     error
		wantMsg string
	}{
		{name: "silent logs no SQL error", mode: logger.Silent, err: errors.New("boom")},
		{name: "info (db.Debug) logs every statement", mode: logger.Info, wantMsg: "SQL query"},
		{name: "error logs no slow query", mode: logger.Error},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			log, h := newRecordingLogger(slog.LevelDebug)
			base := newSlogGormLogger(log, false)
			l := base.LogMode(tt.mode)

			elapsed := time.Duration(0)
			if tt.mode == logger.Error {
				elapsed = time.Second
			}
			l.Trace(context.Background(), time.Now().Add(-elapsed), func() (string, int64) { return "SELECT 1", 1 }, tt.err)

			records := h.all()
			if tt.wantMsg == "" {
				assert.Empty(t, records)
			} else {
				require.Len(t, records, 1)
				assert.Equal(t, tt.wantMsg, records[0].Message)
			}
			assert.Equal(t, logger.Warn, base.level, "LogMode returns a copy")
		})
	}
}

func TestSlogGormLogger_NilContext(t *testing.T) {
	t.Parallel()
	log, h := newRecordingLogger(slog.LevelDebug)
	l := newSlogGormLogger(log, true)
	var nilCtx context.Context

	assert.NotPanics(t, func() {
		l.Trace(nilCtx, time.Now(), func() (string, int64) { return "SELECT 1", 1 }, nil)
		l.Warn(nilCtx, "warn")
	})
	assert.Len(t, h.all(), 2)
}

func TestNewGormLogger(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name              string
		debug             bool
		wantLevel         logger.LogLevel
		wantParameterized bool
	}{
		{name: "release", debug: false, wantLevel: logger.Warn, wantParameterized: true},
		{name: "debug", debug: true, wantLevel: logger.Info, wantParameterized: false},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			l, ok := newGormLogger(tt.debug).(*slogGormLogger)
			require.True(t, ok)
			assert.Nil(t, l.log, "uses slog.Default() at each call")
			assert.Equal(t, tt.wantLevel, l.level)
			assert.Equal(t, tt.wantParameterized, l.parameterized)
			assert.Equal(t, gormSlowQueryThreshold, l.slowThreshold)
		})
	}
}

// memLogExporter keeps the exported OTel log records in memory.
type memLogExporter struct {
	mu      sync.Mutex
	records []sdklog.Record
}

func (e *memLogExporter) Export(_ context.Context, records []sdklog.Record) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, r := range records {
		e.records = append(e.records, r.Clone())
	}
	return nil
}

func (e *memLogExporter) Shutdown(context.Context) error   { return nil }
func (e *memLogExporter) ForceFlush(context.Context) error { return nil }

func (e *memLogExporter) all() []sdklog.Record {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]sdklog.Record(nil), e.records...)
}

// TestSlogGormLogger_OTelTraceContext sends GORM lines through the handler
// of the backend default logger with OTEL_ENABLED (telemetry.LogHandler) and
// checks that the OTel records carry the trace and span ID of the context.
func TestSlogGormLogger_OTelTraceContext(t *testing.T) {
	t.Parallel()

	traceID, err := trace.TraceIDFromHex("0102030405060708090a0b0c0d0e0f10")
	require.NoError(t, err)
	spanID, err := trace.SpanIDFromHex("0102030405060708")
	require.NoError(t, err)
	spanCtx := trace.ContextWithSpanContext(context.Background(), trace.NewSpanContext(trace.SpanContextConfig{
		TraceID: traceID, SpanID: spanID, TraceFlags: trace.FlagsSampled,
	}))

	tests := []struct {
		name    string
		err     error
		elapsed time.Duration
		wantMsg string
	}{
		{name: "SQL error", err: errors.New("Error 1213: Deadlock found"), wantMsg: "SQL error"},
		{name: "slow query", elapsed: time.Second, wantMsg: "Slow SQL query"},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			exp := &memLogExporter{}
			lp := sdklog.NewLoggerProvider(sdklog.WithProcessor(sdklog.NewSimpleProcessor(exp)))
			t.Cleanup(func() { _ = lp.Shutdown(context.Background()) })
			tel := &telemetry.Telemetry{LoggerProvider: lp}
			var local strings.Builder
			base := slog.NewTextHandler(&local, &slog.HandlerOptions{Level: slog.LevelInfo})
			l := newSlogGormLogger(slog.New(tel.LogHandler(base, slog.LevelInfo)), false)

			l.Trace(spanCtx, time.Now().Add(-tt.elapsed), func() (string, int64) { return "UPDATE t SET a = ?", 1 }, tt.err)

			records := exp.all()
			require.Len(t, records, 1)
			assert.Equal(t, tt.wantMsg, records[0].Body().AsString())
			assert.Equal(t, traceID, records[0].TraceID())
			assert.Equal(t, spanID, records[0].SpanID())
			assert.Contains(t, local.String(), "sql=\"UPDATE t SET a = ?\"", "the local stderr line too")
		})
	}
}

// TestSlogGormLogger_WithDB runs real queries through GORM (SQLite in
// memory): placeholders in release mode, values in debug mode, the context of
// the query and the caller (the code that ran the query, not GORM).
func TestSlogGormLogger_WithDB(t *testing.T) {
	t.Parallel()

	type row struct {
		ID   uint
		Name string
	}
	type ctxKey struct{}

	tests := []struct {
		name     string
		debug    bool
		run      func(db *gorm.DB) error
		wantErr  bool
		wantMsg  string // empty: nothing is logged
		wantSQL  []string
		denySQL  []string
		wantRows bool
	}{
		{
			name: "release: First without a row writes nothing",
			run: func(db *gorm.DB) error {
				var r row
				return db.Where("name = ?", "absent").First(&r).Error
			},
			wantErr: true,
		},
		{
			name: "release: successful query writes nothing",
			run: func(db *gorm.DB) error {
				var rs []row
				return db.Where("name = ?", "x").Find(&rs).Error
			},
		},
		{
			name: "release: SQL error has placeholders, not values",
			run: func(db *gorm.DB) error {
				var r row
				return db.Table("missing_table").Where("name = ?", "secret-value").Find(&r).Error
			},
			wantErr: true,
			wantMsg: "SQL error",
			wantSQL: []string{"missing_table", "name = ?"},
			denySQL: []string{"secret-value"},
		},
		{
			name:  "debug: SQL error has the values",
			debug: true,
			run: func(db *gorm.DB) error {
				var r row
				return db.Table("missing_table").Where("name = ?", "debug-value").Find(&r).Error
			},
			wantErr: true,
			wantMsg: "SQL error",
			wantSQL: []string{"missing_table", "debug-value"},
		},
		{
			name:  "debug: statement with the row count",
			debug: true,
			run: func(db *gorm.DB) error {
				var rs []row
				return db.Where("name = ?", "seeded").Find(&rs).Error
			},
			wantMsg:  "SQL query",
			wantSQL:  []string{"seeded"},
			wantRows: true,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{SkipDefaultTransaction: true, Logger: logger.Discard})
			require.NoError(t, err)
			sqlDB, err := db.DB()
			require.NoError(t, err)
			t.Cleanup(func() { _ = sqlDB.Close() })
			sqlDB.SetMaxOpenConns(1)
			require.NoError(t, db.AutoMigrate(&row{}))
			require.NoError(t, db.Create(&row{Name: "seeded"}).Error)

			log, h := newRecordingLogger(slog.LevelDebug)
			ctx := context.WithValue(context.Background(), ctxKey{}, "request-1")
			db = db.Session(&gorm.Session{Logger: newSlogGormLogger(log, tt.debug)}).WithContext(ctx)

			err = tt.run(db)
			assert.Equal(t, tt.wantErr, err != nil)

			records := h.all()
			if tt.wantMsg == "" {
				assert.Empty(t, records)
				return
			}
			require.Len(t, records, 1)
			r := records[0]
			assert.Equal(t, tt.wantMsg, r.Message)
			sql := recordAttrs(r)["sql"].String()
			for _, s := range tt.wantSQL {
				assert.Contains(t, sql, s)
			}
			for _, s := range tt.denySQL {
				assert.NotContains(t, sql, s)
			}
			if tt.wantRows {
				assert.Equal(t, int64(1), recordAttrs(r)["rows"].Int64())
			}
			h.mu.Lock()
			assert.Equal(t, "request-1", h.ctxs[0].Value(ctxKey{}), "the handler gets the query context")
			h.mu.Unlock()

			frame, _ := runtime.CallersFrames([]uintptr{r.PC}).Next()
			assert.True(t, strings.HasSuffix(frame.File, "gorm_logger_test.go"), "caller is the query code, got %q", frame.File)
		})
	}
}

func TestRedactSQLError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "1062 keeps the number and the key",
			in:   "Error 1062 (23000): Duplicate entry 'alice' for key 'users.idx_users_username'",
			want: "Error 1062 (23000): Duplicate entry '?' for key 'users.idx_users_username'",
		},
		{
			name: "1062 value with a quote",
			in:   "Error 1062 (23000): Duplicate entry 'o'brien' for key 'users.idx_users_username'",
			want: "Error 1062 (23000): Duplicate entry '?' for key 'users.idx_users_username'",
		},
		{
			name: "1062 value that looks like a key",
			in:   "Error 1062 (23000): Duplicate entry 'x' for key 'y' for key 'users.idx_users_username'",
			want: "Error 1062 (23000): Duplicate entry '?' for key 'users.idx_users_username'",
		},
		{
			name: "joined errors: the earlier error loses its values too",
			in:   "Error 1366 (HY000): Incorrect string value: 'v1-secret' for column 'name' at row 1; Error 1062 (23000): Duplicate entry 'v2-secret' for key 'users.idx_users_username'",
			want: "Error 1366 (HY000): Incorrect string value: '?' at row 1; Error 1062 (23000): Duplicate entry '?' for key 'users.idx_users_username'",
		},
		{
			name: "1366 keeps the column",
			in:   "Error 1366 (HY000): Incorrect integer value: 'abc' for column 'pod_limit' at row 1",
			want: "Error 1366 (HY000): Incorrect integer value: '?' for column 'pod_limit' at row 1",
		},
		{
			name: "1292 datetime value",
			in:   "Error 1292 (22007): Incorrect datetime value: '2026-13-45' for column 'expires_at' at row 1",
			want: "Error 1292 (22007): Incorrect datetime value: '?' for column 'expires_at' at row 1",
		},
		{
			name: "1292 truncated value without a column",
			in:   "Error 1292 (22007): Truncated incorrect DOUBLE value: 'secret'",
			want: "Error 1292 (22007): Truncated incorrect DOUBLE value: '?'",
		},
		{
			name: "other quoted text",
			in:   "Error 1146 (42S02): Table 'app.missing' doesn't exist",
			want: "Error 1146 (42S02): Table '?' doesn't exist",
		},
		{
			name: "apostrophe inside a word stays",
			in:   "Error 1005 (HY000): Can't create table 'app.x' (errno: 150)",
			want: "Error 1005 (HY000): Can't create table '?' (errno: 150)",
		},
		{
			name: "value with an apostrophe",
			in:   "Error 1292 (22007): Truncated incorrect DOUBLE value: 'o'brien'",
			want: "Error 1292 (22007): Truncated incorrect DOUBLE value: '?'",
		},
		{name: "one quote", in: "near 'abc", want: "near '?'"},
		{
			name: "no quotes",
			in:   "Error 1213 (40001): Deadlock found when trying to get lock; try restarting transaction",
			want: "Error 1213 (40001): Deadlock found when trying to get lock; try restarting transaction",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, redactSQLError(tt.in))
		})
	}
}

// TestSlogGormLogger_ErrorValues checks the error attribute: no quoted
// value in release mode, the full text in debug mode, and a duplicate key at
// level warn.
func TestSlogGormLogger_ErrorValues(t *testing.T) {
	t.Parallel()

	dup := &mysqldriver.MySQLError{Number: 1062, SQLState: [5]byte{'2', '3', '0', '0', '0'}, Message: "Duplicate entry 'secret-user' for key 'users.idx_users_username'"}
	badValue := &mysqldriver.MySQLError{Number: 1366, SQLState: [5]byte{'H', 'Y', '0', '0', '0'}, Message: "Incorrect integer value: 'secret-value' for column 'pod_limit' at row 1"}

	tests := []struct {
		name      string
		err       error
		debug     bool
		wantLevel slog.Level
		wantMsg   string
		want      []string
		deny      []string
	}{
		{name: "release: duplicate entry", err: dup, wantLevel: slog.LevelWarn, wantMsg: "SQL duplicate key", want: []string{"Error 1062", "users.idx_users_username"}, deny: []string{"secret-user"}},
		{name: "debug: duplicate entry", err: dup, debug: true, wantLevel: slog.LevelWarn, wantMsg: "SQL duplicate key", want: []string{"Error 1062", "secret-user"}},
		{name: "release: incorrect value", err: badValue, wantLevel: slog.LevelError, wantMsg: "SQL error", want: []string{"Error 1366", "pod_limit"}, deny: []string{"secret-value"}},
		{name: "debug: incorrect value", err: badValue, debug: true, wantLevel: slog.LevelError, wantMsg: "SQL error", want: []string{"secret-value"}},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			log, h := newRecordingLogger(slog.LevelInfo)
			l := newSlogGormLogger(log, tt.debug)
			l.Trace(context.Background(), time.Now(), func() (string, int64) { return "INSERT INTO users (username) VALUES (?)", 0 }, tt.err)

			records := h.all()
			require.Len(t, records, 1)
			assert.Equal(t, tt.wantLevel, records[0].Level)
			assert.Equal(t, tt.wantMsg, records[0].Message)
			errText := recordAttrs(records[0])["error"].String()
			for _, s := range tt.want {
				assert.Contains(t, errText, s)
			}
			for _, s := range tt.deny {
				assert.NotContains(t, errText, s)
			}
		})
	}
}

// TestSlogGormLogger_Scan checks db.Scan, which logs through
// logger.Recorder: placeholders in release mode (useParameterizedScanSQL),
// values in debug mode.
func TestSlogGormLogger_Scan(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		debug   bool
		wantSQL string
		denySQL string
	}{
		{name: "release: placeholders", wantSQL: "name = ?", denySQL: "scan-secret"},
		{name: "debug: values", debug: true, wantSQL: "scan-secret"},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			log, h := newRecordingLogger(slog.LevelInfo)
			db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{SkipDefaultTransaction: true, Logger: newSlogGormLogger(log, tt.debug)})
			require.NoError(t, err)
			sqlDB, err := db.DB()
			require.NoError(t, err)
			t.Cleanup(func() { _ = sqlDB.Close() })
			if !tt.debug {
				require.NoError(t, useParameterizedScanSQL(db))
			}

			var out []map[string]any
			require.Error(t, db.Raw("SELECT id FROM missing_table WHERE name = ?", "scan-secret").Scan(&out).Error)

			records := h.all()
			require.Len(t, records, 1)
			sql := recordAttrs(records[0])["sql"].String()
			assert.Contains(t, sql, tt.wantSQL)
			if tt.denySQL != "" {
				assert.NotContains(t, sql, tt.denySQL)
			}
		})
	}
}
