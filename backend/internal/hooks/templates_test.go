package hooks

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTemplateResolver_Resolve(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	r := NewTemplateResolver(func(ctx context.Context, ids []string) (map[string]TemplateRef, error) {
		calls.Add(1)
		assert.Equal(t, []string{"def-a"}, ids)
		return map[string]TemplateRef{"def-a": {ID: "tpl-a", Name: "A", Category: "team-a"}}, nil
	})
	t.Cleanup(r.Stop)
	ctx := context.Background()

	in := &InstanceRef{ID: "i1", StackDefinitionID: "def-a"}
	out := r.resolve(ctx, in)
	assert.True(t, out.templateResolved)
	assert.Equal(t, "tpl-a", out.TemplateID)
	assert.False(t, in.templateResolved, "the caller's ref is not changed")

	assert.Same(t, out, r.resolve(ctx, out), "a resolved ref is not looked up again")
	r.resolve(ctx, &InstanceRef{ID: "i2", StackDefinitionID: "def-a"})
	assert.Equal(t, int32(1), calls.Load(), "the second instance of the definition uses the cache")
}

func TestTemplateResolver_NoTemplateIsCached(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	r := NewTemplateResolver(func(context.Context, []string) (map[string]TemplateRef, error) {
		calls.Add(1)
		return map[string]TemplateRef{}, nil
	})
	t.Cleanup(r.Stop)

	for range 2 {
		out := r.resolve(context.Background(), &InstanceRef{ID: "i1", StackDefinitionID: "def-x"})
		assert.True(t, out.templateResolved)
		assert.Empty(t, out.TemplateID)
	}
	assert.Equal(t, int32(1), calls.Load())
}

func TestTemplateResolver_Edges(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	var nilResolver *TemplateResolver
	ref := &InstanceRef{ID: "i1", StackDefinitionID: "def-a"}
	assert.Same(t, ref, nilResolver.resolve(ctx, ref))
	nilResolver.Stop()
	assert.Nil(t, NewTemplateResolver(nil))

	var calls atomic.Int32
	r := NewTemplateResolver(func(context.Context, []string) (map[string]TemplateRef, error) {
		calls.Add(1)
		return nil, errors.New("db down")
	})
	t.Cleanup(r.Stop)
	assert.Nil(t, r.resolve(ctx, nil))

	noDef := r.resolve(ctx, &InstanceRef{ID: "i1"})
	assert.True(t, noDef.templateResolved, "an instance without a definition has no template")
	assert.Zero(t, calls.Load())

	failed := r.resolve(ctx, ref)
	assert.Same(t, ref, failed)
	assert.False(t, failed.templateResolved)

	// A caller-set template field is not trusted: the lookup decides.
	forged := &InstanceRef{ID: "i1", TemplateID: "tpl-a"}
	assert.Empty(t, r.resolve(ctx, forged).TemplateID)

	var nilDispatcher *Dispatcher
	assert.Same(t, ref, nilDispatcher.ResolveInstance(ctx, ref))
}

func TestTemplateResolver_Timeout(t *testing.T) {
	t.Parallel()
	release := make(chan struct{})
	answered := make(chan struct{})
	r := NewTemplateResolver(func(context.Context, []string) (map[string]TemplateRef, error) {
		<-release
		defer close(answered)
		return map[string]TemplateRef{"def-a": {ID: "tpl-a"}}, nil
	})
	r.timeout = 20 * time.Millisecond
	t.Cleanup(r.Stop)

	ref := &InstanceRef{ID: "i1", StackDefinitionID: "def-a"}
	out := r.resolve(context.Background(), ref)
	assert.False(t, out.templateResolved, "a timed-out lookup leaves the scope unknown")

	close(release)
	<-answered
	require.Eventually(t, func() bool {
		_, ok := r.cache.Get("def-a")
		return ok
	}, time.Second, 10*time.Millisecond, "the late answer fills the cache")
	assert.Equal(t, "tpl-a", r.resolve(context.Background(), ref).TemplateID)
}

// TestDispatcher_LookupsRunConcurrently checks that the cluster name and the
// template lookups run at the same time: each lookup waits until the other
// one has started, so lookups that run one after the other would time out
// and leave their fields out of the envelope.
func TestDispatcher_LookupsRunConcurrently(t *testing.T) {
	t.Parallel()
	srv, last := captureServer(t)
	d, err := NewDispatcher(Config{Subscriptions: []Subscription{{
		Name: "s", Events: []string{EventPreDeploy}, URL: srv.URL,
	}}}, srv.Client())
	require.NoError(t, err)

	clusterStarted, templateStarted := make(chan struct{}), make(chan struct{})
	waitFor := func(ch chan struct{}) {
		select {
		case <-ch:
		case <-time.After(5 * time.Second):
		}
	}
	clusters := NewClusterNameResolver(func(context.Context, []string) (map[string]string, error) {
		close(clusterStarted)
		waitFor(templateStarted)
		return map[string]string{"c1": "Production"}, nil
	})
	t.Cleanup(clusters.Stop)
	templates := NewTemplateResolver(func(context.Context, []string) (map[string]TemplateRef, error) {
		close(templateStarted)
		waitFor(clusterStarted)
		return map[string]TemplateRef{"def-a": {ID: "tpl-a", Category: "team-a"}}, nil
	})
	t.Cleanup(templates.Stop)
	d.WithClusterNames(clusters).WithTemplates(templates)

	require.NoError(t, d.Fire(context.Background(), EventPreDeploy, EventEnvelope{
		InstanceRef: &InstanceRef{ID: "i1", ClusterID: "c1", StackDefinitionID: "def-a"},
	}))
	inst, _ := last()["instance"].(map[string]any)
	assert.Equal(t, "Production", inst["cluster_name"])
	assert.Equal(t, "tpl-a", inst["template_id"])
}

func TestWithTemplateOf(t *testing.T) {
	t.Parallel()
	dst := &InstanceRef{ID: "i1", ClusterName: "Production"}
	resolved := &InstanceRef{ID: "i1", TemplateID: "tpl-a", templateResolved: true}

	got := withTemplateOf(dst, resolved)
	assert.Equal(t, "Production", got.ClusterName)
	assert.Equal(t, "tpl-a", got.TemplateID)
	assert.True(t, got.templateResolved)
	assert.Empty(t, dst.TemplateID, "dst is not changed")

	assert.Same(t, dst, withTemplateOf(dst, &InstanceRef{ID: "i1"}), "an unresolved src changes nothing")
	assert.Same(t, resolved, withTemplateOf(resolved, resolved))
	assert.Nil(t, withTemplateOf(nil, resolved))
}
