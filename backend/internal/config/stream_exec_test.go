package config

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var execEnvKeys = []string{
	"STACK_EXEC_ENABLED", "STACK_EXEC_ALLOWED_CONTAINERS", "STACK_EXEC_MAX_DURATION",
	"STACK_EXEC_MAX_COMMAND_LENGTH", "STACK_EXEC_MAX_PER_USER", "STACK_EXEC_ALLOW_SERVICE_ACCOUNT_TOKEN",
}

// No t.Parallel: the cases set environment variables (t.Setenv).
func TestLoadStreamConfigExec(t *testing.T) {
	tests := []struct {
		name    string
		env     map[string]string
		want    ExecConfig
		wantErr string
	}{
		{name: "defaults", want: ExecConfig{MaxDuration: 10 * time.Minute, MaxCommandLength: 4096, MaxPerUser: 3}},
		{
			name: "custom",
			env: map[string]string{
				"STACK_EXEC_ENABLED": "true", "STACK_EXEC_ALLOWED_CONTAINERS": " web, worker ,",
				"STACK_EXEC_MAX_DURATION": "1h", "STACK_EXEC_MAX_COMMAND_LENGTH": "100",
				"STACK_EXEC_MAX_PER_USER": "7", "STACK_EXEC_ALLOW_SERVICE_ACCOUNT_TOKEN": "true",
			},
			want: ExecConfig{
				Enabled: true, AllowedContainers: []string{"web", "worker"}, MaxDuration: time.Hour,
				MaxCommandLength: 100, MaxPerUser: 7, AllowServiceAccountToken: true,
			},
		},
		{name: "bad bool", env: map[string]string{"STACK_EXEC_ENABLED": "maybe"}, wantErr: "STACK_EXEC_ENABLED"},
		{name: "bad sa bool", env: map[string]string{"STACK_EXEC_ALLOW_SERVICE_ACCOUNT_TOKEN": "x"}, wantErr: "STACK_EXEC_ALLOW_SERVICE_ACCOUNT_TOKEN"},
		{name: "bad duration", env: map[string]string{"STACK_EXEC_MAX_DURATION": "soon"}, wantErr: "STACK_EXEC_MAX_DURATION"},
		{name: "zero duration", env: map[string]string{"STACK_EXEC_MAX_DURATION": "0s"}, wantErr: "STACK_EXEC_MAX_DURATION"},
		{name: "duration above 24h", env: map[string]string{"STACK_EXEC_MAX_DURATION": "25h"}, wantErr: "STACK_EXEC_MAX_DURATION"},
		{name: "length not a number", env: map[string]string{"STACK_EXEC_MAX_COMMAND_LENGTH": "x"}, wantErr: "STACK_EXEC_MAX_COMMAND_LENGTH"},
		{name: "length zero", env: map[string]string{"STACK_EXEC_MAX_COMMAND_LENGTH": "0"}, wantErr: "STACK_EXEC_MAX_COMMAND_LENGTH"},
		{name: "length too big", env: map[string]string{"STACK_EXEC_MAX_COMMAND_LENGTH": "32769"}, wantErr: "STACK_EXEC_MAX_COMMAND_LENGTH"},
		{name: "per user not a number", env: map[string]string{"STACK_EXEC_MAX_PER_USER": "x"}, wantErr: "STACK_EXEC_MAX_PER_USER"},
		{name: "per user zero", env: map[string]string{"STACK_EXEC_MAX_PER_USER": "0"}, wantErr: "STACK_EXEC_MAX_PER_USER"},
		{name: "per user above 100", env: map[string]string{"STACK_EXEC_MAX_PER_USER": "101"}, wantErr: "STACK_EXEC_MAX_PER_USER"},
		{name: "wildcard container", env: map[string]string{"STACK_EXEC_ALLOWED_CONTAINERS": "web,*"}, wantErr: "STACK_EXEC_ALLOWED_CONTAINERS"},
		{name: "upper case container", env: map[string]string{"STACK_EXEC_ALLOWED_CONTAINERS": "Web"}, wantErr: "STACK_EXEC_ALLOWED_CONTAINERS"},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			for _, k := range execEnvKeys {
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
			assert.Equal(t, tt.want, c.Exec)
		})
	}
}

func TestExecConfigContainerAllowed(t *testing.T) {
	t.Parallel()
	e := ExecConfig{AllowedContainers: []string{"web"}}
	assert.True(t, e.ContainerAllowed("web"))
	assert.False(t, e.ContainerAllowed("web2"))
	assert.False(t, ExecConfig{}.ContainerAllowed("web"))
}
