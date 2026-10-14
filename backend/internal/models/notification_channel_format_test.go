package models

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNormalizeNotificationChannelFormat(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		in      string
		want    string
		wantErr bool
	}{
		{name: "empty is generic", in: "", want: NotificationChannelFormatGeneric},
		{name: "spaces are generic", in: "  ", want: NotificationChannelFormatGeneric},
		{name: "generic", in: "generic", want: NotificationChannelFormatGeneric},
		{name: "teams", in: "teams", want: NotificationChannelFormatTeams},
		{name: "case and spaces", in: " TEAMS ", want: NotificationChannelFormatTeams},
		{name: "unknown", in: "slack", wantErr: true},
		{name: "custom is not yet supported", in: "custom", wantErr: true},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := NormalizeNotificationChannelFormat(tt.in)
			if tt.wantErr {
				require.Error(t, err)
				assert.Equal(t, "format must be one of: generic, teams", err.Error())
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}
