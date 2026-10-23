package cluster

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"backend/internal/models"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// loadCtxRepo records the context of each cluster-row read of the registry.
// A non-nil block makes FindDefault wait until block closes or ctx ends.
type loadCtxRepo struct {
	*mockClusterRepo
	mu               sync.Mutex
	ctxs             []context.Context
	errsAtCall       []error
	block            chan struct{}
	findDefaultCalls atomic.Int32
}

func (r *loadCtxRepo) record(ctx context.Context) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ctxs = append(r.ctxs, ctx)
	r.errsAtCall = append(r.errsAtCall, ctx.Err())
}

func (r *loadCtxRepo) recorded() ([]context.Context, []error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]context.Context(nil), r.ctxs...), append([]error(nil), r.errsAtCall...)
}

func (r *loadCtxRepo) FindByID(ctx context.Context, id string) (*models.Cluster, error) {
	r.record(ctx)
	return r.mockClusterRepo.FindByID(ctx, id)
}

func (r *loadCtxRepo) FindDefault(ctx context.Context) (*models.Cluster, error) {
	r.findDefaultCalls.Add(1)
	r.record(ctx)
	if r.block != nil {
		select {
		case <-r.block:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return r.mockClusterRepo.FindDefault(ctx)
}

func newLoadCtxRepo() *loadCtxRepo {
	repo := &loadCtxRepo{mockClusterRepo: newMockClusterRepo()}
	repo.clusters["c1"] = &models.Cluster{ID: "c1", Name: "dev", KubeconfigPath: "/fake/kubeconfig", IsDefault: true}
	return repo
}

// TestRegistry_LoadsUseRegistryContext checks that every cluster-row read of
// the registry runs on a registry context with the load timeout (#656): the
// cache and the default resolve serve every caller, so no caller context may
// end them, and a hung read cannot hold the registry lock forever.
func TestRegistry_LoadsUseRegistryContext(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		call func(r *Registry) error
	}{
		{name: "GetClients", call: func(r *Registry) error { _, err := r.GetClients("c1"); return err }},
		{name: "GetDefaultClients", call: func(r *Registry) error { _, err := r.GetDefaultClients(); return err }},
		{name: "ResolveClusterID", call: func(r *Registry) error { _, err := r.ResolveClusterID(""); return err }},
		{name: "GetK8sClient default", call: func(r *Registry) error { _, err := r.GetK8sClient(""); return err }},
		{name: "GetHelmExecutor", call: func(r *Registry) error { _, err := r.GetHelmExecutor("c1"); return err }},
		{name: "GetRegistryConfig default", call: func(r *Registry) error { _, err := r.GetRegistryConfig(""); return err }},
		{name: "GetRegistryConfig", call: func(r *Registry) error { _, err := r.GetRegistryConfig("c1"); return err }},
		{name: "ClusterExists", call: func(r *Registry) error {
			if !r.ClusterExists("c1") {
				return assert.AnError
			}
			return nil
		}},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			repo := newLoadCtxRepo()
			reg := newTestRegistry(repo)
			start := time.Now()

			require.NoError(t, tt.call(reg))

			ctxs, errsAtCall := repo.recorded()
			require.NotEmpty(t, ctxs, "the call read a cluster row")
			for i, ctx := range ctxs {
				deadline, ok := ctx.Deadline()
				require.True(t, ok, "the load context has a deadline")
				assert.WithinDuration(t, start.Add(registryLoadTimeout), deadline, 2*time.Second)
				assert.NoError(t, errsAtCall[i], "the load context was live during the read")
				assert.ErrorIs(t, ctx.Err(), context.Canceled, "the load context ends with the read")
			}
		})
	}
}

// TestRegistry_LoadTimeoutBoundsDefaultResolve checks that a hung default
// read ends at the load timeout: the resolve fails without caching "no
// default", the registry lock is free for the next caller, and a later
// resolve loads the default once and caches it.
func TestRegistry_LoadTimeoutBoundsDefaultResolve(t *testing.T) {
	t.Parallel()
	repo := newLoadCtxRepo()
	repo.block = make(chan struct{})
	reg := newTestRegistry(repo)
	reg.loadTimeout = 50 * time.Millisecond

	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = reg.ResolveClusterID("")
		}(i)
	}
	finished := make(chan struct{})
	go func() {
		wg.Wait()
		close(finished)
	}()
	select {
	case <-finished:
	case <-time.After(3 * time.Second):
		close(repo.block)
		t.Fatal("the load timeout did not end the default read or free the lock")
	}

	for _, err := range errs {
		require.Error(t, err)
		assert.ErrorIs(t, err, context.DeadlineExceeded)
		assert.NotErrorIs(t, err, ErrNoDefaultCluster, "a timeout is not cached as no default")
	}

	close(repo.block)
	id, err := reg.ResolveClusterID("")
	require.NoError(t, err)
	assert.Equal(t, "c1", id)
	calls := repo.findDefaultCalls.Load()
	id, err = reg.ResolveClusterID("")
	require.NoError(t, err)
	assert.Equal(t, "c1", id)
	assert.Equal(t, calls, repo.findDefaultCalls.Load(), "the resolved default is cached")
}
