package database

import (
	"context"
	"errors"
	"strings"
	"testing"

	"backend/internal/models"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A keyless update never writes a secret that has the encrypted prefix, even
// when the caller sends exactly the stored value.
func TestChannelRepository_KeylessUpdateWithPrefixedStoredSecret(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := channelDB(t)
	keyed := NewGORMNotificationChannelRepository(db, channelKey)
	require.NoError(t, keyed.CreateChannel(ctx, newChannel("a", "s3cret")))
	stored := storedChannelColumn(t, db, "ch-a", "secret")
	require.True(t, strings.HasPrefix(stored, "enc:v1:"))

	keyless := NewGORMNotificationChannelRepository(db, "")
	upd := newChannel("a", stored)
	err := keyless.UpdateChannel(ctx, upd, true)
	require.Error(t, err)
	assert.True(t, errors.Is(err, models.ErrChannelSecretNoKey))
	assert.Equal(t, stored, storedChannelColumn(t, db, "ch-a", "secret"))
}

// An update of a channel with an unreadable URL: a new URL repairs it with
// the key (encrypted) and without it (plaintext); an update without a new URL
// keeps the stored value.
func TestChannelRepository_UpdateUnreadableURL(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	tests := []struct {
		name        string
		repoKey     string
		newURL      string
		wantPrefix  bool
		wantUnchang bool
	}{
		{"new URL with a key is encrypted", channelKey, "https://new.example.com/x", true, false},
		{"new URL without a key is plaintext", "", "https://new.example.com/x", false, false},
		{"no new URL keeps the stored value", channelKey, "", false, true},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			db := channelDB(t)
			// Written under another key, so this repository cannot read it.
			require.NoError(t, NewGORMNotificationChannelRepository(db, channelOtherKey).CreateChannel(ctx, newChannel("a", "")))
			before := storedChannelColumn(t, db, "ch-a", "webhook_url")

			repo := NewGORMNotificationChannelRepository(db, tt.repoKey)
			got, err := repo.GetChannel(ctx, "ch-a")
			require.NoError(t, err)
			require.True(t, got.WebhookURLUnreadable)
			got.WebhookURL = tt.newURL
			require.NoError(t, repo.UpdateChannel(ctx, got, false))

			after := storedChannelColumn(t, db, "ch-a", "webhook_url")
			if tt.wantUnchang {
				assert.Equal(t, before, after)
				return
			}
			assert.Equal(t, tt.wantPrefix, strings.HasPrefix(after, "enc:v1:"))
			read, err := repo.GetChannel(ctx, "ch-a")
			require.NoError(t, err)
			assert.False(t, read.WebhookURLUnreadable)
			assert.Equal(t, tt.newURL, read.WebhookURL)
		})
	}
}

func TestChannelRepository_ListChannelsFlags(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := channelDB(t)
	keyed := NewGORMNotificationChannelRepository(db, channelKey)
	require.NoError(t, keyed.CreateChannel(ctx, newChannel("a-ok", "s3cret")))
	require.NoError(t, keyed.CreateChannel(ctx, newChannel("b-badsecret", "s3cret")))
	require.NoError(t, keyed.CreateChannel(ctx, newChannel("c-badurl", "")))
	setStored(t, db, "ch-b-badsecret", "secret", "enc:v1:%%%")
	setStored(t, db, "ch-c-badurl", "webhook_url", "enc:v1:%%%")

	list, err := keyed.ListChannels(ctx)
	require.NoError(t, err)
	require.Len(t, list, 3)
	assert.False(t, list[0].SecretUnreadable || list[0].WebhookURLUnreadable)
	assert.Equal(t, "s3cret", list[0].Secret)
	assert.True(t, list[1].SecretUnreadable)
	assert.Empty(t, list[1].Secret)
	assert.False(t, list[1].WebhookURLUnreadable)
	assert.True(t, list[2].WebhookURLUnreadable)
	assert.Empty(t, list[2].WebhookURL)

	enabled, err := keyed.ListEnabledChannels(ctx)
	require.NoError(t, err)
	require.Len(t, enabled, 3)
	assert.True(t, enabled[1].SecretUnreadable)
	assert.True(t, enabled[2].WebhookURLUnreadable)
}

// Not parallel: it replaces the default slog logger. A channel that breaks
// again after a repair is logged again, and no line holds a secret or URL.
func TestChannelRepository_LogOnceResetsAfterRepair(t *testing.T) {
	ctx := context.Background()
	db := channelDB(t)
	require.NoError(t, NewGORMNotificationChannelRepository(db, channelKey).CreateChannel(ctx, newChannel("a", "s3cret")))
	buf := captureSlog(t)
	repo := NewGORMNotificationChannelRepository(db, channelOtherKey)

	_, err := repo.GetChannel(ctx, "ch-a")
	require.NoError(t, err)
	require.NoError(t, repo.UpdateChannel(ctx, &models.NotificationChannel{
		ID: "ch-a", Name: "a", WebhookURL: "https://hooks.example.com/a?sig=tok", Secret: "newsecret", Enabled: true}, true))
	// Break it again: the stored value is under a key the repository lacks.
	setStored(t, db, "ch-a", "secret", "enc:v1:%%%")
	_, err = repo.GetChannel(ctx, "ch-a")
	require.NoError(t, err)

	assert.Equal(t, 2, strings.Count(buf.String(), "secret cannot be decrypted"))
	for _, leak := range []string{"s3cret", "newsecret", "sig=tok", "hooks.example.com"} {
		assert.NotContains(t, buf.String(), leak)
	}
}
