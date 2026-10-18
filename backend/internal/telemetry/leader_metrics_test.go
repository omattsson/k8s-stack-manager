package telemetry

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

func TestStartLeaderMetric(t *testing.T) {
	// Not parallel: it replaces the global meter provider.
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	prev := otel.GetMeterProvider()
	otel.SetMeterProvider(mp)
	t.Cleanup(func() {
		otel.SetMeterProvider(prev)
		_ = mp.Shutdown(context.Background())
	})

	var leading atomic.Bool
	require.NoError(t, StartLeaderMetric(leading.Load))

	collect := func() int64 {
		var rm metricdata.ResourceMetrics
		require.NoError(t, reader.Collect(context.Background(), &rm))
		for _, sm := range rm.ScopeMetrics {
			for _, m := range sm.Metrics {
				if m.Name != LeaderMetricName {
					continue
				}
				g, ok := m.Data.(metricdata.Gauge[int64])
				require.True(t, ok)
				require.Len(t, g.DataPoints, 1)
				return g.DataPoints[0].Value
			}
		}
		t.Fatalf("metric %s not found", LeaderMetricName)
		return -1
	}

	tests := []struct {
		name    string
		leading bool
		want    int64
	}{
		{name: "follower", leading: false, want: 0},
		{name: "leader", leading: true, want: 1},
		{name: "lost leadership", leading: false, want: 0},
	}
	for _, tt := range tests {
		leading.Store(tt.leading)
		assert.Equal(t, tt.want, collect(), tt.name)
	}
}
