package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"backend/internal/models"
	"backend/internal/notifier"
	"backend/pkg/dberrors"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---- MockNotificationChannelRepository ----

type MockNotificationChannelRepository struct {
	mu            sync.RWMutex
	channels      map[string]*models.NotificationChannel
	subscriptions map[string][]models.NotificationChannelSubscription
	deliveryLogs  map[string][]models.NotificationDeliveryLog
	err           error
}

func NewMockNotificationChannelRepository() *MockNotificationChannelRepository {
	return &MockNotificationChannelRepository{
		channels:      make(map[string]*models.NotificationChannel),
		subscriptions: make(map[string][]models.NotificationChannelSubscription),
		deliveryLogs:  make(map[string][]models.NotificationDeliveryLog),
	}
}

func (m *MockNotificationChannelRepository) SetError(err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.err = err
}

func (m *MockNotificationChannelRepository) CreateChannel(_ context.Context, ch *models.NotificationChannel) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return m.err
	}
	// Check for duplicate name.
	for _, existing := range m.channels {
		if existing.Name == ch.Name {
			return dberrors.NewDatabaseError("create_channel", dberrors.ErrDuplicateKey)
		}
	}
	m.channels[ch.ID] = ch
	return nil
}

func (m *MockNotificationChannelRepository) GetChannel(_ context.Context, id string) (*models.NotificationChannel, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.err != nil {
		return nil, m.err
	}
	ch, ok := m.channels[id]
	if !ok {
		return nil, dberrors.NewDatabaseError("get_channel", dberrors.ErrNotFound)
	}
	return ch, nil
}

func (m *MockNotificationChannelRepository) UpdateChannel(_ context.Context, ch *models.NotificationChannel, _ bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return m.err
	}
	if _, ok := m.channels[ch.ID]; !ok {
		return dberrors.NewDatabaseError("update_channel", dberrors.ErrNotFound)
	}
	m.channels[ch.ID] = ch
	return nil
}

func (m *MockNotificationChannelRepository) DeleteChannel(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return m.err
	}
	if _, ok := m.channels[id]; !ok {
		return dberrors.NewDatabaseError("delete_channel", dberrors.ErrNotFound)
	}
	delete(m.channels, id)
	delete(m.subscriptions, id)
	delete(m.deliveryLogs, id)
	return nil
}

func (m *MockNotificationChannelRepository) ListChannels(_ context.Context) ([]models.NotificationChannel, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.err != nil {
		return nil, m.err
	}
	result := make([]models.NotificationChannel, 0, len(m.channels))
	for _, ch := range m.channels {
		result = append(result, *ch)
	}
	return result, nil
}

func (m *MockNotificationChannelRepository) ListEnabledChannels(_ context.Context) ([]models.NotificationChannel, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.err != nil {
		return nil, m.err
	}
	var result []models.NotificationChannel
	for _, ch := range m.channels {
		if ch.Enabled {
			result = append(result, *ch)
		}
	}
	return result, nil
}

func (m *MockNotificationChannelRepository) SetSubscriptions(_ context.Context, channelID string, eventTypes []string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return m.err
	}
	subs := make([]models.NotificationChannelSubscription, len(eventTypes))
	for i, et := range eventTypes {
		subs[i] = models.NotificationChannelSubscription{
			ID:        uuid.New().String(),
			ChannelID: channelID,
			EventType: et,
		}
	}
	m.subscriptions[channelID] = subs
	return nil
}

func (m *MockNotificationChannelRepository) GetSubscriptions(_ context.Context, channelID string) ([]models.NotificationChannelSubscription, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.err != nil {
		return nil, m.err
	}
	return m.subscriptions[channelID], nil
}

func (m *MockNotificationChannelRepository) CountSubscriptionsByChannel(_ context.Context) (map[string]int, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.err != nil {
		return nil, m.err
	}
	counts := make(map[string]int)
	for chID, subs := range m.subscriptions {
		counts[chID] = len(subs)
	}
	return counts, nil
}

func (m *MockNotificationChannelRepository) FindChannelsByEvent(_ context.Context, eventType string) ([]models.NotificationChannel, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.err != nil {
		return nil, m.err
	}
	var result []models.NotificationChannel
	for chID, subs := range m.subscriptions {
		for _, s := range subs {
			if s.EventType == eventType {
				if ch, ok := m.channels[chID]; ok {
					result = append(result, *ch)
				}
				break
			}
		}
	}
	return result, nil
}

