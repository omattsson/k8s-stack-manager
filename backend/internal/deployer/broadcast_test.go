package deployer

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestManager_BroadcastInstanceCreated(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		instanceID string
		wantSent   bool
	}{
		{name: "sends instance.created with the ID", instanceID: "inst-42", wantSent: true},
		{name: "an empty ID sends nothing", instanceID: "", wantSent: false},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			hub := &mockBroadcaster{}
			mgr := NewManager(ManagerConfig{Hub: hub, MaxConcurrent: 1})

			mgr.BroadcastInstanceCreated(tt.instanceID)

			msgs := hub.getMessages()
			if !tt.wantSent {
				assert.Empty(t, msgs)
				return
			}
			require.Len(t, msgs, 1)
			var envelope struct {
				Type    string `json:"type"`
				Payload struct {
					InstanceID string `json:"instance_id"`
				} `json:"payload"`
			}
			require.NoError(t, json.Unmarshal(msgs[0], &envelope))
			assert.Equal(t, "instance.created", envelope.Type)
			assert.Equal(t, tt.instanceID, envelope.Payload.InstanceID)
		})
	}
}

func TestManager_BroadcastInstanceCreated_NilSafe(t *testing.T) {
	t.Parallel()

	var nilMgr *Manager
	assert.NotPanics(t, func() { nilMgr.BroadcastInstanceCreated("inst-1") })

	noHub := NewManager(ManagerConfig{MaxConcurrent: 1})
	assert.NotPanics(t, func() { noHub.BroadcastInstanceCreated("inst-1") })
}

func TestManager_BroadcastInstanceDeleted(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		instanceID string
		wantSent   bool
	}{
		{name: "sends instance.deleted with the ID and no log ID", instanceID: "inst-7", wantSent: true},
		{name: "an empty ID sends nothing", instanceID: "", wantSent: false},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			hub := &mockBroadcaster{}
			mgr := NewManager(ManagerConfig{Hub: hub, MaxConcurrent: 1})

			mgr.BroadcastInstanceDeleted(tt.instanceID)

			msgs := hub.getMessages()
			if !tt.wantSent {
				assert.Empty(t, msgs)
				return
			}
			require.Len(t, msgs, 1)
			var envelope struct {
				Type    string         `json:"type"`
				Payload map[string]any `json:"payload"`
			}
			require.NoError(t, json.Unmarshal(msgs[0], &envelope))
			assert.Equal(t, "instance.deleted", envelope.Type)
			assert.Equal(t, tt.instanceID, envelope.Payload["instance_id"])
			_, hasLogID := envelope.Payload["log_id"]
			assert.False(t, hasLogID, "a direct delete has no clean log")
		})
	}

	var nilMgr *Manager
	assert.NotPanics(t, func() { nilMgr.BroadcastInstanceDeleted("inst-1") })
}
