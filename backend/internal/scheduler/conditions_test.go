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

func TestStoppedDaysCondition(t *testing.T) {
	t.Parallel()

	now := time.Now()
	ago := func(d time.Duration) *time.Time { ts := now.Add(-d); return &ts }
	day := 24 * time.Hour

	tests := []struct {
		name      string
		condition string
		inst      models.StackInstance
		want      bool
	}{
		{"stopped long ago matches", "stopped_days:3",
			models.StackInstance{Status: models.StackStatusStopped, CreatedAt: now.Add(-10 * day), StoppedAt: ago(4 * day)}, true},
		{"old instance stopped recently does not match", "stopped_days:3",
			models.StackInstance{Status: models.StackStatusStopped, CreatedAt: now.Add(-10 * day), StoppedAt: ago(time.Minute)}, false},
		{"new instance stopped long ago matches", "stopped_days:1",
			models.StackInstance{Status: models.StackStatusStopped, CreatedAt: now.Add(-2 * day), StoppedAt: ago(36 * time.Hour)}, true},
		{"stopped without stop time never matches", "stopped_days:1",
			models.StackInstance{Status: models.StackStatusStopped, CreatedAt: now.Add(-10 * day)}, false},
		{"running instance with an old stop time does not match", "stopped_days:1",
			models.StackInstance{Status: models.StackStatusRunning, CreatedAt: now.Add(-10 * day), StoppedAt: ago(5 * day)}, false},
		{"stopped_days:0 matches any recorded stop", "stopped_days:0",
			models.StackInstance{Status: models.StackStatusStopped, StoppedAt: ago(time.Second)}, true},
		{"combined with age_days", "stopped_days:2,age_days:30",
			models.StackInstance{Status: models.StackStatusStopped, CreatedAt: now.Add(-5 * day), StoppedAt: ago(3 * day)}, false},
		{"legacy status:stopped,age_days still uses the creation age", "status:stopped,age_days:3",
			models.StackInstance{Status: models.StackStatusStopped, CreatedAt: now.Add(-7 * day), StoppedAt: ago(time.Minute)}, true},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f, err := ParseCondition(tt.condition)
			assert.NoError(t, err)
			inst := tt.inst
			assert.Equal(t, tt.want, f.MatchesInstance(&inst))
		})
	}
}

func TestStoppedDaysCondition_InvalidValues(t *testing.T) {
	t.Parallel()
	for _, cond := range []string{"stopped_days:x", "stopped_days:-1", "stopped_days"} {
		_, err := ParseCondition(cond)
		assert.Error(t, err, cond)
	}
}
