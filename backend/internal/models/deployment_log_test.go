package models

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIsDeployLogActionAndStatus(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		value      string
		wantAction bool
		wantStatus bool
	}{
		{name: "deploy", value: DeployActionDeploy, wantAction: true},
		{name: "rollback", value: DeployActionRollback, wantAction: true},
		{name: "stop", value: DeployActionStop, wantAction: true},
		{name: "clean", value: DeployActionClean, wantAction: true},
		{name: "running", value: DeployLogRunning, wantStatus: true},
		{name: "success", value: DeployLogSuccess, wantStatus: true},
		{name: "error", value: DeployLogError, wantStatus: true},
		{name: "partial", value: DeployLogPartial, wantStatus: true},
		{name: "empty", value: ""},
		{name: "upper case", value: "Deploy"},
		{name: "unknown", value: "upgrade"},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.wantAction, IsDeployLogAction(tt.value))
			assert.Equal(t, tt.wantStatus, IsDeployLogStatus(tt.value))
		})
	}
}

func TestDeploymentLogJSON(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		log     DeploymentLog
		present []string
		absent  []string
	}{
		{
			name:    "full log",
			log:     DeploymentLog{ID: "l1", Output: "out", ValuesSnapshot: "{}", ChartVersions: "{}", StartedBy: "alice", ReplicaID: "pod-1"},
			present: []string{"output", "values_snapshot", "chart_versions", "started_by"},
			absent:  []string{"replica_id", "ReplicaID", "deadline_at", "DeadlineAt"},
		},
		{
			name:   "summary row",
			log:    DeploymentLog{ID: "l1"},
			absent: []string{"output", "values_snapshot", "chart_versions", "started_by"},
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			data, err := json.Marshal(tt.log)
			require.NoError(t, err)
			var got map[string]interface{}
			require.NoError(t, json.Unmarshal(data, &got))
			for _, k := range tt.present {
				assert.Contains(t, got, k)
			}
			for _, k := range tt.absent {
				assert.NotContains(t, got, k)
			}
		})
	}
}
