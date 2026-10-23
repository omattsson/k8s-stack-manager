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

const (
	msgUnknownClusterID    = "Unknown cluster_id"
	msgSourceClusterIsGone = "The cluster of the source instance no longer exists"
)

// instanceLimit is the per-user instance limit for a new instance.
type instanceLimit struct {
	clusterID string // the cluster whose limit applies
	max       int    // 0: no limit
	// isDefault: the instances without a cluster run on this cluster, so
	// they count too (the rule of clusterInstances).
	isDefault bool
}

// clusterGone is the response when the cluster of a new instance does not
// exist.
type clusterGone struct {
	message string
	status  int
}

var (
	unknownClusterID = clusterGone{status: http.StatusBadRequest, message: msgUnknownClusterID}
	// A clone request names no cluster: the source instance refers to a
	// cluster that is gone, a state conflict like a missing published version.
	sourceClusterGone = clusterGone{status: http.StatusConflict, message: msgSourceClusterIsGone}
)

// instanceLimitMessage is the 409 message for a user at the limit.
func instanceLimitMessage(limit int) string {
	return fmt.Sprintf("Maximum instances per user reached for this cluster (limit: %d)", limit)
}

// instanceLimitFor returns the limit for a new instance on clusterID. An
// empty clusterID means the default cluster; without a default cluster there
// is no limit. A failed read must not skip the limit (#656): it writes the
// error response and returns false.
func instanceLimitFor(c *gin.Context, repo models.ClusterRepository, clusterID string, gone clusterGone) (instanceLimit, bool) {
	if repo == nil {
		return instanceLimit{}, true
	}
	ctx := c.Request.Context()
	var (
		cl  *models.Cluster
		err error
	)
	if clusterID == "" {
		cl, err = repo.FindDefault(ctx)
		if errors.Is(err, dberrors.ErrNotFound) {
			return instanceLimit{}, true
		}
	} else {
		cl, err = repo.FindByID(ctx, clusterID)
	}
	if err != nil {
		if respondCancelled(c, err) {
			return instanceLimit{}, false
		}
		if errors.Is(err, dberrors.ErrNotFound) {
			c.JSON(gone.status, gin.H{"error": gone.message})
			return instanceLimit{}, false
		}
		slog.ErrorContext(ctx, "Failed to read the cluster of a new instance", "cluster_id", clusterID, "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": msgInternalServerError})
		return instanceLimit{}, false
	}
	limit := instanceLimit{clusterID: cl.ID, isDefault: cl.IsDefault}
	if cl.MaxInstancesPerUser > 0 {
		limit.max = cl.MaxInstancesPerUser
	}
	return limit, true
}

// checkInstanceLimit returns ErrInstanceLimitExceeded when ownerID already
// has limit.max instances on the cluster. Call it as the first statement of
// the transaction that creates the instance: it locks the user row, so a
// concurrent create of the same owner waits for the commit. A limit of 0
// checks nothing.
func checkInstanceLimit(ctx context.Context, repo models.StackInstanceRepository, limit instanceLimit, ownerID string) error {
	if limit.max <= 0 {
		return nil
	}
	count, err := repo.CountForInstanceLimit(ctx, limit.clusterID, ownerID, limit.isDefault)
	if err != nil {
		return err
	}
	if count >= limit.max {
		return fmt.Errorf("%w: %s", ErrInstanceLimitExceeded, instanceLimitMessage(limit.max))
	}
	return nil
}

// respondInstanceLimit writes the 409 response when err is
// ErrInstanceLimitExceeded and reports whether it did.
func respondInstanceLimit(c *gin.Context, err error, limit instanceLimit) bool {
	if !errors.Is(err, ErrInstanceLimitExceeded) {
		return false
	}
	c.JSON(http.StatusConflict, gin.H{"error": instanceLimitMessage(limit.max)})
	return true
}
