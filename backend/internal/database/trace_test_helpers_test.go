package database

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"backend/internal/telemetry"

	"github.com/stretchr/testify/require"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	"go.opentelemetry.io/otel/trace"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// newTraceTestDB returns an in-memory SQLite DB with the given tables. Its
// GORM logger is the release-mode backend logger on the OTel log pipeline
// (telemetry.LogHandler, as with OTEL_ENABLED) at level debug. The returned
// function gives the exported OTel log records (#656).
func newTraceTestDB(t *testing.T, tables ...any) (*gorm.DB, func() []sdklog.Record) {
	t.Helper()
	exp := &memLogExporter{}
	lp := sdklog.NewLoggerProvider(sdklog.WithProcessor(sdklog.NewSimpleProcessor(exp)))
	t.Cleanup(func() { _ = lp.Shutdown(context.Background()) })
	tel := &telemetry.Telemetry{LoggerProvider: lp}
	base := slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelDebug})
	gormLog := newSlogGormLogger(slog.New(tel.LogHandler(base, slog.LevelDebug)), false)

	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{SkipDefaultTransaction: true, Logger: logger.Discard})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	sqlDB.SetMaxOpenConns(1)
	if len(tables) > 0 {
		require.NoError(t, db.AutoMigrate(tables...))
	}
	return db.Session(&gorm.Session{Logger: gormLog}), exp.all
}

// newTraceTestSpanContext returns a context with a sampled span and its IDs.
func newTraceTestSpanContext(t *testing.T) (context.Context, trace.TraceID, trace.SpanID) {
	t.Helper()
	traceID, err := trace.TraceIDFromHex("0102030405060708090a0b0c0d0e0f10")
	require.NoError(t, err)
	spanID, err := trace.SpanIDFromHex("0102030405060708")
	require.NoError(t, err)
	ctx := trace.ContextWithSpanContext(context.Background(), trace.NewSpanContext(trace.SpanContextConfig{
		TraceID: traceID, SpanID: spanID, TraceFlags: trace.FlagsSampled,
	}))
	return ctx, traceID, spanID
}
