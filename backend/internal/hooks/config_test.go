package hooks

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestConfigValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		cfg       Config
		expectErr string
		expectFP  FailurePolicy
		expectTO  int
	}{
		{
			name: "valid minimal config defaults failure_policy and timeout",
			cfg: Config{Subscriptions: []Subscription{{
				Name:   "ok",
				Events: []string{EventPreDeploy},
				URL:    "https://example.com/hook",
			}}},
			expectFP: FailurePolicyIgnore,
			expectTO: 5,
		},
		{
			name: "valid full config preserves explicit values",
			cfg: Config{Subscriptions: []Subscription{{
				Name:           "full",
				Events:         []string{EventPreDeploy, EventPostDeploy},
				URL:            "https://example.com/hook",
				FailurePolicy:  FailurePolicyFail,
				TimeoutSeconds: 10,
			}}},
			expectFP: FailurePolicyFail,
			expectTO: 10,
		},
		{
			name:      "missing name",
			cfg:       Config{Subscriptions: []Subscription{{Events: []string{EventPreDeploy}, URL: "https://example.com"}}},
			expectErr: "name is required",
		},
		{
			name: "duplicate name",
			cfg: Config{Subscriptions: []Subscription{
				{Name: "dup", Events: []string{EventPreDeploy}, URL: "https://a.example/h"},
				{Name: "dup", Events: []string{EventPostDeploy}, URL: "https://b.example/h"},
			}},
			expectErr: "duplicate name",
		},
		{
			name: "no events",
			cfg: Config{Subscriptions: []Subscription{{
				Name: "noevents", Events: []string{}, URL: "https://example.com",
			}}},
			expectErr: "at least one event",
		},
		{
			name: "unknown event",
			cfg: Config{Subscriptions: []Subscription{{
				Name: "bad", Events: []string{"not-an-event"}, URL: "https://example.com",
			}}},
			expectErr: "unknown event",
		},
		{
			name: "invalid url scheme",
			cfg: Config{Subscriptions: []Subscription{{
				Name: "bad", Events: []string{EventPreDeploy}, URL: "ftp://example.com",
			}}},
			expectErr: "invalid url",
		},
		{
			name: "missing url",
			cfg: Config{Subscriptions: []Subscription{{
				Name: "bad", Events: []string{EventPreDeploy},
			}}},
			expectErr: "url is required",
		},
		{
			name: "invalid failure_policy",
			cfg: Config{Subscriptions: []Subscription{{
				Name: "bad", Events: []string{EventPreDeploy}, URL: "https://example.com",
				FailurePolicy: FailurePolicy("warn"),
			}}},
			expectErr: "failure_policy",
		},
		{
			name: "negative timeout",
			cfg: Config{Subscriptions: []Subscription{{
				Name: "bad", Events: []string{EventPreDeploy}, URL: "https://example.com",
				TimeoutSeconds: -1,
			}}},
			expectErr: "timeout_seconds must be >= 0",
		},
		{
			name: "timeout above ceiling",
			cfg: Config{Subscriptions: []Subscription{{
				Name: "bad", Events: []string{EventPreDeploy}, URL: "https://example.com",
				TimeoutSeconds: 601,
			}}},
			expectErr: "timeout_seconds must be <= 600",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := tt.cfg.Validate()
			if tt.expectErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.expectErr)
				return
			}
			require.NoError(t, err)
			s := tt.cfg.Subscriptions[0]
			assert.Equal(t, tt.expectFP, s.FailurePolicy)
			assert.Equal(t, tt.expectTO, s.TimeoutSeconds)
		})
	}
}
