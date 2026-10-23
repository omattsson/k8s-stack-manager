package main

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"testing"

	"backend/internal/cluster"
	"backend/internal/models"
	"backend/pkg/dberrors"

	"github.com/stretchr/testify/assert"
)

type findDefaultClusterRepo struct {
	stubClusterRepo
	cluster *models.Cluster
	err     error
}

func (r *findDefaultClusterRepo) FindDefault(_ context.Context) (*models.Cluster, error) {
	return r.cluster, r.err
}

// TestStartupDefaultClusterID swaps the process-wide default logger, so it
// does not run in parallel.
func TestStartupDefaultClusterID(t *testing.T) {
	prev := slog.Default()
	t.Cleanup(func() { slog.SetDefault(prev) })

	tests := []struct {
		name     string
		repo     *findDefaultClusterRepo
		nilReg   bool
		wantID   string
		wantWarn bool
	}{
		{name: "no registry", nilReg: true},
		{name: "default found", repo: &findDefaultClusterRepo{cluster: &models.Cluster{ID: "c1"}}, wantID: "c1"},
		{name: "no default is not a warning", repo: &findDefaultClusterRepo{err: dberrors.NewDatabaseError("find_default", dberrors.ErrNotFound)}},
		{name: "lookup failure logs a warning", repo: &findDefaultClusterRepo{err: errors.New("connection refused")}, wantWarn: true},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))

			var reg *cluster.Registry
			if !tt.nilReg {
				reg = cluster.NewRegistry(cluster.RegistryOptions{ClusterRepo: tt.repo})
			}
			assert.Equal(t, tt.wantID, startupDefaultClusterID(reg))
			if tt.wantWarn {
				assert.Contains(t, buf.String(), "level=WARN")
				assert.Contains(t, buf.String(), "default cluster lookup failed")
			} else {
				assert.NotContains(t, buf.String(), "level=WARN")
			}
		})
	}
}
