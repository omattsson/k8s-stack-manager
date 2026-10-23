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

type countingChannelRepo struct {
	models.NotificationChannelRepository
	cleartext, encrypted int
	err                  error
}

func (r *countingChannelRepo) CountSecretsWithoutKey(context.Context) (int, int, error) {
	return r.cleartext, r.encrypted, r.err
}

// Not parallel: it replaces the default slog logger.
func TestWarnPlaintextChannelSecrets(t *testing.T) {
	tests := []struct {
		name    string
		repo    models.NotificationChannelRepository
		want    []string
		notWant []string
	}{
		{"cleartext count warns and says to set the secret again", &countingChannelRepo{cleartext: 2},
			[]string{"level=WARN", "channels=2", "plaintext", "set the secret of these channels again"}, []string{"encrypted secret"}},
		{"encrypted count warns about the missing key", &countingChannelRepo{encrypted: 1},
			[]string{"level=WARN", "channels=1", "encrypted secret"}, []string{"in plaintext"}},
		{"zero is silent", &countingChannelRepo{}, nil, []string{"plaintext", "encrypted secret"}},
		{"error warns without count", &countingChannelRepo{err: errors.New("db down")}, []string{"Could not check"}, nil},
		{"repository without counter is silent", nil, nil, []string{"plaintext", "encrypted secret"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			old := slog.Default()
			slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
			defer slog.SetDefault(old)

			warnPlaintextChannelSecrets(tt.repo)

			for _, w := range tt.want {
				assert.Contains(t, buf.String(), w)
			}
			for _, w := range tt.notWant {
				assert.NotContains(t, buf.String(), w)
			}
		})
	}
}
