package main

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"testing"

	"backend/internal/models"

	"github.com/stretchr/testify/assert"
)

type statsChannelRepo struct {
	models.NotificationChannelRepository
	stats models.ChannelSecretStats
	err   error
}

func (r *statsChannelRepo) ChannelSecretStats(context.Context) (models.ChannelSecretStats, error) {
	return r.stats, r.err
}

// Not parallel: it replaces the slog default.
func TestWarnChannelSecrets(t *testing.T) {
	tests := []struct {
		name    string
		repo    models.NotificationChannelRepository
		keySet  bool
		want    []string
		notWant []string
	}{
		{"plaintext secret without a key", &statsChannelRepo{stats: models.ChannelSecretStats{PlaintextSecrets: 2}}, false,
			[]string{"level=WARN", "channels=2", "signing secret in plaintext", "Set KUBECONFIG_ENCRYPTION_KEY, then set the secret of these channels again."}, []string{"cannot decrypt"}},
		{"encrypted secret without a key", &statsChannelRepo{stats: models.ChannelSecretStats{UnreadableSecrets: 1}}, false,
			[]string{"level=WARN", "channels=1", "ignores these secrets", "until the same key is set again, or the secret is removed"}, nil},
		{"plaintext URL without a key", &statsChannelRepo{stats: models.ChannelSecretStats{PlaintextURLs: 3}}, false,
			[]string{"level=WARN", "channels=3", "webhook URL in plaintext"}, nil},
		{"unreadable values with a key", &statsChannelRepo{stats: models.ChannelSecretStats{UnreadableSecrets: 1, UnreadableURLs: 4}}, true,
			[]string{"level=WARN", "cannot decrypt", "Set the secret of these channels again", "Set the URL again", "channels=4"}, nil},
		{"plaintext secret with a key", &statsChannelRepo{stats: models.ChannelSecretStats{PlaintextSecrets: 1}}, true,
			[]string{"level=WARN", "The next save of each channel encrypts it"}, []string{"KUBECONFIG_ENCRYPTION_KEY", "Set the secret"}},
		{"maybe ciphertext without a key", &statsChannelRepo{stats: models.ChannelSecretStats{MaybeCipherSecrets: 2}}, false,
			[]string{"level=WARN", "channels=2", "may be ciphertext of an older version", "otherwise ignore this"}, []string{"in plaintext"}},
		{"plaintext URL with a key is info", &statsChannelRepo{stats: models.ChannelSecretStats{PlaintextURLs: 1}}, true,
			[]string{"level=INFO", "Any save of the channel encrypts it"}, []string{"level=WARN"}},
		{"zero is silent", &statsChannelRepo{}, false, nil, []string{"plaintext", "decrypt"}},
		{"error warns without count", &statsChannelRepo{err: errors.New("db down")}, false, []string{"Could not check"}, nil},
		{"repository without stats is silent", nil, false, nil, []string{"plaintext"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			old := slog.Default()
			slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
			defer slog.SetDefault(old)

			warnChannelSecrets(tt.repo, tt.keySet)

			for _, w := range tt.want {
				assert.Contains(t, buf.String(), w)
			}
			for _, w := range tt.notWant {
				assert.NotContains(t, buf.String(), w)
			}
		})
	}
}
