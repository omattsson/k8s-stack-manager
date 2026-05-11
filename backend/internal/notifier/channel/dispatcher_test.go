package channel

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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

	d := NewDispatcher(repo)
	d.Dispatch(context.Background(), EventPayload{
		EventType:       "deployment.success",
		Timestamp:       time.Now(),
		Title:           "Deploy succeeded",
		Message:         "my-stack deployed",
		UserDisplayName: "Test User",
		EntityType:      "stack_instance",
		EntityID:        "inst-1",
	})

	assert.Equal(t, int32(2), calls.Load())
	assert.Len(t, repo.deliveries, 2)
	assert.Equal(t, "success", repo.deliveries[0].Status)
	assert.Equal(t, "success", repo.deliveries[1].Status)
}

func TestDispatch_NoChannels(t *testing.T) {
	t.Parallel()

	repo := &mockChannelRepo{channels: nil}
	d := NewDispatcher(repo)
	d.Dispatch(context.Background(), EventPayload{EventType: "deployment.success"})
	assert.Empty(t, repo.deliveries)
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

	d := NewDispatcher(repo)
	d.Dispatch(context.Background(), EventPayload{EventType: "deployment.error"})

	require.Len(t, repo.deliveries, 1)
	assert.Equal(t, "failed", repo.deliveries[0].Status)
	assert.Contains(t, repo.deliveries[0].ErrorMessage, "500")
}

func TestDispatch_HMACSignature(t *testing.T) {
	t.Parallel()

	var gotSig string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotSig = r.Header.Get("X-StackManager-Signature")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	repo := &mockChannelRepo{
		channels: []models.NotificationChannel{
			{ID: "ch-1", Name: "signed", WebhookURL: server.URL, Secret: "test-secret", Enabled: true},
		},
	}

	d := NewDispatcher(repo)
	d.Dispatch(context.Background(), EventPayload{EventType: "test.event", Title: "test"})

	require.NotEmpty(t, gotSig)
	assert.True(t, len(gotSig) > len("sha256="), "signature should have sha256= prefix")
	assert.Contains(t, gotSig, "sha256=")
}

func TestDispatch_NoSignatureWithoutSecret(t *testing.T) {
	t.Parallel()

	var gotSig string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotSig = r.Header.Get("X-StackManager-Signature")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	repo := &mockChannelRepo{
		channels: []models.NotificationChannel{
			{ID: "ch-1", Name: "unsigned", WebhookURL: server.URL, Enabled: true},
		},
	}

	d := NewDispatcher(repo)
	d.Dispatch(context.Background(), EventPayload{EventType: "test.event"})

	assert.Empty(t, gotSig)
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
