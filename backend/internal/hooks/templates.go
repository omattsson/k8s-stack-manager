package hooks

import (
	"context"
	"sync"
	"time"

	"backend/internal/cache"
)

// templateRefCacheTTL is how long the resolver keeps the template of a
// definition. A changed template category or template link applies to the
// envelopes and the applies_to scopes after at most this time.
const templateRefCacheTTL = time.Minute

// templateRefLookupTimeout bounds the wait for a template lookup, as for
// cluster names. After the timeout the envelope has no template fields and
// a scope that needs the template is unknown (ErrScopeUnknown).
const templateRefLookupTimeout = 2 * time.Second

// templateRefQueryTimeout bounds the lookup query, which outlives the
// caller's wait (#656).
const templateRefQueryTimeout = 10 * time.Second

// TemplateRef is the template of a stack definition.
type TemplateRef struct {
	ID       string
	Name     string
	Category string
}

// TemplateRefLookup returns the template of each definition in
// definitionIDs that has an existing template, keyed by definition ID, with
// one query that runs with ctx (for example
// models.StackDefinitionRepository.TemplateRefsByIDs). A definition without
// a template is missing from the result.
type TemplateRefLookup func(ctx context.Context, definitionIDs []string) (map[string]TemplateRef, error)

// TemplateResolver gives the template of an instance for hook envelopes,
// action requests and applies_to scopes (#720). It keeps the templates in a
// short-lived cache and bounds the wait for a lookup. It is safe for
// concurrent use.
type TemplateResolver struct {
	lookup       TemplateRefLookup
	cache        *cache.TTLCache[TemplateRef]
	timeout      time.Duration
	queryTimeout time.Duration
	stopOnce     sync.Once
}

// NewTemplateResolver returns a resolver that uses lookup. Call Stop to end
// the cache cleanup goroutine. It returns nil when lookup is nil.
func NewTemplateResolver(lookup TemplateRefLookup) *TemplateResolver {
	if lookup == nil {
		return nil
	}
	return &TemplateResolver{
		lookup:       lookup,
		cache:        cache.New[TemplateRef](templateRefCacheTTL, templateRefCacheTTL),
		timeout:      templateRefLookupTimeout,
		queryTimeout: templateRefQueryTimeout,
	}
}

// Stop ends the cache cleanup goroutine. The resolver stays usable. A nil
// resolver or a second call does nothing.
func (r *TemplateResolver) Stop() {
	if r == nil {
		return
	}
	r.stopOnce.Do(r.cache.Stop)
}

// WithTemplates makes the dispatcher set the template fields of the
// instance in the envelope and decide the applies_to scopes that need the
// template. Without a resolver, such a scope is unknown. Call it before the
// first dispatch. It returns d for chaining.
func (d *Dispatcher) WithTemplates(r *TemplateResolver) *Dispatcher {
	if d != nil {
		d.templates = r
	}
	return d
}

// WithTemplates makes the registry set the template fields in action
// requests and decide the applies_to scopes of the actions, as the
// dispatcher does. Call it before the first invoke. It returns a for
// chaining.
func (a *ActionRegistry) WithTemplates(r *TemplateResolver) *ActionRegistry {
	if a != nil {
		a.templates = r
	}
	return a
}

// resolve returns ref with the template fields set and marked resolved. It
// returns a copy, so the caller's value does not change. It returns ref
// itself when ref is nil, already resolved, or the lookup did not answer
// (the scopes that need the template are then unknown). A nil resolver
// resolves nothing.
func (r *TemplateResolver) resolve(ctx context.Context, ref *InstanceRef) *InstanceRef {
	if r == nil || ref == nil || ref.templateResolved {
		return ref
	}
	cp := *ref
	cp.TemplateID, cp.TemplateName, cp.TemplateCategory = "", "", ""
	cp.templateResolved = true
	if ref.StackDefinitionID == "" {
		return &cp
	}
	refs, complete := batchLookup(ctx, r.cache, []string{ref.StackDefinitionID}, r.timeout, r.queryTimeout, "template", r.lookup)
	if !complete {
		return ref
	}
	t := refs[ref.StackDefinitionID]
	cp.TemplateID, cp.TemplateName, cp.TemplateCategory = t.ID, t.Name, t.Category
	return &cp
}

// startResolve starts resolve(ref) in the background and returns a function
// that waits for its result. A caller runs the cluster name lookup in the
// meantime, so a dispatch waits for one lookup timeout, not two. Without a
// lookup to do, the function returns ref at once.
func (r *TemplateResolver) startResolve(ctx context.Context, ref *InstanceRef) func() *InstanceRef {
	if r == nil || ref == nil || ref.templateResolved {
		return func() *InstanceRef { return ref }
	}
	done := make(chan *InstanceRef, 1)
	go func() { done <- r.resolve(ctx, ref) }()
	return func() *InstanceRef { return <-done }
}

// withTemplateOf returns dst with the template fields of src, as a copy,
// when src is resolved; else dst. dst and src are two copies of one
// instance: dst from the cluster name lookup, src from resolve.
func withTemplateOf(dst, src *InstanceRef) *InstanceRef {
	if dst == nil || src == nil || dst == src || !src.templateResolved {
		return dst
	}
	cp := *dst
	cp.TemplateID, cp.TemplateName, cp.TemplateCategory = src.TemplateID, src.TemplateName, src.TemplateCategory
	cp.templateResolved = true
	return &cp
}

// ResolveInstance returns ref with the template fields of the instance set,
// for a caller that makes several decisions for one operation (for example
// HasBlockingFor, BlockingTimeoutFor and FireBlocking for one deploy): an
// envelope with the returned ref is not looked up again, so all decisions
// agree. It returns ref unchanged for a nil dispatcher, a dispatcher without
// a template resolver, or a lookup that did not answer.
func (d *Dispatcher) ResolveInstance(ctx context.Context, ref *InstanceRef) *InstanceRef {
	if d == nil {
		return ref
	}
	return d.templates.resolve(ctx, ref)
}
