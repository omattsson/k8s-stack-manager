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
	plainSecrets, badSecrets, plainURLs, badURLs int
	err                                          error
}

func (r *statsChannelRepo) ChannelSecretStats(context.Context) (int, int, int, int, error) {
	return r.plainSecrets, r.badSecrets, r.plainURLs, r.badURLs, r.err
}

// Not parallel: it replaces the default slog logger.
func TestWarnChannelSecrets(t *testing.T) {
	tests := []struct {
		name    string
		repo    models.NotificationChannelRepository
		keySet  bool
		want    []string
		notWant []string
	}{
		{"plaintext secret without a key", &statsChannelRepo{plainSecrets: 2}, false,
			[]string{"level=WARN", "channels=2", "signing secret in plaintext", "Set KUBECONFIG_ENCRYPTION_KEY, then set the secret of these channels again."}, []string{"cannot decrypt"}},
		{"encrypted secret without a key", &statsChannelRepo{badSecrets: 1}, false,
			[]string{"level=WARN", "channels=1", "ignores these secrets", "until the same key is set or the secret is set again"}, nil},
		{"plaintext URL without a key", &statsChannelRepo{plainURLs: 3}, false,
			[]string{"level=WARN", "channels=3", "webhook URL in plaintext"}, nil},
		{"unreadable values with a key", &statsChannelRepo{badSecrets: 1, badURLs: 4}, true,
			[]string{"level=WARN", "cannot decrypt", "Set the secret of these channels again", "Set the URL again", "channels=4"}, nil},
		{"plaintext secret with a key", &statsChannelRepo{plainSecrets: 1}, true,
			[]string{"level=WARN", "Set the secret of these channels again"}, []string{"KUBECONFIG_ENCRYPTION_KEY"}},
		{"plaintext URL with a key is info", &statsChannelRepo{plainURLs: 1}, true,
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
