package main

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"backend/internal/health"
	"backend/internal/models"
	"backend/internal/sessionstore"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// adminCtxUserRepo has no admin user and records the contexts of the
// startup admin lookup and create.
type adminCtxUserRepo struct {
	stubUserRepo
	mu        sync.Mutex
	lookupCtx context.Context
	createCtx context.Context
}

func (r *adminCtxUserRepo) FindByUsername(ctx context.Context, _ string) (*models.User, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.lookupCtx = ctx
	return nil, errors.New("not found")
}

func (r *adminCtxUserRepo) Create(ctx context.Context, _ *models.User) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.createCtx = ctx
	return nil
}

// TestBuildHandlers_EnsureAdminUserStartupContext checks that the startup
// admin user check runs with a bounded context that ends when the check
// returns (#656).
func TestBuildHandlers_EnsureAdminUserStartupContext(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	cfg := buildTestConfig()
	repos := buildTestRepositorySet()
	users := &adminCtxUserRepo{}
	repos.User = users
	hub := buildTestHub(t)
	svc, err := buildDomainServices(cfg, repos, hub, health.New())
	require.NoError(t, err)
	sessStore := sessionstore.NewMemoryStore()
	t.Cleanup(func() { sessStore.Stop() })

	start := time.Now()
	_, err = buildHandlers(cfg, repos, svc, sessStore, hub)
	require.NoError(t, err)

	users.mu.Lock()
	defer users.mu.Unlock()
	require.NotNil(t, users.lookupCtx, "the admin lookup ran")
	require.NotNil(t, users.createCtx, "the admin user was created")
	assert.Same(t, users.lookupCtx, users.createCtx, "lookup and create share the startup context")
	deadline, ok := users.lookupCtx.Deadline()
	require.True(t, ok, "the startup context has a deadline")
	assert.WithinDuration(t, start.Add(30*time.Second), deadline, 5*time.Second)
	assert.ErrorIs(t, users.lookupCtx.Err(), context.Canceled, "the context is cancelled after the check")
}
