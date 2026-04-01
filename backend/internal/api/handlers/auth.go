package handlers

import (
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
	"net/http"
	"runtime"
	"time"

	"backend/internal/api/middleware"
	"backend/internal/cache"
	"backend/internal/config"
	"backend/internal/models"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"
)

// bcryptSem limits concurrent bcrypt operations to the number of CPU cores.
// bcrypt is intentionally CPU-expensive (~200ms). Without a limit, 100 concurrent
// logins would spawn 100 goroutines all competing for CPU, starving other requests.
var bcryptSem = make(chan struct{}, runtime.NumCPU())

// AuthHandler handles authentication and user management endpoints.
type AuthHandler struct {
	userRepo   models.UserRepository
	cfg        *config.AuthConfig
	loginCache *cache.TTLCache[*models.User]
}

// NewAuthHandler creates a new AuthHandler.
func NewAuthHandler(userRepo models.UserRepository, cfg *config.AuthConfig) *AuthHandler {
	h := &AuthHandler{userRepo: userRepo, cfg: cfg}
	if cfg.LoginCacheTTL > 0 {
		h.loginCache = cache.New[*models.User](cfg.LoginCacheTTL, cfg.LoginCacheTTL)
	}
	return h
}

// loginCacheKey derives a cache key from the username, stored password hash,
// and the submitted password. Including the submitted password ensures that
// only the correct password produces a cache hit.
func loginCacheKey(username, passwordHash, submittedPassword string) string {
	h := sha256.Sum256([]byte(username + ":" + passwordHash + ":" + submittedPassword))
	return hex.EncodeToString(h[:])
}

// LoginRequest represents the login request body.
type LoginRequest struct {
	Username string `json:"username" binding:"required"`
	Password string `json:"password" binding:"required"`
}

// LoginResponse represents the login response.
type LoginResponse struct {
	Token string      `json:"token"`
	User  models.User `json:"user"`
}

// RegisterRequest represents the register request body.
type RegisterRequest struct {
	Username    string `json:"username" binding:"required"`
	Password    string `json:"password" binding:"required"`
	DisplayName string `json:"display_name"`
	Role        string `json:"role"`
}

// Login godoc
// @Summary     User login
// @Description Authenticate with username and password, returns a JWT token
// @Tags        auth
// @Accept      json
// @Produce     json
// @Param       credentials body     LoginRequest true "Login credentials"
// @Success     200         {object} LoginResponse
// @Failure     400         {object} map[string]string
// @Failure     401         {object} map[string]string
// @Router      /api/v1/auth/login [post]
func (h *AuthHandler) Login(c *gin.Context) {
	var req LoginRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": msgInvalidRequestFormat})
		return
	}

	user, err := h.userRepo.FindByUsername(req.Username)
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Invalid username or password"})
		return
	}

	// Check login cache to skip expensive bcrypt comparison on repeated logins.
	cacheKey := loginCacheKey(req.Username, user.PasswordHash, req.Password)
	cacheHit := false
	if h.loginCache != nil {
		if cached, ok := h.loginCache.Get(cacheKey); ok {
			user = cached
			cacheHit = true
		}
	}

	if !cacheHit {
		// Limit concurrent bcrypt to NumCPU to prevent CPU starvation under spike.
		bcryptSem <- struct{}{}
		bcryptErr := bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(req.Password))
		<-bcryptSem

		if bcryptErr != nil {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "Invalid username or password"})
			return
		}
		if h.loginCache != nil {
			h.loginCache.Set(cacheKey, user)
		}
	}

	token, err := middleware.GenerateToken(user.ID, user.Username, user.Role, h.cfg.JWTSecret, h.cfg.JWTExpiration)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": msgInternalServerError})
		return
	}

	c.JSON(http.StatusOK, LoginResponse{Token: token, User: *user})
}

// Register godoc
// @Summary     Register a new user
// @Description Create a new user account (admin only, or when self-registration is enabled)
// @Tags        auth
// @Accept      json
// @Produce     json
// @Param       user body     RegisterRequest true "User registration data"
// @Success     201  {object} models.User
// @Failure     400  {object} map[string]string
// @Failure     403  {object} map[string]string
// @Failure     409  {object} map[string]string
// @Router      /api/v1/auth/register [post]
func (h *AuthHandler) Register(c *gin.Context) {
	// Check if registration is allowed.
	callerRole := middleware.GetRoleFromContext(c)
	if !h.cfg.SelfRegistration && callerRole != "admin" {
		c.JSON(http.StatusForbidden, gin.H{"error": "Registration is disabled"})
		return
	}

	var req RegisterRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": msgInvalidRequestFormat})
		return
	}

	if req.Username == "" || req.Password == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Username and password are required"})
		return
	}

	// Only admins can set a role other than "user".
	role := "user"
	if req.Role != "" && callerRole == "admin" {
		role = req.Role
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": msgInternalServerError})
		return
	}

	user := &models.User{
		ID:           uuid.New().String(),
		Username:     req.Username,
		PasswordHash: string(hash),
		DisplayName:  req.DisplayName,
		Role:         role,
		AuthProvider: "local",
		CreatedAt:    time.Now().UTC(),
		UpdatedAt:    time.Now().UTC(),
	}

	if user.DisplayName == "" {
		user.DisplayName = user.Username
	}

	if err := h.userRepo.Create(user); err != nil {
		status, message := mapError(err, "User")
		c.JSON(status, gin.H{"error": message})
		return
	}

	c.JSON(http.StatusCreated, user)
}

// GetCurrentUser godoc
// @Summary     Get current user
// @Description Returns the authenticated user's information
// @Tags        auth
// @Produce     json
// @Success     200 {object} models.User
// @Failure     401 {object} map[string]string
// @Failure     404 {object} map[string]string
// @Router      /api/v1/auth/me [get]
func (h *AuthHandler) GetCurrentUser(c *gin.Context) {
	userID := middleware.GetUserIDFromContext(c)
	if userID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Authentication required"})
		return
	}

	user, err := h.userRepo.FindByID(userID)
	if err != nil {
		status, message := mapError(err, "User")
		c.JSON(status, gin.H{"error": message})
		return
	}

	c.JSON(http.StatusOK, user)
}

// EnsureAdminUser creates the initial admin user if configured and not already present.
func (h *AuthHandler) EnsureAdminUser() {
	if h.cfg.AdminUsername == "" || h.cfg.AdminPassword == "" {
		return
	}

	_, err := h.userRepo.FindByUsername(h.cfg.AdminUsername)
	if err == nil {
		return // Admin already exists.
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(h.cfg.AdminPassword), bcrypt.DefaultCost)
	if err != nil {
		slog.Error("Failed to hash admin password", "error", err)
		return
	}

	admin := &models.User{
		ID:           uuid.New().String(),
		Username:     h.cfg.AdminUsername,
		PasswordHash: string(hash),
		DisplayName:  "Administrator",
		Role:         "admin",
		CreatedAt:    time.Now().UTC(),
		UpdatedAt:    time.Now().UTC(),
	}

	if err := h.userRepo.Create(admin); err != nil {
		slog.Error("Failed to create admin user", "error", err)
		return
	}

	slog.Info("Admin user created", "username", h.cfg.AdminUsername)
}
