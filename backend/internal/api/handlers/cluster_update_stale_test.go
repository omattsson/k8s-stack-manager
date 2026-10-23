package handlers

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"backend/internal/models"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// beforeUpdateClusterRepo runs before once, right before the first Update,
// so a write of another actor lands between the handler read and its write.
type beforeUpdateClusterRepo struct {
	*MockClusterRepository
	before func()
}

func (r *beforeUpdateClusterRepo) Update(ctx context.Context, cl *models.Cluster) error {
	if r.before != nil {
		r.before()
		r.before = nil
	}
	return r.MockClusterRepository.Update(ctx, cl)
}

// TestUpdateCluster_KeepsDefaultAndHealthOfOtherWriters checks that a cluster
// update keeps a health status and a default change made after its read:
// Update does not write is_default or health_status (#678).
func TestUpdateCluster_KeepsDefaultAndHealthOfOtherWriters(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	mock := NewMockClusterRepository()
	require.NoError(t, mock.Create(ctx, &models.Cluster{
		ID: "cl-1", Name: "one", APIServerURL: "https://one.example.com", KubeconfigPath: "/kc/1",
		IsDefault: true, HealthStatus: models.ClusterHealthy,
	}))
	require.NoError(t, mock.Create(ctx, &models.Cluster{
		ID: "cl-2", Name: "two", APIServerURL: "https://two.example.com", KubeconfigPath: "/kc/2",
		HealthStatus: models.ClusterHealthy,
	}))
	repo := &beforeUpdateClusterRepo{MockClusterRepository: mock, before: func() {
		require.NoError(t, mock.UpdateHealthStatus(ctx, "cl-1", models.ClusterUnreachable))
		require.NoError(t, mock.SetDefault(ctx, "cl-2"))
	}}

	h := NewClusterHandler(repo, nil, NewMockStackInstanceRepository())
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(injectAuthContext("test-user-id", "admin"))
	r.PUT("/api/v1/clusters/:id", h.UpdateCluster)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/api/v1/clusters/cl-1", bytes.NewBufferString(`{"name":"renamed"}`))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	got, err := mock.FindByID(ctx, "cl-1")
	require.NoError(t, err)
	assert.Equal(t, "renamed", got.Name)
	assert.Equal(t, models.ClusterUnreachable, got.HealthStatus, "the poller status stays")
	assert.False(t, got.IsDefault, "the stale is_default is not written back")
	other, err := mock.FindByID(ctx, "cl-2")
	require.NoError(t, err)
	assert.True(t, other.IsDefault)
}
