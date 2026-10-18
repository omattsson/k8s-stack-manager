package handlers

import (
	"log/slog"
	"net/http"
	"strconv"

	"backend/internal/api/middleware"
	"backend/internal/models"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// maxIDFilterLength is the largest accepted length of an ID query filter
// (cluster_id, definition_id). IDs are stored in 36-character columns.
const maxIDFilterLength = 36

// maxOwnerFilterLength is the largest accepted length of the owner filter
// (a user ID or a username).
const maxOwnerFilterLength = 255

// ownerFilterMe selects the authenticated user in the owner filter.
const ownerFilterMe = "me"

// uniqueNonEmpty returns the distinct non-empty values of ids.
func uniqueNonEmpty(ids []string) []string {
	seen := make(map[string]struct{}, len(ids))
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}

// lookupUsernames returns the username of each user in ids, keyed by user
// ID, with one query. A lookup error is logged and gives an empty map: the
// names are display data and must not fail the response.
func lookupUsernames(userRepo models.UserRepository, ids []string) map[string]string {
	result := make(map[string]string)
	ids = uniqueNonEmpty(ids)
	if userRepo == nil || len(ids) == 0 {
		return result
	}
	users, err := userRepo.FindByIDs(ids)
	if err != nil {
		slog.Warn("failed to batch-fetch owner usernames", "error", err)
		return result
	}
	for id, u := range users {
		if u != nil {
			result[id] = u.Username
		}
	}
	return result
}

// nameLookup is a batch lookup of names by ID (NamesByIDs).
type nameLookup func(ids []string) (map[string]string, error)

// lookupNames calls lookup once for the distinct non-empty ids. A nil lookup
// or a lookup error gives an empty map (the error is logged).
func lookupNames(lookup nameLookup, kind string, ids []string) map[string]string {
	ids = uniqueNonEmpty(ids)
	if lookup == nil || len(ids) == 0 {
		return map[string]string{}
	}
	names, err := lookup(ids)
	if err != nil {
		slog.Warn("failed to batch-fetch names", "kind", kind, "error", err)
		return map[string]string{}
	}
	return names
}

// setInstanceNames sets OwnerUsername, DefinitionName and ClusterName on
// each instance. It runs at most one query per kind for all instances
// together (no N+1). A missing owner, definition or cluster gives an empty
// name, which the JSON omits. An instance with an empty cluster_id (older
// instances) gets an empty cluster name.
func (h *InstanceHandler) setInstanceNames(instances ...*models.StackInstance) {
	if len(instances) == 0 {
		return
	}
	ownerIDs := make([]string, 0, len(instances))
	defIDs := make([]string, 0, len(instances))
	clusterIDs := make([]string, 0, len(instances))
	for _, inst := range instances {
		ownerIDs = append(ownerIDs, inst.OwnerID)
		defIDs = append(defIDs, inst.StackDefinitionID)
		clusterIDs = append(clusterIDs, inst.ClusterID)
	}

	usernames := lookupUsernames(h.userRepo, ownerIDs)
	var defLookup, clusterLookup nameLookup
	if h.definitionRepo != nil {
		defLookup = h.definitionRepo.NamesByIDs
	}
	if h.clusterRepo != nil {
		clusterLookup = h.clusterRepo.NamesByIDs
	}
	defNames := lookupNames(defLookup, "stack_definition", defIDs)
	clusterNames := lookupNames(clusterLookup, "cluster", clusterIDs)

	for _, inst := range instances {
		inst.OwnerUsername = usernames[inst.OwnerID]
		inst.DefinitionName = defNames[inst.StackDefinitionID]
		inst.ClusterName = clusterNames[inst.ClusterID]
	}
}

// setInstanceListNames is setInstanceNames for a slice of instances.
func (h *InstanceHandler) setInstanceListNames(instances []models.StackInstance) {
	ptrs := make([]*models.StackInstance, len(instances))
	for i := range instances {
		ptrs[i] = &instances[i]
	}
	h.setInstanceNames(ptrs...)
}

// setDefinitionOwnerNames sets OwnerUsername on each definition with one
// user query.
func setDefinitionOwnerNames(userRepo models.UserRepository, defs ...*models.StackDefinition) {
	ownerIDs := make([]string, len(defs))
	for i, d := range defs {
		ownerIDs[i] = d.OwnerID
	}
	usernames := lookupUsernames(userRepo, ownerIDs)
	for _, d := range defs {
		d.OwnerUsername = usernames[d.OwnerID]
	}
}

// resolveOwnerFilter turns the owner query parameter into an owner ID.
// "me" is the authenticated user (401 without a user ID in the context).
// A value in UUID form is looked up as a user ID first, then as a username;
// another value is looked up as a username. When no user matches, the value
// is used as a user ID, so instances of a deleted user can still be selected
// by ID. An empty value gives "" (no filter). On an invalid value or a lookup
// error it writes the response and returns false.
func resolveOwnerFilter(c *gin.Context, userRepo models.UserRepository, owner string) (string, bool) {
	switch {
	case owner == "":
		return "", true
	case owner == ownerFilterMe:
		userID := middleware.GetUserIDFromContext(c)
		if userID == "" {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "Authentication required"})
			return "", false
		}
		return userID, true
	case len(owner) > maxOwnerFilterLength:
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid owner filter"})
		return "", false
	case userRepo == nil:
		return owner, true
	}

	if isUUIDForm(owner) {
		u, err := userRepo.FindByID(owner)
		if err == nil && u != nil {
			return u.ID, true
		}
		if err != nil && !isNotFoundError(err) {
			return "", ownerLookupFailed(c, err)
		}
	}

	u, err := userRepo.FindByUsername(owner)
	if err == nil && u != nil {
		return u.ID, true
	}
	if err == nil || isNotFoundError(err) {
		return owner, true
	}
	return "", ownerLookupFailed(c, err)
}

