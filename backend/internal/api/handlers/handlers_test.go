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

func TestHealthCheckHandler(t *testing.T) {
	t.Parallel()
	// Set Gin to Test Mode
	gin.SetMode(gin.TestMode)

	// Setup the router
	r := gin.Default()
	r.GET("/health", HealthCheck)

	// Create a mock request
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/health", nil)

	// Serve the request
	r.ServeHTTP(w, req)

	// Assert the response
	assert.Equal(t, http.StatusOK, w.Code)

	var response map[string]string
	err := json.Unmarshal(w.Body.Bytes(), &response)

	assert.Nil(t, err)
	assert.Equal(t, "ok", response["status"])
}

func TestPingHandler(t *testing.T) {
	t.Parallel()
	// Set Gin to Test Mode
	gin.SetMode(gin.TestMode)

	// Setup the router
	r := gin.Default()
	r.GET("/ping", Ping)

	// Create a mock request
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/ping", nil)

	// Serve the request
	r.ServeHTTP(w, req)

	// Assert the response
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
			gin.SetMode(gin.TestMode)
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
		wantStatus int
		wantField  string
		wantValue  string
	}{
		{
			name:       "healthy - service ready with no checks",
			ready:      true,
			addCheck:   false,
			wantStatus: http.StatusOK,
			wantField:  "status",
			wantValue:  "UP",
		},
		{
			name:       "healthy - service ready with passing check",
			ready:      true,
			addCheck:   true,
			checkErr:   nil,
			wantStatus: http.StatusOK,
			wantField:  "status",
			wantValue:  "UP",
		},
		{
			name:       "unhealthy - service not ready",
			ready:      false,
			addCheck:   false,
			wantStatus: http.StatusServiceUnavailable,
			wantField:  "status",
			wantValue:  "DOWN",
		},
		{
			name:       "unhealthy - service ready but check fails",
			ready:      true,
			addCheck:   true,
			checkErr:   errors.New("database connection lost"),
			wantStatus: http.StatusServiceUnavailable,
			wantField:  "status",
			wantValue:  "DOWN",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			gin.SetMode(gin.TestMode)
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

			w := httptest.NewRecorder()
			req, _ := http.NewRequest("GET", "/health/ready", nil)
			r.ServeHTTP(w, req)

			assert.Equal(t, tt.wantStatus, w.Code)

			var response map[string]interface{}
			err := json.Unmarshal(w.Body.Bytes(), &response)
			assert.NoError(t, err)
			assert.Equal(t, tt.wantValue, response[tt.wantField])
		})
	}
}