func (m *MockNotificationChannelRepository) CreateDeliveryLog(_ context.Context, log *models.NotificationDeliveryLog) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return m.err
	}
	m.deliveryLogs[log.ChannelID] = append(m.deliveryLogs[log.ChannelID], *log)
	return nil
}

func (m *MockNotificationChannelRepository) ListDeliveryLogs(_ context.Context, channelID string, limit, offset int) ([]models.NotificationDeliveryLog, int64, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.err != nil {
		return nil, 0, m.err
	}
	logs := m.deliveryLogs[channelID]
	total := int64(len(logs))
	if offset > len(logs) {
		offset = len(logs)
	}
	logs = logs[offset:]
	if limit > 0 && limit < len(logs) {
		logs = logs[:limit]
	}
	return logs, total, nil
}

// seedChannel is a test helper that inserts a channel directly into the mock.
func seedChannel(t *testing.T, repo *MockNotificationChannelRepository, id, name, webhookURL string, enabled bool) {
	t.Helper()
	now := time.Now().UTC()
	repo.mu.Lock()
	defer repo.mu.Unlock()
	repo.channels[id] = &models.NotificationChannel{
		ID:         id,
		Name:       name,
		WebhookURL: webhookURL,
		Enabled:    enabled,
		CreatedAt:  now,
		UpdatedAt:  now,
	}
}

// seedDeliveryLog is a test helper that inserts a delivery log directly into the mock.
func seedDeliveryLog(t *testing.T, repo *MockNotificationChannelRepository, channelID, eventType, status string, statusCode int) {
	t.Helper()
	repo.mu.Lock()
	defer repo.mu.Unlock()
	repo.deliveryLogs[channelID] = append(repo.deliveryLogs[channelID], models.NotificationDeliveryLog{
		ID:         uuid.New().String(),
		ChannelID:  channelID,
		EventType:  eventType,
		Status:     status,
		StatusCode: statusCode,
		CreatedAt:  time.Now().UTC(),
	})
}

// ---- Test setup ----

func setupNotificationChannelRouter() (*gin.Engine, *MockNotificationChannelRepository) {
	gin.SetMode(gin.TestMode)
	repo := NewMockNotificationChannelRepository()
	handler := NewNotificationChannelHandler(repo)

	r := gin.New()
	g := r.Group("/api/v1/admin/notification-channels")
	{
		g.GET("", handler.ListChannels)
		g.POST("", handler.CreateChannel)
		g.GET("/event-types", handler.ListEventTypes)
		g.GET("/:id", handler.GetChannel)
		g.PUT("/:id", handler.UpdateChannel)
		g.DELETE("/:id", handler.DeleteChannel)
		g.GET("/:id/subscriptions", handler.GetSubscriptions)
		g.PUT("/:id/subscriptions", handler.UpdateSubscriptions)
		g.POST("/:id/test", handler.TestChannel)
		g.GET("/:id/delivery-logs", handler.ListDeliveryLogs)
	}
	return r, repo
}

// ---- Tests ----

func TestNotificationChannelHandler_ListChannels(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		seed          func(*testing.T, *MockNotificationChannelRepository)
		expectedCode  int
		expectedCount int
	}{
		{
			name:          "empty list",
			seed:          func(_ *testing.T, _ *MockNotificationChannelRepository) {},
			expectedCode:  http.StatusOK,
			expectedCount: 0,
		},
		{
			name: "returns all channels with subscription counts",
			seed: func(t *testing.T, repo *MockNotificationChannelRepository) {
				seedChannel(t, repo, "ch1", "slack-prod", "https://hooks.slack.com/1", true)
				seedChannel(t, repo, "ch2", "teams-dev", "https://outlook.office.com/webhook/2", false)
				// Add subscriptions to ch1.
				require.NoError(t, repo.SetSubscriptions(context.Background(), "ch1", []string{"deployment.success", "deployment.error"}))
			},
			expectedCode:  http.StatusOK,
			expectedCount: 2,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			router, repo := setupNotificationChannelRouter()
			tt.seed(t, repo)

			w := httptest.NewRecorder()
			req, _ := http.NewRequest("GET", "/api/v1/admin/notification-channels", nil)
			router.ServeHTTP(w, req)

			assert.Equal(t, tt.expectedCode, w.Code)

			var result []map[string]interface{}
			err := json.Unmarshal(w.Body.Bytes(), &result)
			require.NoError(t, err)
			assert.Len(t, result, tt.expectedCount)

			// When channels exist, verify subscription_count is present.
			if tt.expectedCount > 0 {
				for _, ch := range result {
					_, hasSubCount := ch["subscription_count"]
					assert.True(t, hasSubCount, "response should include subscription_count")
				}
			}
		})
	}
}

