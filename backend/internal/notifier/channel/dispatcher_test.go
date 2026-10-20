package channel

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"backend/internal/models"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type mockChannelRepo struct {
	channels    []models.NotificationChannel
	deliveries  []models.NotificationDeliveryLog
	findErr     error
	deliveryErr error
	mu          sync.Mutex
}

// Deliveries returns a copy of the delivery logs. The channel workers write
// them in other goroutines.
func (m *mockChannelRepo) Deliveries() []models.NotificationDeliveryLog {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]models.NotificationDeliveryLog(nil), m.deliveries...)
}

// newTestDispatcher returns a dispatcher that the test stops at its end.
func newTestDispatcher(t *testing.T, repo models.NotificationChannelRepository) *Dispatcher {
	t.Helper()
	d := NewDispatcher(repo)
	t.Cleanup(d.Stop)
	return d
}

// waitDeliveries waits until repo has n delivery logs and returns them.
func waitDeliveries(t *testing.T, repo *mockChannelRepo, n int) []models.NotificationDeliveryLog {
	t.Helper()
	require.Eventually(t, func() bool { return len(repo.Deliveries()) >= n },
		10*time.Second, 5*time.Millisecond, "want %d delivery logs", n)
	got := repo.Deliveries()
	require.Len(t, got, n)
	return got
}

func (m *mockChannelRepo) CreateChannel(_ context.Context, _ *models.NotificationChannel) error {
	return nil
}
func (m *mockChannelRepo) GetChannel(_ context.Context, _ string) (*models.NotificationChannel, error) {
	return nil, nil
}
func (m *mockChannelRepo) UpdateChannel(_ context.Context, _ *models.NotificationChannel, _ bool) error {
	return nil
}
func (m *mockChannelRepo) DeleteChannel(_ context.Context, _ string) error { return nil }
func (m *mockChannelRepo) ListChannels(_ context.Context) ([]models.NotificationChannel, error) {
	return m.channels, nil
}
func (m *mockChannelRepo) ListEnabledChannels(_ context.Context) ([]models.NotificationChannel, error) {
	return m.channels, nil
}
func (m *mockChannelRepo) SetSubscriptions(_ context.Context, _ string, _ []string) error {
	return nil
}
func (m *mockChannelRepo) GetSubscriptions(_ context.Context, _ string) ([]models.NotificationChannelSubscription, error) {
	return nil, nil
}
func (m *mockChannelRepo) CountSubscriptionsByChannel(_ context.Context) (map[string]int, error) {
	return nil, nil
}
func (m *mockChannelRepo) FindChannelsByEvent(_ context.Context, _ string) ([]models.NotificationChannel, error) {
	if m.findErr != nil {
		return nil, m.findErr
	}
	return m.channels, nil
}
func (m *mockChannelRepo) CreateDeliveryLog(_ context.Context, log *models.NotificationDeliveryLog) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.deliveries = append(m.deliveries, *log)
	return m.deliveryErr
}
func (m *mockChannelRepo) ListDeliveryLogs(_ context.Context, _ string, _, _ int) ([]models.NotificationDeliveryLog, int64, error) {
	return nil, 0, nil
}

