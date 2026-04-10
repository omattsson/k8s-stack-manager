package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"backend/internal/health"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
)

func init() {
	gin.SetMode(gin.TestMode)
}

func TestHealthCheckHandler(t *testing.T) {
	t.Parallel()

	r := gin.New()
	r.GET("/health", HealthCheck)

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/health", nil)
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var response map[string]string
	err := json.Unmarshal(w.Body.Bytes(), &response)

	assert.Nil(t, err)
	assert.Equal(t, "ok", response["status"])
}

func TestPingHandler(t *testing.T) {
	t.Parallel()

	r := gin.New()
	r.GET("/ping", Ping)

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/ping", nil)
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var response map[string]string
	err := json.Unmarshal(w.Body.Bytes(), &response)

	assert.Nil(t, err)
	assert.Equal(t, "pong", response["message"])
}

func TestLivenessHandler(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		wantStatus int
		wantField  string
		wantValue  string
	}{
		{
			name:       "returns 200 with UP status",
			wantStatus: http.StatusOK,
			wantField:  "status",
			wantValue:  "UP",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r := gin.New()

			hc := health.New()
			r.GET("/health/live", LivenessHandler(hc))

			w := httptest.NewRecorder()
			req, _ := http.NewRequest("GET", "/health/live", nil)
			r.ServeHTTP(w, req)

			assert.Equal(t, tt.wantStatus, w.Code)

			var response map[string]interface{}
			err := json.Unmarshal(w.Body.Bytes(), &response)
			assert.NoError(t, err)
			assert.Equal(t, tt.wantValue, response[tt.wantField])
			assert.NotEmpty(t, response["uptime"], "uptime should be present")
		})
	}
}

func TestReadinessHandler(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		ready      bool
		addCheck   bool
		checkErr   error
		verbose    bool
		wantStatus int
		wantField  string
		wantValue  string
		wantChecks bool
	}{
		{
			name:       "healthy - service ready with no checks (non-verbose)",
			ready:      true,
			addCheck:   false,
			wantStatus: http.StatusOK,
			wantField:  "status",
			wantValue:  "UP",
			wantChecks: false,
		},
		{
			name:       "healthy - service ready with passing check (non-verbose)",
			ready:      true,
			addCheck:   true,
			checkErr:   nil,
			wantStatus: http.StatusOK,
			wantField:  "status",
			wantValue:  "UP",
			wantChecks: false,
		},
		{
			name:       "healthy - verbose includes checks",
			ready:      true,
			addCheck:   true,
			checkErr:   nil,
			verbose:    true,
			wantStatus: http.StatusOK,
			wantField:  "status",
			wantValue:  "UP",
			wantChecks: true,
		},
		{
			name:       "unhealthy - service not ready",
			ready:      false,
			addCheck:   false,
			wantStatus: http.StatusServiceUnavailable,
			wantField:  "status",
			wantValue:  "DOWN",
			wantChecks: false,
		},
		{
			name:       "unhealthy - service not ready verbose",
			ready:      false,
			addCheck:   false,
			verbose:    true,
			wantStatus: http.StatusServiceUnavailable,
			wantField:  "status",
			wantValue:  "DOWN",
			wantChecks: true,
		},
		{
			name:       "unhealthy - service ready but check fails (non-verbose)",
			ready:      true,
			addCheck:   true,
			checkErr:   errors.New("database connection lost"),
			wantStatus: http.StatusServiceUnavailable,
			wantField:  "status",
			wantValue:  "DOWN",
			wantChecks: false,
		},
		{
			name:       "unhealthy - service ready but check fails (verbose)",
			ready:      true,
			addCheck:   true,
			checkErr:   errors.New("database connection lost"),
			verbose:    true,
			wantStatus: http.StatusServiceUnavailable,
			wantField:  "status",
			wantValue:  "DOWN",
			wantChecks: true,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r := gin.New()

			hc := health.New()
			hc.SetReady(tt.ready)
			if tt.addCheck {
				checkErr := tt.checkErr
				hc.AddCheck("database", func(_ context.Context) error {
					return checkErr
				})
			}
			r.GET("/health/ready", ReadinessHandler(hc, true))

			url := "/health/ready"
			if tt.verbose {
				url += "?verbose=true"
			}

			w := httptest.NewRecorder()
			req, _ := http.NewRequest("GET", url, nil)
			r.ServeHTTP(w, req)

			assert.Equal(t, tt.wantStatus, w.Code)

			var response map[string]interface{}
			err := json.Unmarshal(w.Body.Bytes(), &response)
			assert.NoError(t, err)
			assert.Equal(t, tt.wantValue, response[tt.wantField])

			if tt.wantChecks {
				assert.Contains(t, response, "checks", "verbose response should include checks")
			} else {
				assert.NotContains(t, response, "checks", "non-verbose response should not include checks")
			}
		})
	}
}

