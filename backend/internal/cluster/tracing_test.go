package cluster

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

func TestClusterMetrics_RecordHealthTransitionAndRefresh(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	prev := otel.GetMeterProvider()
	otel.SetMeterProvider(mp)
	t.Cleanup(func() {
		otel.SetMeterProvider(prev)
		_ = mp.Shutdown(context.Background())
	})

	prevClusterMeter := clusterMeter
	clusterMeter = mp.Meter("cluster")
	initClusterMetrics()
	t.Cleanup(func() { clusterMeter = prevClusterMeter })

	recordClusterHealthCheck("prod", "healthy", 120*time.Millisecond)
	recordClusterTransition("prod", "unreachable", "healthy")
	recordSecretRefreshResult("success", 500*time.Millisecond)

	var rm metricdata.ResourceMetrics
	require.NoError(t, reader.Collect(context.Background(), &rm))

	found := map[string]bool{}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			found[m.Name] = true
		}
	}
	assert.True(t, found["cluster.health.transitions.total"], "transitions counter missing")
	assert.True(t, found["cluster.secret.refresh.total"], "secret refresh counter missing")
	assert.True(t, found["cluster.health.check.duration"], "health check duration histogram missing")
	assert.True(t, found["cluster.secret.refresh.duration"], "secret refresh duration histogram missing")
}
