package middleware

import (
	"context"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

var authMeter = otel.Meter("auth")

var authMetrics struct {
	loginTotal   metric.Int64Counter
	refreshTotal metric.Int64Counter
	apiKeyTotal  metric.Int64Counter
}

func init() {
	initAuthMetrics()
}

func initAuthMetrics() {
	var err error

	authMetrics.loginTotal, err = authMeter.Int64Counter(
		"auth.login.total",
		metric.WithDescription("Total login attempts by method and outcome."),
		metric.WithUnit("{login}"),
	)
	if err != nil {
		otel.Handle(err)
	}

	authMetrics.refreshTotal, err = authMeter.Int64Counter(
		"auth.token.refresh.total",
		metric.WithDescription("Total refresh-token attempts by outcome."),
		metric.WithUnit("{refresh}"),
	)
	if err != nil {
		otel.Handle(err)
	}

	authMetrics.apiKeyTotal, err = authMeter.Int64Counter(
		"auth.apikey.total",
		metric.WithDescription("Total API-key authentication attempts by outcome."),
		metric.WithUnit("{attempt}"),
	)
	if err != nil {
		otel.Handle(err)
	}
}

// RebindAuthMeter rebinds the auth metric instruments to the current global
// MeterProvider. The instruments are created at package initialization against
// whatever provider is installed then, so a provider installed later (notably
// a test manual reader in another package) only takes effect after this call.
func RebindAuthMeter() {
	authMeter = otel.Meter("auth")
	initAuthMetrics()
}

func RecordLogin(method, status string) {
	if method == "" || status == "" {
		return
	}
	authMetrics.loginTotal.Add(context.Background(), 1,
		metric.WithAttributes(
			attribute.String("method", method),
			attribute.String("status", status),
		),
	)
}

func RecordRefresh(status string) {
	if status == "" {
		return
	}
	authMetrics.refreshTotal.Add(context.Background(), 1,
		metric.WithAttributes(
			attribute.String("status", status),
		),
	)
}

func RecordAPIKeyAuth(status string) {
	if status == "" {
		return
	}
	authMetrics.apiKeyTotal.Add(context.Background(), 1,
		metric.WithAttributes(
			attribute.String("status", status),
		),
	)
}
