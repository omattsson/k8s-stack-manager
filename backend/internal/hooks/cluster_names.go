package hooks

import (
	"context"
	"sync"
	"time"

	"backend/internal/cache"
)

// clusterNameCacheTTL is how long the resolver keeps a cluster name. A
// renamed cluster shows the new name in hook envelopes after at most this
// time.
const clusterNameCacheTTL = time.Minute

// clusterNameLookupTimeout bounds the wait for a cluster name lookup, so a
// slow database cannot delay a dispatch (for example a blocking pre-deploy
// gate). After the timeout the envelope has no cluster_name.
const clusterNameLookupTimeout = 2 * time.Second

// clusterNameQueryTimeout bounds the lookup query, which outlives the caller's wait (#656).
const clusterNameQueryTimeout = 10 * time.Second

// ClusterNameLookup returns the name of each cluster in ids that exists,
// keyed by cluster ID, with one query that runs with ctx (for example
// models.ClusterRepository.NamesByIDs, the lookup that the API uses for
// cluster_name).
type ClusterNameLookup func(ctx context.Context, ids []string) (map[string]string, error)

// ClusterNameResolver gives the cluster names for hook envelopes and action
// requests. It keeps the names in a short-lived cache and bounds the wait for
// a lookup. It is safe for concurrent use.
type ClusterNameResolver struct {
	lookup       ClusterNameLookup
	cache        *cache.TTLCache[string]
	timeout      time.Duration
	queryTimeout time.Duration
	stopOnce     sync.Once
}

// NewClusterNameResolver returns a resolver that uses lookup. Call Stop to
// end the cache cleanup goroutine. It returns nil when lookup is nil.
func NewClusterNameResolver(lookup ClusterNameLookup) *ClusterNameResolver {
	if lookup == nil {
		return nil
	}
	return &ClusterNameResolver{
		lookup:       lookup,
		cache:        cache.New[string](clusterNameCacheTTL, clusterNameCacheTTL),
		timeout:      clusterNameLookupTimeout,
		queryTimeout: clusterNameQueryTimeout,
	}
}

// Stop ends the cache cleanup goroutine. The resolver stays usable, so a
// late dispatch after Stop still works. A nil resolver or a second call does
// nothing.
func (r *ClusterNameResolver) Stop() {
	if r == nil {
		return
	}
	r.stopOnce.Do(r.cache.Stop)
}

// WithClusterNames makes the dispatcher set cluster_name next to cluster_id
// in the envelope (instance.cluster_name and cleanup_policy.cluster_name).
// An unknown cluster, a lookup error or a lookup timeout leaves cluster_name
// empty (omitted in the JSON); the dispatch continues. Call it before the
// first dispatch. It returns d for chaining.
func (d *Dispatcher) WithClusterNames(r *ClusterNameResolver) *Dispatcher {
	if d != nil {
		d.clusterNames = r
	}
	return d
}

// WithClusterNames makes the registry set instance.cluster_name in action
// requests, as the dispatcher does in envelopes. Call it before the first
// invoke. It returns r for chaining.
func (a *ActionRegistry) WithClusterNames(r *ClusterNameResolver) *ActionRegistry {
	if a != nil {
		a.clusterNames = r
	}
	return a
}

// names returns the name of each ID in ids ("" when unknown or when the
// lookup did not answer, see batchLookup).
func (r *ClusterNameResolver) names(ctx context.Context, ids []string) map[string]string {
	// Display data only: the dispatch goes on without the name.
	out, _ := batchLookup(ctx, r.cache, ids, r.timeout, r.queryTimeout, "cluster name", r.lookup)
	return out
}

// isClusterRef reports whether id refers to one cluster. A cleanup policy
// uses "all" for all clusters, which has no name.
func isClusterRef(id string) bool {
	return id != "" && id != "all"
}

// NameFor returns the name of the cluster id, with the cache and the wait
// limit of the resolver. It returns "" for a nil resolver, an empty id, "all",
// an unknown cluster, a lookup error or a timeout. The notifier uses it for
// cluster_name in the notification channel payload.
func (r *ClusterNameResolver) NameFor(ctx context.Context, id string) string {
	if r == nil || !isClusterRef(id) {
		return ""
	}
	return r.names(ctx, []string{id})[id]
}

// instanceRef returns ref with cluster_name set. It returns a copy, so the
// caller's value does not change; it returns ref itself when there is
// nothing to set. A cluster_name that the caller set is kept.
func (r *ClusterNameResolver) instanceRef(ctx context.Context, ref *InstanceRef) *InstanceRef {
	if r == nil || ref == nil || ref.ClusterName != "" || !isClusterRef(ref.ClusterID) {
		return ref
	}
	name := r.names(ctx, []string{ref.ClusterID})[ref.ClusterID]
	if name == "" {
		return ref
	}
	cp := *ref
	cp.ClusterName = name
	return &cp
}

// setClusterNames fills cluster_name in the instance and the cleanup policy
// of envelope, with one lookup for both. It copies the referenced structs
// first, so the caller's values do not change. A cluster_name that the
// caller set is kept.
func (r *ClusterNameResolver) setClusterNames(ctx context.Context, envelope EventEnvelope) EventEnvelope {
	if r == nil {
		return envelope
	}
	var ids []string
	if ref := envelope.InstanceRef; ref != nil && ref.ClusterName == "" && isClusterRef(ref.ClusterID) {
		ids = append(ids, ref.ClusterID)
	}
	if run := envelope.CleanupPolicy; run != nil && run.ClusterName == "" && isClusterRef(run.ClusterID) {
		ids = append(ids, run.ClusterID)
	}
	if len(ids) == 0 {
		return envelope
	}
	names := r.names(ctx, ids)
	if ref := envelope.InstanceRef; ref != nil && ref.ClusterName == "" {
		if name := names[ref.ClusterID]; name != "" {
			cp := *ref
			cp.ClusterName = name
			envelope.InstanceRef = &cp
		}
	}
	if run := envelope.CleanupPolicy; run != nil && run.ClusterName == "" {
		if name := names[run.ClusterID]; name != "" {
			cp := *run
			cp.ClusterName = name
			envelope.CleanupPolicy = &cp
		}
	}
	return envelope
}
