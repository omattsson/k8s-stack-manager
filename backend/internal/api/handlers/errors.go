package handlers

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"backend/pkg/dberrors"

	"github.com/gin-gonic/gin"
)

// Sentinel errors for domain-specific conditions (not database errors).
var (
	// ErrInstanceLimitExceeded is returned when a user has reached the
	// maximum number of stack instances allowed on a cluster.
	ErrInstanceLimitExceeded = errors.New("instance limit exceeded")
)

// Common error messages shared across handlers.
const (
	msgInternalServerError  = "Internal server error"
	msgInvalidRequestFormat = "Invalid request format"
	// msgCleanConflict is the 409 message when a clean lost against another
	// clean or a delete of the same instance.
	msgCleanConflict = "Cannot clean: another operation started on the instance"
	// msgClientClosedRequest is the message of statusClientClosedRequest.
	msgClientClosedRequest = "Client closed request"
	// statusClientClosedRequest is the nginx status for a request that the
	// client cancelled before the response (net/http has no constant for it).
	statusClientClosedRequest = 499
)

// Common entity names used with mapError across multiple handler files.
const (
	entityStackInstance   = "Stack instance"
	entityStackDefinition = "Stack definition"
	entityChartConfig     = "Chart config"
	entityChartConfigs    = "Chart configs"
	entityTemplate        = "Template"
	entityCluster         = "Cluster"
)

// mapError translates a repository error into an appropriate HTTP status code
// and a safe, user-facing message. entityName is used to build contextual
// "not found" / "already exists" messages (e.g. "Stack definition").
func mapError(err error, entityName string) (int, string) {
	if err == nil {
		return http.StatusOK, ""
	}

	if status, msg, ok := cancelledRequestError(err); ok {
		return status, msg
	}

	// Check for dberrors sentinel types via errors.Is (works with wrapped errors).
	if errors.Is(err, dberrors.ErrValidation) {
		return http.StatusBadRequest, err.Error()
	}
	if errors.Is(err, dberrors.ErrNotFound) {
		return http.StatusNotFound, entityName + " not found"
	}
	if errors.Is(err, dberrors.ErrDuplicateKey) {
		return http.StatusConflict, entityName + " already exists"
	}
	if errors.Is(err, dberrors.ErrNotImplemented) {
		return http.StatusNotImplemented, "Feature not implemented"
	}

	// Fallback to string matching for implementations that don't wrap sentinels.
	errStr := err.Error()
	if strings.Contains(errStr, "not found") {
		return http.StatusNotFound, entityName + " not found"
	}
	if strings.Contains(errStr, "duplicate") || strings.Contains(errStr, "already exists") {
		return http.StatusConflict, entityName + " already exists"
	}
	if strings.Contains(errStr, "validation") {
		return http.StatusBadRequest, errStr
	}

	// Never leak internal error details.
	return http.StatusInternalServerError, msgInternalServerError
}

// cancelledRequestError maps an error caused by a cancelled request context
// to 499. A deadline that passed is not a client cancel and stays a 500.
// It assumes context.Canceled comes from the request; a server-side cancel also gives 499.
func cancelledRequestError(err error) (int, string, bool) {
	if !errors.Is(err, context.Canceled) {
		return 0, "", false
	}
	slog.Debug("Request cancelled by the client", "error", err)
	return statusClientClosedRequest, msgClientClosedRequest, true
}

// respondCancelled writes the 499 answer when err comes from a cancelled
// request context and reports whether it did. Use it where a handler would
// otherwise log the error and answer 500.
func respondCancelled(c *gin.Context, err error) bool {
	status, msg, ok := cancelledRequestError(err)
	if ok {
		c.JSON(status, gin.H{"error": msg})
	}
	return ok
}
