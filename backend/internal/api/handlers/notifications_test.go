package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"backend/internal/models"
	"backend/pkg/dberrors"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---- MockNotificationRepository ----

type MockNotificationRepository struct {
	mu            sync.RWMutex
	notifications []models.Notification
	preferences   []models.NotificationPreference
	err           error
}

func NewMockNotificationRepository() *MockNotificationRepository {
	return &MockNotificationRepository{}
}

func (m *MockNotificationRepository) SetError(err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.err = err
}

func (m *MockNotificationRepository) Create(_ context.Context, n *models.Notification) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return m.err
	}
	if n.ID == "" {
		n.ID = uuid.New().String()
	}
	if n.CreatedAt.IsZero() {
		n.CreatedAt = time.Now().UTC()
	}
	m.notifications = append(m.notifications, *n)
	return nil
}

func (m *MockNotificationRepository) ListByUser(_ context.Context, userID string, unreadOnly bool, limit, offset int) ([]models.Notification, int64, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.err != nil {
		return nil, 0, m.err
	}

	var filtered []models.Notification
	for _, n := range m.notifications {
		if n.UserID != userID {
			continue
		}
		if unreadOnly && n.IsRead {
			continue
		}
		filtered = append(filtered, n)
	}

	total := int64(len(filtered))

	if offset > len(filtered) {
		offset = len(filtered)
	}
	filtered = filtered[offset:]
	if limit > 0 && limit < len(filtered) {
		filtered = filtered[:limit]
	}

	return filtered, total, nil
}

func (m *MockNotificationRepository) CountUnread(_ context.Context, userID string) (int64, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.err != nil {
		return 0, m.err
	}

	var count int64
	for _, n := range m.notifications {
		if n.UserID == userID && !n.IsRead {
			count++
		}
	}
	return count, nil
}

func (m *MockNotificationRepository) MarkAsRead(_ context.Context, id string, userID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return m.err
	}

	for i := range m.notifications {
		if m.notifications[i].ID == id && m.notifications[i].UserID == userID {
			m.notifications[i].IsRead = true
			return nil
		}
	}
	return dberrors.NewDatabaseError("mark_read", dberrors.ErrNotFound)
}

func (m *MockNotificationRepository) MarkAllAsRead(_ context.Context, userID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return m.err
	}

	for i := range m.notifications {
		if m.notifications[i].UserID == userID {
			m.notifications[i].IsRead = true
		}
	}
	return nil
}

func (m *MockNotificationRepository) GetPreferences(_ context.Context, userID string) ([]models.NotificationPreference, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.err != nil {
		return nil, m.err
	}

	var result []models.NotificationPreference
	for _, p := range m.preferences {
		if p.UserID == userID {
			result = append(result, p)
		}
	}
	return result, nil
}

func (m *MockNotificationRepository) UpdatePreference(_ context.Context, pref *models.NotificationPreference) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return m.err
	}

	// Simulate ON CONFLICT (user_id, event_type) DO UPDATE.
	for i := range m.preferences {
		if m.preferences[i].UserID == pref.UserID && m.preferences[i].EventType == pref.EventType {
			pref.ID = m.preferences[i].ID // keep original ID
			m.preferences[i] = *pref
			return nil
		}
	}
	m.preferences = append(m.preferences, *pref)
	return nil
}

// ---- Test setup ----

func setupNotificationRouter() (*gin.Engine, *MockNotificationRepository) {
	gin.SetMode(gin.TestMode)
	repo := NewMockNotificationRepository()
	handler := NewNotificationHandler(repo)

	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set("userID", "user-1")
		c.Set("username", "testuser")
		c.Set("role", "developer")
		c.Next()
	})
	notifications := r.Group("/api/v1/notifications")
	{
		notifications.GET("", handler.List)
		notifications.GET("/count", handler.CountUnread)
		notifications.POST("/:id/read", handler.MarkAsRead)
		notifications.POST("/read-all", handler.MarkAllAsRead)
		notifications.GET("/preferences", handler.GetPreferences)
		notifications.PUT("/preferences", handler.UpdatePreferences)
	}
	return r, repo
}

