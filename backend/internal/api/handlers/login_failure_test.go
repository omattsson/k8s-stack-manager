package handlers

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"backend/internal/api/middleware"
	"backend/internal/config"
	"backend/internal/models"
	"backend/pkg/dberrors"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

// The password of the login failure tests. No log line may contain it.
const loginFailurePassword = "S3cret-Pw-619!"

// scriptedUserRepo wraps MockUserRepository. byUsernameErr replaces the
// FindByUsername result; byID, when set, can replace the result of each
// FindByID call (1-based call number; the Login re-read is call 1, the
// re-check after signing is call 2).
type scriptedUserRepo struct {
	*MockUserRepository
	byUsernameErr error
	byID          func(call int, u *models.User, err error) (*models.User, error)
	mu            sync.Mutex
	byIDCalls     int
}

func (s *scriptedUserRepo) FindByUsername(username string) (*models.User, error) {
	if s.byUsernameErr != nil {
		return nil, s.byUsernameErr
	}
	return s.MockUserRepository.FindByUsername(username)
}

func (s *scriptedUserRepo) FindByID(id string) (*models.User, error) {
	u, err := s.MockUserRepository.FindByID(id)
	s.mu.Lock()
	s.byIDCalls++
	call := s.byIDCalls
	s.mu.Unlock()
	if s.byID != nil {
		return s.byID(call, u, err)
	}
	return u, err
}

var errNotFoundForLogin = dberrors.NewDatabaseError("find_by_id", dberrors.ErrNotFound)

