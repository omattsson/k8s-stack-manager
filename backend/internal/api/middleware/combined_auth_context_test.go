package middleware

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"backend/internal/models"
	"backend/pkg/dberrors"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type requestCtxKey struct{}

// ctxAPIKeyRepo records the contexts of the API key calls. UpdateLastUsed
// blocks until release is closed, so the test sees its context before the
// write ends.
type ctxAPIKeyRepo struct {
	models.APIKeyRepository
	records   []*models.APIKey
	prefixCtx chan context.Context
	lastUsed  chan context.Context
	release   chan struct{}
}

func (r *ctxAPIKeyRepo) FindByPrefix(ctx context.Context, _ string) ([]*models.APIKey, error) {
	r.prefixCtx <- ctx
	return r.records, nil
}

func (r *ctxAPIKeyRepo) UpdateLastUsed(ctx context.Context, _, _ string, _ time.Time) error {
	r.lastUsed <- ctx
	<-r.release
	return nil
}

// TestCombinedAuth_APIKeyRequestContext checks that the key lookup runs with
// the request context, and that the last-used write after the response has
// the request values but neither its cancel nor an unbounded lifetime (#656).
func TestCombinedAuth_APIKeyRequestContext(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	rawKey, prefix, hash, err := models.GenerateAPIKey()
	require.NoError(t, err)
	repo := &ctxAPIKeyRepo{
		records:   []*models.APIKey{{ID: "k-ctx-" + prefix, UserID: "u1", Prefix: prefix, KeyHash: hash}},
		prefixCtx: make(chan context.Context, 1),
		lastUsed:  make(chan context.Context, 1),
		release:   make(chan struct{}),
	}
	t.Cleanup(func() { close(repo.release) })

	r := gin.New()
	r.Use(CombinedAuth(APIKeyAuthDeps{
		APIKeyRepo: repo,
		UserRepo:   &stubUserRepo{user: &models.User{ID: "u1", Username: "svc", Role: "user"}},
	}))
	r.GET("/x", func(c *gin.Context) { c.Status(http.StatusOK) })

	reqCtx, cancelReq := context.WithCancel(context.WithValue(context.Background(), requestCtxKey{}, "req-1"))
	defer cancelReq()
	req := httptest.NewRequest(http.MethodGet, "/x", nil).WithContext(reqCtx)
	req.Header.Set("X-API-Key", "sk_"+rawKey)
	w := httptest.NewRecorder()
	start := time.Now()
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	prefixCtx := <-repo.prefixCtx
	assert.Equal(t, "req-1", prefixCtx.Value(requestCtxKey{}), "the lookup gets the request context")

	cancelReq()
	assert.ErrorIs(t, prefixCtx.Err(), context.Canceled, "the lookup context follows the request cancel")
	var lastUsedCtx context.Context
	select {
	case lastUsedCtx = <-repo.lastUsed:
	case <-time.After(5 * time.Second):
		t.Fatal("UpdateLastUsed was not called")
	}
	assert.Equal(t, "req-1", lastUsedCtx.Value(requestCtxKey{}), "the write keeps the request values")
	select {
	case <-lastUsedCtx.Done():
		t.Fatal("the request cancel reached the last-used write")
	default:
	}
	deadline, ok := lastUsedCtx.Deadline()
	require.True(t, ok, "the last-used write has a deadline")
	assert.WithinDuration(t, start.Add(lastUsedUpdateTimeout), deadline, time.Second)
}

