package config

import (
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var streamEnvKeys = []string{
	"STACK_PORT_FORWARD_ENABLED", "STACK_PORT_FORWARD_MAX_DURATION", "STACK_PORT_FORWARD_IDLE_TIMEOUT",
	"STACK_PORT_FORWARD_MAX_PER_USER", "STACK_STREAM_ALLOW_API_KEYS", "POD_NAMESPACE",
}

// No t.Parallel: the cases set environment variables (t.Setenv).
func TestLoadStreamConfig(t *testing.T) {
	tests := []struct {
		name    string
		env     map[string]string
		want    PortForwardConfig
		allow   bool
		wantErr string
	}{
		{name: "defaults", want: PortForwardConfig{MaxDuration: time.Hour, IdleTimeout: 15 * time.Minute, MaxPerUser: 5}},
		{
			name:  "custom",
			env:   map[string]string{"STACK_PORT_FORWARD_ENABLED": "true", "STACK_PORT_FORWARD_MAX_DURATION": "2h", "STACK_PORT_FORWARD_IDLE_TIMEOUT": "30m", "STACK_PORT_FORWARD_MAX_PER_USER": " 10 ", "STACK_STREAM_ALLOW_API_KEYS": "true"},
			want:  PortForwardConfig{Enabled: true, MaxDuration: 2 * time.Hour, IdleTimeout: 30 * time.Minute, MaxPerUser: 10},
			allow: true,
		},
		{name: "bad bool", env: map[string]string{"STACK_PORT_FORWARD_ENABLED": "maybe"}, wantErr: "STACK_PORT_FORWARD_ENABLED"},
		{name: "bad allow api keys", env: map[string]string{"STACK_STREAM_ALLOW_API_KEYS": "x"}, wantErr: "STACK_STREAM_ALLOW_API_KEYS"},
		{name: "bad duration", env: map[string]string{"STACK_PORT_FORWARD_MAX_DURATION": "soon"}, wantErr: "STACK_PORT_FORWARD_MAX_DURATION"},
		{name: "zero duration", env: map[string]string{"STACK_PORT_FORWARD_MAX_DURATION": "0s"}, wantErr: "MAX_DURATION"},
		{name: "duration above 24h", env: map[string]string{"STACK_PORT_FORWARD_MAX_DURATION": "25h"}, wantErr: "MAX_DURATION"},
		{name: "negative idle", env: map[string]string{"STACK_PORT_FORWARD_IDLE_TIMEOUT": "-1m"}, wantErr: "IDLE_TIMEOUT"},
		{name: "idle above max", env: map[string]string{"STACK_PORT_FORWARD_MAX_DURATION": "10m"}, wantErr: "not be longer"},
		{name: "per user not a number", env: map[string]string{"STACK_PORT_FORWARD_MAX_PER_USER": "many"}, wantErr: "MAX_PER_USER"},
		{name: "per user zero", env: map[string]string{"STACK_PORT_FORWARD_MAX_PER_USER": "0"}, wantErr: "MAX_PER_USER"},
		{name: "per user above 100", env: map[string]string{"STACK_PORT_FORWARD_MAX_PER_USER": "101"}, wantErr: "MAX_PER_USER"},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			for _, k := range streamEnvKeys {
				t.Setenv(k, "")
			}
			for k, v := range tt.env {
				t.Setenv(k, v)
			}
			c := loadStreamConfig()
			err := c.Validate()
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, c.PortForward)
			assert.Equal(t, tt.allow, c.AllowAPIKeys)
		})
	}
}

func TestStreamConfigValidateZeroValue(t *testing.T) {
	t.Parallel()
	c := StreamConfig{}
	assert.NoError(t, c.Validate())
}

// No t.Parallel: sets environment variables and a package variable.
func TestLoadStreamConfigBackendNamespace(t *testing.T) {
	orig := serviceAccountNamespaceFile
	t.Cleanup(func() { serviceAccountNamespaceFile = orig })

	tests := []struct {
		name string
		env  string
		file string
		want string
	}{
		{name: "env wins", env: "from-env", file: "from-file", want: "from-env"},
		{name: "file fallback", file: "from-file\n", want: "from-file"},
		{name: "none", want: ""},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("POD_NAMESPACE", tt.env)
			serviceAccountNamespaceFile = "/nonexistent/ns"
			if tt.file != "" {
				f := t.TempDir() + "/ns"
				require.NoError(t, writeFile(f, tt.file))
				serviceAccountNamespaceFile = f
			}
			assert.Equal(t, tt.want, loadStreamConfig().BackendNamespace)
		})
	}
}

func writeFile(path, content string) error { return os.WriteFile(path, []byte(content), 0o600) }