// TestLogin_FailureReasonLogged checks that each failed login path writes one
// log line with its internal reason, the username and the cache-hit flag, that
// the response stays the same generic message, and that no secret is logged.
func TestLogin_FailureReasonLogged(t *testing.T) {
	t.Parallel()

	tests := []struct {
		setup      func(t *testing.T, repo *scriptedUserRepo, h *AuthHandler, refresh *MockRefreshTokenRepository)
		name       string
		username   string
		reason     string
		wantBody   string
		wantLevel  string
		wantCode   int
		oidcOnly   bool // OIDC on without local auth
		useCache   bool // LOGIN_CACHE_TTL on, and the credentials are cached
		useRefresh bool
	}{
		{
			name: "unknown user", username: "ghost", reason: loginReasonUserNotFound,
			wantCode: http.StatusUnauthorized, wantBody: `{"error":"Invalid username or password"}`, wantLevel: "WARN",
		},
		{
			name: "wrong password", username: "alice", reason: loginReasonPasswordMismatch,
			setup: func(t *testing.T, repo *scriptedUserRepo, _ *AuthHandler, _ *MockRefreshTokenRepository) {
				seedUser(t, repo.MockUserRepository, "uid-1", "alice", "another-password", "admin")
			},
			wantCode: http.StatusUnauthorized, wantBody: `{"error":"Invalid username or password"}`, wantLevel: "WARN",
		},
		{
			name: "no local password (SSO user)", username: "alice", reason: loginReasonNoPassword,
			setup: func(t *testing.T, repo *scriptedUserRepo, _ *AuthHandler, _ *MockRefreshTokenRepository) {
				require.NoError(t, repo.Create(&models.User{ID: "uid-1", Username: "alice", Role: "user", AuthProvider: "oidc"}))
			},
			wantCode: http.StatusUnauthorized, wantBody: `{"error":"Invalid username or password"}`, wantLevel: "WARN",
		},
		{
			name: "stored hash malformed", username: "alice", reason: loginReasonHashInvalid,
			setup: func(t *testing.T, repo *scriptedUserRepo, _ *AuthHandler, _ *MockRefreshTokenRepository) {
				require.NoError(t, repo.Create(&models.User{ID: "uid-1", Username: "alice", Role: "user", PasswordHash: "not-a-hash"}))
			},
			wantCode: http.StatusUnauthorized, wantBody: `{"error":"Invalid username or password"}`, wantLevel: "WARN", // Warn: any client can trigger it
		},
		{
			name: "user deleted during the password check", username: "alice", reason: loginReasonRereadNotFound,
			setup: func(t *testing.T, repo *scriptedUserRepo, _ *AuthHandler, _ *MockRefreshTokenRepository) {
				seedUser(t, repo.MockUserRepository, "uid-1", "alice", loginFailurePassword, "admin")
				repo.byID = func(int, *models.User, error) (*models.User, error) { return nil, errNotFoundForLogin }
			},
			wantCode: http.StatusUnauthorized, wantBody: `{"error":"Invalid username or password"}`, wantLevel: "WARN",
		},
		{
			name: "password changed during the check, cache hit", username: "alice", reason: loginReasonHashChanged, useCache: true,
			setup: func(t *testing.T, repo *scriptedUserRepo, _ *AuthHandler, _ *MockRefreshTokenRepository) {
				seedUser(t, repo.MockUserRepository, "uid-1", "alice", loginFailurePassword, "admin")
				repo.byID = func(_ int, u *models.User, err error) (*models.User, error) {
					if err != nil {
						return nil, err
					}
					u.PasswordHash = hashPassword(t, "new-password")
					return u, nil
				}
			},
			wantCode: http.StatusUnauthorized, wantBody: `{"error":"Invalid username or password"}`, wantLevel: "WARN",
		},
		{
			name: "user deleted after signing", username: "alice", reason: loginReasonRecheckFailed,
			setup: func(t *testing.T, repo *scriptedUserRepo, _ *AuthHandler, _ *MockRefreshTokenRepository) {
				seedUser(t, repo.MockUserRepository, "uid-1", "alice", loginFailurePassword, "admin")
				repo.byID = func(call int, u *models.User, err error) (*models.User, error) {
					if call == 2 {
						return nil, errNotFoundForLogin
					}
					return u, err
				}
			},
			wantCode: http.StatusUnauthorized, wantBody: `{"error":"Invalid username or password"}`, wantLevel: "WARN",
		},
		{
			name: "session revoked before the response", username: "alice", reason: loginReasonSessionRevoked, useRefresh: true,
			setup: func(t *testing.T, repo *scriptedUserRepo, _ *AuthHandler, refresh *MockRefreshTokenRepository) {
				seedUser(t, repo.MockUserRepository, "uid-1", "alice", loginFailurePassword, "admin")
				refresh.countHook = func(int) { _ = refresh.RevokeAllForUser("uid-1") }
			},
			wantCode: http.StatusUnauthorized, wantBody: `{"error":"Session revoked"}`, wantLevel: "WARN",
		},
		{
			name: "disabled user", username: "alice", reason: loginReasonDisabled,
			setup: func(t *testing.T, repo *scriptedUserRepo, _ *AuthHandler, _ *MockRefreshTokenRepository) {
				require.NoError(t, repo.Create(&models.User{ID: "uid-1", Username: "alice", Role: "user",
					PasswordHash: hashPassword(t, loginFailurePassword), Disabled: true}))
			},
			wantCode: http.StatusForbidden, wantBody: `{"error":"Account disabled"}`, wantLevel: "WARN",
		},
		{
			name: "disabled after signing", username: "alice", reason: loginReasonDisabled,
			setup: func(t *testing.T, repo *scriptedUserRepo, _ *AuthHandler, _ *MockRefreshTokenRepository) {
				seedUser(t, repo.MockUserRepository, "uid-1", "alice", loginFailurePassword, "admin")
				repo.byID = func(call int, u *models.User, err error) (*models.User, error) {
					if call == 2 && u != nil {
						u.Disabled = true
					}
					return u, err
				}
			},
			wantCode: http.StatusForbidden, wantBody: `{"error":"Account disabled"}`, wantLevel: "WARN",
		},
		{
			name: "local login not allowed with OIDC", username: "alice", reason: loginReasonLocalLoginNotAllowed, oidcOnly: true,
			setup: func(t *testing.T, repo *scriptedUserRepo, _ *AuthHandler, _ *MockRefreshTokenRepository) {
				seedUser(t, repo.MockUserRepository, "uid-1", "alice", loginFailurePassword, "user")
			},
			wantCode: http.StatusForbidden, wantBody: `{"error":"Local login is restricted to service accounts. Please use SSO."}`, wantLevel: "WARN",
		},
		{
			name: "user lookup error", username: "alice", reason: loginReasonLookupError,
			setup: func(t *testing.T, repo *scriptedUserRepo, _ *AuthHandler, _ *MockRefreshTokenRepository) {
				repo.byUsernameErr = errors.New("dial tcp: connection refused")
			},
			wantCode: http.StatusInternalServerError, wantBody: `{"error":"Internal server error"}`, wantLevel: "ERROR",
		},
		{
			name: "re-read error", username: "alice", reason: loginReasonRereadError,
			setup: func(t *testing.T, repo *scriptedUserRepo, _ *AuthHandler, _ *MockRefreshTokenRepository) {
				seedUser(t, repo.MockUserRepository, "uid-1", "alice", loginFailurePassword, "admin")
				repo.byID = func(int, *models.User, error) (*models.User, error) {
					return nil, errors.New("driver: bad connection")
				}
			},
			wantCode: http.StatusInternalServerError, wantBody: `{"error":"Internal server error"}`, wantLevel: "ERROR",
		},
		{
			name: "re-check error", username: "alice", reason: loginReasonRecheckError,
			setup: func(t *testing.T, repo *scriptedUserRepo, _ *AuthHandler, _ *MockRefreshTokenRepository) {
				seedUser(t, repo.MockUserRepository, "uid-1", "alice", loginFailurePassword, "admin")
				repo.byID = func(call int, u *models.User, err error) (*models.User, error) {
					if call == 2 {
						return nil, errors.New("driver: bad connection")
					}
					return u, err
				}
			},
			wantCode: http.StatusInternalServerError, wantBody: `{"error":"Internal server error"}`, wantLevel: "ERROR",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			repo := &scriptedUserRepo{MockUserRepository: NewMockUserRepository()}
			cfg := testAuthConfig(false)
			if tt.useCache {
				cfg.LoginCacheTTL = time.Hour
			}
			oidcCfg := &config.OIDCConfig{}
			if tt.oidcOnly {
				oidcCfg = &config.OIDCConfig{Enabled: true}
			}
			h := NewAuthHandler(repo, cfg, oidcCfg)
			var buf bytes.Buffer
			h.logger = slog.New(slog.NewTextHandler(&buf, nil))
			refresh := NewMockRefreshTokenRepository()
			if tt.useRefresh {
				h.cfg = testAuthConfigWithRefresh()
				h.SetRefreshTokenRepo(refresh)
			}
			if tt.setup != nil {
				tt.setup(t, repo, h, refresh)
			}
			var storedHash string
			if u, err := repo.MockUserRepository.FindByUsername(tt.username); err == nil {
				storedHash = u.PasswordHash
			}
			if tt.useCache {
				h.loginCache.Set(loginCacheKey(tt.username, storedHash, loginFailurePassword), struct{}{})
			}

			gin.SetMode(gin.TestMode)
			r := gin.New()
			r.Use(middleware.RequestID())
			r.POST("/api/v1/auth/login", h.Login)
			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login",
				strings.NewReader(`{"username":"`+tt.username+`","password":"`+loginFailurePassword+`"}`))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("X-Request-ID", "req-619-test")
			req.RemoteAddr = "203.0.113.7:4711"
			r.ServeHTTP(w, req)

			assert.Equal(t, tt.wantCode, w.Code)
			assert.JSONEq(t, tt.wantBody, w.Body.String(), "the response never shows the reason")

			out := buf.String()
			assert.Equal(t, 1, strings.Count(out, `msg="Login failed"`), "one log line: %s", out)
			assert.Contains(t, out, "level="+tt.wantLevel)
			assert.Contains(t, out, "reason="+tt.reason+" ")
			assert.Contains(t, out, "username="+tt.username+" ")
			if tt.useCache {
				assert.Contains(t, out, "cache_hit=true")
			} else {
				assert.Contains(t, out, "cache_hit=false")
			}
			assert.Contains(t, out, "status="+strconv.Itoa(tt.wantCode))
			assert.Contains(t, out, "client_ip=203.0.113.7 ")
			assert.Contains(t, out, "request_id=req-619-test")
			assert.NotContains(t, out, loginFailurePassword, "never log the password")
			if storedHash != "" {
				assert.NotContains(t, out, storedHash, "never log the password hash")
			}
			assert.NotContains(t, out, "eyJ", "never log a token")
		})
	}
}

