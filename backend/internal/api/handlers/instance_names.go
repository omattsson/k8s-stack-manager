package handlers

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"

	"backend/internal/cluster"
	"backend/internal/models"
	"backend/pkg/dberrors"

	"github.com/gin-gonic/gin"
)

// errNoInstanceNameLookup means that the instance repository cannot look up
// names on a cluster (models.InstanceNameLookup). Bootstrap refuses
// UNIQUE_INSTANCE_NAMES for such a repository, so this is a wiring error.
var errNoInstanceNameLookup = errors.New("instance repository does not implement models.InstanceNameLookup")

// msgInstanceNameExists is the error code of a 409 for a name that another
// instance uses on the same cluster (UNIQUE_INSTANCE_NAMES).
const msgInstanceNameExists = "instance name already exists"

// instanceNameGuard applies UNIQUE_INSTANCE_NAMES: an instance name must be
// unique on its cluster, compared without case. Installations that build
// Ingress hosts from {{.InstanceName}} need it, because two stacks with the
// same name claim the same host.
//
// The check is an application check, like the namespace check: two
// concurrent requests can both pass it.
type instanceNameGuard struct {
	instanceRepo models.StackInstanceRepository
	userRepo     models.UserRepository
	registry     *cluster.Registry
}

// defaultClusterID returns the ID of the default cluster, or "" when there
// is no registry or no default cluster.
func (g instanceNameGuard) defaultClusterID() string {
	if g.registry == nil {
		return ""
	}
	id, err := g.registry.ResolveClusterID("")
	if err != nil {
		return ""
	}
	return id
}

// nameHolder returns an instance other than excludeID that uses name on
// clusterID, or nil. An empty clusterID means the default cluster. On the
// default cluster, the instances without a cluster count too.
func (g instanceNameGuard) nameHolder(name, clusterID, excludeID string) (*models.StackInstance, error) {
	def := g.defaultClusterID()
	if clusterID == "" {
		clusterID = def
	}
	lookup, ok := g.instanceRepo.(models.InstanceNameLookup)
	if !ok {
		return nil, errNoInstanceNameLookup
	}
	list, err := lookup.FindByNameOnCluster(name, clusterID, clusterID == def)
	if err != nil {
		return nil, err
	}
	for i := range list {
		if list[i].ID != excludeID {
			return &list[i], nil
		}
	}
	return nil, nil
}

// check returns false when no other instance (excludeID is the instance
// itself on a rename) uses name on clusterID. Else it writes a 409 with the
// owner of the other instance and free name suggestions (or a 500 on a
// lookup error) and returns true. The caller must then return. owner is the
// username of the new instance (its namespace is stack-<name>-<owner>); a
// rename keeps the namespace and passes "".
func (g instanceNameGuard) check(c *gin.Context, name, clusterID, excludeID, owner string) bool {
	holder, err := g.nameHolder(name, clusterID, excludeID)
	if err != nil {
		slog.Error("Failed to check instance name uniqueness",
			"name", name,
			logKeyClusterID, clusterID,
			"error", err,
		)
		c.JSON(http.StatusInternalServerError, gin.H{"error": msgInternalServerError})
		return true
	}
	if holder == nil {
		return false
	}
	c.JSON(http.StatusConflict, NamespaceConflictResponse{
		Error: msgInstanceNameExists,
		Message: fmt.Sprintf("A stack named %q already exists on this cluster (owner: %s)",
			holder.Name, resolveOwnerName(g.userRepo, holder.OwnerID)),
		Suggestions: g.freeSuggestions(name, clusterID, owner),
	})
	return true
}

// freeSuggestions returns the name suggestions of generateNameSuggestions
// that no instance uses on clusterID and, when owner is set, whose namespace
// stack-<name>-<owner> is free (namespaces are global, not per cluster). A
// lookup error drops the suggestion.
func (g instanceNameGuard) freeSuggestions(name, clusterID, owner string) []string {
	out := make([]string, 0, 3)
	for _, s := range generateNameSuggestions(name) {
		if holder, err := g.nameHolder(s, clusterID, ""); err != nil || holder != nil {
			continue
		}
		if owner != "" {
			if _, err := g.instanceRepo.FindByNamespace(buildNamespace(s, owner)); !errors.Is(err, dberrors.ErrNotFound) {
				continue
			}
		}
		out = append(out, s)
	}
	return out
}

// LogDuplicateInstanceNames logs a warning for each name that more than one
// instance uses on the same cluster. UNIQUE_INSTANCE_NAMES refuses new
// duplicates but keeps the existing ones. defaultClusterID is the cluster of
// the instances without a cluster ("" when unknown).
func LogDuplicateInstanceNames(repo models.InstanceNameLookup, defaultClusterID string) {
	dups, err := repo.DuplicateInstanceNames(defaultClusterID)
	if err != nil {
		slog.Warn("UNIQUE_INSTANCE_NAMES: failed to list the existing duplicate instance names", "error", err)
		return
	}
	for _, d := range dups {
		slog.Warn("UNIQUE_INSTANCE_NAMES: instances share a name on one cluster; rename all but one of them",
			"name", d.Name,
			logKeyClusterID, d.ClusterID,
			"count", d.Count,
		)
	}
}
