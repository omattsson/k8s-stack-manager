package handlers

import (
	"backend/internal/health"
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
)

// @Summary     Health Check
// @Description Get API health status
// @Tags        health
// @Produce     json
// @Success     200 {object} map[string]string
// @Router      /health [get]
func HealthCheck(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

// @Summary     Ping test
// @Description Ping test endpoint
// @Tags        ping
// @Produce     json
// @Success     200 {object} map[string]string
// @Router      /api/v1/ping [get]
func Ping(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"message": "pong"})
}

// LivenessHandler returns a handler for liveness checks.
// The health checker is injected so the same instance used in main is checked.
//
// @Summary     Liveness Check
// @Description Get API liveness status
// @Tags        health
// @Produce     json
// @Success     200 {object} health.HealthStatus
// @Router      /health/live [get]
func LivenessHandler(hc *health.HealthChecker) gin.HandlerFunc {
	return func(c *gin.Context) {
		status := hc.CheckLiveness(c.Request.Context())
		c.JSON(http.StatusOK, status)
	}
}

// ReadinessHandler returns a handler for readiness checks.
// The health checker is injected so the same instance used in main is checked.
//
// By default, only the top-level status is returned. Append ?verbose=true to
// include per-check details.
//
// @Summary     Readiness Check
// @Description Get API readiness status
// @Tags        health
// @Produce     json
// @Param       verbose query    bool false "Include per-check details"
// @Success     200 {object} health.HealthStatus
// @Failure     503 {object} health.HealthStatus
// @Router      /health/ready [get]
func ReadinessHandler(hc *health.HealthChecker, verboseEnabled bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		// Bound total readiness latency so it completes within typical
		// Kubernetes probe timeouts (default 1-5s).
		ctx, cancel := context.WithTimeout(c.Request.Context(), 4*time.Second)
		defer cancel()

		status := hc.CheckReadiness(ctx)

		if verboseEnabled && c.Query("verbose") == "true" {
			// Log full details server-side, strip messages from response.
			for name, check := range status.Checks {
				if check.Message != "" {
					slog.Info("readiness check detail", "check", name, "status", check.Status, "message", check.Message)
					check.Message = ""
					status.Checks[name] = check
				}
			}
		} else {
			status.Checks = nil
		}

		if status.Status == "DOWN" {
			c.JSON(http.StatusServiceUnavailable, status)
			return
		}
		c.JSON(http.StatusOK, status)
	}
}
