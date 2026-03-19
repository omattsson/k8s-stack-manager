package routes

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"backend/internal/api/handlers"
	"backend/internal/config"
	"backend/internal/health"
	"backend/internal/websocket"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
)

func TestSetupRoutes(t *testing.T) {
	// Set Gin to Test Mode
	gin.SetMode(gin.TestMode)

	// Create a new router and mock repository
	router := gin.Default() // Use gin.Default() to include default middleware
	mockRepo := handlers.NewMockRepository()

	// Initialize health checker and set it as ready
	healthChecker := health.New()
	healthChecker.SetReady(true)

	// Create a minimal config for testing
	cfg := &config.Config{
		CORS: config.CORSConfig{
			AllowedOrigins: "*",
		},
		Server: config.ServerConfig{
			RateLimit: 100,
		},
	}

	// Create a test WebSocket hub
	hub := websocket.NewHub()
	go hub.Run()
	defer hub.Shutdown()

	// Setup routes
	rl := SetupRoutes(router, Deps{
		Repository:    mockRepo,
		HealthChecker: healthChecker,
		Config:        cfg,
		Hub:           hub,
	})
	defer rl.Stop()

	// Test cases
	tests := []struct {
		name         string
		route        string
		method       string
		expectedCode int
		expectedBody map[string]string
	}{
		{
			name:         "Health Check",
			route:        "/health",
			method:       "GET",
			expectedCode: 200,
			expectedBody: map[string]string{"status": "ok"},
		},
		{
			name:         "Ping endpoint",
			route:        "/api/v1/ping",
			method:       "GET",
			expectedCode: 200,
			expectedBody: map[string]string{"message": "pong"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			req, _ := http.NewRequest(tt.method, tt.route, nil)
			router.ServeHTTP(w, req)

			assert.Equal(t, tt.expectedCode, w.Code)

			if tt.expectedBody != nil {
				var response map[string]string
				err := json.Unmarshal(w.Body.Bytes(), &response)
				assert.Nil(t, err)
				assert.Equal(t, tt.expectedBody, response)
			}
		})
	}
}
