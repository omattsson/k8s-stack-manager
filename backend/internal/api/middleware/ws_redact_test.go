package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRedactWSToken_StripsTokenAndStashesInContext(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	var (
		seenRawQuery   string
		seenRequestURI string
		stashedToken   string
	)
	router := gin.New()
	router.Use(RedactWSToken())
	// Downstream middleware sees the URL AFTER redaction — this is the
	// observation point that simulates otelgin / Logger / metrics
	// reading the URL for their attribute / log payload.
	router.Use(func(c *gin.Context) {
		seenRawQuery = c.Request.URL.RawQuery
		seenRequestURI = c.Request.RequestURI
		c.Next()
	})
	router.GET("/ws", func(c *gin.Context) {
		stashedToken = WSTokenFromContext(c)
		c.Status(http.StatusOK)
	})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/ws?token=secret-jwt&keep=this", nil)
	req.RequestURI = req.URL.RequestURI()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "secret-jwt", stashedToken, "handler must see the token via WSTokenFromContext")
	assert.NotContains(t, seenRawQuery, "token=", "downstream middleware must NOT see the token query param")
	assert.NotContains(t, seenRawQuery, "secret-jwt", "downstream middleware must NOT see the raw token value")
	assert.Contains(t, seenRawQuery, "keep=this", "non-sensitive query params must survive")
	assert.NotContains(t, seenRequestURI, "secret-jwt", "RequestURI must also be scrubbed for middleware that reads it instead of RawQuery")
}

func TestRedactWSToken_PassthroughForOtherPaths(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	var seenRawQuery string
	router := gin.New()
	router.Use(RedactWSToken())
	router.Use(func(c *gin.Context) {
		seenRawQuery = c.Request.URL.RawQuery
		c.Next()
	})
	router.GET("/api/v1/audit-logs", func(c *gin.Context) {
		// Audit logs uses a `cursor` param that is not sensitive; ensure
		// the middleware never touches non-/ws traffic.
		c.Status(http.StatusOK)
	})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/audit-logs?token=should-pass-through&cursor=xyz", nil)
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, seenRawQuery, "token=should-pass-through",
		"middleware must not strip token from paths other than /ws — query semantics are path-specific")
	assert.Contains(t, seenRawQuery, "cursor=xyz")
}

func TestRedactWSToken_NoTokenIsNoOp(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	var (
		seenRawQuery string
		stashed      string
	)
	router := gin.New()
	router.Use(RedactWSToken())
	router.Use(func(c *gin.Context) {
		seenRawQuery = c.Request.URL.RawQuery
		c.Next()
	})
	router.GET("/ws", func(c *gin.Context) {
		stashed = WSTokenFromContext(c)
		c.Status(http.StatusOK)
	})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/ws?keep=this", nil)
	router.ServeHTTP(w, req)

	assert.Equal(t, "keep=this", seenRawQuery)
	assert.Empty(t, stashed, "no token in URL → empty stash")
}

func TestRedactWSToken_MatchesDescendantPathsButNotLookalikes(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	// Locks the path-matching contract documented in RedactWSToken:
	// exact `/ws` and `/ws/<anything>` get scrubbed; `/wso`, `/wsdebug`,
	// `/ws-debug` do NOT (the second segment must be path-separated).
	cases := []struct {
		path       string
		wantScrub  bool
	}{
		{path: "/ws", wantScrub: true},
		{path: "/ws/v2", wantScrub: true},
		{path: "/ws/foo/bar", wantScrub: true},
		{path: "/wsfoo", wantScrub: false},
		{path: "/wso", wantScrub: false},
		{path: "/ws-debug", wantScrub: false},
		{path: "/api/v1/audit-logs", wantScrub: false},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.path, func(t *testing.T) {
			t.Parallel()
			var seenRawQuery string
			router := gin.New()
			router.Use(RedactWSToken())
			router.Use(func(c *gin.Context) {
				seenRawQuery = c.Request.URL.RawQuery
				c.Next()
			})
			router.GET(tc.path, func(c *gin.Context) { c.Status(http.StatusOK) })

			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, tc.path+"?token=jwt", nil)
			router.ServeHTTP(w, req)

			if tc.wantScrub {
				assert.NotContains(t, seenRawQuery, "token=", "expected %s to be scrubbed", tc.path)
			} else {
				assert.Contains(t, seenRawQuery, "token=jwt", "expected %s to pass through", tc.path)
			}
		})
	}
}

func TestWSTokenFromContext_EmptyWithoutMiddleware(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodGet, "/ws?token=foo", nil)

	// No middleware ran → context key was never set → empty.
	assert.Empty(t, WSTokenFromContext(c))
}

func TestWSTokenFromContext_HandlesWrongType(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	// Defensive: if someone (a future test, a misbehaving middleware)
	// stashes a non-string under the key, the helper must NOT panic.
	c.Set(wsContextKey, 12345)
	require.NotPanics(t, func() {
		assert.Empty(t, WSTokenFromContext(c))
	})
}
