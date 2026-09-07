package middleware

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

func TestAuthMetrics_RecordLoginAndRefresh(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	prev := otel.GetMeterProvider()
	otel.SetMeterProvider(mp)
	t.Cleanup(func() {
		otel.SetMeterProvider(prev)
		_ = mp.Shutdown(context.Background())
	})

	prevMeter := authMeter
	authMeter = mp.Meter("auth")
	initAuthMetrics()
	t.Cleanup(func() { authMeter = prevMeter })

	RecordLogin("local", "success")
	RecordLogin("oidc", "failure")
	RecordRefresh("success")

	var rm metricdata.ResourceMetrics
	require.NoError(t, reader.Collect(context.Background(), &rm))

	count := 0
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name == "auth.login.total" || m.Name == "auth.token.refresh.total" {
				count++
			}
		}
	}
	assert.GreaterOrEqual(t, count, 2)
}
