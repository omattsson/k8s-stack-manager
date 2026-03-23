package cluster

import (
	"fmt"
	"testing"
	"time"

	"backend/internal/models"
	"backend/pkg/dberrors"

	"github.com/stretchr/testify/assert"
)

func TestClusterExists(t *testing.T) {
	t.Parallel()

	t.Run("existing cluster returns true", func(t *testing.T) {
		t.Parallel()
		repo := newMockClusterRepo()
		repo.clusters["c1"] = &models.Cluster{
			ID:             "c1",
			Name:           "Cluster 1",
			KubeconfigPath: "/fake/kubeconfig",
		}
		reg := newTestRegistry(repo)
		assert.True(t, reg.ClusterExists("c1"))
	})

	t.Run("nonexistent cluster with not-found error returns false", func(t *testing.T) {
		t.Parallel()
		reg := newTestRegistry(&notFoundClusterRepo{})
		assert.False(t, reg.ClusterExists("nonexistent"))
	})

	t.Run("transient error conservatively returns true", func(t *testing.T) {
		t.Parallel()
		reg := newTestRegistry(&transientErrorClusterRepo{err: fmt.Errorf("connection refused")})
		assert.True(t, reg.ClusterExists("maybe-exists"))
	})
}

func TestClusterExists_NotFoundError(t *testing.T) {
	t.Parallel()

	// Verify that a wrapped dberrors.ErrNotFound is properly detected as not-found.
	repo := &notFoundClusterRepo{}
	reg := newTestRegistry(repo)
	got := reg.ClusterExists("missing-cluster")
	assert.False(t, got)
}

func TestNewRegistry_Fields(t *testing.T) {
	t.Parallel()

	repo := newMockClusterRepo()
	cfg := RegistryConfig{
		ClusterRepo: repo,
		HelmBinary:  "/usr/local/bin/helm",
		HelmTimeout: 10 * time.Minute,
	}

	reg := NewRegistry(cfg)

	assert.NotNil(t, reg)
	assert.NotNil(t, reg.clients)
	assert.NotNil(t, reg.k8sFactory)
	assert.NotNil(t, reg.helmFactory)
	assert.Equal(t, "/usr/local/bin/helm", reg.helmBinary)
	assert.Equal(t, 10*time.Minute, reg.helmTimeout)
	assert.False(t, reg.defaultResolved)
	assert.Empty(t, reg.defaultID)
}

func TestIsNotFoundError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "direct ErrNotFound",
			err:  dberrors.ErrNotFound,
			want: true,
		},
		{
			name: "wrapped ErrNotFound",
			err:  fmt.Errorf("finding cluster: %w", dberrors.ErrNotFound),
			want: true,
		},
		{
			name: "DatabaseError wrapping ErrNotFound",
			err:  dberrors.NewDatabaseError("FindByID", dberrors.ErrNotFound),
			want: true,
		},
		{
			name: "unrelated error",
			err:  fmt.Errorf("connection refused"),
			want: false,
		},
		{
			name: "nil error",
			err:  nil,
			want: false,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := isNotFoundError(tt.err)
			assert.Equal(t, tt.want, got)
		})
	}
}

// transientErrorClusterRepo always returns a transient error from FindByID.
type transientErrorClusterRepo struct {
	err error
}

func (r *transientErrorClusterRepo) Create(_ *models.Cluster) error             { return nil }
func (r *transientErrorClusterRepo) FindByID(_ string) (*models.Cluster, error)  { return nil, r.err }
func (r *transientErrorClusterRepo) Update(_ *models.Cluster) error             { return nil }
func (r *transientErrorClusterRepo) Delete(_ string) error                       { return nil }
func (r *transientErrorClusterRepo) List() ([]models.Cluster, error)             { return nil, nil }
func (r *transientErrorClusterRepo) FindDefault() (*models.Cluster, error)       { return nil, r.err }
func (r *transientErrorClusterRepo) SetDefault(_ string) error                   { return nil }

// notFoundClusterRepo returns a dberrors-wrapped not-found error from FindByID.
type notFoundClusterRepo struct{}

func (r *notFoundClusterRepo) Create(_ *models.Cluster) error             { return nil }
func (r *notFoundClusterRepo) FindByID(_ string) (*models.Cluster, error) {
	return nil, dberrors.NewDatabaseError("FindByID", dberrors.ErrNotFound)
}
func (r *notFoundClusterRepo) Update(_ *models.Cluster) error           { return nil }
func (r *notFoundClusterRepo) Delete(_ string) error                     { return nil }
func (r *notFoundClusterRepo) List() ([]models.Cluster, error)           { return nil, nil }
func (r *notFoundClusterRepo) FindDefault() (*models.Cluster, error)     { return nil, dberrors.ErrNotFound }
func (r *notFoundClusterRepo) SetDefault(_ string) error                 { return nil }
