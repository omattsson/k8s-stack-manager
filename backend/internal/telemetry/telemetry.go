// Package telemetry provides OpenTelemetry SDK bootstrap and shutdown.
// When disabled (OTEL_ENABLED=false), all providers are set to no-ops,
// ensuring zero overhead for tracing, metrics, and logging.
package telemetry

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"backend/internal/config"

	otelruntime "go.opentelemetry.io/contrib/instrumentation/runtime"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploggrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

// Telemetry holds the initialised OTel SDK providers.
// When OTel is disabled, all fields are nil and Shutdown is a no-op.
type Telemetry struct {
	TracerProvider *sdktrace.TracerProvider
	MeterProvider  *sdkmetric.MeterProvider
	LoggerProvider *sdklog.LoggerProvider
}

// Init bootstraps OpenTelemetry SDK providers according to cfg.
// When cfg.Enabled is false it registers global no-op providers and returns
// immediately, guaranteeing zero allocation overhead in the hot path.
func Init(cfg config.OtelConfig) (*Telemetry, error) {
	if !cfg.Enabled {
		slog.Info("OpenTelemetry disabled, using no-op providers")
		return &Telemetry{}, nil
	}

	ctx := context.Background()

	// Build a resource describing this service.
	// Use attribute.String directly to avoid semconv schema version conflicts
	// between resource.Default() (latest) and pinned semconv versions.
	res := resource.NewWithAttributes("",
		attribute.String("service.name", cfg.ServiceName),
	)

	// --- Trace ---
	traceExporter, err := otlptracegrpc.New(ctx,
		otlptracegrpc.WithEndpoint(cfg.Endpoint),
		otlptracegrpc.WithInsecure(),
	)
	if err != nil {
		return nil, err
	}

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(traceExporter),
		sdktrace.WithResource(res),
		sdktrace.WithSampler(sdktrace.TraceIDRatioBased(cfg.SampleRate)),
	)

	// --- Metrics ---
	metricExporter, err := otlpmetricgrpc.New(ctx,
		otlpmetricgrpc.WithEndpoint(cfg.Endpoint),
		otlpmetricgrpc.WithInsecure(),
	)
	if err != nil {
		return nil, err
	}

	mp := sdkmetric.NewMeterProvider(
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(metricExporter)),
		sdkmetric.WithResource(res),
	)

	// --- Logs ---
	logExporter, err := otlploggrpc.New(ctx,
		otlploggrpc.WithEndpoint(cfg.Endpoint),
		otlploggrpc.WithInsecure(),
	)
	if err != nil {
		return nil, err
	}

	lp := sdklog.NewLoggerProvider(
		sdklog.WithProcessor(sdklog.NewBatchProcessor(logExporter)),
		sdklog.WithResource(res),
	)

	// Register global providers and propagator.
	otel.SetTracerProvider(tp)
	otel.SetMeterProvider(mp)

	if err := otelruntime.Start(otelruntime.WithMinimumReadMemStatsInterval(time.Second)); err != nil {
		return nil, err
	}

	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))

	slog.Info("OpenTelemetry enabled",
		"endpoint", cfg.Endpoint,
		"service", cfg.ServiceName,
		"sample_rate", cfg.SampleRate,
	)

	return &Telemetry{
		TracerProvider: tp,
		MeterProvider:  mp,
		LoggerProvider: lp,
	}, nil
}

// Shutdown flushes and shuts down all SDK providers.
// Safe to call on a disabled (zero-value) Telemetry instance.
func (t *Telemetry) Shutdown(ctx context.Context) error {
	var errs []error

	if t.TracerProvider != nil {
		if err := t.TracerProvider.Shutdown(ctx); err != nil {
			errs = append(errs, err)
		}
	}

	if t.MeterProvider != nil {
		if err := t.MeterProvider.Shutdown(ctx); err != nil {
			errs = append(errs, err)
		}
	}

	if t.LoggerProvider != nil {
		if err := t.LoggerProvider.Shutdown(ctx); err != nil {
			errs = append(errs, err)
		}
	}

	return errors.Join(errs...)
}