func seedNotification(t *testing.T, repo *MockNotificationRepository, id, userID, notifType, title string, isRead bool) {
	t.Helper()
	require.NoError(t, repo.Create(context.Background(), &models.Notification{
		ID:        id,
		UserID:    userID,
		Type:      notifType,
		Title:     title,
		Message:   "test message",
		IsRead:    isRead,
		CreatedAt: time.Now().UTC(),
	}))
}

// ---- Tests ----

func TestNotificationHandler_List(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		query          string
		seed           func(*testing.T, *MockNotificationRepository)
		expectedStatus int
		expectedTotal  int64
		expectedCount  int
	}{
		{
			name:           "empty list",
			query:          "",
			seed:           func(_ *testing.T, _ *MockNotificationRepository) {},
			expectedStatus: http.StatusOK,
			expectedTotal:  0,
			expectedCount:  0,
		},
		{
			name:  "returns own notifications",
			query: "",
			seed: func(t *testing.T, repo *MockNotificationRepository) {
				seedNotification(t, repo, "n1", "user-1", "deployment.success", "Deploy OK", false)
				seedNotification(t, repo, "n2", "user-1", "deployment.error", "Deploy Failed", true)
				seedNotification(t, repo, "n3", "user-2", "deployment.success", "Other user", false)
			},
			expectedStatus: http.StatusOK,
			expectedTotal:  2,
			expectedCount:  2,
		},
		{
			name:  "unread_only filter",
			query: "?unread_only=true",
			seed: func(t *testing.T, repo *MockNotificationRepository) {
				seedNotification(t, repo, "n1", "user-1", "deployment.success", "Deploy OK", false)
				seedNotification(t, repo, "n2", "user-1", "deployment.error", "Deploy Failed", true)
			},
			expectedStatus: http.StatusOK,
			expectedTotal:  1,
			expectedCount:  1,
		},
		{
			name:  "pagination with limit and offset",
			query: "?limit=2&offset=1",
			seed: func(t *testing.T, repo *MockNotificationRepository) {
				for i := 0; i < 5; i++ {
					seedNotification(t, repo, uuid.New().String(), "user-1", "deployment.success", "Notif", false)
				}
			},
			expectedStatus: http.StatusOK,
			expectedTotal:  5,
			expectedCount:  2,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			router, repo := setupNotificationRouter()
			tt.seed(t, repo)

			w := httptest.NewRecorder()
			req, _ := http.NewRequest("GET", "/api/v1/notifications"+tt.query, nil)
			router.ServeHTTP(w, req)

			assert.Equal(t, tt.expectedStatus, w.Code)

			var result models.PaginatedNotifications
			err := json.Unmarshal(w.Body.Bytes(), &result)
			require.NoError(t, err)
			assert.Equal(t, tt.expectedTotal, result.Total)
			assert.Len(t, result.Notifications, tt.expectedCount)
		})
	}
}

func TestNotificationHandler_List_InvalidParams(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		query string
	}{
		{name: "invalid limit", query: "?limit=abc"},
		{name: "negative limit", query: "?limit=-1"},
		{name: "invalid offset", query: "?offset=xyz"},
		{name: "negative offset", query: "?offset=-5"},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			router, _ := setupNotificationRouter()

			w := httptest.NewRecorder()
			req, _ := http.NewRequest("GET", "/api/v1/notifications"+tt.query, nil)
			router.ServeHTTP(w, req)

			assert.Equal(t, http.StatusBadRequest, w.Code)
		})
	}
}

func TestNotificationHandler_List_RepoError(t *testing.T) {
	t.Parallel()
	router, repo := setupNotificationRouter()
	repo.SetError(errors.New("db failure"))

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/api/v1/notifications", nil)
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusInternalServerError, w.Code)
	var body map[string]string
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	assert.Equal(t, "Internal server error", body["error"])
}

