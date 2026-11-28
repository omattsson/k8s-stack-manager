package imagedigest

import (
	"context"
	"strings"
	"sync"
)

// PreviousDigests returns the digests that the last successful deploys of
// the instance rendered: "repository:tag" -> "sha256:...", newest deploy
// first for a key. It is called at most once per build, at the first failed
// lookup.
type PreviousDigests func(ctx context.Context) (map[string]string, error)

// maxRendered caps the references that a Build records for its deploy log.
const maxRendered = MaxLookups * maxKeysFactor

// Build resolves imageDigest for one build (one deploy, one preview, one
// instance of a bulk deploy). It implements helm.DigestResolver. It has its
// own lookup limits (MaxLookups requests, the reference limit, BuildBudget
// from its first request); the cache and the dead hosts of its resolver are
// shared with the other builds of the session.
//
// When a lookup fails (registry error, timeout, not found, or a lookup
// limit), it renders the digest that the last successful deploy of the
// instance rendered for the same reference, so the pod annotation does not
// change and the pods do not restart for a registry outage; "" when there is
// none. A cancelled build, an invalid reference and a host that is not
// allowed render "" (no fallback).
//
// Rendered returns the digests that the build rendered (resolved and
// fallback), for the deploy log: a second outage still finds them. Safe for
// concurrent use. A nil *Build renders "".
type Build struct {
	sess     *Session
	registry func(ctx context.Context) Registry
	previous PreviousDigests
	resolver *Resolver
	lim      *limits // the lookup limits of this build
	prev     map[string]string
	rendered map[string]string
	mu       sync.Mutex
	// resolverSet and prevLoaded record the lazy loads (the cluster row
	// and the deploy logs are read only when a build needs them).
	resolverSet    bool
	prevLoaded     bool
	fallbackWarned bool
	prevErrWarned  bool
}

// NewBuild returns a Build in the session. registry gives the cluster
// registry of the instance; it is called once, at the first imageDigest
// call. previous may be nil (no fallback). A nil session returns nil.
func (s *Session) NewBuild(registry func(ctx context.Context) Registry, previous PreviousDigests) *Build {
	if s == nil {
		return nil
	}
	return &Build{sess: s, registry: registry, previous: previous, lim: &limits{}, rendered: make(map[string]string)}
}

// Key is the reference key of the stored digests: "repository:tag" with the
// spaces around both parts removed.
func Key(repository, tag string) string {
	return strings.TrimSpace(repository) + ":" + strings.TrimSpace(tag)
}

// Digest implements helm.DigestResolver.
func (b *Build) Digest(ctx context.Context, repository, tag string) string {
	if b == nil {
		return ""
	}
	var res Result
	if r := b.resolverFor(ctx); r != nil {
		res = r.lookup(ctx, b.lim, repository, tag)
	}
	key := Key(repository, tag)
	switch res.Status {
	case StatusResolved:
		b.record(key, res.Digest)
		return res.Digest
	case StatusFailed:
		if ctx.Err() != nil {
			return ""
		}
		d := b.fallback(ctx, key)
		if d != "" {
			b.record(key, d)
		}
		return d
	default:
		return ""
	}
}

// Rendered returns a copy of the digests that the build rendered, or nil when
// it rendered none.
func (b *Build) Rendered() map[string]string {
	if b == nil {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.rendered) == 0 {
		return nil
	}
	out := make(map[string]string, len(b.rendered))
	for k, v := range b.rendered {
		out[k] = v
	}
	return out
}

func (b *Build) resolverFor(ctx context.Context) *Resolver {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.resolverSet {
		b.resolverSet = true
		var reg Registry
		if b.registry != nil {
			reg = b.registry(ctx)
		}
		b.resolver = b.sess.ForRegistry(reg)
	}
	return b.resolver
}

func (b *Build) record(key, digest string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, ok := b.rendered[key]; ok || len(b.rendered) < maxRendered {
		b.rendered[key] = digest
	}
}

// fallback returns the last deployed digest of key, or "". The deploy logs
// are read at the first call of the build only.
func (b *Build) fallback(ctx context.Context, key string) string {
	b.mu.Lock()
	defer b.mu.Unlock()
	svc := b.sess.svc
	if !b.prevLoaded && b.previous != nil {
		b.prevLoaded = true
		prev, err := b.previous(ctx)
		switch {
		case err != nil && ctx.Err() == nil:
			if !b.prevErrWarned {
				b.prevErrWarned = true
				svc.log().Warn("imageDigest: reading the last deployed digests failed, rendering empty digests for failed lookups",
					"error", err)
			}
		case err == nil:
			b.prev = prev
		}
	}
	d := b.prev[key]
	if d == "" {
		return ""
	}
	svc.tel.recordOutcome(ctx, outcomeFallback)
	if !b.fallbackWarned {
		b.fallbackWarned = true
		svc.log().Warn("imageDigest: digest lookup failed, using last deployed digest; further fallbacks in this deploy are not logged",
			"reference", cut(key))
	}
	return d
}
