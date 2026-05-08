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

	t.Run("creates notifier with all dependencies", func(t *testing.T) {
		t.Parallel()
		repo := newMockNotificationRepo()
		hub := websocket.NewHub()
		userRepo := &mockUserRepository{}
		n := NewNotifier(repo, hub, userRepo)
		assert.NotNil(t, n)
	})
}

// ---- Mock UserRepository ----

type mockUserRepository struct {
	mu           sync.Mutex
	users        []models.User
	listByRolesErr error
	listByRolesCalled bool
	rolesReceived []string
}

func (m *mockUserRepository) ListByRoles(roles []string) ([]models.User, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.listByRolesCalled = true
	m.rolesReceived = roles
	if m.listByRolesErr != nil {
		return nil, m.listByRolesErr
	}
	return m.users, nil
}

// Stub methods to satisfy models.UserRepository interface.
func (m *mockUserRepository) Create(_ *models.User) error                             { return nil }
func (m *mockUserRepository) FindByID(_ string) (*models.User, error)                 { return nil, nil }
func (m *mockUserRepository) FindByIDs(_ []string) (map[string]*models.User, error)   { return nil, nil }
func (m *mockUserRepository) FindByUsername(_ string) (*models.User, error)            { return nil, nil }
func (m *mockUserRepository) FindByExternalID(_, _ string) (*models.User, error)      { return nil, nil }
func (m *mockUserRepository) Update(_ *models.User) error                             { return nil }
func (m *mockUserRepository) Delete(_ string) error                                   { return nil }
func (m *mockUserRepository) List() ([]models.User, error)                            { return nil, nil }
func (m *mockUserRepository) Count() (int64, error)                                   { return 0, nil }

// ---- NotifySystem Tests ----

func TestNotifySystem(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name             string
		userRepo         *mockUserRepository
		nilUserRepo      bool
		createErr        error
		wantErr          bool
		wantCreatedCount int
		wantRoles        []string
	}{
		{
			name: "notifies all admin and devops users",
			userRepo: &mockUserRepository{
				users: []models.User{
					{ID: "admin-1", Username: "alice", Role: "admin"},
					{ID: "devops-1", Username: "bob", Role: "devops"},
					{ID: "admin-2", Username: "carol", Role: "admin"},
				},
			},
			wantCreatedCount: 3,
			wantRoles:        []string{"admin", "devops"},
		},
		{
			name:             "nil userRepo returns nil without error",
			nilUserRepo:      true,
			wantCreatedCount: 0,
		},
		{
			name: "ListByRoles error is propagated",
			userRepo: &mockUserRepository{
				listByRolesErr: errors.New("database unavailable"),
			},
			wantErr:          true,
			wantCreatedCount: 0,
			wantRoles:        []string{"admin", "devops"},
		},
		{
			name: "empty admin list creates no notifications",
			userRepo: &mockUserRepository{
				users: []models.User{},
			},
			wantCreatedCount: 0,
			wantRoles:        []string{"admin", "devops"},
		},
		{
			name: "single user notified",
			userRepo: &mockUserRepository{
				users: []models.User{
					{ID: "devops-solo", Username: "dave", Role: "devops"},
				},
			},
			wantCreatedCount: 1,
			wantRoles:        []string{"admin", "devops"},
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

			var userRepo models.UserRepository
			if !tt.nilUserRepo {
				userRepo = tt.userRepo
			}

			n := NewNotifier(repo, nil, userRepo)

			err := n.NotifySystem(
				context.Background(),
				"system_alert",
				"Cluster Unhealthy",
				"Cluster prod-eu lost connectivity",
				"cluster",
				"cluster-99",
			)

			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}

			assert.Equal(t, tt.wantCreatedCount, len(repo.Created()))

			// Verify roles passed to ListByRoles if userRepo was provided.
			if tt.userRepo != nil && tt.wantRoles != nil {
				tt.userRepo.mu.Lock()
				assert.True(t, tt.userRepo.listByRolesCalled, "ListByRoles should have been called")
				assert.Equal(t, tt.wantRoles, tt.userRepo.rolesReceived)
				tt.userRepo.mu.Unlock()
			}
		})
	}
}

