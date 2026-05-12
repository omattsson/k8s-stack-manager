// Package telemetry provides OpenTelemetry SDK bootstrap and shutdown.
// When disabled (OTEL_ENABLED=false), all providers are set to no-ops,
// ensuring zero overhead for tracing, metrics, and logging.
package telemetry

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"backend/internal/config"

	otelruntime "go.opentelemetry.io/contrib/instrumentation/runtime"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploggrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/exporters/prometheus"
	"go.opentelemetry.io/otel/propagation"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
	noopmetric "go.opentelemetry.io/otel/metric/noop"

	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Telemetry holds the initialised OTel SDK providers.
// When OTel is disabled, all fields are nil and Shutdown is a no-op.
type Telemetry struct {
	TracerProvider *sdktrace.TracerProvider
	MeterProvider  *sdkmetric.MeterProvider
	LoggerProvider *sdklog.LoggerProvider
	// MetricsHandler is non-nil when METRICS_ENABLED=true.
	// It serves the Prometheus /metrics endpoint on a dedicated port.
	MetricsHandler http.Handler
}

// Init bootstraps OpenTelemetry SDK providers according to cfg.
// When neither cfg.Enabled nor cfg.MetricsEnabled is true it returns
// immediately with no-op providers, guaranteeing zero allocation overhead.
func Init(cfg config.OtelConfig) (*Telemetry, error) {
	tel := &Telemetry{}

	anyActive := cfg.Enabled || cfg.MetricsEnabled
	if !anyActive {
		slog.Info("OpenTelemetry disabled, using no-op providers")
		otel.SetTracerProvider(trace.NewNoopTracerProvider())
		otel.SetMeterProvider(noopmetric.NewMeterProvider())
		return tel, nil
	}

	ctx := context.Background()

	// Build a resource describing this service.
	// Use attribute.String directly to avoid semconv schema version conflicts
	// between resource.Default() (latest) and pinned semconv versions.
	res := resource.NewWithAttributes("",
		attribute.String("service.name", cfg.ServiceName),
	)

	var readers []sdkmetric.Reader
	var tp *sdktrace.TracerProvider
	var lp *sdklog.LoggerProvider

	if cfg.Enabled {
		// --- Trace ---
		traceExporter, err := otlptracegrpc.New(ctx,
			otlptracegrpc.WithEndpoint(cfg.Endpoint),
			otlptracegrpc.WithInsecure(),
		)
		if err != nil {
			return nil, err
		}

		tp = sdktrace.NewTracerProvider(
			sdktrace.WithBatcher(traceExporter),
			sdktrace.WithResource(res),
			sdktrace.WithSampler(sdktrace.TraceIDRatioBased(cfg.SampleRate)),
		)

		// --- OTLP Metrics ---
		metricExporter, err := otlpmetricgrpc.New(ctx,
			otlpmetricgrpc.WithEndpoint(cfg.Endpoint),
			otlpmetricgrpc.WithInsecure(),
		)
		if err != nil {
			return nil, err
		}
		readers = append(readers, sdkmetric.NewPeriodicReader(metricExporter))

		// --- Logs ---
		logExporter, err := otlploggrpc.New(ctx,
			otlploggrpc.WithEndpoint(cfg.Endpoint),
			otlploggrpc.WithInsecure(),
		)
		if err != nil {
			return nil, err
		}

		lp = sdklog.NewLoggerProvider(
			sdklog.WithProcessor(sdklog.NewBatchProcessor(logExporter)),
			sdklog.WithResource(res),
		)

		// Register global trace provider and propagator.
		otel.SetTracerProvider(tp)
		otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
			propagation.TraceContext{},
			propagation.Baggage{},
		))

		slog.Info("OpenTelemetry enabled",
			"endpoint", cfg.Endpoint,
			"service", cfg.ServiceName,
			"sample_rate", cfg.SampleRate,
		)
	}

	// --- Prometheus metrics ---
	if cfg.MetricsEnabled {
		promExporter, err := prometheus.New()
		if err != nil {
			return nil, err
		}
		// The OTel Prometheus exporter registers metrics with the default
		// prometheus registry; promhttp.Handler() serves that registry.
		tel.MetricsHandler = promhttp.Handler()
		readers = append(readers, promExporter)
		slog.Info("Prometheus metrics enabled", "addr", cfg.MetricsAddr)
	}

	// Build meter provider with all active readers.
	mpOpts := []sdkmetric.Option{sdkmetric.WithResource(res)}
	for _, r := range readers {
		mpOpts = append(mpOpts, sdkmetric.WithReader(r))
	}
	mp := sdkmetric.NewMeterProvider(mpOpts...)

	// Register global meter provider and start runtime instrumentation.
	otel.SetMeterProvider(mp)
	if err := otelruntime.Start(otelruntime.WithMinimumReadMemStatsInterval(time.Second)); err != nil {
		return nil, err
	}

	tel.TracerProvider = tp
	tel.MeterProvider = mp
	tel.LoggerProvider = lp

	return tel, nil
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