// A successful login writes no failure line.
func TestLogin_SuccessLogsNoFailure(t *testing.T) {
	t.Parallel()

	repo := NewMockUserRepository()
	seedUser(t, repo, "uid-1", "alice", loginFailurePassword, "admin")
	h := NewAuthHandler(repo, testAuthConfig(false), &config.OIDCConfig{})
	var buf bytes.Buffer
	h.logger = slog.New(slog.NewTextHandler(&buf, nil))

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.POST("/api/v1/auth/login", h.Login)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login",
		strings.NewReader(`{"username":"alice","password":"`+loginFailurePassword+`"}`))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	assert.Empty(t, buf.String())
}

func TestTruncateForLog(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   string
		want string
		n    int
	}{
		{name: "short", in: "admin", n: 8, want: "admin"},
		{name: "exact", in: "abcd", n: 4, want: "abcd"},
		{name: "cut", in: "abcdef", n: 4, want: "abcd..."},
		{name: "cut at rune start", in: "abé", n: 3, want: "ab..."},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, truncateForLog(tt.in, tt.n))
		})
	}
}

// loginReasonCount sums auth.login.total for method "local", the status and
// the reason ("" matches points without a reason).
func loginReasonCount(t *testing.T, reader sdkmetric.Reader, status, reason string) int64 {
	t.Helper()
	var rm metricdata.ResourceMetrics
	require.NoError(t, reader.Collect(context.Background(), &rm))
	var total int64
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != "auth.login.total" {
				continue
			}
			sum, ok := m.Data.(metricdata.Sum[int64])
			require.True(t, ok)
			for _, dp := range sum.DataPoints {
				me, _ := dp.Attributes.Value(attribute.Key("method"))
				st, _ := dp.Attributes.Value(attribute.Key("status"))
				re, _ := dp.Attributes.Value(attribute.Key("reason"))
				if me.AsString() == "local" && st.AsString() == status && re.AsString() == reason {
					total += dp.Value
				}
			}
		}
	}
	return total
}

