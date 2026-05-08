package database

import (
	"encoding/base64"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func rawEncode(s string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(s))
}

func TestAuditCursor_RoundTrip(t *testing.T) {
	t.Parallel()

	ts := time.Date(2026, 5, 8, 14, 30, 0, 123456789, time.UTC)
	id := "audit-entry-42"

	encoded := encodeAuditCursor(ts, id)
	require.NotEmpty(t, encoded)

	gotTS, gotID, err := decodeAuditCursor(encoded)
	require.NoError(t, err)
	assert.True(t, ts.Equal(gotTS), "timestamp mismatch: want %v, got %v", ts, gotTS)
	assert.Equal(t, id, gotID)
}

func TestAuditCursor_DecodeErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		cursor string
	}{
		{"invalid base64", "%%%not-valid%%%"},
		{"missing separator", rawEncode("nopipe")},
		{"empty timestamp", rawEncode("|some-id")},
		{"empty id", rawEncode("2026-05-08T14:30:00Z|")},
		{"bad timestamp format", rawEncode("not-a-time|some-id")},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, _, err := decodeAuditCursor(tt.cursor)
			assert.Error(t, err)
		})
	}
}

func TestDeployLogCursor_RoundTrip(t *testing.T) {
	t.Parallel()

	ts := time.Date(2026, 1, 15, 9, 0, 0, 999000000, time.UTC)
	id := "deploy-log-99"

	encoded := encodeDeployLogCursor(ts, id)
	require.NotEmpty(t, encoded)

	gotTS, gotID, err := decodeDeployLogCursor(encoded)
	require.NoError(t, err)
	assert.True(t, ts.Equal(gotTS))
	assert.Equal(t, id, gotID)
}

func TestDeployLogCursor_DecodeErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		cursor string
	}{
		{"invalid base64", "!!!invalid!!!"},
		{"missing separator", rawEncode("nopipe")},
		{"empty id", rawEncode("2026-05-08T14:30:00Z|")},
		{"bad timestamp", rawEncode("xyz|some-id")},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, _, err := decodeDeployLogCursor(tt.cursor)
			assert.Error(t, err)
		})
	}
}
