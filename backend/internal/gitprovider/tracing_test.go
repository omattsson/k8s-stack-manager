package gitprovider

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

func TestGitProviderMetrics_RecordBranchList(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	prev := otel.GetMeterProvider()
	otel.SetMeterProvider(mp)
	t.Cleanup(func() {
		otel.SetMeterProvider(prev)
		_ = mp.Shutdown(context.Background())
	})

	prevMeter := gitProviderMeter
	gitProviderMeter = mp.Meter("gitprovider")
	initGitProviderMetrics()
	t.Cleanup(func() { gitProviderMeter = prevMeter })

	recordBranchListResult("azure_devops", "success", 250*time.Millisecond)

	var rm metricdata.ResourceMetrics
	require.NoError(t, reader.Collect(context.Background(), &rm))

	found := false
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name == "gitprovider.branch_list.total" || m.Name == "gitprovider.branch_list.duration" {
				found = true
			}
		}
	}
	assert.True(t, found)
}