func TestReadinessHandler_VerboseCheckContent(t *testing.T) {
	t.Parallel()

	t.Run("verbose shows check name and error message", func(t *testing.T) {
		t.Parallel()

		r := gin.New()
		hc := health.New()
		hc.SetReady(true)
		hc.AddCheck("database", func(_ context.Context) error {
			return errors.New("connection refused")
		})
		r.GET("/health/ready", ReadinessHandler(hc, true))

		w := httptest.NewRecorder()
		req, _ := http.NewRequest("GET", "/health/ready?verbose=true", nil)
		r.ServeHTTP(w, req)

		assert.Equal(t, http.StatusServiceUnavailable, w.Code)

		var response map[string]interface{}
		err := json.Unmarshal(w.Body.Bytes(), &response)
		assert.NoError(t, err)

		checks, ok := response["checks"].(map[string]interface{})
		assert.True(t, ok, "checks should be a map")

		dbCheck, ok := checks["database"].(map[string]interface{})
		assert.True(t, ok, "database check should be present")
		assert.Equal(t, "DOWN", dbCheck["status"])
		// Message should be stripped from response (logged server-side)
		msg, hasMsg := dbCheck["message"]
		assert.True(t, !hasMsg || msg == "", "message should be stripped from verbose response")
	})

	t.Run("multiple checks mixed pass and fail", func(t *testing.T) {
		t.Parallel()

		r := gin.New()
		hc := health.New()
		hc.SetReady(true)
		hc.AddCheck("database", func(_ context.Context) error {
			return nil
		})
		hc.AddCheck("cache", func(_ context.Context) error {
			return errors.New("cache timeout")
		})
		r.GET("/health/ready", ReadinessHandler(hc, true))

		w := httptest.NewRecorder()
		req, _ := http.NewRequest("GET", "/health/ready?verbose=true", nil)
		r.ServeHTTP(w, req)

		assert.Equal(t, http.StatusServiceUnavailable, w.Code)

		var response map[string]interface{}
		err := json.Unmarshal(w.Body.Bytes(), &response)
		assert.NoError(t, err)
		assert.Equal(t, "DOWN", response["status"])

		checks, ok := response["checks"].(map[string]interface{})
		assert.True(t, ok, "checks should be a map")

		dbCheck := checks["database"].(map[string]interface{})
		assert.Equal(t, "UP", dbCheck["status"])

		cacheCheck := checks["cache"].(map[string]interface{})
		assert.Equal(t, "DOWN", cacheCheck["status"])
		// Message should be stripped from response (logged server-side)
		cacheMsg, hasCacheMsg := cacheCheck["message"]
		assert.True(t, !hasCacheMsg || cacheMsg == "", "message should be stripped from verbose response")
	})

	t.Run("non-verbose hides check details on failure", func(t *testing.T) {
		t.Parallel()

		r := gin.New()
		hc := health.New()
		hc.SetReady(true)
		hc.AddCheck("database", func(_ context.Context) error {
			return errors.New("connection refused")
		})
		r.GET("/health/ready", ReadinessHandler(hc, true))

		w := httptest.NewRecorder()
		req, _ := http.NewRequest("GET", "/health/ready", nil)
		r.ServeHTTP(w, req)

		assert.Equal(t, http.StatusServiceUnavailable, w.Code)

		var response map[string]interface{}
		err := json.Unmarshal(w.Body.Bytes(), &response)
		assert.NoError(t, err)
		assert.Equal(t, "DOWN", response["status"])
		assert.NotContains(t, response, "checks", "non-verbose should hide checks even on failure")
	})

	t.Run("verboseEnabled=false hides checks even with verbose query param", func(t *testing.T) {
		t.Parallel()

		r := gin.New()
		hc := health.New()
		hc.SetReady(true)
		hc.AddCheck("database", func(_ context.Context) error {
			return errors.New("connection refused")
		})
		r.GET("/health/ready", ReadinessHandler(hc, false))

		w := httptest.NewRecorder()
		req, _ := http.NewRequest("GET", "/health/ready?verbose=true", nil)
		r.ServeHTTP(w, req)

		assert.Equal(t, http.StatusServiceUnavailable, w.Code)

		var response map[string]interface{}
		err := json.Unmarshal(w.Body.Bytes(), &response)
		assert.NoError(t, err)
		assert.Equal(t, "DOWN", response["status"])
		assert.NotContains(t, response, "checks", "verboseEnabled=false should hide checks")
	})
}
