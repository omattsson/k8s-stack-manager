package handlers

import (
	"backend/internal/health"
	"net/http"

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
// @Param       verbose query    string false "Include per-check details" Enums(true,false)
// @Success     200 {object} health.HealthStatus
// @Failure     503 {object} health.HealthStatus
// @Router      /health/ready [get]
func ReadinessHandler(hc *health.HealthChecker) gin.HandlerFunc {
	return func(c *gin.Context) {
		status := hc.CheckReadiness(c.Request.Context())

		if c.Query("verbose") != "true" {
			status.Checks = nil
		}

		if status.Status == "DOWN" {
			c.JSON(http.StatusServiceUnavailable, status)
			return
		}
		c.JSON(http.StatusOK, status)
	}
}
