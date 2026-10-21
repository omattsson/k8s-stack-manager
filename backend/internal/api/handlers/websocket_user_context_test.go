package handlers

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"backend/internal/api/middleware"
	"backend/internal/models"
	"backend/internal/sessionstore"
	"backend/internal/websocket"
	"backend/pkg/dberrors"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCheckSession_UserLookupContext checks that the user lookup of the
// WebSocket upgrade runs with the request context, and that a client that
// left gets 499 and no error line (#656).
// Not parallel: it replaces the default slog logger.
func TestCheckSession_UserLookupContext(t *testing.T) {
	var logBuf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	tests := []struct {
		name       string
		cancelled  bool
		wantOK     bool
		wantStatus int
	}{
		{name: "live request", wantOK: true, wantStatus: http.StatusOK},
		{name: "cancelled request", cancelled: true, wantStatus: statusClientClosedRequest},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			logBuf.Reset()
			base := NewMockUserRepository()
			seedUser(t, base, "u1", "alice", "secret-pass", "user")
			repo := &hookUserRepo{MockUserRepository: base}
			var lookupCtx context.Context
			repo.setHook(func(method string, ctx context.Context) {
				if method == "FindByID" {
					lookupCtx = ctx
				}
			})
			h := NewWebSocketHandler(nil, "*", wsTestJWTSecret).WithRevocationChecks(newMockHandlerSessionStore(), repo)

			ctx, cancel := context.WithCancel(context.WithValue(context.Background(), userCtxMarkerKey{}, "ws-req"))
			defer cancel()
			if tt.cancelled {
				cancel()
			}
			gin.SetMode(gin.TestMode)
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = httptest.NewRequest(http.MethodGet, "/ws", nil).WithContext(ctx)
			ok := h.checkSession(c, &middleware.Claims{UserID: "u1"})
			if ok {
				c.Status(http.StatusOK)
				c.Writer.WriteHeaderNow()
			}

			assert.Equal(t, tt.wantOK, ok)
			assert.Equal(t, tt.wantStatus, w.Code, w.Body.String())
			require.NotNil(t, lookupCtx)
			assert.Equal(t, "ws-req", lookupCtx.Value(userCtxMarkerKey{}), "the lookup gets the request context")
			assert.NotContains(t, logBuf.String(), "level=ERROR")
		})
	}
}

// TestAPIKeySocketRevoked_UserLookupContext checks that the user re-check
// after an API-key upgrade runs with the context of the upgrade (#656).
func TestAPIKeySocketRevoked_UserLookupContext(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		userID      string
		keyID       string
		wantRevoked bool
	}{
		{name: "active user", userID: "u-active", keyID: "key-ok"},
		{name: "disabled user", userID: "u-disabled", keyID: "key-disabled", wantRevoked: true},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newWSKeyFixture(t)
			repo := &hookUserRepo{MockUserRepository: f.users}
			var markers []any
			repo.setHook(func(method string, ctx context.Context) {
				if method == "FindByID" {
					markers = append(markers, ctx.Value(userCtxMarkerKey{}))
				}
			})
			h := NewWebSocketHandler(nil, "*", wsTestJWTSecret).
				WithRevocationChecks(newMockHandlerSessionStore(), repo).
				WithAPIKeys(f.keys)
			ctx := context.WithValue(context.Background(), userCtxMarkerKey{}, "ws-upgrade")

			revoked := h.apiKeySocketRevoked(ctx, websocket.ClientIdentity{UserID: tt.userID, APIKeyID: tt.keyID})

			assert.Equal(t, tt.wantRevoked, revoked)
			assert.Equal(t, []any{"ws-upgrade"}, markers, "the user lookup gets the upgrade context")
		})
	}
}