func TestDispatch_SendsToSubscribedChannels(t *testing.T) {
	t.Parallel()

	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		assert.Equal(t, "application/json", r.Header.Get("Content-Type"))
		assert.Equal(t, "deployment.success", r.Header.Get("X-StackManager-Event"))

		var payload EventPayload
		require.NoError(t, json.NewDecoder(r.Body).Decode(&payload))
		assert.Equal(t, "deployment.success", payload.EventType)
		assert.Equal(t, "Test User", payload.UserDisplayName)

		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	repo := &mockChannelRepo{
		channels: []models.NotificationChannel{
			{ID: "ch-1", Name: "dev-channel", WebhookURL: server.URL, Enabled: true},
			{ID: "ch-2", Name: "ops-channel", WebhookURL: server.URL, Enabled: true},
		},
	}

	d := newTestDispatcher(t, repo)
	d.Dispatch(context.Background(), EventPayload{
		EventType:       "deployment.success",
		Timestamp:       time.Now(),
		Title:           "Deploy succeeded",
		Message:         "my-stack deployed",
		UserDisplayName: "Test User",
		EntityType:      "stack_instance",
		EntityID:        "inst-1",
	})

	deliveries := waitDeliveries(t, repo, 2)
	assert.Equal(t, int32(2), calls.Load())
	assert.Equal(t, "success", deliveries[0].Status)
	assert.Equal(t, "success", deliveries[1].Status)
	assert.ElementsMatch(t, []string{"ch-1", "ch-2"}, []string{deliveries[0].ChannelID, deliveries[1].ChannelID})
}

func TestDispatch_NoChannels(t *testing.T) {
	t.Parallel()

	repo := &mockChannelRepo{channels: nil}
	d := newTestDispatcher(t, repo)
	d.Dispatch(context.Background(), EventPayload{EventType: "deployment.success"})
	assert.Zero(t, d.workers.workerCount(), "no job was queued")
	assert.Empty(t, repo.Deliveries())
}

// TestDispatch_LookupError checks that a failed channel lookup sends
// nothing, and that a cancelled lookup counts as a shutdown drop.
func TestDispatch_LookupError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		cancelled bool
		wantDrops int64
	}{
		{name: "database error"},
		{name: "cancelled lookup", cancelled: true, wantDrops: 1},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			repo := &mockChannelRepo{findErr: errors.New("db down")}
			d := newTestDispatcher(t, repo)
			reader := useTestCounter(t, d)
			ctx, cancel := context.WithCancel(context.Background())
			if tt.cancelled {
				cancel()
			}
			defer cancel()

			d.Dispatch(ctx, EventPayload{EventType: "deployment.success"})
			assert.Zero(t, d.workers.workerCount())
			assert.Empty(t, repo.Deliveries())
			assert.Equal(t, tt.wantDrops, droppedSum(t, reader, "deployment.success", "", DropReasonShutdown))
		})
	}
}

func TestDispatch_WebhookFailure(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	repo := &mockChannelRepo{
		channels: []models.NotificationChannel{
			{ID: "ch-1", Name: "bad-channel", WebhookURL: server.URL, Enabled: true},
		},
	}

	d := newTestDispatcher(t, repo)
	d.Dispatch(context.Background(), EventPayload{EventType: "deployment.error"})

	deliveries := waitDeliveries(t, repo, 1)
	assert.Equal(t, "failed", deliveries[0].Status)
	assert.Contains(t, deliveries[0].ErrorMessage, "500")
}

func TestDispatch_HMACSignature(t *testing.T) {
	t.Parallel()

	var gotSig atomic.Value
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotSig.Store(r.Header.Get("X-StackManager-Signature"))
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	repo := &mockChannelRepo{
		channels: []models.NotificationChannel{
			{ID: "ch-1", Name: "signed", WebhookURL: server.URL, Secret: "test-secret", Enabled: true},
		},
	}

	d := newTestDispatcher(t, repo)
	d.Dispatch(context.Background(), EventPayload{EventType: "test.event", Title: "test"})
	waitDeliveries(t, repo, 1)

	sig, _ := gotSig.Load().(string)
	require.NotEmpty(t, sig)
	assert.True(t, len(sig) > len("sha256="), "signature should have sha256= prefix")
	assert.Contains(t, sig, "sha256=")
}

