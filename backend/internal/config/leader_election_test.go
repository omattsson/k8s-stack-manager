package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadLeaderElectionConfig(t *testing.T) {
	// Not parallel: subtests use t.Setenv and change serviceAccountNamespaceFile.

	dir := t.TempDir()
	nsFile := filepath.Join(dir, "namespace")
	require.NoError(t, os.WriteFile(nsFile, []byte("from-file\n"), 0o600))
	orig := serviceAccountNamespaceFile
	t.Cleanup(func() { serviceAccountNamespaceFile = orig })

	host, err := os.Hostname()
	require.NoError(t, err)

	tests := []struct {
		env           map[string]string
		name          string
		nsFile        string
		wantNamespace string
		wantIdentity  string
		wantLease     string
		wantDuration  time.Duration
		wantEnabled   bool
	}{
		{
			name:          "defaults",
			env:           map[string]string{},
			nsFile:        filepath.Join(dir, "missing"),
			wantIdentity:  host,
			wantLease:     "k8s-stack-manager-workers",
			wantDuration:  15 * time.Second,
			wantNamespace: "",
		},
		{
			name:          "namespace from the service account file",
			env:           map[string]string{},
			nsFile:        nsFile,
			wantIdentity:  host,
			wantLease:     "k8s-stack-manager-workers",
			wantDuration:  15 * time.Second,
			wantNamespace: "from-file",
		},
		{
			name: "POD_NAMESPACE wins over the file, POD_NAME is the identity",
			env: map[string]string{
				"LEADER_ELECTION_ENABLED": "true",
				"POD_NAMESPACE":           "pod-ns",
				"POD_NAME":                "backend-abc",
			},
			nsFile:        nsFile,
			wantEnabled:   true,
			wantIdentity:  "backend-abc",
			wantLease:     "k8s-stack-manager-workers",
			wantDuration:  15 * time.Second,
			wantNamespace: "pod-ns",
		},
		{
			name: "LEADER_ELECTION_NAMESPACE wins over POD_NAMESPACE",
			env: map[string]string{
				"LEADER_ELECTION_NAMESPACE":      "explicit",
				"POD_NAMESPACE":                  "pod-ns",
				"LEADER_ELECTION_LEASE_NAME":     "custom-workers",
				"LEADER_ELECTION_LEASE_DURATION": "30s",
			},
			nsFile:        nsFile,
			wantIdentity:  host,
			wantLease:     "custom-workers",
			wantDuration:  30 * time.Second,
			wantNamespace: "explicit",
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			for _, k := range []string{
				"LEADER_ELECTION_ENABLED", "LEADER_ELECTION_NAMESPACE", "POD_NAMESPACE", "POD_NAME",
				"LEADER_ELECTION_LEASE_NAME", "LEADER_ELECTION_LEASE_DURATION",
				"LEADER_ELECTION_RENEW_DEADLINE", "LEADER_ELECTION_RETRY_PERIOD",
			} {
				t.Setenv(k, tt.env[k])
			}
			serviceAccountNamespaceFile = tt.nsFile

			c := loadLeaderElectionConfig()
			assert.Equal(t, tt.wantEnabled, c.Enabled)
			assert.Equal(t, tt.wantNamespace, c.Namespace)
			assert.Equal(t, tt.wantIdentity, c.Identity)
			assert.Equal(t, tt.wantLease, c.LeaseName)
			assert.Equal(t, tt.wantDuration, c.LeaseDuration)
			assert.Equal(t, 10*time.Second, c.RenewDeadline)
			assert.Equal(t, 2*time.Second, c.RetryPeriod)
		})
	}
}

func TestLeaderElectionConfigValidate(t *testing.T) {
	t.Parallel()

	valid := LeaderElectionConfig{
		Enabled: true, LeaseName: "workers", Namespace: "ns", Identity: "pod-a",
		LeaseDuration: 15 * time.Second, RenewDeadline: 10 * time.Second, RetryPeriod: 2 * time.Second,
	}
	tests := []struct {
		mutate  func(c *LeaderElectionConfig)
		name    string
		wantErr string
	}{
		{name: "valid", mutate: func(*LeaderElectionConfig) {}},
		{name: "disabled is not checked", mutate: func(c *LeaderElectionConfig) { *c = LeaderElectionConfig{} }},
		{name: "no lease name", mutate: func(c *LeaderElectionConfig) { c.LeaseName = "" }, wantErr: "LEADER_ELECTION_LEASE_NAME"},
		{name: "no namespace", mutate: func(c *LeaderElectionConfig) { c.Namespace = "" }, wantErr: "POD_NAMESPACE"},
		{name: "no identity", mutate: func(c *LeaderElectionConfig) { c.Identity = "" }, wantErr: "POD_NAME"},
		{name: "lease below 1s", mutate: func(c *LeaderElectionConfig) { c.LeaseDuration = 500 * time.Millisecond }, wantErr: "at least 1s"},
		{name: "zero retry", mutate: func(c *LeaderElectionConfig) { c.RetryPeriod = 0 }, wantErr: "greater than zero"},
		{name: "lease not above renew", mutate: func(c *LeaderElectionConfig) { c.LeaseDuration = c.RenewDeadline }, wantErr: "greater than LEADER_ELECTION_RENEW_DEADLINE"},
		{name: "renew not above 1.2 x retry", mutate: func(c *LeaderElectionConfig) { c.RetryPeriod = 9 * time.Second }, wantErr: "1.2 x"},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			c := valid
			tt.mutate(&c)
			err := c.Validate()
			if tt.wantErr == "" {
				assert.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}
