package middleware

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"backend/internal/sessionstore"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// activityRecorder is a SessionActivityFunc test double that counts calls per
// session ID.
type activityRecorder struct {
	mu    sync.Mutex
	calls map[string]int
	err   error
}

func newActivityRecorder() *activityRecorder {
	return &activityRecorder{calls: make(map[string]int)}
}

func (r *activityRecorder) fn(_ context.Context, sessionID string, _ time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls[sessionID]++
	return r.err
}

func (r *activityRecorder) count(sessionID string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls[sessionID]
}

func (r *activityRecorder) total() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, c := range r.calls {
		n += c
	}
	return n
}

func makeSessionJWT(t *testing.T, sessionID string) string {
	t.Helper()
	token, err := GenerateTokenWithOpts(GenerateTokenOptions{
		UserID:     "user-1",
		Username:   "alice",
		Role:       "user",
		Secret:     combinedTestSecret,
		Expiration: time.Hour,
		SessionID:  sessionID,
	})
	require.NoError(t, err)
	return token
}

func TestGenerateTokenWithOpts_SessionIDClaim(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		sessionID string
	}{
		{name: "sid set", sessionID: "family-1"},
		{name: "sid omitted", sessionID: ""},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			claims, err := ValidateJWT(makeSessionJWT(t, tt.sessionID), combinedTestSecret)
			require.NoError(t, err)
			assert.Equal(t, tt.sessionID, claims.SessionID)
		})
	}
}

func TestAuthRequiredWithOptions_SessionActivity(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		sessionID string // "" = token without sid
		requests  int
		blockJTI  bool
		hookErr   error
		wantCalls int
		wantCode  int
	}{
		{name: "token with sid records activity once per minute", sessionID: "fam-a", requests: 3, wantCalls: 1, wantCode: http.StatusOK},
		{name: "token without sid records nothing", requests: 2, wantCalls: 0, wantCode: http.StatusOK},
		{name: "revoked token records nothing", sessionID: "fam-b", requests: 1, blockJTI: true, wantCalls: 0, wantCode: http.StatusUnauthorized},
		{name: "hook error does not fail the request", sessionID: "fam-c", requests: 1, hookErr: errors.New("db down"), wantCalls: 1, wantCode: http.StatusOK},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			rec := newActivityRecorder()
			rec.err = tt.hookErr
			store := sessionstore.NewMemoryStore()
			t.Cleanup(store.Stop)

			token := makeSessionJWT(t, tt.sessionID)
			if tt.blockJTI {
				claims, err := ValidateJWT(token, combinedTestSecret)
				require.NoError(t, err)
				require.NoError(t, store.BlockToken(context.Background(), claims.ID, time.Now().Add(time.Hour)))
			}

			gin.SetMode(gin.TestMode)
			r := gin.New()
			r.Use(AuthRequiredWithOptions(JWTAuthOptions{
				JWTSecret:         combinedTestSecret,
				SessionStore:      store,
				OnSessionActivity: rec.fn,
			}))
			r.GET("/test", func(c *gin.Context) {
				c.JSON(http.StatusOK, gin.H{"sid": GetSessionIDFromContext(c)})
			})

			for i := 0; i < tt.requests; i++ {
				w := doGET(r, map[string]string{"Authorization": "Bearer " + token})
				assert.Equal(t, tt.wantCode, w.Code)
			}

			if tt.wantCalls > 0 {
				assert.Eventually(t, func() bool { return rec.count(tt.sessionID) == tt.wantCalls },
					2*time.Second, 10*time.Millisecond)
			}
			// Give a stray async call time to show up before checking the upper bound.
			time.Sleep(50 * time.Millisecond)
			assert.Equal(t, tt.wantCalls, rec.total())
		})
	}
}

func TestCombinedAuth_SessionActivity(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		useAPIKey bool
		wantCalls int
	}{
		{name: "JWT with sid records activity", wantCalls: 1},
		{name: "API key never records session activity", useAPIKey: true, wantCalls: 0},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			rec := newActivityRecorder()
			apiKeyRepo := newTestAPIKeyRepo()
			userRepo := newTestUserRepo()
			rawKey := seedKeyInRepo(t, apiKeyRepo, userRepo, "key-1", "user-1", "alice", "user", nil)

			r := buildCombinedAuthRouter(APIKeyAuthDeps{
				JWTSecret:         combinedTestSecret,
				APIKeyRepo:        apiKeyRepo,
				UserRepo:          userRepo,
				OnSessionActivity: rec.fn,
			})

			headers := map[string]string{"Authorization": "Bearer " + makeSessionJWT(t, "fam-combined")}
			if tt.useAPIKey {
				headers = map[string]string{"X-API-Key": "sk_" + rawKey}
			}
			w := doGET(r, headers)
			require.Equal(t, http.StatusOK, w.Code)

			if tt.wantCalls > 0 {
				assert.Eventually(t, func() bool { return rec.total() == tt.wantCalls },
					2*time.Second, 10*time.Millisecond)
			}
			time.Sleep(50 * time.Millisecond)
			assert.Equal(t, tt.wantCalls, rec.total())
		})
	}
}

func TestAuthRequiredWithOptions_NilHook(t *testing.T) {
	t.Parallel()

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(AuthRequiredWithOptions(JWTAuthOptions{JWTSecret: combinedTestSecret}))
	r.GET("/test", func(c *gin.Context) { c.Status(http.StatusOK) })

	w := doGET(r, map[string]string{"Authorization": "Bearer " + makeSessionJWT(t, "fam-nil")})
	assert.Equal(t, http.StatusOK, w.Code)
}

// The activity write gets its own deadline and is not cancelled with the
// request: the request context here is cancelled while the request runs, so
// the test fails if the middleware drops context.WithoutCancel.
func TestAuthRequiredWithOptions_SessionActivityDeadline(t *testing.T) {
	t.Parallel()

	type result struct {
		deadline time.Time
		ok       bool
		err      error
	}
	got := make(chan result, 1)
	release := make(chan struct{})
	hook := func(ctx context.Context, _ string, _ time.Time) error {
		<-release // run after the request context is cancelled
		d, ok := ctx.Deadline()
		got <- result{deadline: d, ok: ok, err: ctx.Err()}
		return nil
	}

	reqCtx, cancelReq := context.WithCancel(context.Background())
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(AuthRequiredWithOptions(JWTAuthOptions{JWTSecret: combinedTestSecret, OnSessionActivity: hook}))
	r.GET("/test", func(c *gin.Context) {
		cancelReq() // client goes away while the handler runs
		c.Status(http.StatusOK)
	})

	start := time.Now()
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/test", nil).WithContext(reqCtx)
	req.Header.Set("Authorization", "Bearer "+makeSessionJWT(t, "fam-deadline"))
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)
	require.Error(t, reqCtx.Err(), "request context must be cancelled before the hook runs")
	close(release)

	select {
	case res := <-got:
		require.True(t, res.ok, "activity context must have a deadline")
		assert.NoError(t, res.err, "activity context must not be cancelled with the request")
		assert.WithinDuration(t, start.Add(sessionActivityTimeout), res.deadline, time.Second)
	case <-time.After(2 * time.Second):
		t.Fatal("activity hook was not called")
	}
}