// isUUIDForm reports whether value has the canonical UUID form
// (xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx).
func isUUIDForm(value string) bool {
	if len(value) != 36 {
		return false
	}
	_, err := uuid.Parse(value)
	return err == nil
}

// ownerLookupFailed logs err, writes a 500 response and returns false.
func ownerLookupFailed(c *gin.Context, err error) bool {
	slog.Error("failed to resolve owner filter", "error", err)
	c.JSON(http.StatusInternalServerError, gin.H{"error": msgInternalServerError})
	return false
}

// validIDFilter reports whether value is an acceptable ID query filter
// (empty, or at most maxIDFilterLength characters). Otherwise it writes a
// 400 response that names param.
func validIDFilter(c *gin.Context, param, value string) bool {
	if len(value) <= maxIDFilterLength {
		return true
	}
	c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid " + param + " filter"})
	return false
}

// listPagination reads page/pageSize (preferred) or the legacy limit/offset
// query parameters. It returns the page number, the page size (default
// listPageSizeDefault, at most listPageSizeMax) and the row offset.
func listPagination(c *gin.Context) (page, pageSize, offset int) {
	pageSize = listPageSizeDefault
	page = 1

	if ps := c.Query("pageSize"); ps != "" {
		if v, err := strconv.Atoi(ps); err == nil && v > 0 {
			pageSize = v
		}
		if pageSize > listPageSizeMax {
			pageSize = listPageSizeMax
		}
	}

	if p := c.Query("page"); p != "" {
		if v, err := strconv.Atoi(p); err == nil && v > 0 {
			page = v
		}
		offset = (page - 1) * pageSize
	} else if l := c.Query("limit"); l != "" {
		if v, err := strconv.Atoi(l); err == nil && v > 0 {
			pageSize = v
			if pageSize > listPageSizeMax {
				pageSize = listPageSizeMax
			}
		}
		if o := c.Query("offset"); o != "" {
			if v, err := strconv.Atoi(o); err == nil && v >= 0 {
				offset = v
			}
		}
	}
	return page, pageSize, offset
}