func TestDispatch_NoSignatureWithoutSecret(t *testing.T) {
	t.Parallel()

	var gotSig atomic.Value
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotSig.Store(r.Header.Get("X-StackManager-Signature"))
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	repo := &mockChannelRepo{
		channels: []models.NotificationChannel{
			{ID: "ch-1", Name: "unsigned", WebhookURL: server.URL, Enabled: true},
		},
	}

	d := newTestDispatcher(t, repo)
	d.Dispatch(context.Background(), EventPayload{EventType: "test.event"})
	waitDeliveries(t, repo, 1)

	sig, ok := gotSig.Load().(string)
	require.True(t, ok, "the channel got the request")
	assert.Empty(t, sig)
}

func TestDispatchTo_SingleChannel(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	repo := &mockChannelRepo{}
	d := NewDispatcher(repo)

	status, code, errMsg := d.DispatchTo(context.Background(),
		models.NotificationChannel{ID: "ch-1", Name: "test", WebhookURL: server.URL},
		EventPayload{EventType: "test.event", Title: "Test"},
	)

	assert.Equal(t, "success", status)
	assert.Equal(t, 200, code)
	assert.Empty(t, errMsg)
}

func TestDispatch_ChannelFilters(t *testing.T) {
	t.Parallel()

	teamA := models.NotificationTarget{InstanceID: "i-a", InstanceName: "team-a-1", OwnerID: "u1", DefinitionID: "d1", ClusterID: "c1"}
	teamB := models.NotificationTarget{InstanceID: "i-b", InstanceName: "team-b-1", OwnerID: "u2", DefinitionID: "d2", ClusterID: "c2"}

	tests := []struct {
		name      string
		filters   models.NotificationChannelFilters
		instances []models.NotificationTarget
		wantSent  bool
	}{
		{name: "no filters get every event", instances: []models.NotificationTarget{teamB}, wantSent: true},
		{name: "no filters get events without instance", wantSent: true},
		{name: "name filter match", filters: models.NotificationChannelFilters{InstanceNamePatterns: []string{"team-a-*"}},
			instances: []models.NotificationTarget{teamA}, wantSent: true},
		{name: "name filter skip", filters: models.NotificationChannelFilters{InstanceNamePatterns: []string{"team-a-*"}},
			instances: []models.NotificationTarget{teamB}, wantSent: false},
		{name: "owner and cluster filter match", filters: models.NotificationChannelFilters{OwnerIDs: []string{"u1"}, ClusterIDs: []string{"c1"}},
			instances: []models.NotificationTarget{teamA}, wantSent: true},
		{name: "definition filter skip", filters: models.NotificationChannelFilters{DefinitionIDs: []string{"d1"}},
			instances: []models.NotificationTarget{teamB}, wantSent: false},
		{name: "filters skip events without instance", filters: models.NotificationChannelFilters{ClusterIDs: []string{"c1"}},
			wantSent: false},
		{name: "policy run with one matching instance", filters: models.NotificationChannelFilters{OwnerIDs: []string{"u1"}},
			instances: []models.NotificationTarget{teamB, teamA}, wantSent: true},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				var body map[string]any
				assert.NoError(t, json.NewDecoder(r.Body).Decode(&body))
				assert.NotContains(t, body, "instances", "the filter data is not sent")
				w.WriteHeader(http.StatusOK)
			}))
			defer server.Close()

			repo := &mockChannelRepo{channels: []models.NotificationChannel{
				{ID: "ch-1", Name: "filtered", WebhookURL: server.URL, Enabled: true, Filters: tt.filters},
			}}
			d := newTestDispatcher(t, repo)
			d.Dispatch(context.Background(), EventPayload{
				EventType: "deployment.success", Timestamp: time.Now(), Instances: tt.instances,
			})

			if tt.wantSent {
				waitDeliveries(t, repo, 1)
				assert.Equal(t, int32(1), calls.Load())
			} else {
				assert.Zero(t, d.workers.workerCount(), "no job was queued")
				assert.Equal(t, int32(0), calls.Load())
				assert.Empty(t, repo.Deliveries(), "a skipped channel gets no delivery log")
			}
			assert.Len(t, repo.channels, 1, "the dispatcher does not change the repository result")
		})
	}
}