func TestNotificationHandler_CountUnread(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		seed          func(*testing.T, *MockNotificationRepository)
		expectedCount int64
	}{
		{
			name:          "no notifications",
			seed:          func(_ *testing.T, _ *MockNotificationRepository) {},
			expectedCount: 0,
		},
		{
			name: "counts only unread",
			seed: func(t *testing.T, repo *MockNotificationRepository) {
				seedNotification(t, repo, "n1", "user-1", "deployment.success", "Deploy OK", false)
				seedNotification(t, repo, "n2", "user-1", "deployment.error", "Deploy Failed", true)
				seedNotification(t, repo, "n3", "user-1", "instance.deleted", "Deleted", false)
			},
			expectedCount: 2,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			router, repo := setupNotificationRouter()
			tt.seed(t, repo)

			w := httptest.NewRecorder()
			req, _ := http.NewRequest("GET", "/api/v1/notifications/count", nil)
			router.ServeHTTP(w, req)

			assert.Equal(t, http.StatusOK, w.Code)

			var result map[string]int64
			err := json.Unmarshal(w.Body.Bytes(), &result)
			require.NoError(t, err)
			assert.Equal(t, tt.expectedCount, result["unread_count"])
		})
	}
}

func TestNotificationHandler_CountUnread_RepoError(t *testing.T) {
	t.Parallel()
	router, repo := setupNotificationRouter()
	repo.SetError(errors.New("db failure"))

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/api/v1/notifications/count", nil)
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusInternalServerError, w.Code)
}

func TestNotificationHandler_MarkAsRead(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		notifID        string
		seed           func(*testing.T, *MockNotificationRepository)
		expectedStatus int
	}{
		{
			name:    "mark own notification as read",
			notifID: "n1",
			seed: func(t *testing.T, repo *MockNotificationRepository) {
				seedNotification(t, repo, "n1", "user-1", "deployment.success", "Deploy OK", false)
			},
			expectedStatus: http.StatusOK,
		},
		{
			name:           "notification not found",
			notifID:        "nonexistent",
			seed:           func(_ *testing.T, _ *MockNotificationRepository) {},
			expectedStatus: http.StatusNotFound,
		},
		{
			name:    "wrong user cannot mark as read",
			notifID: "n2",
			seed: func(t *testing.T, repo *MockNotificationRepository) {
				seedNotification(t, repo, "n2", "user-2", "deployment.success", "Other user", false)
			},
			expectedStatus: http.StatusNotFound,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			router, repo := setupNotificationRouter()
			tt.seed(t, repo)

			w := httptest.NewRecorder()
			req, _ := http.NewRequest("POST", "/api/v1/notifications/"+tt.notifID+"/read", nil)
			router.ServeHTTP(w, req)

			assert.Equal(t, tt.expectedStatus, w.Code)
		})
	}
}

func TestNotificationHandler_MarkAsRead_RepoError(t *testing.T) {
	t.Parallel()
	router, repo := setupNotificationRouter()
	seedNotification(t, repo, "n1", "user-1", "deployment.success", "Deploy OK", false)
	repo.SetError(errors.New("db failure"))

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/api/v1/notifications/n1/read", nil)
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusInternalServerError, w.Code)
}

func TestNotificationHandler_MarkAllAsRead(t *testing.T) {
	t.Parallel()

	router, repo := setupNotificationRouter()
	seedNotification(t, repo, "n1", "user-1", "deployment.success", "Deploy OK", false)
	seedNotification(t, repo, "n2", "user-1", "deployment.error", "Deploy Failed", false)

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/api/v1/notifications/read-all", nil)
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	// Verify all are now read.
	count, err := repo.CountUnread(context.Background(), "user-1")
	require.NoError(t, err)
	assert.Equal(t, int64(0), count)
}

func TestNotificationHandler_MarkAllAsRead_RepoError(t *testing.T) {
	t.Parallel()
	router, repo := setupNotificationRouter()
	repo.SetError(errors.New("db failure"))

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/api/v1/notifications/read-all", nil)
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusInternalServerError, w.Code)
}

