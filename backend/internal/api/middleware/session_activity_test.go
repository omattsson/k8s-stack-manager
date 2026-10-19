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

func TestSessionActivityInterval(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		idle time.Duration
		want time.Duration
	}{
		{name: "unset idle timeout keeps one minute", idle: 0, want: time.Minute},
		{name: "negative idle timeout keeps one minute", idle: -time.Second, want: time.Minute},
		{name: "default 30m idle timeout caps at one minute", idle: 30 * time.Minute, want: time.Minute},
		{name: "4m idle timeout gives exactly one minute", idle: 4 * time.Minute, want: time.Minute},
		{name: "2m idle timeout gives a quarter", idle: 2 * time.Minute, want: 30 * time.Second},
		{name: "20s idle timeout gives a quarter", idle: 20 * time.Second, want: 5 * time.Second},
		{name: "4s idle timeout gives the floor", idle: 4 * time.Second, want: time.Second},
		{name: "very short idle timeout gives the floor", idle: 2 * time.Second, want: time.Second},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, SessionActivityInterval(tt.idle))
		})
	}
}

// fakeClock is a manually advanced clock for sessionActivityTracker.now.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// timedActivityRecorder records the activity times that the tracker writes.
type timedActivityRecorder struct {
	mu  sync.Mutex
	ats []time.Time
}

func (r *timedActivityRecorder) fn(_ context.Context, _ string, at time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ats = append(r.ats, at)
	return nil
}

func (r *timedActivityRecorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.ats)
}

func (r *timedActivityRecorder) last() time.Time {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.ats[len(r.ats)-1]
}

func TestNewSessionActivityTracker_Interval(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		interval time.Duration
		want     time.Duration
	}{
		{name: "zero keeps one minute", interval: 0, want: time.Minute},
		{name: "negative keeps one minute", interval: -time.Second, want: time.Minute},
		{name: "explicit interval is used", interval: 5 * time.Second, want: 5 * time.Second},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			tr := newSessionActivityTracker(newActivityRecorder().fn, tt.interval)
			require.NotNil(t, tr)
			assert.Equal(t, tt.want, tr.interval)
		})
	}

	assert.Nil(t, newSessionActivityTracker(nil, time.Second), "nil hook gives a nil tracker")
}

func TestSessionActivityTracker_Throttle(t *testing.T) {
	t.Parallel()

	const interval = 5 * time.Second
	tests := []struct {
		name      string
		steps     []time.Duration // clock advance before each touch
		wantCalls int
	}{
		{name: "two touches within the interval write once", steps: []time.Duration{0, interval - time.Millisecond}, wantCalls: 1},
		{name: "touch after the interval writes again", steps: []time.Duration{0, interval}, wantCalls: 2},
		{name: "touches within then after the interval", steps: []time.Duration{0, time.Second, time.Second, 3 * time.Second}, wantCalls: 2},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			rec := &timedActivityRecorder{}
			clock := &fakeClock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
			tr := newSessionActivityTracker(rec.fn, interval)
			tr.now = clock.Now

			for _, d := range tt.steps {
				clock.Advance(d)
				tr.touch(context.Background(), "fam-throttle")
			}

			assert.Eventually(t, func() bool { return rec.count() == tt.wantCalls },
				2*time.Second, 5*time.Millisecond)
			time.Sleep(20 * time.Millisecond)
			assert.Equal(t, tt.wantCalls, rec.count())
		})
	}
}

// An active session must never look idle to the refresh handler, which
// rejects a refresh when now - LastActivity > SESSION_IDLE_TIMEOUT. The test
// simulates a request every 3s for 2 minutes with the e2e settings
// (idle=20s) and checks the recorded activity lag at every request. The
// fixed one-minute interval (the old behavior) fails this check.
func TestSessionActivityTracker_ActiveSessionStaysWithinIdleTimeout(t *testing.T) {
	t.Parallel()

	const idle = 20 * time.Second
	const requestEvery = 3 * time.Second
	tests := []struct {
		name     string
		interval time.Duration
		wantIdle bool // true: the recorded activity falls behind the idle timeout
	}{
		{name: "interval derived from idle timeout", interval: SessionActivityInterval(idle), wantIdle: false},
		{name: "fixed one-minute interval", interval: time.Minute, wantIdle: true},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			rec := &timedActivityRecorder{}
			clock := &fakeClock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
			tr := newSessionActivityTracker(rec.fn, tt.interval)
			tr.now = clock.Now

			idleSeen := false
			for elapsed := time.Duration(0); elapsed <= 2*time.Minute; elapsed += requestEvery {
				before := rec.count()
				tr.touch(context.Background(), "fam-active")
				// The tracker stores the time of each scheduled write; wait
				// for the async write when this touch scheduled one.
				if prev, ok := tr.lastSeen.Get("fam-active"); ok && prev.Equal(clock.Now().UTC()) {
					require.Eventually(t, func() bool { return rec.count() > before },
						2*time.Second, time.Millisecond)
				}
				// A refresh right before the next request.
				refreshAt := clock.Now().Add(requestEvery - time.Millisecond)
				if refreshAt.Sub(rec.last()) > idle {
					idleSeen = true
				}
				clock.Advance(requestEvery)
			}
			assert.Equal(t, tt.wantIdle, idleSeen)
		})
	}
}

// The interval set in JWTAuthOptions and APIKeyAuthDeps reaches the tracker:
// with a 30ms interval a second request after 40ms writes again, with the
// default (one minute) it does not.
func TestAuthMiddleware_SessionActivityIntervalOption(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		combined  bool
		interval  time.Duration
		wantCalls int
	}{
		{name: "JWT options short interval writes twice", interval: 30 * time.Millisecond, wantCalls: 2},
		{name: "JWT options default interval writes once", interval: 0, wantCalls: 1},
		{name: "combined deps short interval writes twice", combined: true, interval: 30 * time.Millisecond, wantCalls: 2},
		{name: "combined deps default interval writes once", combined: true, interval: 0, wantCalls: 1},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			rec := newActivityRecorder()
			var r *gin.Engine
			if tt.combined {
				r = buildCombinedAuthRouter(APIKeyAuthDeps{
					JWTSecret:               combinedTestSecret,
					APIKeyRepo:              newTestAPIKeyRepo(),
					UserRepo:                newTestUserRepo(),
					OnSessionActivity:       rec.fn,
					SessionActivityInterval: tt.interval,
				})
			} else {
				gin.SetMode(gin.TestMode)
				r = gin.New()
				r.Use(AuthRequiredWithOptions(JWTAuthOptions{
					JWTSecret:               combinedTestSecret,
					OnSessionActivity:       rec.fn,
					SessionActivityInterval: tt.interval,
				}))
				r.GET("/test", func(c *gin.Context) { c.Status(http.StatusOK) })
			}

			headers := map[string]string{"Authorization": "Bearer " + makeSessionJWT(t, "fam-option")}
			require.Equal(t, http.StatusOK, doGET(r, headers).Code)
			assert.Eventually(t, func() bool { return rec.total() == 1 }, 2*time.Second, 5*time.Millisecond)
			time.Sleep(40 * time.Millisecond)
			require.Equal(t, http.StatusOK, doGET(r, headers).Code)

			if tt.wantCalls > 1 {
				assert.Eventually(t, func() bool { return rec.total() == tt.wantCalls },
					2*time.Second, 5*time.Millisecond)
			}
			time.Sleep(20 * time.Millisecond)
			assert.Equal(t, tt.wantCalls, rec.total())
		})
	}
}