func TestNotificationChannelHandler_ListChannels_RepoError(t *testing.T) {
	t.Parallel()
	router, repo := setupNotificationChannelRouter()
	repo.SetError(fmt.Errorf("db failure"))

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/api/v1/admin/notification-channels", nil)
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusInternalServerError, w.Code)
	var body map[string]string
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	assert.Equal(t, "Internal server error", body["error"])
}

func TestNotificationChannelHandler_CreateChannel(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		body         string
		seed         func(*testing.T, *MockNotificationChannelRepository)
		expectedCode int
	}{
		{
			name:         "valid request returns 201",
			body:         `{"name":"slack-prod","webhook_url":"https://hooks.slack.com/services/T00/B00/xxx"}`,
			seed:         func(_ *testing.T, _ *MockNotificationChannelRepository) {},
			expectedCode: http.StatusCreated,
		},
		{
			name:         "valid request with all fields",
			body:         `{"name":"teams-ops","webhook_url":"https://outlook.office.com/webhook/abc","secret":"s3cret","enabled":false}`,
			seed:         func(_ *testing.T, _ *MockNotificationChannelRepository) {},
			expectedCode: http.StatusCreated,
		},
		{
			name:         "missing name returns 400",
			body:         `{"webhook_url":"https://hooks.slack.com/services/T00/B00/xxx"}`,
			seed:         func(_ *testing.T, _ *MockNotificationChannelRepository) {},
			expectedCode: http.StatusBadRequest,
		},
		{
			name:         "missing webhook_url returns 400",
			body:         `{"name":"slack-prod"}`,
			seed:         func(_ *testing.T, _ *MockNotificationChannelRepository) {},
			expectedCode: http.StatusBadRequest,
		},
		{
			name:         "http webhook_url returns 400",
			body:         `{"name":"insecure","webhook_url":"http://hooks.slack.com/services/T00/B00/xxx"}`,
			seed:         func(_ *testing.T, _ *MockNotificationChannelRepository) {},
			expectedCode: http.StatusBadRequest,
		},
		{
			name:         "invalid JSON returns 400",
			body:         `{not json}`,
			seed:         func(_ *testing.T, _ *MockNotificationChannelRepository) {},
			expectedCode: http.StatusBadRequest,
		},
		{
			name: "duplicate name returns 409",
			body: `{"name":"slack-prod","webhook_url":"https://hooks.slack.com/services/T00/B00/yyy"}`,
			seed: func(t *testing.T, repo *MockNotificationChannelRepository) {
				seedChannel(t, repo, "existing-id", "slack-prod", "https://hooks.slack.com/services/T00/B00/xxx", true)
			},
			expectedCode: http.StatusConflict,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			router, repo := setupNotificationChannelRouter()
			tt.seed(t, repo)

			w := httptest.NewRecorder()
			req, _ := http.NewRequest("POST", "/api/v1/admin/notification-channels", strings.NewReader(tt.body))
			req.Header.Set("Content-Type", "application/json")
			router.ServeHTTP(w, req)

			assert.Equal(t, tt.expectedCode, w.Code)

			if tt.expectedCode == http.StatusCreated {
				var ch models.NotificationChannel
				err := json.Unmarshal(w.Body.Bytes(), &ch)
				require.NoError(t, err)
				assert.NotEmpty(t, ch.ID)
				assert.NotEmpty(t, ch.Name)
			}
		})
	}
}

func TestNotificationChannelHandler_CreateChannel_DefaultEnabled(t *testing.T) {
	t.Parallel()
	router, _ := setupNotificationChannelRouter()

	body := `{"name":"default-enabled","webhook_url":"https://hooks.example.com/w"}`
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/api/v1/admin/notification-channels", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusCreated, w.Code)

	var ch models.NotificationChannel
	err := json.Unmarshal(w.Body.Bytes(), &ch)
	require.NoError(t, err)
	assert.True(t, ch.Enabled, "channel should be enabled by default")
}

