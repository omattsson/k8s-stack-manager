package gitprovider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

// branchListCounts returns the branch_list.total counter values keyed by
// {provider, status}, so tests can assert the label sets emitted by
// Registry.ListBranches rather than only that some instrument exists.
func branchListCounts(t *testing.T, reader sdkmetric.Reader) map[[2]string]int64 {
	t.Helper()
	var rm metricdata.ResourceMetrics
	require.NoError(t, reader.Collect(context.Background(), &rm))

	out := map[[2]string]int64{}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != "gitprovider.branch_list.total" {
				continue
			}
			sum, ok := m.Data.(metricdata.Sum[int64])
			require.True(t, ok, "branch_list.total should be an int64 sum")
			for _, dp := range sum.DataPoints {
				provider, _ := dp.Attributes.Value(attribute.Key("provider"))
				status, _ := dp.Attributes.Value(attribute.Key("status"))
				out[[2]string{provider.AsString(), status.AsString()}] += dp.Value
			}
		}
	}
	return out
}

func branchListDurationCount(t *testing.T, reader sdkmetric.Reader) int {
	t.Helper()
	var rm metricdata.ResourceMetrics
	require.NoError(t, reader.Collect(context.Background(), &rm))

	total := 0
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != "gitprovider.branch_list.duration" {
				continue
			}
			hist, ok := m.Data.(metricdata.Histogram[float64])
			require.True(t, ok, "branch_list.duration should be a float64 histogram")
			total += len(hist.DataPoints)
		}
	}
	return total
}

func TestRegistryListBranches_MetricLabels(t *testing.T) {
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

	ctx := context.Background()

	okServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(azureRefsResponse{Value: []azureRef{{Name: "refs/heads/main", ObjectID: "a"}}})
	}))
	defer okServer.Close()
	failServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer failServer.Close()

	r := &Registry{cache: make(map[string]cacheEntry), nowFunc: time.Now}

	// 1. Detection failure — unsupported host, no provider configured.
	_, err := r.ListBranches(ctx, "https://example.com/foo/bar")
	require.Error(t, err)

	// 2. Provider success.
	r.azureDevOps = newTestAzureProvider(t, okServer)
	branches, err := r.ListBranches(ctx, "https://dev.azure.com/org/proj/_git/repo")
	require.NoError(t, err)
	require.Len(t, branches, 1)

	// 3. Cache hit — same URL within TTL returns the cached result.
	_, err = r.ListBranches(ctx, "https://dev.azure.com/org/proj/_git/repo")
	require.NoError(t, err)

	// 4. Provider failure — different repo (cache miss); server returns 500.
	r.azureDevOps = newTestAzureProvider(t, failServer)
	_, err = r.ListBranches(ctx, "https://dev.azure.com/org/proj/_git/other")
	require.Error(t, err)

	counts := branchListCounts(t, reader)
	assert.Equal(t, int64(1), counts[[2]string{"unknown", "failure"}], "detection failure -> provider=unknown,status=failure")
	assert.Equal(t, int64(2), counts[[2]string{"azure_devops", "success"}], "provider success + cache hit")
	assert.Equal(t, int64(1), counts[[2]string{"azure_devops", "failure"}], "provider failure")
	assert.GreaterOrEqual(t, branchListDurationCount(t, reader), 1, "branch_list.duration must have datapoints")
}