// TestWebSocketRevocationChecker_UserLookupContext checks that the
// revalidation looks up users with the context of the checker, and that a
// cancel (hub shutdown) during a lookup stops the run without an error line
// and without closing sockets (#656).
// Not parallel: it replaces the default slog logger.
func TestWebSocketRevocationChecker_UserLookupContext(t *testing.T) {
	var logBuf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	base := NewMockUserRepository()
	seedUser(t, base, "u-active", "alice", "secret-pass", "user")
	seedUser(t, base, "u-disabled", "bob", "secret-pass", "user")
	base.mu.Lock()
	base.users["u-disabled"].Disabled = true
	base.mu.Unlock()
	identities := []websocket.ClientIdentity{
		{UserID: "u-active", TokenID: "jti-1", IssuedAt: time.Now()},
		{UserID: "u-disabled", TokenID: "jti-2", IssuedAt: time.Now()},
	}

	t.Run("live context", func(t *testing.T) {
		logBuf.Reset()
		repo := &hookUserRepo{MockUserRepository: base}
		var seen []any
		repo.setHook(func(method string, ctx context.Context) {
			seen = append(seen, ctx.Value(userCtxMarkerKey{}))
		})
		check := NewWebSocketRevocationChecker(nil, repo, nil)
		ctx := context.WithValue(context.Background(), userCtxMarkerKey{}, "revalidate")

		revoked := check(ctx, identities)

		assert.Equal(t, []bool{false, true}, revoked)
		assert.Equal(t, []any{"revalidate", "revalidate"}, seen, "each lookup gets the checker context")
	})

	t.Run("cancel during a lookup", func(t *testing.T) {
		logBuf.Reset()
		repo := &hookUserRepo{MockUserRepository: base}
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		repo.setHook(cancelOnCall("FindByID", 1, cancel))
		check := NewWebSocketRevocationChecker(nil, repo, nil)

		revoked := check(ctx, identities)

		assert.Equal(t, []bool{false, false}, revoked, "no socket is closed")
		assert.Contains(t, logBuf.String(), "WebSocket revalidation cancelled")
		assert.NotContains(t, logBuf.String(), "level=ERROR")
	})
}

// endingKeyRepo ends the run during the key lookup and returns the context
// error, as the GORM repository does.
type endingKeyRepo struct {
	models.APIKeyRepository
	end func(ctx context.Context)
}

func (r *endingKeyRepo) FindByIDs(ctx context.Context, _ []string) ([]*models.APIKey, error) {
	r.end(ctx)
	return nil, dberrors.NewDatabaseError("find_by_ids", ctx.Err())
}

// endingStore ends the run during the blocklist read.
type endingStore struct {
	sessionstore.SessionStore
	end func(ctx context.Context)
}

func (s *endingStore) IsTokenBlocked(ctx context.Context, _ string) (bool, error) {
	s.end(ctx)
	return false, ctx.Err()
}

func (s *endingStore) IsUserBlocked(ctx context.Context, _ string, _ time.Time) (bool, error) {
	return false, ctx.Err()
}