func TestNotificationChannelHandler_CreateChannel_ExplicitDisabled(t *testing.T) {
	t.Parallel()
	router, _ := setupNotificationChannelRouter()

	body := `{"name":"disabled-ch","webhook_url":"https://hooks.example.com/w","enabled":false}`
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/api/v1/admin/notification-channels", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusCreated, w.Code)

	var ch models.NotificationChannel
	err := json.Unmarshal(w.Body.Bytes(), &ch)
	require.NoError(t, err)
	assert.False(t, ch.Enabled, "channel should be explicitly disabled")
}

func TestNotificationChannelHandler_GetChannel(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		channelID    string
		seed         func(*testing.T, *MockNotificationChannelRepository)
		expectedCode int
	}{
		{
			name:      "found returns 200",
			channelID: "ch1",
			seed: func(t *testing.T, repo *MockNotificationChannelRepository) {
				seedChannel(t, repo, "ch1", "slack-prod", "https://hooks.slack.com/1", true)
			},
			expectedCode: http.StatusOK,
		},
		{
			name:         "not found returns 404",
			channelID:    "nonexistent",
			seed:         func(_ *testing.T, _ *MockNotificationChannelRepository) {},
			expectedCode: http.StatusNotFound,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			router, repo := setupNotificationChannelRouter()
			tt.seed(t, repo)

			w := httptest.NewRecorder()
			req, _ := http.NewRequest("GET", "/api/v1/admin/notification-channels/"+tt.channelID, nil)
			router.ServeHTTP(w, req)

			assert.Equal(t, tt.expectedCode, w.Code)

			if tt.expectedCode == http.StatusOK {
				var ch models.NotificationChannel
				err := json.Unmarshal(w.Body.Bytes(), &ch)
				require.NoError(t, err)
				assert.Equal(t, tt.channelID, ch.ID)
			}
		})
	}
}

func TestNotificationChannelHandler_UpdateChannel(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		channelID    string
		body         string
		seed         func(*testing.T, *MockNotificationChannelRepository)
		expectedCode int
		checkName    string
	}{
		{
			name:      "updates name",
			channelID: "ch1",
			body:      `{"name":"new-name"}`,
			seed: func(t *testing.T, repo *MockNotificationChannelRepository) {
				seedChannel(t, repo, "ch1", "old-name", "https://hooks.slack.com/1", true)
			},
			expectedCode: http.StatusOK,
			checkName:    "new-name",
		},
		{
			name:      "updates webhook_url",
			channelID: "ch1",
			body:      `{"webhook_url":"https://new.webhook.url/hook"}`,
			seed: func(t *testing.T, repo *MockNotificationChannelRepository) {
				seedChannel(t, repo, "ch1", "unchanged", "https://old.webhook.url/hook", true)
			},
			expectedCode: http.StatusOK,
			checkName:    "unchanged",
		},
		{
			name:      "updates enabled to false",
			channelID: "ch1",
			body:      `{"enabled":false}`,
			seed: func(t *testing.T, repo *MockNotificationChannelRepository) {
				seedChannel(t, repo, "ch1", "test", "https://hooks.slack.com/1", true)
			},
			expectedCode: http.StatusOK,
		},
		{
			name:         "not found returns 404",
			channelID:    "nonexistent",
			body:         `{"name":"new"}`,
			seed:         func(_ *testing.T, _ *MockNotificationChannelRepository) {},
			expectedCode: http.StatusNotFound,
		},
		{
			name:      "invalid JSON returns 400",
			channelID: "ch1",
			body:      `{not valid`,
			seed: func(t *testing.T, repo *MockNotificationChannelRepository) {
				seedChannel(t, repo, "ch1", "test", "https://hooks.slack.com/1", true)
			},
			expectedCode: http.StatusBadRequest,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			router, repo := setupNotificationChannelRouter()
			tt.seed(t, repo)

			w := httptest.NewRecorder()
			req, _ := http.NewRequest("PUT", "/api/v1/admin/notification-channels/"+tt.channelID, strings.NewReader(tt.body))
			req.Header.Set("Content-Type", "application/json")
			router.ServeHTTP(w, req)

			assert.Equal(t, tt.expectedCode, w.Code)

			if tt.expectedCode == http.StatusOK && tt.checkName != "" {
				var ch models.NotificationChannel
				err := json.Unmarshal(w.Body.Bytes(), &ch)
				require.NoError(t, err)
				assert.Equal(t, tt.checkName, ch.Name)
			}
		})
	}
}

