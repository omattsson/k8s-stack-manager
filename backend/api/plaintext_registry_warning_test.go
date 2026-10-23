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
	n   int
	err error
}

func (r *countingClusterRepo) CountPlaintextRegistryPasswords(context.Context) (int, error) {
	return r.n, r.err
}

// Not parallel: it replaces the default slog logger.
func TestWarnPlaintextRegistryPasswords(t *testing.T) {
	tests := []struct {
		name     string
		repo     models.ClusterRepository
		wantWarn bool
		wantText string
	}{
		{"count above zero warns", &countingClusterRepo{mockClusterRepo: newMockClusterRepo(), n: 2}, true, "clusters=2"},
		{"zero is silent", &countingClusterRepo{mockClusterRepo: newMockClusterRepo()}, false, ""},
		{"error warns without count", &countingClusterRepo{mockClusterRepo: newMockClusterRepo(), err: errors.New("db down")}, true, "Could not check"},
		{"repository without counter is silent", newMockClusterRepo(), false, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			old := slog.Default()
			slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
			defer slog.SetDefault(old)

			warnPlaintextRegistryPasswords(tt.repo)

			if !tt.wantWarn {
				assert.Empty(t, buf.String())
				return
			}
			assert.Contains(t, buf.String(), "level=WARN")
			assert.Contains(t, buf.String(), tt.wantText)
		})
	}
}
