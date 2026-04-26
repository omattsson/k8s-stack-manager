package notifier

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"backend/internal/models"
	"backend/internal/websocket"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---- Mock NotificationRepository ----

type mockNotificationRepository struct {
	mu          sync.Mutex
	created     []*models.Notification
	createErr   error
	callCount   int
}

func newMockNotificationRepo() *mockNotificationRepository {
	return &mockNotificationRepository{}
}

func (m *mockNotificationRepository) Create(_ context.Context, notification *models.Notification) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.callCount++
	if m.createErr != nil {
		return m.createErr
	}
	cp := *notification
	m.created = append(m.created, &cp)
	return nil
}

func (m *mockNotificationRepository) ListByUser(_ context.Context, _ string, _ bool, _, _ int) ([]models.Notification, int64, error) {
	return nil, 0, nil
}

func (m *mockNotificationRepository) CountUnread(_ context.Context, _ string) (int64, error) {
	return 0, nil
}

func (m *mockNotificationRepository) MarkAsRead(_ context.Context, _, _ string) error {
	return nil
}

func (m *mockNotificationRepository) MarkAllAsRead(_ context.Context, _ string) error {
	return nil
}

func (m *mockNotificationRepository) GetPreferences(_ context.Context, _ string) ([]models.NotificationPreference, error) {
	return nil, nil
}

func (m *mockNotificationRepository) UpdatePreference(_ context.Context, _ *models.NotificationPreference) error {
	return nil
}

func (m *mockNotificationRepository) SetCreateError(err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.createErr = err
}

func (m *mockNotificationRepository) Created() []*models.Notification {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*models.Notification, len(m.created))
	copy(out, m.created)
	return out
}

func (m *mockNotificationRepository) CallCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.callCount
}

// ---- Tests ----

func TestNotify(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		hub          *websocket.Hub
		createErr    error
		wantErr      bool
		wantCreated  bool
	}{
		{
			name:        "successful notification creation with nil hub",
			hub:         nil,
			wantErr:     false,
			wantCreated: true,
		},
		{
			name:        "successful notification creation with hub",
			hub:         websocket.NewHub(),
			wantErr:     false,
			wantCreated: true,
		},
		{
			name:        "repo Create error returns error",
			hub:         nil,
			createErr:   errors.New("database connection lost"),
			wantErr:     true,
			wantCreated: false,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			repo := newMockNotificationRepo()
			if tt.createErr != nil {
				repo.SetCreateError(tt.createErr)
			}

			// If we have a hub, start it so Broadcast does not block.
			if tt.hub != nil {
				go tt.hub.Run()
				defer tt.hub.Shutdown()
			}

			n := NewNotifier(repo, tt.hub, nil)

			err := n.Notify(
				context.Background(),
				"user-123",
				"deployment",
				"Deploy Complete",
				"Instance my-app deployed successfully",
				"stack_instance",
				"inst-456",
			)

			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}

			if tt.wantCreated {
				created := repo.Created()
				require.Len(t, created, 1)

				notif := created[0]
				assert.Equal(t, "user-123", notif.UserID)
				assert.Equal(t, "deployment", notif.Type)
				assert.Equal(t, "Deploy Complete", notif.Title)
				assert.Equal(t, "Instance my-app deployed successfully", notif.Message)
				assert.Equal(t, "stack_instance", notif.EntityType)
				assert.Equal(t, "inst-456", notif.EntityID)
				assert.False(t, notif.IsRead)
				assert.NotEmpty(t, notif.ID, "ID should be a generated UUID")
				assert.WithinDuration(t, time.Now().UTC(), notif.CreatedAt, 5*time.Second)
			} else {
				assert.Empty(t, repo.Created())
			}

			// Repo should always be called exactly once.
			assert.Equal(t, 1, repo.CallCount())
		})
	}
}

func TestNotifyUUIDUniqueness(t *testing.T) {
	t.Parallel()

	repo := newMockNotificationRepo()
	n := NewNotifier(repo, nil, nil)

	// Create multiple notifications and verify unique IDs.
	for i := 0; i < 10; i++ {
		err := n.Notify(context.Background(), "user-1", "info", "Title", "Msg", "", "")
		require.NoError(t, err)
	}

	created := repo.Created()
	require.Len(t, created, 10)

	ids := make(map[string]bool, 10)
	for _, notif := range created {
		assert.NotEmpty(t, notif.ID)
		assert.False(t, ids[notif.ID], "duplicate ID found: %s", notif.ID)
		ids[notif.ID] = true
	}
}

func TestNotifyFieldsAreSetCorrectly(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		userID     string
		notifType  string
		title      string
		message    string
		entityType string
		entityID   string
	}{
		{
			name:       "all fields populated",
			userID:     "user-abc",
			notifType:  "warning",
			title:      "Expiry Warning",
			message:    "Instance will expire in 30 minutes",
			entityType: "stack_instance",
			entityID:   "inst-789",
		},
		{
			name:       "optional fields empty",
			userID:     "user-xyz",
			notifType:  "info",
			title:      "System Update",
			message:    "New version available",
			entityType: "",
			entityID:   "",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			repo := newMockNotificationRepo()
			n := NewNotifier(repo, nil, nil)

			before := time.Now().UTC()
			err := n.Notify(context.Background(), tt.userID, tt.notifType, tt.title, tt.message, tt.entityType, tt.entityID)
			after := time.Now().UTC()

			require.NoError(t, err)
			created := repo.Created()
			require.Len(t, created, 1)

			notif := created[0]
			assert.Equal(t, tt.userID, notif.UserID)
			assert.Equal(t, tt.notifType, notif.Type)
			assert.Equal(t, tt.title, notif.Title)
			assert.Equal(t, tt.message, notif.Message)
			assert.Equal(t, tt.entityType, notif.EntityType)
			assert.Equal(t, tt.entityID, notif.EntityID)
			assert.False(t, notif.IsRead)
			assert.NotEmpty(t, notif.ID)

			// CreatedAt should be between before and after.
			assert.True(t, !notif.CreatedAt.Before(before), "CreatedAt should not be before test start")
			assert.True(t, !notif.CreatedAt.After(after), "CreatedAt should not be after test end")
		})
	}
}

func TestNotifyWithHubBroadcasts(t *testing.T) {
	t.Parallel()

	hub := websocket.NewHub()
	go hub.Run()
	defer hub.Shutdown()

	repo := newMockNotificationRepo()
	n := NewNotifier(repo, hub, nil)

	err := n.Notify(context.Background(), "user-1", "deploy", "Done", "Deployed", "instance", "i1")
	assert.NoError(t, err)

	// Verify DB record was created even with hub present.
	created := repo.Created()
	require.Len(t, created, 1)
	assert.Equal(t, "user-1", created[0].UserID)
}

func TestNewNotifier(t *testing.T) {
	t.Parallel()

	t.Run("creates notifier with nil hub", func(t *testing.T) {
		t.Parallel()
		repo := newMockNotificationRepo()
		n := NewNotifier(repo, nil, nil)
		assert.NotNil(t, n)
	})

	t.Run("creates notifier with hub", func(t *testing.T) {
		t.Parallel()
		repo := newMockNotificationRepo()
		hub := websocket.NewHub()
		n := NewNotifier(repo, hub, nil)
		assert.NotNil(t, n)
	})
}
