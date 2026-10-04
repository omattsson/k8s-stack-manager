package handlers

import (
	"errors"
	"math"
	"net/http"
	"strconv"
	"time"

	"backend/internal/api/middleware"
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
// @Failure     401  {object} map[string]string
// @Failure     403  {object} map[string]string
// @Failure     404  {object} map[string]string
// @Failure     429  {object} map[string]string
// @Failure     500  {object} map[string]string
// @Failure     502  {object} map[string]string
// @Failure     503  {object} map[string]string
// @Header      429  {integer} Retry-After "Provider rate limit retry delay in seconds, when supplied"
// @Router      /api/v1/git/branches [get]
func (h *GitHandler) ListBranches(c *gin.Context) {
	repoURL := middleware.GitRepoFromContext(c)
	if repoURL == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "repo query parameter is required"})
		return
	}

	branches, err := h.registry.ListBranches(c.Request.Context(), repoURL)
	if err != nil {
		writeGitProviderError(c, err)
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
// @Failure     401    {object} map[string]string
// @Failure     403    {object} map[string]string
// @Failure     404    {object} map[string]string
// @Failure     429    {object} map[string]string
// @Failure     500    {object} map[string]string
// @Failure     502    {object} map[string]string
// @Failure     503    {object} map[string]string
// @Header      429    {integer} Retry-After "Provider rate limit retry delay in seconds, when supplied"
// @Router      /api/v1/git/validate-branch [get]
func (h *GitHandler) ValidateBranch(c *gin.Context) {
	repoURL := middleware.GitRepoFromContext(c)
	branch := c.Query("branch")
	if repoURL == "" || branch == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "repo and branch query parameters are required"})
		return
	}

	valid, err := h.registry.ValidateBranch(c.Request.Context(), repoURL, branch)
	if err != nil {
		writeGitProviderError(c, err)
		return
	}

	c.JSON(http.StatusOK, gin.H{"valid": valid, "branch": branch})
}

func writeGitProviderError(c *gin.Context, err error) {
	var rateLimitErr *gitprovider.RateLimitError
	if errors.As(err, &rateLimitErr) {
		retryAfterSeconds := int64(math.Ceil(time.Until(rateLimitErr.RetryAfter).Seconds()))
		if retryAfterSeconds < 0 {
			retryAfterSeconds = 0
		}
		c.Header("Retry-After", strconv.FormatInt(retryAfterSeconds, 10))
		c.JSON(http.StatusTooManyRequests, gin.H{"error": "Git provider rate limit exceeded"})
		return
	}

	switch {
	case errors.Is(err, gitprovider.ErrInvalidRepositoryURL):
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid repository URL"})
	case errors.Is(err, gitprovider.ErrUnsupportedProvider):
		c.JSON(http.StatusBadRequest, gin.H{"error": "Unsupported Git provider"})
	case errors.Is(err, gitprovider.ErrRepositoryNotAllowed):
		c.JSON(http.StatusForbidden, gin.H{"error": "Repository is not permitted"})
	case errors.Is(err, gitprovider.ErrRepositoryNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": "Repository not found"})
	case errors.Is(err, gitprovider.ErrAuthentication):
		c.JSON(http.StatusBadGateway, gin.H{"error": "Git provider authentication or permission denied"})
	case errors.Is(err, gitprovider.ErrUpstreamFailure):
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Git provider is unavailable"})
	default:
		c.JSON(http.StatusInternalServerError, gin.H{"error": msgInternalServerError})
	}
}

// GetProviders godoc
// @Summary     List Git providers
// @Description Get the status of all configured Git providers
// @Tags        git
// @Produce     json
// @Success     200 {array} gitprovider.ProviderStatus
// @Failure     401 {object} map[string]string
// @Failure     403 {object} map[string]string
// @Failure     429 {object} map[string]string
// @Router      /api/v1/git/providers [get]
func (h *GitHandler) GetProviders(c *gin.Context) {
	c.JSON(http.StatusOK, h.registry.GetProviderStatus())
}
