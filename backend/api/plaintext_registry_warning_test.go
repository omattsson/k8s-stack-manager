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

type countingClusterRepo struct {
	*mockClusterRepo
	cleartext, encrypted int
	err                  error
}

func (r *countingClusterRepo) CountRegistryPasswordsWithoutKey(context.Context) (int, int, error) {
	return r.cleartext, r.encrypted, r.err
}

// Not parallel: it replaces the default slog logger.
func TestWarnPlaintextRegistryPasswords(t *testing.T) {
	tests := []struct {
		name    string
		repo    models.ClusterRepository
		want    []string
		notWant []string
	}{
		{"cleartext count warns and says to set the password again", &countingClusterRepo{mockClusterRepo: newMockClusterRepo(), cleartext: 2},
			[]string{"level=WARN", "clusters=2", "plaintext", "set the registry password of these clusters again"}, []string{"encrypted registry password"}},
		{"encrypted count warns about the missing key", &countingClusterRepo{mockClusterRepo: newMockClusterRepo(), encrypted: 1},
			[]string{"level=WARN", "clusters=1", "encrypted registry password"}, []string{"in plaintext"}},
		{"zero is silent", &countingClusterRepo{mockClusterRepo: newMockClusterRepo()}, nil, []string{"plaintext", "encrypted registry password"}},
		{"error warns without count", &countingClusterRepo{mockClusterRepo: newMockClusterRepo(), err: errors.New("db down")}, []string{"Could not check"}, nil},
		{"repository without counter is silent", newMockClusterRepo(), nil, []string{"plaintext", "encrypted registry password"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			old := slog.Default()
			slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
			defer slog.SetDefault(old)

			warnPlaintextRegistryPasswords(tt.repo)

			for _, w := range tt.want {
				assert.Contains(t, buf.String(), w)
			}
			for _, w := range tt.notWant {
				assert.NotContains(t, buf.String(), w)
			}
		})
	}
}
