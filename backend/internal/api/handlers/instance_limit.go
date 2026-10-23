package handlers

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"

	"backend/internal/models"
	"backend/pkg/dberrors"

	"github.com/gin-gonic/gin"
)

// The per-user instance limit of a cluster (max_instances_per_user) applies
// to every path that creates an instance: create, clone and quick deploy
// (#682).

// instanceLimitMessage is the 409 message for a user at the limit.
func instanceLimitMessage(limit int) string {
	return fmt.Sprintf("Maximum instances per user reached for this cluster (limit: %d)", limit)
}

// instanceLimitFor returns max_instances_per_user of clusterID; 0 means no
// limit. A failed read must not skip the limit (#656): it writes the error
// response and returns false.
func instanceLimitFor(c *gin.Context, repo models.ClusterRepository, clusterID string) (int, bool) {
	if repo == nil || clusterID == "" {
		return 0, true
	}
	cl, err := repo.FindByID(c.Request.Context(), clusterID)
	if err != nil {
		if respondCancelled(c, err) {
			return 0, false
		}
		if errors.Is(err, dberrors.ErrNotFound) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Unknown cluster_id"})
			return 0, false
		}
		slog.ErrorContext(c.Request.Context(), "Failed to read the cluster of a new instance", "cluster_id", clusterID, "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": msgInternalServerError})
		return 0, false
	}
	if cl.MaxInstancesPerUser > 0 {
		return cl.MaxInstancesPerUser, true
	}
	return 0, true
}

// checkInstanceLimit returns ErrInstanceLimitExceeded when ownerID already
// has limit instances on clusterID. Call it in the transaction that creates
// the instance. A limit of 0 checks nothing.
func checkInstanceLimit(ctx context.Context, repo models.StackInstanceRepository, clusterID, ownerID string, limit int) error {
	if limit <= 0 {
		return nil
	}
	count, err := repo.CountByClusterAndOwner(ctx, clusterID, ownerID)
	if err != nil {
		return err
	}
	if count >= limit {
		return fmt.Errorf("%w: %s", ErrInstanceLimitExceeded, instanceLimitMessage(limit))
	}
	return nil
}

// respondInstanceLimit writes the 409 response when err is
// ErrInstanceLimitExceeded and reports whether it did.
func respondInstanceLimit(c *gin.Context, err error, limit int) bool {
	if !errors.Is(err, ErrInstanceLimitExceeded) {
		return false
	}
	c.JSON(http.StatusConflict, gin.H{"error": instanceLimitMessage(limit)})
	return true
}