func TestNotificationChannelHandler_DeleteChannel(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		channelID    string
		seed         func(*testing.T, *MockNotificationChannelRepository)
		expectedCode int
	}{
		{
			name:      "success returns 204",
			channelID: "ch1",
			seed: func(t *testing.T, repo *MockNotificationChannelRepository) {
				seedChannel(t, repo, "ch1", "slack-prod", "https://hooks.slack.com/1", true)
			},
			expectedCode: http.StatusNoContent,
		},
		{
			name:         "not found returns 404",
			channelID:    "nonexistent",
			seed:         func(_ *testing.T, _ *MockNotificationChannelRepository) {},
			expectedCode: http.StatusNotFound,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			router, repo := setupNotificationChannelRouter()
			tt.seed(t, repo)

			w := httptest.NewRecorder()
			req, _ := http.NewRequest("DELETE", "/api/v1/admin/notification-channels/"+tt.channelID, nil)
			router.ServeHTTP(w, req)

			assert.Equal(t, tt.expectedCode, w.Code)
		})
	}
}

func TestNotificationChannelHandler_DeleteChannel_CleansUpRelated(t *testing.T) {
	t.Parallel()
	router, repo := setupNotificationChannelRouter()

	seedChannel(t, repo, "ch1", "test", "https://hooks.example.com/w", true)
	require.NoError(t, repo.SetSubscriptions(context.Background(), "ch1", []string{"deployment.success"}))
	seedDeliveryLog(t, repo, "ch1", "deployment.success", "ok", 200)

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("DELETE", "/api/v1/admin/notification-channels/ch1", nil)
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusNoContent, w.Code)

	// Verify subscriptions and logs are cleaned up.
	repo.mu.RLock()
	defer repo.mu.RUnlock()
	assert.Empty(t, repo.subscriptions["ch1"])
	assert.Empty(t, repo.deliveryLogs["ch1"])
}

func TestNotificationChannelHandler_UpdateSubscriptions(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		channelID    string
		body         string
		seed         func(*testing.T, *MockNotificationChannelRepository)
		expectedCode int
		checkTypes   []string
	}{
		{
			name:      "valid event types saved",
			channelID: "ch1",
			body:      `{"event_types":["deployment.success","deployment.error"]}`,
			seed: func(t *testing.T, repo *MockNotificationChannelRepository) {
				seedChannel(t, repo, "ch1", "test", "https://hooks.example.com/w", true)
			},
			expectedCode: http.StatusOK,
			checkTypes:   []string{"deployment.success", "deployment.error"},
		},
		{
			name:      "empty array clears subscriptions",
			channelID: "ch1",
			body:      `{"event_types":[]}`,
			seed: func(t *testing.T, repo *MockNotificationChannelRepository) {
				seedChannel(t, repo, "ch1", "test", "https://hooks.example.com/w", true)
				require.NoError(t, repo.SetSubscriptions(context.Background(), "ch1", []string{"deployment.success"}))
			},
			expectedCode: http.StatusOK,
			checkTypes:   []string{},
		},
		{
			name:      "unknown event type returns 400",
			channelID: "ch1",
			body:      `{"event_types":["deployment.success","bogus.event"]}`,
			seed: func(t *testing.T, repo *MockNotificationChannelRepository) {
				seedChannel(t, repo, "ch1", "test", "https://hooks.example.com/w", true)
			},
			expectedCode: http.StatusBadRequest,
		},
		{
			name:         "non-existent channel returns 404",
			channelID:    "nonexistent",
			body:         `{"event_types":["deployment.success"]}`,
			seed:         func(_ *testing.T, _ *MockNotificationChannelRepository) {},
			expectedCode: http.StatusNotFound,
		},
		{
			name:      "deduplicates event types",
			channelID: "ch1",
			body:      `{"event_types":["deployment.success","deployment.success","deployment.error"]}`,
			seed: func(t *testing.T, repo *MockNotificationChannelRepository) {
				seedChannel(t, repo, "ch1", "test", "https://hooks.example.com/w", true)
			},
			expectedCode: http.StatusOK,
			checkTypes:   []string{"deployment.success", "deployment.error"},
		},
		{
			name:      "invalid JSON returns 400",
			channelID: "ch1",
			body:      `{not valid`,
			seed: func(t *testing.T, repo *MockNotificationChannelRepository) {
				seedChannel(t, repo, "ch1", "test", "https://hooks.example.com/w", true)
			},
			expectedCode: http.StatusBadRequest,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			router, repo := setupNotificationChannelRouter()
			tt.seed(t, repo)

			w := httptest.NewRecorder()
			req, _ := http.NewRequest("PUT", "/api/v1/admin/notification-channels/"+tt.channelID+"/subscriptions", strings.NewReader(tt.body))
			req.Header.Set("Content-Type", "application/json")
			router.ServeHTTP(w, req)

			assert.Equal(t, tt.expectedCode, w.Code)

			if tt.expectedCode == http.StatusOK && tt.checkTypes != nil {
				var result map[string][]string
				err := json.Unmarshal(w.Body.Bytes(), &result)
				require.NoError(t, err)
				assert.ElementsMatch(t, tt.checkTypes, result["event_types"])
			}
		})
	}
}

