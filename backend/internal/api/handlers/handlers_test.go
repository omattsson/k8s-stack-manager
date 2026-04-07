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
			r.GET("/health/ready", ReadinessHandler(hc))

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
