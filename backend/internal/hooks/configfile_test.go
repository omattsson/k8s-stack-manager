package hooks

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadConfigFile_EmptyPathReturnsEmpty(t *testing.T) {
	t.Parallel()
	cfg, actions, err := LoadConfigFile("")
	require.NoError(t, err)
	assert.Empty(t, cfg.Subscriptions)
	assert.Empty(t, actions)
}

func TestLoadConfigFile_ParsesSubscriptionsAndActions(t *testing.T) {
	// t.Setenv is incompatible with t.Parallel; keep serial.
	t.Setenv("CMDB_SECRET", "cmdb-topsecret")
	t.Setenv("REFRESH_DB_SECRET", "refresh-topsecret")

	dir := t.TempDir()
	path := filepath.Join(dir, "hooks.json")
	require.NoError(t, os.WriteFile(path, []byte(`{
  "subscriptions": [
    {
      "name": "cmdb-sync",
      "events": ["post-instance-create", "post-instance-delete"],
      "url": "https://cmdb.example/hooks",
      "timeout_seconds": 5,
      "failure_policy": "ignore",
      "secret_env": "CMDB_SECRET"
    }
  ],
  "actions": [
    {
      "name": "refresh-db",
      "url": "http://refresh-db.example:8080/",
      "description": "Wipe and re-extract golden DB",
      "timeout_seconds": 30,
      "secret_env": "REFRESH_DB_SECRET"
    }
  ]
}`), 0o600))

	cfg, actions, err := LoadConfigFile(path)
	require.NoError(t, err)

	require.Len(t, cfg.Subscriptions, 1)
	sub := cfg.Subscriptions[0]
	assert.Equal(t, "cmdb-sync", sub.Name)
	assert.Equal(t, []string{EventPostInstanceCreate, EventPostInstanceDelete}, sub.Events)
	assert.Equal(t, FailurePolicyIgnore, sub.FailurePolicy)
	assert.Equal(t, "cmdb-topsecret", sub.Secret, "secret resolved from env")

	require.Len(t, actions, 1)
	act := actions[0]
	assert.Equal(t, "refresh-db", act.Name)
	assert.Equal(t, "refresh-topsecret", act.Secret)

	// End-to-end: the resolved config must be accepted by NewDispatcher and NewActionRegistry.
	_, err = NewDispatcher(cfg, http.DefaultClient)
	require.NoError(t, err)
	_, err = NewActionRegistry(actions, http.DefaultClient)
	require.NoError(t, err)
}

func TestLoadConfigFile_MissingFileReturnsError(t *testing.T) {
	t.Parallel()
	_, _, err := LoadConfigFile("/nonexistent/path/hooks.json")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "read")
}

func TestLoadConfigFile_InvalidJSONReturnsError(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.json")
	require.NoError(t, os.WriteFile(path, []byte("not json"), 0o600))
	_, _, err := LoadConfigFile(path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parse")
}

func TestLoadConfigFile_EmptySecretEnvSkipsSecret(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "hooks.json")
	require.NoError(t, os.WriteFile(path, []byte(`{
  "subscriptions": [{"name":"no-sig","events":["post-deploy"],"url":"https://example.com/h"}]
}`), 0o600))
	cfg, _, err := LoadConfigFile(path)
	require.NoError(t, err)
	assert.Equal(t, "", cfg.Subscriptions[0].Secret)
}