func TestNotificationChannelHandler_GetSubscriptions(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		channelID    string
		seed         func(*testing.T, *MockNotificationChannelRepository)
		expectedCode int
		expectedLen  int
	}{
		{
			name:      "returns event types",
			channelID: "ch1",
			seed: func(t *testing.T, repo *MockNotificationChannelRepository) {
				seedChannel(t, repo, "ch1", "test", "https://hooks.example.com/w", true)
				require.NoError(t, repo.SetSubscriptions(context.Background(), "ch1", []string{"deployment.success", "deployment.error"}))
			},
			expectedCode: http.StatusOK,
			expectedLen:  2,
		},
		{
			name:      "empty subscriptions",
			channelID: "ch1",
			seed: func(t *testing.T, repo *MockNotificationChannelRepository) {
				seedChannel(t, repo, "ch1", "test", "https://hooks.example.com/w", true)
			},
			expectedCode: http.StatusOK,
			expectedLen:  0,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			router, repo := setupNotificationChannelRouter()
			tt.seed(t, repo)

			w := httptest.NewRecorder()
			req, _ := http.NewRequest("GET", "/api/v1/admin/notification-channels/"+tt.channelID+"/subscriptions", nil)
			router.ServeHTTP(w, req)

			assert.Equal(t, tt.expectedCode, w.Code)

			if tt.expectedCode == http.StatusOK {
				var result map[string][]string
				err := json.Unmarshal(w.Body.Bytes(), &result)
				require.NoError(t, err)
				assert.Len(t, result["event_types"], tt.expectedLen)
			}
		})
	}
}

func TestNotificationChannelHandler_GetSubscriptions_RepoError(t *testing.T) {
	t.Parallel()
	router, repo := setupNotificationChannelRouter()
	seedChannel(t, repo, "ch1", "test", "https://hooks.example.com/w", true)
	repo.SetError(fmt.Errorf("db failure"))

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/api/v1/admin/notification-channels/ch1/subscriptions", nil)
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusInternalServerError, w.Code)
}

func TestNotificationChannelHandler_ListEventTypes(t *testing.T) {
	t.Parallel()
	router, _ := setupNotificationChannelRouter()

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/api/v1/admin/notification-channels/event-types", nil)
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var result map[string][]string
	err := json.Unmarshal(w.Body.Bytes(), &result)
	require.NoError(t, err)
	assert.ElementsMatch(t, notifier.AllEventTypes(), result["event_types"])
	assert.NotEmpty(t, result["event_types"])
}

