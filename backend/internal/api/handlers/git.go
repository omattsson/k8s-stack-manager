package handlers

import (
	"net/http"
	"strings"

	"backend/internal/gitprovider"

	"github.com/gin-gonic/gin"
)

// GitHandler handles Git provider endpoints.
type GitHandler struct {
	registry *gitprovider.Registry
}

// NewGitHandler creates a new GitHandler.
func NewGitHandler(registry *gitprovider.Registry) *GitHandler {
	return &GitHandler{registry: registry}
}

// ListBranches godoc
// @Summary     List branches
// @Description List branches for a given repository URL
// @Tags        git
// @Produce     json
// @Param       repo query    string true "Repository URL"
// @Success     200  {array}  gitprovider.Branch
// @Failure     400  {object} map[string]string
// @Failure     500  {object} map[string]string
// @Failure     503  {object} map[string]string
// @Router      /api/v1/git/branches [get]
func (h *GitHandler) ListBranches(c *gin.Context) {
	repoURL := c.Query("repo")
	if repoURL == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "repo query parameter is required"})
		return
	}

	branches, err := h.registry.ListBranches(c.Request.Context(), repoURL)
	if err != nil {
		if strings.Contains(err.Error(), "not configured") {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Git provider is not configured"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Internal server error"})
		return
	}

	c.JSON(http.StatusOK, branches)
}

// ValidateBranch godoc
// @Summary     Validate a branch
// @Description Check if a branch exists in the given repository
// @Tags        git
// @Produce     json
// @Param       repo   query    string true "Repository URL"
// @Param       branch query    string true "Branch name"
// @Success     200    {object} map[string]interface{}
// @Failure     400    {object} map[string]string
// @Failure     500    {object} map[string]string
// @Failure     503    {object} map[string]string
// @Router      /api/v1/git/validate-branch [get]
func (h *GitHandler) ValidateBranch(c *gin.Context) {
	repoURL := c.Query("repo")
	branch := c.Query("branch")
	if repoURL == "" || branch == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "repo and branch query parameters are required"})
		return
	}

	valid, err := h.registry.ValidateBranch(c.Request.Context(), repoURL, branch)
	if err != nil {
		if strings.Contains(err.Error(), "not configured") {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Git provider is not configured"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Internal server error"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"valid": valid, "branch": branch})
}

// GetProviders godoc
// @Summary     List Git providers
// @Description Get the status of all configured Git providers
// @Tags        git
// @Produce     json
// @Success     200 {array} gitprovider.ProviderStatus
// @Router      /api/v1/git/providers [get]
func (h *GitHandler) GetProviders(c *gin.Context) {
	c.JSON(http.StatusOK, h.registry.GetProviderStatus())
}