// TestLoginHandler_MetricReasons checks the "reason" attribute of
// auth.login.total. Not parallel: it swaps the global meter provider.
func TestLoginHandler_MetricReasons(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	prev := otel.GetMeterProvider()
	otel.SetMeterProvider(mp)
	middleware.RebindAuthMeter()
	t.Cleanup(func() {
		otel.SetMeterProvider(prev)
		middleware.RebindAuthMeter()
		_ = mp.Shutdown(context.Background())
	})

	userRepo := NewMockUserRepository()
	seedUser(t, userRepo, "u1", "alice", "correct-horse", "devops")
	require.NoError(t, userRepo.Create(&models.User{
		ID: "u2", Username: "bob", PasswordHash: hashPassword(t, "pw"), Disabled: true,
	}))
	require.NoError(t, userRepo.Create(&models.User{ID: "u3", Username: "carol", AuthProvider: "oidc"}))
	require.NoError(t, userRepo.Create(&models.User{ID: "u4", Username: "dave", PasswordHash: "not-a-hash"}))
	h := NewAuthHandler(userRepo, testAuthConfig(false), &config.OIDCConfig{})
	h.logger = slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.POST("/api/v1/auth/login", h.Login)

	post := func(body string) {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(httptest.NewRecorder(), req)
	}
	post(`{"username":"ghost","password":"whatever"}`)
	post(`{"username":"alice","password":"wrong"}`)
	post(`{"username":"alice","password":"wrong"}`)
	post(`{"username":"bob","password":"pw"}`)
	post(`{"username":"carol","password":"pw"}`)
	post(`{"username":"dave","password":"pw"}`)
	post(`{"username":"alice","password":"correct-horse"}`)

	tests := []struct {
		status string
		reason string
		want   int64
	}{
		{status: "invalid", reason: loginReasonUserNotFound, want: 1},
		{status: "invalid", reason: loginReasonPasswordMismatch, want: 2},
		{status: "invalid", reason: loginReasonNoPassword, want: 1},
		{status: "failure", reason: loginReasonHashInvalid, want: 1},
		{status: "disabled", reason: loginReasonDisabled, want: 1},
		{status: "success", reason: "", want: 1},
	}
	for _, tt := range tests {
		assert.Equal(t, tt.want, loginReasonCount(t, reader, tt.status, tt.reason), "%s/%s", tt.status, tt.reason)
	}
}