func TestNotifySystem_CreateFailureContinuesToNextUser(t *testing.T) {
	t.Parallel()

	// Custom repo that fails on the first Create call, succeeds on subsequent.
	repo := &failOnceMockNotificationRepo{
		failOnCallN: 1,
		failErr:     errors.New("transient DB error"),
	}

	userRepo := &mockUserRepository{
		users: []models.User{
			{ID: "user-1", Username: "alice", Role: "admin"},
			{ID: "user-2", Username: "bob", Role: "admin"},
			{ID: "user-3", Username: "carol", Role: "devops"},
		},
	}

	n := NewNotifier(repo, nil, userRepo)

	err := n.NotifySystem(
		context.Background(),
		"warning",
		"Disk Full",
		"Node disk usage above 90%",
		"node",
		"node-5",
	)

	// NotifySystem should not return an error even if individual Notify calls fail.
	assert.NoError(t, err)

	// Should have attempted all 3 users; only 2 succeed (first fails).
	repo.mu.Lock()
	assert.Equal(t, 3, repo.callCount, "all 3 users should have been attempted")
	assert.Len(t, repo.created, 2, "2 notifications should have been created (1 failed)")
	repo.mu.Unlock()
}

func TestNotifySystem_VerifiesNotificationContent(t *testing.T) {
	t.Parallel()

	userRepo := &mockUserRepository{
		users: []models.User{
			{ID: "admin-42", Username: "sysadmin", Role: "admin"},
		},
	}

	repo := newMockNotificationRepo()
	n := NewNotifier(repo, nil, userRepo)

	err := n.NotifySystem(
		context.Background(),
		"cluster_warning",
		"Node Pressure",
		"Memory pressure on node-3",
		"cluster",
		"cluster-7",
	)

	require.NoError(t, err)

	created := repo.Created()
	require.Len(t, created, 1)

	notif := created[0]
	assert.Equal(t, "admin-42", notif.UserID)
	assert.Equal(t, "cluster_warning", notif.Type)
	assert.Equal(t, "Node Pressure", notif.Title)
	assert.Equal(t, "Memory pressure on node-3", notif.Message)
	assert.Equal(t, "cluster", notif.EntityType)
	assert.Equal(t, "cluster-7", notif.EntityID)
	assert.False(t, notif.IsRead)
	assert.NotEmpty(t, notif.ID)
}

func TestNotifySystem_WithHubBroadcasts(t *testing.T) {
	t.Parallel()

	hub := websocket.NewHub()
	go hub.Run()
	defer hub.Shutdown()

	userRepo := &mockUserRepository{
		users: []models.User{
			{ID: "admin-ws", Username: "wsadmin", Role: "admin"},
		},
	}

	repo := newMockNotificationRepo()
	n := NewNotifier(repo, hub, userRepo)

	err := n.NotifySystem(
		context.Background(),
		"deploy",
		"Deploy Started",
		"Deploying to prod",
		"instance",
		"inst-1",
	)

	assert.NoError(t, err)
	created := repo.Created()
	require.Len(t, created, 1)
	assert.Equal(t, "admin-ws", created[0].UserID)
}

// ---- failOnceMockNotificationRepo ----

// failOnceMockNotificationRepo fails on the Nth Create call and succeeds on others.
type failOnceMockNotificationRepo struct {
	mu          sync.Mutex
	created     []*models.Notification
	callCount   int
	failOnCallN int
	failErr     error
}

func (m *failOnceMockNotificationRepo) Create(_ context.Context, notification *models.Notification) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.callCount++
	if m.callCount == m.failOnCallN {
		return m.failErr
	}
	cp := *notification
	m.created = append(m.created, &cp)
	return nil
}

func (m *failOnceMockNotificationRepo) ListByUser(_ context.Context, _ string, _ bool, _, _ int) ([]models.Notification, int64, error) {
	return nil, 0, nil
}
func (m *failOnceMockNotificationRepo) CountUnread(_ context.Context, _ string) (int64, error) {
	return 0, nil
}
func (m *failOnceMockNotificationRepo) MarkAsRead(_ context.Context, _, _ string) error { return nil }
func (m *failOnceMockNotificationRepo) MarkAllAsRead(_ context.Context, _ string) error { return nil }
func (m *failOnceMockNotificationRepo) GetPreferences(_ context.Context, _ string) ([]models.NotificationPreference, error) {
	return nil, nil
}
func (m *failOnceMockNotificationRepo) UpdatePreference(_ context.Context, _ *models.NotificationPreference) error {
	return nil
}
