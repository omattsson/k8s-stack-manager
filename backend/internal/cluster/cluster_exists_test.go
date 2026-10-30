package cluster

import (
	"context"
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
	cfg := RegistryOptions{
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

func (r *transientErrorClusterRepo) Create(ctx context.Context, _ *models.Cluster) error {
	return ctx.Err()
}
func (r *transientErrorClusterRepo) FindByID(ctx context.Context, _ string) (*models.Cluster, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return nil, r.err
}
func (r *transientErrorClusterRepo) Update(ctx context.Context, _ *models.Cluster) error {
	return ctx.Err()
}
func (r *transientErrorClusterRepo) UpdateHealthStatus(ctx context.Context, _, _ string) error {
	return ctx.Err()
}
func (r *transientErrorClusterRepo) Delete(ctx context.Context, _ string) error { return ctx.Err() }
func (r *transientErrorClusterRepo) List(ctx context.Context) ([]models.Cluster, error) {
	return nil, ctx.Err()
}
func (r *transientErrorClusterRepo) FindDefault(ctx context.Context) (*models.Cluster, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return nil, r.err
}
func (r *transientErrorClusterRepo) SetDefault(ctx context.Context, _ string) error { return ctx.Err() }

// notFoundClusterRepo returns a dberrors-wrapped not-found error from FindByID.
type notFoundClusterRepo struct{}

func (r *notFoundClusterRepo) Create(ctx context.Context, _ *models.Cluster) error {
	return ctx.Err()
}
func (r *notFoundClusterRepo) FindByID(ctx context.Context, _ string) (*models.Cluster, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return nil, dberrors.NewDatabaseError("FindByID", dberrors.ErrNotFound)
}
func (r *notFoundClusterRepo) Update(ctx context.Context, _ *models.Cluster) error {
	return ctx.Err()
}
func (r *notFoundClusterRepo) UpdateHealthStatus(ctx context.Context, _, _ string) error {
	return ctx.Err()
}
func (r *notFoundClusterRepo) Delete(ctx context.Context, _ string) error { return ctx.Err() }
func (r *notFoundClusterRepo) List(ctx context.Context) ([]models.Cluster, error) {
	return nil, ctx.Err()
}
func (r *notFoundClusterRepo) FindDefault(ctx context.Context) (*models.Cluster, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return nil, dberrors.ErrNotFound
}
func (r *notFoundClusterRepo) SetDefault(ctx context.Context, _ string) error { return ctx.Err() }

func (*transientErrorClusterRepo) CountAll(ctx context.Context) (int, error) { return 0, ctx.Err() }

func (*transientErrorClusterRepo) StreamFlagsByIDs(ctx context.Context, _ []string, _ bool) (map[string]models.ClusterStreamFlags, error) {
	return nil, ctx.Err()
}

func (*transientErrorClusterRepo) NamesByIDs(ctx context.Context, _ []string) (map[string]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return map[string]string{}, nil
}
func (*transientErrorClusterRepo) CountByHealthStatus(ctx context.Context, status string) (int, error) {
	return 0, ctx.Err()
}

func (*notFoundClusterRepo) CountAll(ctx context.Context) (int, error) { return 0, ctx.Err() }

func (*notFoundClusterRepo) StreamFlagsByIDs(ctx context.Context, _ []string, _ bool) (map[string]models.ClusterStreamFlags, error) {
	return nil, ctx.Err()
}

func (*notFoundClusterRepo) NamesByIDs(ctx context.Context, _ []string) (map[string]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return map[string]string{}, nil
}
func (*notFoundClusterRepo) CountByHealthStatus(ctx context.Context, status string) (int, error) {
	return 0, ctx.Err()
}