// TestAuthenticateAPIKey_CancelledLookup checks a request context that is
// cancelled during the key lookup or before the hash verify: 499, a debug
// line, no error line, and no Argon2id verify.
// Not parallel: it replaces the default slog logger.
func TestAuthenticateAPIKey_CancelledLookup(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	rawKey, prefix, hash, err := models.GenerateAPIKey()
	require.NoError(t, err)
	argonHash, err := models.HashAPIKeyWithSalt(rawKey)
	require.NoError(t, err)
	cancelledCtx, cancel := context.WithCancel(context.Background())
	cancel()

	tests := []struct {
		ctx     context.Context
		repo    *stubAPIKeyRepo
		userErr error
		name    string
		wantLog string
	}{
		{
			name:    "lookup context error",
			ctx:     context.Background(),
			repo:    &stubAPIKeyRepo{err: context.Canceled},
			wantLog: `level=DEBUG msg="API key lookup cancelled"`,
		},
		{
			name:    "lookup database error",
			ctx:     context.Background(),
			repo:    &stubAPIKeyRepo{err: dberrors.NewDatabaseError("find_by_prefix", context.Canceled)},
			wantLog: `level=DEBUG msg="API key lookup cancelled"`,
		},
		{
			name: "cancelled before the Argon2id verify",
			ctx:  cancelledCtx,
			repo: &stubAPIKeyRepo{records: []*models.APIKey{
				{ID: "k-argon-cancel", UserID: "u1", Prefix: prefix, KeyHash: argonHash},
			}},
			wantLog: `level=DEBUG msg="API key verify cancelled"`,
		},
		{
			name: "user lookup cancelled",
			ctx:  context.Background(),
			repo: &stubAPIKeyRepo{records: []*models.APIKey{
				{ID: "k-user-cancel", UserID: "u1", Prefix: prefix, KeyHash: hash},
			}},
			userErr: dberrors.NewDatabaseError("find_by_id", context.Canceled),
			wantLog: `level=DEBUG msg="API key user lookup cancelled"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			buf.Reset()
			limiter := newVerifyLimiter(1, time.Millisecond)
			key, user, failure := authenticateAPIKey(tt.ctx, tt.repo,
				&stubUserRepo{user: &models.User{ID: "u1", Username: "svc", Role: "user"}, err: tt.userErr},
				"sk_"+rawKey, limiter)

			assert.Nil(t, key)
			assert.Nil(t, user)
			require.NotNil(t, failure)
			assert.Equal(t, statusClientClosedRequest, failure.Status)
			assert.Equal(t, "Client closed request", failure.Message)
			assert.Equal(t, "cancelled", failure.Outcome)
			assert.Contains(t, buf.String(), tt.wantLog)
			assert.NotContains(t, buf.String(), "level=ERROR")
			assert.Empty(t, limiter.slots, "no verify slot is held")
		})
	}
}

// ctxUserRepo records the context of the API-key user lookup.
type ctxUserRepo struct {
	models.UserRepository
	user *models.User
	ctxs chan context.Context
}

func (r *ctxUserRepo) FindByID(ctx context.Context, _ string) (*models.User, error) {
	r.ctxs <- ctx
	return r.user, nil
}

// TestCombinedAuth_APIKeyUserLookupRequestContext checks that the user
// lookup of the API-key auth runs with the request context (#656).
func TestCombinedAuth_APIKeyUserLookupRequestContext(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	rawKey, prefix, hash, err := models.GenerateAPIKey()
	require.NoError(t, err)
	users := &ctxUserRepo{user: &models.User{ID: "u1", Username: "svc", Role: "user"}, ctxs: make(chan context.Context, 1)}
	r := gin.New()
	r.Use(CombinedAuth(APIKeyAuthDeps{
		APIKeyRepo: &stubAPIKeyRepo{records: []*models.APIKey{{ID: "k-user-ctx-" + prefix, UserID: "u1", Prefix: prefix, KeyHash: hash}}},
		UserRepo:   users,
	}))
	r.GET("/x", func(c *gin.Context) { c.Status(http.StatusOK) })

	reqCtx, cancelReq := context.WithCancel(context.WithValue(context.Background(), requestCtxKey{}, "req-user"))
	defer cancelReq()
	req := httptest.NewRequest(http.MethodGet, "/x", nil).WithContext(reqCtx)
	req.Header.Set("X-API-Key", "sk_"+rawKey)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	userCtx := <-users.ctxs
	assert.Equal(t, "req-user", userCtx.Value(requestCtxKey{}), "the user lookup gets the request context")
	cancelReq()
	assert.ErrorIs(t, userCtx.Err(), context.Canceled, "the user lookup context follows the request cancel")
}
