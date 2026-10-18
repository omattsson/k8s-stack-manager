package models

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNotificationChannelFilters_Matches(t *testing.T) {
	t.Parallel()

	target := NotificationTarget{
		InstanceID: "i1", InstanceName: "rdbtest-se", OwnerID: "u1", DefinitionID: "d1", ClusterID: "c1",
	}
	tests := []struct {
		name    string
		filters NotificationChannelFilters
		target  *NotificationTarget // nil: the default target
		want    bool
	}{
		{name: "empty filters match all", want: true},
		{name: "glob prefix", filters: NotificationChannelFilters{InstanceNamePatterns: []string{"rdbtest-*"}}, want: true},
		{name: "glob suffix", filters: NotificationChannelFilters{InstanceNamePatterns: []string{"*-se"}}, want: true},
		{name: "glob single character", filters: NotificationChannelFilters{InstanceNamePatterns: []string{"rdbtest-?e"}}, want: true},
		{name: "glob character class", filters: NotificationChannelFilters{InstanceNamePatterns: []string{"rdbtest-[a-z][a-z]"}}, want: true},
		{name: "glob no match", filters: NotificationChannelFilters{InstanceNamePatterns: []string{"other-*"}}, want: false},
		{name: "exact name", filters: NotificationChannelFilters{InstanceNamePatterns: []string{"rdbtest-se"}}, want: true},
		{name: "glob ignores case of the name",
			filters: NotificationChannelFilters{InstanceNamePatterns: []string{"rdbtest-*"}},
			target:  &NotificationTarget{InstanceName: "RdbTest-DK"}, want: true},
		{name: "one of several patterns (OR)", filters: NotificationChannelFilters{InstanceNamePatterns: []string{"x-*", "*-se"}}, want: true},
		{name: "owner match", filters: NotificationChannelFilters{OwnerIDs: []string{"u2", "u1"}}, want: true},
		{name: "owner no match", filters: NotificationChannelFilters{OwnerIDs: []string{"u2"}}, want: false},
		{name: "definition match", filters: NotificationChannelFilters{DefinitionIDs: []string{"d1"}}, want: true},
		{name: "definition no match", filters: NotificationChannelFilters{DefinitionIDs: []string{"d2"}}, want: false},
		{name: "cluster match", filters: NotificationChannelFilters{ClusterIDs: []string{"c1"}}, want: true},
		{name: "cluster no match", filters: NotificationChannelFilters{ClusterIDs: []string{"c2"}}, want: false},
		{name: "empty cluster of an older instance does not match a cluster filter",
			filters: NotificationChannelFilters{ClusterIDs: []string{"c1"}},
			target:  &NotificationTarget{InstanceName: "old"}, want: false},
		{name: "all filters match (AND)", filters: NotificationChannelFilters{
			InstanceNamePatterns: []string{"rdbtest-*"}, OwnerIDs: []string{"u1"}, DefinitionIDs: []string{"d1"}, ClusterIDs: []string{"c1"},
		}, want: true},
		{name: "one filter fails (AND)", filters: NotificationChannelFilters{
			InstanceNamePatterns: []string{"rdbtest-*"}, OwnerIDs: []string{"u1"}, ClusterIDs: []string{"c2"},
		}, want: false},
		{name: "name and owner, name fails", filters: NotificationChannelFilters{
			InstanceNamePatterns: []string{"prod-*"}, OwnerIDs: []string{"u1"},
		}, want: false},
		{name: "event without instance name does not match a name filter",
			filters: NotificationChannelFilters{InstanceNamePatterns: []string{"*"}},
			target:  &NotificationTarget{OwnerID: "u1"}, want: false},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			tgt := target
			if tt.target != nil {
				tgt = *tt.target
			}
			assert.Equal(t, tt.want, tt.filters.Matches(tgt))
		})
	}
}

