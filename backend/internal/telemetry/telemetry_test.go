package telemetry_test

import (
	"context"
	"testing"
	"time"

	"backend/internal/config"
	"backend/internal/telemetry"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

func TestInit(t *testing.T) {
	t.Parallel()

	t.Run("disabled returns no-op telemetry without error", func(t *testing.T) {
		t.Parallel()
		tel, err := telemetry.Init(config.OtelConfig{Enabled: false})
		require.NoError(t, err)
		require.NotNil(t, tel)

		// Providers should be nil when disabled.
		assert.Nil(t, tel.TracerProvider)
		assert.Nil(t, tel.MeterProvider)
		assert.Nil(t, tel.LoggerProvider)
	})

	t.Run("disabled does not set SDK tracer provider as global", func(t *testing.T) {
		t.Parallel()
		_, err := telemetry.Init(config.OtelConfig{Enabled: false})
		require.NoError(t, err)

		// The global tracer provider should NOT be an SDK provider when disabled.
		// It may be the default no-op or a previously-set provider, but never the
		// *sdktrace.TracerProvider that Init would register when enabled.
		_, isSDK := otel.GetTracerProvider().(*sdktrace.TracerProvider)
		assert.False(t, isSDK, "expected no-op global tracer provider when disabled")
	})

	t.Run("disabled config returns non-nil Telemetry", func(t *testing.T) {
		t.Parallel()
		tel, err := telemetry.Init(config.OtelConfig{
			Enabled:     false,
			Endpoint:    "localhost:4317",
			ServiceName: "test-service",
			SampleRate:  1.0,
		})
		require.NoError(t, err)
		assert.NotNil(t, tel)
	})
}

func TestTelemetry_Shutdown(t *testing.T) {
	t.Parallel()

	t.Run("shutdown on disabled telemetry does not panic", func(t *testing.T) {
		t.Parallel()
		tel := &telemetry.Telemetry{}
		err := tel.Shutdown(context.Background())
		assert.NoError(t, err)
	})

	t.Run("shutdown on nil providers returns no error", func(t *testing.T) {
		t.Parallel()
		tel, err := telemetry.Init(config.OtelConfig{Enabled: false})
		require.NoError(t, err)

		err = tel.Shutdown(context.Background())
		assert.NoError(t, err)
	})
}

func TestInit_MetricsHandler(t *testing.T) {
	// Not parallel: subtests call Init with MetricsEnabled=true, which modifies
	// the global OTel MeterProvider and the default Prometheus registry.
	// Subtests run sequentially so each can safely call Shutdown before the next
	// subtest registers new metrics.

	t.Run("MetricsEnabled false returns nil MetricsHandler", func(t *testing.T) {
		tel, err := telemetry.Init(config.OtelConfig{
			Enabled:        false,
			MetricsEnabled: false,
		})
		require.NoError(t, err)
		assert.Nil(t, tel.MetricsHandler)
		assert.Nil(t, tel.MeterProvider)
	})

	t.Run("MetricsEnabled true sets non-nil MetricsHandler and MeterProvider", func(t *testing.T) {
		tel, err := telemetry.Init(config.OtelConfig{
			Enabled:        false,
			MetricsEnabled: true,
			ServiceName:    "test-service",
			MetricsAddr:    ":0",
		})
		require.NoError(t, err)
		require.NotNil(t, tel)
		assert.NotNil(t, tel.MetricsHandler, "MetricsHandler should be set when MetricsEnabled=true")
		assert.NotNil(t, tel.MeterProvider, "MeterProvider should be set when MetricsEnabled=true")
		// No OTLP exporters configured, so trace and log providers should be nil.
		assert.Nil(t, tel.TracerProvider)
		assert.Nil(t, tel.LoggerProvider)

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		assert.NoError(t, tel.Shutdown(ctx))
	})
}