func TestNotificationChannelHandler_TestChannel(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		channelID       string
		webhookStatus   int
		seed            func(*testing.T, *MockNotificationChannelRepository, string)
		expectedCode    int
		expectedSuccess bool
	}{
		{
			name:          "successful webhook returns success true",
			channelID:     "ch1",
			webhookStatus: http.StatusOK,
			seed: func(t *testing.T, repo *MockNotificationChannelRepository, webhookURL string) {
				seedChannel(t, repo, "ch1", "test-channel", webhookURL, true)
			},
			expectedCode:    http.StatusOK,
			expectedSuccess: true,
		},
		{
			name:          "webhook returns 500 returns success false",
			channelID:     "ch1",
			webhookStatus: http.StatusInternalServerError,
			seed: func(t *testing.T, repo *MockNotificationChannelRepository, webhookURL string) {
				seedChannel(t, repo, "ch1", "test-channel", webhookURL, true)
			},
			expectedCode:    http.StatusOK,
			expectedSuccess: false,
		},
		{
			name:          "not found channel returns 404",
			channelID:     "nonexistent",
			webhookStatus: http.StatusOK,
			seed:          func(_ *testing.T, _ *MockNotificationChannelRepository, _ string) {},
			expectedCode:  http.StatusNotFound,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// Create a test webhook server.
			var receivedContentType string
			var receivedEvent string
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				receivedContentType = r.Header.Get("Content-Type")
				receivedEvent = r.Header.Get("X-StackManager-Event")
				w.WriteHeader(tt.webhookStatus)
			}))
			defer ts.Close()

			router, repo := setupNotificationChannelRouter()
			tt.seed(t, repo, ts.URL)

			w := httptest.NewRecorder()
			req, _ := http.NewRequest("POST", "/api/v1/admin/notification-channels/"+tt.channelID+"/test", nil)
			router.ServeHTTP(w, req)

			assert.Equal(t, tt.expectedCode, w.Code)

			if tt.expectedCode == http.StatusOK {
				var result map[string]interface{}
				err := json.Unmarshal(w.Body.Bytes(), &result)
				require.NoError(t, err)
				assert.Equal(t, tt.expectedSuccess, result["success"])
				assert.NotEmpty(t, result["message"])

				// Verify correct headers were sent to the webhook.
				if tt.channelID != "nonexistent" {
					assert.Equal(t, "application/json", receivedContentType)
					assert.Equal(t, "test", receivedEvent)
				}
			}
		})
	}
}

func TestNotificationChannelHandler_TestChannel_WithSecret(t *testing.T) {
	t.Parallel()

	var receivedSignature string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedSignature = r.Header.Get("X-StackManager-Signature")
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	router, repo := setupNotificationChannelRouter()
	now := time.Now().UTC()
	repo.mu.Lock()
	repo.channels["ch1"] = &models.NotificationChannel{
		ID:         "ch1",
		Name:       "secret-channel",
		WebhookURL: ts.URL,
		Secret:     "my-secret-key",
		Enabled:    true,
		CreatedAt:  now,
		UpdatedAt:  now,
	}
	repo.mu.Unlock()

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/api/v1/admin/notification-channels/ch1/test", nil)
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.True(t, strings.HasPrefix(receivedSignature, "sha256="), "signature should have sha256= prefix")
	assert.Greater(t, len(receivedSignature), len("sha256="), "signature should contain actual HMAC value")
}

func TestNotificationChannelHandler_TestChannel_ConnectionFailure(t *testing.T) {
	t.Parallel()

	router, repo := setupNotificationChannelRouter()
	// Use an unreachable URL to simulate connection failure.
	seedChannel(t, repo, "ch1", "unreachable", "https://192.0.2.1:9999/webhook", true)

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/api/v1/admin/notification-channels/ch1/test", nil)
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var result map[string]interface{}
	err := json.Unmarshal(w.Body.Bytes(), &result)
	require.NoError(t, err)
	assert.Equal(t, false, result["success"])
	assert.Equal(t, "Connection failed", result["message"])
}

