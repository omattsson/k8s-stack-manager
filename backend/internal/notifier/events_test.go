package notifier

import (
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var updateFixture = flag.Bool("update", false, "rewrite the frontend event type fixture")

// eventTypesFixture is the shape of the frontend fixture file.
type eventTypesFixture struct {
	Instance []string `json:"instance"`
	System   []string `json:"system"`
}

// fixturePath is the fixture that the frontend Profile page test reads.
var fixturePath = filepath.Join("..", "..", "..", "frontend", "src", "utils", "__tests__", "notification-event-types.json")

// TestPreferenceEventTypesFixture checks that the frontend fixture has the
// backend event types. The frontend test checks that the Profile page lists
// the same types, so both lists stay equal (issue #498).
func TestPreferenceEventTypesFixture(t *testing.T) {
	t.Parallel()
	want := eventTypesFixture{Instance: InstanceEventTypes(), System: SystemEventTypes()}

	if *updateFixture {
		data, err := json.MarshalIndent(want, "", "  ")
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(fixturePath, append(data, '\n'), 0o600))
		return
	}

	data, err := os.ReadFile(fixturePath)
	if os.IsNotExist(err) {
		t.Skip("frontend fixture not present (backend-only checkout)")
	}
	require.NoError(t, err)
	var got eventTypesFixture
	require.NoError(t, json.Unmarshal(data, &got))
	assert.Equal(t, want, got, "fixture out of date: run go test ./internal/notifier -run TestPreferenceEventTypesFixture -update")
}

func TestIsPreferenceEventType(t *testing.T) {
	t.Parallel()

	tests := []struct {
		eventType string
		want      bool
	}{
		{"deployment.success", true},
		{"cleanup.policy.stop", true},
		{"quota.warning", true},
		{"bogus.event", false},
		{"", false},
		{"Deployment.Success", false},
		// Channel-only types are not preference types.
		{"stack.expired", false},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.eventType, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, IsPreferenceEventType(tt.eventType))
		})
	}
}

func TestPreferenceEventTypes_NoDuplicates(t *testing.T) {
	t.Parallel()
	seen := map[string]bool{}
	for _, et := range PreferenceEventTypes() {
		assert.False(t, seen[et], "duplicate event type %s", et)
		seen[et] = true
	}
}