func TestNotificationHandler_GetPreferences(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		seed          func(*MockNotificationRepository)
		expectedCount int
	}{
		{
			name:          "empty preferences",
			seed:          func(_ *MockNotificationRepository) {},
			expectedCount: 0,
		},
		{
			name: "returns own preferences",
			seed: func(repo *MockNotificationRepository) {
				_ = repo.UpdatePreference(context.Background(), &models.NotificationPreference{
					ID: "p1", UserID: "user-1", EventType: "deployment.success", Enabled: true,
				})
				_ = repo.UpdatePreference(context.Background(), &models.NotificationPreference{
					ID: "p2", UserID: "user-1", EventType: "deployment.error", Enabled: false,
				})
				_ = repo.UpdatePreference(context.Background(), &models.NotificationPreference{
					ID: "p3", UserID: "user-2", EventType: "deployment.success", Enabled: true,
				})
			},
			expectedCount: 2,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			router, repo := setupNotificationRouter()
			tt.seed(repo)

			w := httptest.NewRecorder()
			req, _ := http.NewRequest("GET", "/api/v1/notifications/preferences", nil)
			router.ServeHTTP(w, req)

			assert.Equal(t, http.StatusOK, w.Code)

			var result []models.NotificationPreference
			err := json.Unmarshal(w.Body.Bytes(), &result)
			require.NoError(t, err)
			assert.Len(t, result, tt.expectedCount)
		})
	}
}

func TestNotificationHandler_GetPreferences_RepoError(t *testing.T) {
	t.Parallel()
	router, repo := setupNotificationRouter()
	repo.SetError(errors.New("db failure"))

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/api/v1/notifications/preferences", nil)
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusInternalServerError, w.Code)
}

func TestNotificationHandler_UpdatePreferences(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		body           string
		expectedStatus int
	}{
		{
			name:           "valid update",
			body:           `[{"event_type":"deployment.success","enabled":true},{"event_type":"deployment.error","enabled":false}]`,
			expectedStatus: http.StatusOK,
		},
		{
			name:           "invalid JSON",
			body:           `not json`,
			expectedStatus: http.StatusBadRequest,
		},
		{
			name:           "empty array",
			body:           `[]`,
			expectedStatus: http.StatusBadRequest,
		},
		{
			name:           "missing event_type",
			body:           `[{"enabled":true}]`,
			expectedStatus: http.StatusBadRequest,
		},
		{
			name:           "wrapped in object rejects (regression: #218)",
			body:           `{"preferences":[{"event_type":"deployment.success","enabled":true}]}`,
			expectedStatus: http.StatusBadRequest,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			router, _ := setupNotificationRouter()

			w := httptest.NewRecorder()
			req, _ := http.NewRequest("PUT", "/api/v1/notifications/preferences", strings.NewReader(tt.body))
			req.Header.Set("Content-Type", "application/json")
			router.ServeHTTP(w, req)

			assert.Equal(t, tt.expectedStatus, w.Code)

			if tt.expectedStatus == http.StatusOK {
				var result []models.NotificationPreference
				err := json.Unmarshal(w.Body.Bytes(), &result)
				require.NoError(t, err)
				assert.NotEmpty(t, result)
			}
		})
	}
}

func TestNotificationHandler_UpdatePreferences_RepoError(t *testing.T) {
	t.Parallel()
	router, repo := setupNotificationRouter()
	repo.SetError(errors.New("db failure"))

	body := `[{"event_type":"deployment.success","enabled":true}]`
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("PUT", "/api/v1/notifications/preferences", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusInternalServerError, w.Code)
}

func TestNotificationHandler_UpdatePreferences_UpdatesExisting(t *testing.T) {
	t.Parallel()
	router, repo := setupNotificationRouter()

	// Seed an existing preference.
	_ = repo.UpdatePreference(context.Background(), &models.NotificationPreference{
		ID: "p1", UserID: "user-1", EventType: "deployment.success", Enabled: true,
	})

	// Update it to disabled.
	body := `[{"event_type":"deployment.success","enabled":false}]`
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("PUT", "/api/v1/notifications/preferences", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var result []models.NotificationPreference
	err := json.Unmarshal(w.Body.Bytes(), &result)
	require.NoError(t, err)
	require.Len(t, result, 1)
	assert.Equal(t, "deployment.success", result[0].EventType)
	assert.False(t, result[0].Enabled)
	// Should reuse existing ID, not create a new one.
	assert.Equal(t, "p1", result[0].ID)
}
