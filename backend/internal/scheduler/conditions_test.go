package scheduler

import (
	"testing"
	"time"

	"backend/internal/models"

	"github.com/stretchr/testify/assert"
)

func TestParseCondition(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		condition string
		expected  *InstanceFilter
		wantErr   bool
	}{
		{
			name:      "idle_days only",
			condition: "idle_days:7",
			expected:  &InstanceFilter{IdleDays: 7},
		},
		{
			name:      "status only",
			condition: "status:stopped",
			expected:  &InstanceFilter{Status: "stopped"},
		},
		{
			name:      "age_days only",
			condition: "age_days:14",
			expected:  &InstanceFilter{AgeDays: 14},
		},
		{
			name:      "ttl_expired only",
			condition: "ttl_expired",
			expected:  &InstanceFilter{TTLExpired: true},
		},
		{
			name:      "combined status and age",
			condition: "status:stopped,age_days:14",
			expected:  &InstanceFilter{Status: "stopped", AgeDays: 14},
		},
		{
			name:      "all conditions",
			condition: "status:running,idle_days:3,age_days:30,ttl_expired",
			expected:  &InstanceFilter{Status: "running", IdleDays: 3, AgeDays: 30, TTLExpired: true},
		},
		{
			name:      "with spaces",
			condition: " idle_days : 7 , status : stopped ",
			expected:  &InstanceFilter{IdleDays: 7, Status: "stopped"},
		},
		{
			name:      "unknown key",
			condition: "unknown:value",
			wantErr:   true,
		},
		{
			name:      "invalid idle_days",
			condition: "idle_days:abc",
			wantErr:   true,
		},
		{
			name:      "invalid age_days",
			condition: "age_days:xyz",
			wantErr:   true,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result, err := ParseCondition(tt.condition)
			if tt.wantErr {
				assert.Error(t, err)
				return
			}
			assert.NoError(t, err)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestMatchesInstance(t *testing.T) {
	t.Parallel()

	now := time.Now()
	tenDaysAgo := now.Add(-10 * 24 * time.Hour)
	twoDaysAgo := now.Add(-2 * 24 * time.Hour)
	oneDayAgo := now.Add(-24 * time.Hour)
	oneHourAgo := now.Add(-time.Hour)

	tests := []struct {
		name     string
		filter   *InstanceFilter
		instance *models.StackInstance
		matches  bool
	}{
		{
			name:     "status match",
			filter:   &InstanceFilter{Status: "stopped"},
			instance: &models.StackInstance{Status: "stopped"},
			matches:  true,
		},
		{
			name:     "status no match",
			filter:   &InstanceFilter{Status: "stopped"},
			instance: &models.StackInstance{Status: "running"},
			matches:  false,
		},
		{
			name:     "idle_days match — last deployed long ago",
			filter:   &InstanceFilter{IdleDays: 7},
			instance: &models.StackInstance{LastDeployedAt: &tenDaysAgo},
			matches:  true,
		},
		{
			name:     "idle_days no match — recently deployed",
			filter:   &InstanceFilter{IdleDays: 7},
			instance: &models.StackInstance{LastDeployedAt: &twoDaysAgo},
			matches:  false,
		},
		{
			name:     "idle_days uses created_at when no deploy",
			filter:   &InstanceFilter{IdleDays: 7},
			instance: &models.StackInstance{CreatedAt: tenDaysAgo},
			matches:  true,
		},
		{
			name:     "age_days match",
			filter:   &InstanceFilter{AgeDays: 5},
			instance: &models.StackInstance{CreatedAt: tenDaysAgo},
			matches:  true,
		},
		{
			name:     "age_days no match",
			filter:   &InstanceFilter{AgeDays: 5},
			instance: &models.StackInstance{CreatedAt: twoDaysAgo},
			matches:  false,
		},
		{
			name:     "ttl_expired match",
			filter:   &InstanceFilter{TTLExpired: true},
			instance: &models.StackInstance{ExpiresAt: &oneDayAgo},
			matches:  true,
		},
		{
			name:   "ttl_expired no match — not expired yet",
			filter: &InstanceFilter{TTLExpired: true},
			instance: &models.StackInstance{
				ExpiresAt: func() *time.Time { t := now.Add(time.Hour); return &t }(),
			},
			matches: false,
		},
		{
			name:     "ttl_expired no match — no expiry set",
			filter:   &InstanceFilter{TTLExpired: true},
			instance: &models.StackInstance{ExpiresAt: nil},
			matches:  false,
		},
		{
			name:     "combined — all match",
			filter:   &InstanceFilter{Status: "stopped", AgeDays: 5},
			instance: &models.StackInstance{Status: "stopped", CreatedAt: tenDaysAgo},
			matches:  true,
		},
		{
			name:     "combined — status matches but age doesn't",
			filter:   &InstanceFilter{Status: "stopped", AgeDays: 5},
			instance: &models.StackInstance{Status: "stopped", CreatedAt: twoDaysAgo},
			matches:  false,
		},
		{
			name:   "empty filter matches everything",
			filter: &InstanceFilter{},
			instance: &models.StackInstance{
				Status:         "running",
				CreatedAt:      oneHourAgo,
				LastDeployedAt: &oneHourAgo,
			},
			matches: true,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.matches, tt.filter.MatchesInstance(tt.instance))
		})
	}
}