func TestNotificationChannelHandler_ListDeliveryLogs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		channelID     string
		query         string
		seed          func(*testing.T, *MockNotificationChannelRepository)
		expectedCode  int
		expectedCount int
		expectedLimit int
	}{
		{
			name:      "returns logs with default pagination",
			channelID: "ch1",
			query:     "",
			seed: func(t *testing.T, repo *MockNotificationChannelRepository) {
				seedChannel(t, repo, "ch1", "test", "https://hooks.example.com/w", true)
				for i := 0; i < 5; i++ {
					seedDeliveryLog(t, repo, "ch1", "deployment.success", "ok", 200)
				}
			},
			expectedCode:  http.StatusOK,
			expectedCount: 5,
			expectedLimit: 20,
		},
		{
			name:      "respects limit parameter",
			channelID: "ch1",
			query:     "?limit=2",
			seed: func(t *testing.T, repo *MockNotificationChannelRepository) {
				seedChannel(t, repo, "ch1", "test", "https://hooks.example.com/w", true)
				for i := 0; i < 5; i++ {
					seedDeliveryLog(t, repo, "ch1", "deployment.success", "ok", 200)
				}
			},
			expectedCode:  http.StatusOK,
			expectedCount: 2,
			expectedLimit: 2,
		},
		{
			name:      "caps limit at 100",
			channelID: "ch1",
			query:     "?limit=500",
			seed: func(t *testing.T, repo *MockNotificationChannelRepository) {
				seedChannel(t, repo, "ch1", "test", "https://hooks.example.com/w", true)
			},
			expectedCode:  http.StatusOK,
			expectedCount: 0,
			expectedLimit: 100,
		},
		{
			name:      "respects offset parameter",
			channelID: "ch1",
			query:     "?offset=3",
			seed: func(t *testing.T, repo *MockNotificationChannelRepository) {
				seedChannel(t, repo, "ch1", "test", "https://hooks.example.com/w", true)
				for i := 0; i < 5; i++ {
					seedDeliveryLog(t, repo, "ch1", "deployment.success", "ok", 200)
				}
			},
			expectedCode:  http.StatusOK,
			expectedCount: 2,
			expectedLimit: 20,
		},
		{
			name:      "empty logs",
			channelID: "ch1",
			query:     "",
			seed: func(t *testing.T, repo *MockNotificationChannelRepository) {
				seedChannel(t, repo, "ch1", "test", "https://hooks.example.com/w", true)
			},
			expectedCode:  http.StatusOK,
			expectedCount: 0,
			expectedLimit: 20,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			router, repo := setupNotificationChannelRouter()
			tt.seed(t, repo)

			w := httptest.NewRecorder()
			req, _ := http.NewRequest("GET", "/api/v1/admin/notification-channels/"+tt.channelID+"/delivery-logs"+tt.query, nil)
			router.ServeHTTP(w, req)

			assert.Equal(t, tt.expectedCode, w.Code)

			if tt.expectedCode == http.StatusOK {
				var result struct {
					Logs   []models.NotificationDeliveryLog `json:"logs"`
					Total  int64                            `json:"total"`
					Limit  int                              `json:"limit"`
					Offset int                              `json:"offset"`
				}
				err := json.Unmarshal(w.Body.Bytes(), &result)
				require.NoError(t, err)
				assert.Len(t, result.Logs, tt.expectedCount)
				assert.Equal(t, tt.expectedLimit, result.Limit)
			}
		})
	}
}

func TestNotificationChannelHandler_ListDeliveryLogs_RepoError(t *testing.T) {
	t.Parallel()
	router, repo := setupNotificationChannelRouter()
	seedChannel(t, repo, "ch1", "test", "https://hooks.example.com/w", true)
	repo.SetError(fmt.Errorf("db failure"))

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/api/v1/admin/notification-channels/ch1/delivery-logs", nil)
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusInternalServerError, w.Code)
}

func TestNotificationChannelHandler_ListDeliveryLogs_InvalidParams(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		query         string
		expectedLimit int
	}{
		{
			name:          "invalid limit falls back to default",
			query:         "?limit=abc",
			expectedLimit: 20,
		},
		{
			name:          "negative limit falls back to default",
			query:         "?limit=-5",
			expectedLimit: 20,
		},
		{
			name:          "zero limit falls back to default",
			query:         "?limit=0",
			expectedLimit: 20,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			router, repo := setupNotificationChannelRouter()
			seedChannel(t, repo, "ch1", "test", "https://hooks.example.com/w", true)

			w := httptest.NewRecorder()
			req, _ := http.NewRequest("GET", "/api/v1/admin/notification-channels/ch1/delivery-logs"+tt.query, nil)
			router.ServeHTTP(w, req)

			assert.Equal(t, http.StatusOK, w.Code)

			var result struct {
				Limit int `json:"limit"`
			}
			err := json.Unmarshal(w.Body.Bytes(), &result)
			require.NoError(t, err)
			assert.Equal(t, tt.expectedLimit, result.Limit)
		})
	}
}
