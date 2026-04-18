package k8s

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"k8s.io/client-go/kubernetes/fake"
)

func TestExecInDeploymentPod_RequiresRESTConfig(t *testing.T) {
	t.Parallel()

	// NewClientFromInterface leaves restConfig nil, so exec must refuse
	// rather than attempt a real SPDY dial with a zero-value config.
	cs := fake.NewSimpleClientset()
	c := NewClientFromInterface(cs)

	_, err := c.ExecInDeploymentPod(context.Background(), "ns", "kvk-redis", "", []string{"redis-cli", "FLUSHALL"})
	assert.Error(t, err)
	assert.True(t, errors.Is(err, ErrRESTConfigUnavailable))
}
