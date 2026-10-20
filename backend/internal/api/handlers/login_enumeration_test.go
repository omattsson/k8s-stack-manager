package handlers

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"backend/internal/config"
	"backend/internal/models"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"
)

const enumPassword = "Correct-Pw-632!"

// recordingCompare wraps bcrypt.CompareHashAndPassword and records the hash of each call.
type recordingCompare struct {
	mu     sync.Mutex
	hashes [][]byte
}

func (r *recordingCompare) compare(hash, password []byte) error {
	r.mu.Lock()
	r.hashes = append(r.hashes, hash)
	r.mu.Unlock()
	return bcrypt.CompareHashAndPassword(hash, password)
}

func (r *recordingCompare) calls() [][]byte {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([][]byte(nil), r.hashes...)
}

type enumCase struct {
	user     *models.User // nil: no such user
	name     string
	password string
	oidcOnly bool // OIDC on, local login only for service accounts
	useCache bool // LOGIN_CACHE_TTL on, the correct credentials cached
}

// runEnumLogin posts one login and returns the response and the recorded compares.
func runEnumLogin(t *testing.T, tt enumCase) (*httptest.ResponseRecorder, *recordingCompare) {
	t.Helper()
	repo := NewMockUserRepository()
	if tt.user != nil {
		u := *tt.user
		require.NoError(t, repo.Create(&u))
	}
	cfg := testAuthConfig(false)
	if tt.useCache {
		cfg.LoginCacheTTL = time.Hour
	}
	oidcCfg := &config.OIDCConfig{}
	if tt.oidcOnly {
		oidcCfg = &config.OIDCConfig{Enabled: true}
	}
	h := NewAuthHandler(repo, cfg, oidcCfg)
	h.logger = slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
	rec := &recordingCompare{}
	h.compareHash = rec.compare
	if tt.useCache && tt.user != nil {
		h.loginCache.Set(loginCacheKey(tt.user.Username, tt.user.PasswordHash, enumPassword), struct{}{})
	}

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.POST("/api/v1/auth/login", h.Login)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login",
		strings.NewReader(`{"username":"alice","password":"`+tt.password+`"}`))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	return w, rec
}

func enumUser(t *testing.T, mutate func(u *models.User)) *models.User {
	t.Helper()
	u := &models.User{ID: "uid-632", Username: "alice", Role: models.RoleUser, AuthProvider: "local",
		PasswordHash: hashPassword(t, enumPassword)}
	if mutate != nil {
		mutate(u)
	}
	return u
}

// TestLogin_NoEnumerationWithoutPassword checks that without the correct
// password the answer does not depend on whether the user exists or on its
// state, and that each case runs exactly one bcrypt compare (#632).
func TestLogin_NoEnumerationWithoutPassword(t *testing.T) {
	t.Parallel()

	const wantBody = `{"error":"Invalid username or password"}`
	tests := []struct {
		enumCase
		wantDummy bool // the compare used the dummy hash
	}{
		{enumCase: enumCase{name: "unknown user", password: "wrong"}, wantDummy: true},
		{enumCase: enumCase{name: "unknown user, OIDC only", password: "wrong", oidcOnly: true}, wantDummy: true},
		{enumCase: enumCase{name: "wrong password", password: "wrong", user: enumUser(t, nil)}},
		{enumCase: enumCase{name: "wrong password, disabled user", password: "wrong",
			user: enumUser(t, func(u *models.User) { u.Disabled = true })}},
		{enumCase: enumCase{name: "wrong password, SSO-only local user", password: "wrong", oidcOnly: true,
			user: enumUser(t, nil)}},
		{enumCase: enumCase{name: "wrong password, disabled SSO-only user", password: "wrong", oidcOnly: true,
			user: enumUser(t, func(u *models.User) { u.Disabled = true })}},
		{enumCase: enumCase{name: "SSO user without a local password hash", password: enumPassword, oidcOnly: true,
			user: enumUser(t, func(u *models.User) { u.AuthProvider = "oidc"; u.PasswordHash = "" })}, wantDummy: true},
		{enumCase: enumCase{name: "disabled SSO user without a local password hash", password: "wrong",
			user: enumUser(t, func(u *models.User) { u.AuthProvider = "oidc"; u.PasswordHash = ""; u.Disabled = true })}, wantDummy: true},
		{enumCase: enumCase{name: "wrong password, cache on", password: "wrong", useCache: true,
			user: enumUser(t, func(u *models.User) { u.Disabled = true })}},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			w, rec := runEnumLogin(t, tt.enumCase)

			assert.Equal(t, http.StatusUnauthorized, w.Code)
			assert.Equal(t, wantBody, w.Body.String())
			calls := rec.calls()
			require.Len(t, calls, 1, "exactly one bcrypt compare")
			if tt.wantDummy {
				assert.Equal(t, dummyPasswordHash(), calls[0], "compare against the dummy hash")
			} else {
				assert.Equal(t, []byte(tt.user.PasswordHash), calls[0], "compare against the stored hash")
			}
		})
	}
}

// TestLogin_AccountStateAfterCorrectPassword checks that the account state is
// shown only after a correct password, and that a cache hit skips only bcrypt.
func TestLogin_AccountStateAfterCorrectPassword(t *testing.T) {
	t.Parallel()

	tests := []struct {
		enumCase
		wantBody      string
		wantCode      int
		wantCompares  int
		wantTokenBody bool
	}{
		{enumCase: enumCase{name: "disabled user", user: enumUser(t, func(u *models.User) { u.Disabled = true })},
			wantCode: http.StatusForbidden, wantBody: `{"error":"Account disabled"}`, wantCompares: 1},
		{enumCase: enumCase{name: "disabled user, cache hit", useCache: true, user: enumUser(t, func(u *models.User) { u.Disabled = true })},
			wantCode: http.StatusForbidden, wantBody: `{"error":"Account disabled"}`, wantCompares: 0},
		{enumCase: enumCase{name: "SSO-only local user", oidcOnly: true, user: enumUser(t, nil)},
			wantCode: http.StatusForbidden, wantBody: `{"error":"Local login is restricted to service accounts. Please use SSO."}`, wantCompares: 1},
		{enumCase: enumCase{name: "service account with OIDC only", oidcOnly: true, user: enumUser(t, func(u *models.User) { u.ServiceAccount = true })},
			wantCode: http.StatusOK, wantCompares: 1, wantTokenBody: true},
		{enumCase: enumCase{name: "enabled user", user: enumUser(t, nil)},
			wantCode: http.StatusOK, wantCompares: 1, wantTokenBody: true},
		{enumCase: enumCase{name: "enabled user, cache hit", useCache: true, user: enumUser(t, nil)},
			wantCode: http.StatusOK, wantCompares: 0, wantTokenBody: true},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			tt.password = enumPassword

			w, rec := runEnumLogin(t, tt.enumCase)

			assert.Equal(t, tt.wantCode, w.Code)
			if tt.wantTokenBody {
				assert.Contains(t, w.Body.String(), `"token":"`)
			} else {
				assert.Equal(t, tt.wantBody, w.Body.String())
			}
			assert.Len(t, rec.calls(), tt.wantCompares)
		})
	}
}

func TestDummyPasswordHash(t *testing.T) {
	t.Parallel()

	cost, err := bcrypt.Cost(dummyPasswordHash())
	require.NoError(t, err)
	assert.Equal(t, passwordHashCost, cost, "same cost as the stored hashes")
	assert.Equal(t, dummyPasswordHash(), dummyPasswordHash(), "created once")
}
