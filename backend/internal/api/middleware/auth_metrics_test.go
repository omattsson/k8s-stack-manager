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
	RecordAPIKeyAuth("success")

	var rm metricdata.ResourceMetrics
	require.NoError(t, reader.Collect(context.Background(), &rm))

	found := map[string]bool{}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			found[m.Name] = true
		}
	}
	assert.True(t, found["auth.login.total"], "auth.login.total not recorded")
	assert.True(t, found["auth.token.refresh.total"], "auth.token.refresh.total not recorded")
	assert.True(t, found["auth.apikey.total"], "auth.apikey.total not recorded")
}
