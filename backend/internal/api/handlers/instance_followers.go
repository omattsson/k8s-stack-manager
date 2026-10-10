package handlers

import (
	"log/slog"
	"net/http"

	"backend/internal/api/middleware"
	"backend/internal/models"

	"github.com/gin-gonic/gin"
)

// msgFollowNotAvailable is the 501 message when no follower repository is
// configured.
const msgFollowNotAvailable = "Following instances is not available"

// followStateResponse is the response of the follow and unfollow endpoints.
type followStateResponse struct {
	// Following is true when the caller follows the instance.
	Following bool `json:"following"`
	// FollowerCount is the number of users who follow the instance.
	FollowerCount int64 `json:"follower_count"`
}

// WithFollowers attaches the instance follower repository. It enables
// POST/DELETE /stack-instances/{id}/follow and the following and
// follower_count fields of GET /stack-instances/{id}. Returns h for chaining.
func (h *InstanceHandler) WithFollowers(repo models.InstanceFollowerRepository) *InstanceHandler {
	h.followerRepo = repo
	return h
}

// FollowInstance godoc
// @Summary     Follow a stack instance
// @Description The caller follows the instance and gets its in-app notifications, as the owner does, filtered by the notification preferences of the caller. Every authenticated user who can view the instance can follow it. A repeated follow is not an error. Followers get no channel (webhook) deliveries.
// @Tags        stack-instances
// @Produce     json
// @Param       id  path     string true "Instance ID"
// @Success     200 {object} followStateResponse
// @Failure     400 {object} map[string]string
// @Failure     401 {object} map[string]string
// @Failure     403 {object} map[string]string
// @Failure     404 {object} map[string]string
// @Failure     500 {object} map[string]string
// @Failure     501 {object} map[string]string "Following is not configured"
// @Router      /api/v1/stack-instances/{id}/follow [post]
// @Security    BearerAuth
func (h *InstanceHandler) FollowInstance(c *gin.Context) {
	h.setFollow(c, true)
}

// UnfollowInstance godoc
// @Summary     Unfollow a stack instance
// @Description The caller stops following the instance. Unfollowing an instance that the caller does not follow is not an error.
// @Tags        stack-instances
// @Produce     json
// @Param       id  path     string true "Instance ID"
// @Success     200 {object} followStateResponse
// @Failure     400 {object} map[string]string
// @Failure     401 {object} map[string]string
// @Failure     403 {object} map[string]string
// @Failure     404 {object} map[string]string
// @Failure     500 {object} map[string]string
// @Failure     501 {object} map[string]string "Following is not configured"
// @Router      /api/v1/stack-instances/{id}/follow [delete]
// @Security    BearerAuth
func (h *InstanceHandler) UnfollowInstance(c *gin.Context) {
	h.setFollow(c, false)
}

func (h *InstanceHandler) setFollow(c *gin.Context, follow bool) {
	if h.followerRepo == nil {
		c.JSON(http.StatusNotImplemented, gin.H{"error": msgFollowNotAvailable})
		return
	}
	id := c.Param("id")
	if id == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": msgInstanceIDRequired})
		return
	}
	userID := middleware.GetUserIDFromContext(c)
	if userID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Authentication required"})
		return
	}

	// Every authenticated user can view an instance, so the view rule is
	// only "the instance exists".
	inst, err := h.instanceRepo.FindByID(id)
	if err != nil {
		status, message := mapError(err, entityStackInstance)
		c.JSON(status, gin.H{"error": message})
		return
	}

	ctx := c.Request.Context()
	if follow {
		err = h.followerRepo.Follow(ctx, userID, inst.ID)
	} else {
		err = h.followerRepo.Unfollow(ctx, userID, inst.ID)
	}
	if err != nil {
		slog.Error("Failed to change instance follow", logKeyInstanceID, inst.ID, "user_id", userID, "follow", follow, "error", err)
		status, message := mapError(err, entityStackInstance)
		c.JSON(status, gin.H{"error": message})
		return
	}

	following, count, err := h.followerRepo.FollowState(ctx, userID, inst.ID)
	if err != nil {
		slog.Error("Failed to read instance follow state", logKeyInstanceID, inst.ID, "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": msgInternalServerError})
		return
	}
	c.JSON(http.StatusOK, followStateResponse{Following: following, FollowerCount: count})
}

// enrichInstanceDetail sets all computed fields of a single-instance
// response: values_drift, the display names and the follow state. GET, update
// and extend use it, so a client can replace its state with the response and
// keep every field (issue #497).
func (h *InstanceHandler) enrichInstanceDetail(c *gin.Context, inst *models.StackInstance) {
	if inst == nil {
		return
	}
	inst.ValuesDrift = h.instanceValuesDrift(c.Request.Context(), inst)
	h.setInstanceNames(inst)
	h.setFollowState(c, inst)
}

// setFollowState sets the following and follower_count fields of inst for
// the caller. Without a follower repository it leaves them unset. A lookup
// error is logged and leaves them unset: the instance response still works.
func (h *InstanceHandler) setFollowState(c *gin.Context, inst *models.StackInstance) {
	if h.followerRepo == nil || inst == nil {
		return
	}
	following, count, err := h.followerRepo.FollowState(c.Request.Context(), middleware.GetUserIDFromContext(c), inst.ID)
	if err != nil {
		slog.Error("Failed to read instance follow state", logKeyInstanceID, inst.ID, "error", err)
		return
	}
	inst.Following = &following
	inst.FollowerCount = &count
}
