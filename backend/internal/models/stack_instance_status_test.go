package models

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestIsValidStackStatus(t *testing.T) {
	t.Parallel()

	tests := []struct {
		status string
		want   bool
	}{
		{StackStatusDraft, true},
		{StackStatusQueued, true},
		{StackStatusDeploying, true},
		{StackStatusStabilizing, true},
		{StackStatusRunning, true},
		{StackStatusStopping, true},
		{StackStatusStopped, true},
		{StackStatusCleaning, true},
		{StackStatusPartial, true},
		{StackStatusError, true},
		{"", false},
		{"Running", false},
		{"bogus", false},
	}
	for _, tt := range tests {
		tt := tt
		t.Run("status_"+tt.status, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, IsValidStackStatus(tt.status))
		})
	}
}