// TestWebSocketRevocationChecker_CancelOrDeadline checks the log level when
// the run context ends during a lookup: a cancel (hub shutdown) logs at
// debug, a passed deadline (the time limit of the run) logs at error. No
// socket is closed in either case (#656).
// Not parallel: it replaces the default slog logger.
func TestWebSocketRevocationChecker_CancelOrDeadline(t *testing.T) {
	var logBuf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	users := NewMockUserRepository()
	seedUser(t, users, "u1", "alice", "secret-pass", "user")

	sites := []struct {
		checker  func(end func(context.Context)) websocket.IdentityChecker
		identity websocket.ClientIdentity
		name     string
		errorMsg string
	}{
		{
			name:     "API key lookup",
			identity: websocket.ClientIdentity{UserID: "u1", APIKeyID: "k1"},
			errorMsg: "WebSocket revalidation stopped: API key lookup failed; the next run retries",
			checker: func(end func(context.Context)) websocket.IdentityChecker {
				return NewWebSocketRevocationChecker(nil, nil, &endingKeyRepo{end: end})
			},
		},
		{
			name:     "session store check",
			identity: websocket.ClientIdentity{UserID: "u1", TokenID: "jti-1", IssuedAt: time.Now()},
			errorMsg: "WebSocket revalidation stopped: session store check failed; the next run retries",
			checker: func(end func(context.Context)) websocket.IdentityChecker {
				return NewWebSocketRevocationChecker(&endingStore{end: end}, nil, nil)
			},
		},
		{
			name:     "user lookup",
			identity: websocket.ClientIdentity{UserID: "u1", TokenID: "jti-1", IssuedAt: time.Now()},
			errorMsg: "WebSocket revalidation stopped: user lookup failed; the next run retries",
			checker: func(end func(context.Context)) websocket.IdentityChecker {
				repo := &hookUserRepo{MockUserRepository: users}
				repo.setHook(func(_ string, ctx context.Context) { end(ctx) })
				return NewWebSocketRevocationChecker(nil, repo, nil)
			},
		},
	}
	ends := []struct {
		ctx       func() (context.Context, func(context.Context), context.CancelFunc)
		name      string
		wantError bool
	}{
		{
			name: "cancel",
			ctx: func() (context.Context, func(context.Context), context.CancelFunc) {
				ctx, cancel := context.WithCancel(context.Background())
				return ctx, func(context.Context) { cancel() }, cancel
			},
		},
		{
			name: "deadline", wantError: true,
			ctx: func() (context.Context, func(context.Context), context.CancelFunc) {
				ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
				return ctx, func(c context.Context) { <-c.Done() }, cancel
			},
		},
	}

	for _, site := range sites {
		for _, end := range ends {
			t.Run(site.name+", "+end.name, func(t *testing.T) {
				logBuf.Reset()
				ctx, endFn, cancel := end.ctx()
				defer cancel()

				revoked := site.checker(endFn)(ctx, []websocket.ClientIdentity{site.identity})

				assert.Equal(t, []bool{false}, revoked, "no socket is closed")
				out := logBuf.String()
				if end.wantError {
					assert.Contains(t, out, `level=ERROR msg="`+site.errorMsg+`"`)
					assert.NotContains(t, out, "WebSocket revalidation cancelled")
				} else {
					assert.Contains(t, out, `level=DEBUG msg="WebSocket revalidation cancelled"`)
					assert.NotContains(t, out, "level=ERROR")
				}
			})
		}
	}
}

// TestAPIKeySocketRevoked_CancelOrDeadline checks the log level of the
// re-checks after an API-key upgrade: a cancelled upgrade context logs at
// debug, a passed deadline at error. Both fail open (#656).
// Not parallel: it replaces the default slog logger.
func TestAPIKeySocketRevoked_CancelOrDeadline(t *testing.T) {
	var logBuf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	tests := []struct {
		ctx       func() (context.Context, context.CancelFunc)
		name      string
		wantLevel string
	}{
		{name: "cancelled", wantLevel: "DEBUG", ctx: func() (context.Context, context.CancelFunc) {
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			return ctx, cancel
		}},
		{name: "deadline passed", wantLevel: "ERROR", ctx: func() (context.Context, context.CancelFunc) {
			return context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			logBuf.Reset()
			f := newWSKeyFixture(t)
			h := NewWebSocketHandler(nil, "*", wsTestJWTSecret).
				WithRevocationChecks(newMockHandlerSessionStore(), f.users).
				WithAPIKeys(f.keys)
			ctx, cancel := tt.ctx()
			defer cancel()

			revoked := h.apiKeySocketRevoked(ctx, websocket.ClientIdentity{UserID: "u-active", APIKeyID: "key-ok"})

			assert.False(t, revoked, "a failed re-check passes")
			out := logBuf.String()
			assert.Contains(t, out, "level="+tt.wantLevel+` msg="WebSocket API key re-check failed"`)
			assert.Contains(t, out, "level="+tt.wantLevel+` msg="WebSocket API key user re-check failed"`)
			if tt.wantLevel == "DEBUG" {
				assert.NotContains(t, out, "level=ERROR")
			}
		})
	}
}
