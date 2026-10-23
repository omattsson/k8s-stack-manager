package telemetry

import (
	"context"
	"testing"

	"backend/internal/models"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

// The fakes embed the repository interface so they satisfy it without
// implementing every method; the business-metrics callback only calls the
// overridden count/list methods.

type fakeInstanceRepo struct {
	models.StackInstanceRepository
	countAllCtx   context.Context // context of the last CountAll call
	byStatusesCtx context.Context // context of the last CountByStatuses call
	byStatus      map[string]int
	total         int
}

func (f *fakeInstanceRepo) CountAll(ctx context.Context) (int, error) {
	f.countAllCtx = ctx
	return f.total, nil
}

func (f *fakeInstanceRepo) CountByStatuses(ctx context.Context, statuses []string) (int, error) {
	f.byStatusesCtx = ctx
	sum := 0
	for _, s := range statuses {
		sum += f.byStatus[s]
	}
	return sum, nil
}

type fakeUserRepo struct {
	models.UserRepository
	countCtx context.Context // context of the last Count call
	count    int64
}

func (f *fakeUserRepo) Count(ctx context.Context) (int64, error) {
	f.countCtx = ctx
	return f.count, nil
}

type fakeTemplateRepo struct {
	models.StackTemplateRepository
	count int64
}

func (f *fakeTemplateRepo) Count() (int64, error) { return f.count, nil }

type fakeClusterRepo struct {
	models.ClusterRepository
	countAllCtx context.Context // context of the last CountAll call
	byHealthCtx context.Context // context of the last CountByHealthStatus call
	total       int
	healthy     int
}

func (f *fakeClusterRepo) CountAll(ctx context.Context) (int, error) {
	f.countAllCtx = ctx
	return f.total, nil
}

func (f *fakeClusterRepo) NamesByIDs(_ context.Context, _ []string) (map[string]string, error) {
	return map[string]string{}, nil
}

func (f *fakeClusterRepo) CountByHealthStatus(ctx context.Context, status string) (int, error) {
	f.byHealthCtx = ctx
	if status == models.ClusterHealthy {
		return f.healthy, nil
	}
	return f.total - f.healthy, nil
}

func TestStartBusinessMetrics_ObservesGauges(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	prev := otel.GetMeterProvider()
	otel.SetMeterProvider(mp)
	t.Cleanup(func() {
		otel.SetMeterProvider(prev)
		_ = mp.Shutdown(context.Background())
	})

	instanceRepo := &fakeInstanceRepo{
		total: 10,
		byStatus: map[string]int{
			models.StackStatusRunning:     3,
			models.StackStatusDeploying:   1,
			models.StackStatusStabilizing: 1,
			models.StackStatusPartial:     2,
			// "stopped" is not active and must be excluded from the gauge.
			models.StackStatusStopped: 3,
		},
	}
	userRepo := &fakeUserRepo{count: 5}
	templateRepo := &fakeTemplateRepo{count: 4}
	clusterRepo := &fakeClusterRepo{total: 3, healthy: 2}

	require.NoError(t, StartBusinessMetrics(instanceRepo, userRepo, templateRepo, clusterRepo))

	var rm metricdata.ResourceMetrics
	require.NoError(t, reader.Collect(context.Background(), &rm))

	got := map[string]int64{}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			g, ok := m.Data.(metricdata.Gauge[int64])
			if !ok {
				continue
			}
			require.NotEmpty(t, g.DataPoints, "gauge %s has no data points", m.Name)
			got[m.Name] = g.DataPoints[0].Value
		}
	}

	// active = running + deploying + stabilizing + partial (stopped excluded).
	assert.Equal(t, int64(7), got["business.instances.active"])
	assert.Equal(t, int64(10), got["business.instances.total"])
	assert.Equal(t, int64(5), got["business.users.total"])
	assert.Equal(t, int64(4), got["business.templates.total"])
	assert.Equal(t, int64(3), got["business.clusters.total"])
	assert.Equal(t, int64(2), got["business.clusters.healthy"])
}

type collectCtxKey struct{}

// TestStartBusinessMetrics_UserCountUsesCallbackContext checks that the user,
// instance and cluster counts run with the context of the metrics collection
// (#656).
func TestStartBusinessMetrics_UserCountUsesCallbackContext(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	prev := otel.GetMeterProvider()
	otel.SetMeterProvider(mp)
	t.Cleanup(func() {
		otel.SetMeterProvider(prev)
		_ = mp.Shutdown(context.Background())
	})

	userRepo := &fakeUserRepo{count: 1}
	instanceRepo := &fakeInstanceRepo{}
	clusterRepo := &fakeClusterRepo{}
	require.NoError(t, StartBusinessMetrics(instanceRepo, userRepo, &fakeTemplateRepo{}, clusterRepo))

	var rm metricdata.ResourceMetrics
	require.NoError(t, reader.Collect(context.WithValue(context.Background(), collectCtxKey{}, "scrape"), &rm))

	require.NotNil(t, userRepo.countCtx)
	assert.Equal(t, "scrape", userRepo.countCtx.Value(collectCtxKey{}))
	require.NotNil(t, instanceRepo.countAllCtx)
	assert.Equal(t, "scrape", instanceRepo.countAllCtx.Value(collectCtxKey{}))
	require.NotNil(t, instanceRepo.byStatusesCtx)
	assert.Equal(t, "scrape", instanceRepo.byStatusesCtx.Value(collectCtxKey{}))
	require.NotNil(t, clusterRepo.countAllCtx)
	assert.Equal(t, "scrape", clusterRepo.countAllCtx.Value(collectCtxKey{}))
	require.NotNil(t, clusterRepo.byHealthCtx)
	assert.Equal(t, "scrape", clusterRepo.byHealthCtx.Value(collectCtxKey{}))
}
