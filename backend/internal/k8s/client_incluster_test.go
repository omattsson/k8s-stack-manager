package k8s

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testKubeconfig is a valid kubeconfig that points at a closed local port.
const testKubeconfig = `apiVersion: v1
kind: Config
clusters:
- name: test
  cluster:
    server: https://127.0.0.1:1
contexts:
- name: test
  context:
    cluster: test
    user: test
current-context: test
users:
- name: test
  user:
    token: test-token
`

// writeHomeKubeconfig writes testKubeconfig to <dir>/.kube/config and
// returns its path.
func writeHomeKubeconfig(t *testing.T, dir string) string {
	t.Helper()
	path := filepath.Join(dir, ".kube", "config")
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	require.NoError(t, os.WriteFile(path, []byte(testKubeconfig), 0o600))
	return path
}

// Outside a pod, NewClient("") must fail and must not read ~/.kube/config or
// $KUBECONFIG (#582). Not parallel: it sets environment variables.
func TestNewClient_EmptyPathOutsidePodNeverReadsKubeconfig(t *testing.T) {
	home := t.TempDir()
	path := writeHomeKubeconfig(t, home)
	t.Setenv("HOME", home)
	t.Setenv("KUBECONFIG", path)
	// rest.InClusterConfig needs both variables; empty means "not in a pod".
	t.Setenv("KUBERNETES_SERVICE_HOST", "")
	t.Setenv("KUBERNETES_SERVICE_PORT", "")

	client, err := NewClient("")
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInClusterUnavailable)
	assert.Nil(t, client)
	assert.False(t, InClusterAvailable())
}

func TestNewClient_ExplicitPathUsesThatFile(t *testing.T) {
	t.Parallel()

	path := writeHomeKubeconfig(t, t.TempDir())

	client, err := NewClient(path)
	require.NoError(t, err)
	require.NotNil(t, client.RESTConfig())
	assert.Equal(t, "https://127.0.0.1:1", client.RESTConfig().Host)
}
