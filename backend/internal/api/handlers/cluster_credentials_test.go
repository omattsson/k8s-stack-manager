package handlers

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"backend/internal/models"
	"backend/pkg/dberrors"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// keylessClusterRepo refuses a registry password like a repository without
// KUBECONFIG_ENCRYPTION_KEY.
type keylessClusterRepo struct {
	*MockClusterRepository
	setDefaultCalls int
}

func (r *keylessClusterRepo) CheckCredentialsStorable(_ context.Context, c *models.Cluster) error {
	if c.RegistryPassword != "" {
		return dberrors.NewDatabaseError("validation",
			errors.Join(dberrors.ErrValidation, errors.New("registry_password cannot be stored without KUBECONFIG_ENCRYPTION_KEY configured")))
	}
	return nil
}

func (r *keylessClusterRepo) SetDefault(ctx context.Context, id string) error {
	r.setDefaultCalls++
	return r.MockClusterRepository.SetDefault(ctx, id)
}

func TestUpdateCluster_RefusesCredentialsBeforeSetDefault(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		body       string
		wantStatus int
		wantSetDef int
	}{
		{"password and is_default is refused before SetDefault", `{"is_default":true,"registry_url":"r.example.com","registry_username":"u","registry_password":"pw"}`, http.StatusBadRequest, 0},
		{"is_default alone still works", `{"is_default":true}`, http.StatusOK, 1},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			repo := &keylessClusterRepo{MockClusterRepository: NewMockClusterRepository()}
			seedCluster(repo.MockClusterRepository, "c1", "one")
			router := setupClusterRouter2(repo)

			req := httptest.NewRequest(http.MethodPut, "/api/v1/clusters/c1", bytes.NewBufferString(tt.body))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)

			assert.Equal(t, tt.wantStatus, w.Code, w.Body.String())
			assert.Equal(t, tt.wantSetDef, repo.setDefaultCalls)
			stored, err := repo.FindByID(context.Background(), "c1")
			require.NoError(t, err)
			assert.Equal(t, tt.wantSetDef == 1, stored.IsDefault)
		})
	}
}

// unreadableClusterRepo returns a cluster whose stored password cannot be read.
type unreadableClusterRepo struct{ *MockClusterRepository }

func (r *unreadableClusterRepo) FindByID(ctx context.Context, id string) (*models.Cluster, error) {
	c, err := r.MockClusterRepository.FindByID(ctx, id)
	if err != nil {
		return nil, err
	}
	cp := *c
	cp.RegistryURL, cp.RegistryUsername, cp.RegistryPassword = "r.example.com", "u", ""
	cp.RegistryPasswordUnreadable = true
	return &cp, nil
}

func TestUpdateCluster_UnreadableRegistryPassword(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		body       string
		wantStatus int
	}{
		{"other edit gets a clear 400", `{"region":"west"}`, http.StatusBadRequest},
		{"new password repairs the row", `{"registry_password":"new-pw"}`, http.StatusOK},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			repo := &unreadableClusterRepo{NewMockClusterRepository()}
			seedCluster(repo.MockClusterRepository, "c1", "one")
			router := setupClusterRouter2(repo)

			req := httptest.NewRequest(http.MethodPut, "/api/v1/clusters/c1", bytes.NewBufferString(tt.body))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)

			assert.Equal(t, tt.wantStatus, w.Code, w.Body.String())
			if tt.wantStatus == http.StatusBadRequest {
				assert.Contains(t, w.Body.String(), "Set registry_password again")
			}
		})
	}
}

func setupClusterRouter2(repo models.ClusterRepository) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(injectAuthContext("test-user-id", "admin"))
	h := NewClusterHandler(repo, nil, NewMockStackInstanceRepository())
	r.PUT("/api/v1/clusters/:id", h.UpdateCluster)
	return r
}