func TestNotificationChannelFilters_MatchesAny(t *testing.T) {
	t.Parallel()

	a := NotificationTarget{InstanceName: "team-a-1", OwnerID: "u1"}
	b := NotificationTarget{InstanceName: "team-b-1", OwnerID: "u2"}
	nameFilter := NotificationChannelFilters{InstanceNamePatterns: []string{"team-a-*"}}

	tests := []struct {
		name    string
		filters NotificationChannelFilters
		targets []NotificationTarget
		want    bool
	}{
		{name: "no filters, event without instance", want: true},
		{name: "no filters, event with instance", targets: []NotificationTarget{b}, want: true},
		{name: "filters, event without instance (quota warning)", filters: nameFilter, want: false},
		{name: "filters, single instance matches", filters: nameFilter, targets: []NotificationTarget{a}, want: true},
		{name: "filters, single instance does not match", filters: nameFilter, targets: []NotificationTarget{b}, want: false},
		{name: "policy run, one of the affected instances matches", filters: nameFilter, targets: []NotificationTarget{b, a}, want: true},
		{name: "policy run, no affected instance matches", filters: nameFilter, targets: []NotificationTarget{b, b}, want: false},
		{name: "policy run without affected instances", filters: NotificationChannelFilters{OwnerIDs: []string{"u1"}}, targets: []NotificationTarget{}, want: false},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, tt.filters.MatchesAny(tt.targets))
		})
	}
}

func TestNotificationChannelFilters_Normalize(t *testing.T) {
	t.Parallel()

	many := make([]string, MaxChannelFilterValues+1)
	for i := range many {
		many[i] = fmt.Sprintf("id-%d", i)
	}
	tests := []struct {
		name    string
		in      NotificationChannelFilters
		want    NotificationChannelFilters
		wantErr string
	}{
		{name: "empty stays empty"},
		{name: "trim, lowercase patterns, dedupe, drop empty",
			in:   NotificationChannelFilters{InstanceNamePatterns: []string{" Team-* ", "team-*", ""}, OwnerIDs: []string{"u1", " u1", ""}},
			want: NotificationChannelFilters{InstanceNamePatterns: []string{"team-*"}, OwnerIDs: []string{"u1"}}},
		{name: "only empty values give nil", in: NotificationChannelFilters{ClusterIDs: []string{" ", ""}}},
		{name: "bad pattern", in: NotificationChannelFilters{InstanceNamePatterns: []string{"team-["}}, wantErr: "not a valid pattern"},
		{name: "bad pattern after a match part", in: NotificationChannelFilters{InstanceNamePatterns: []string{"a[\\"}}, wantErr: "not a valid pattern"},
		{name: "pattern too long", in: NotificationChannelFilters{InstanceNamePatterns: []string{strings.Repeat("a", MaxChannelFilterPatternLength+1)}}, wantErr: "longer than"},
		{name: "too many values", in: NotificationChannelFilters{DefinitionIDs: many}, wantErr: "at most"},
		{name: "ID too long", in: NotificationChannelFilters{ClusterIDs: []string{strings.Repeat("c", 37)}}, wantErr: "cluster_ids"},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := tt.in
			err := f.Normalize()
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, f)
		})
	}
}

func TestNotificationChannelFilters_ValueScan(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		filters NotificationChannelFilters
		wantNil bool
	}{
		{name: "empty filters are stored as NULL", wantNil: true},
		{name: "filters round trip", filters: NotificationChannelFilters{
			InstanceNamePatterns: []string{"a-*"}, OwnerIDs: []string{"u1"}, DefinitionIDs: []string{"d1"}, ClusterIDs: []string{"c1"},
		}},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			v, err := tt.filters.Value()
			require.NoError(t, err)
			if tt.wantNil {
				assert.Nil(t, v)
			}
			var got NotificationChannelFilters
			require.NoError(t, got.Scan(v))
			assert.Equal(t, tt.filters, got)
			if s, ok := v.(string); ok {
				var fromBytes NotificationChannelFilters
				require.NoError(t, fromBytes.Scan([]byte(s)))
				assert.Equal(t, tt.filters, fromBytes)
			}
		})
	}

	t.Run("empty string and bad type", func(t *testing.T) {
		t.Parallel()
		var f NotificationChannelFilters
		require.NoError(t, f.Scan(""))
		assert.True(t, f.IsEmpty())
		assert.Error(t, f.Scan(42))
	})
}

func TestNewNotificationTarget(t *testing.T) {
	t.Parallel()
	assert.Equal(t, NotificationTarget{}, NewNotificationTarget(nil))
	got := NewNotificationTarget(&StackInstance{ID: "i1", Name: "n", OwnerID: "u1", StackDefinitionID: "d1", ClusterID: "c1"})
	assert.Equal(t, NotificationTarget{InstanceID: "i1", InstanceName: "n", OwnerID: "u1", DefinitionID: "d1", ClusterID: "c1"}, got)
	assert.Nil(t, got.FollowerIDs, "nil FollowerIDs: the notifier reads the followers")
}
