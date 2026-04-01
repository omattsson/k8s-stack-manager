package telemetry_test

import (
	"context"
	"testing"

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
