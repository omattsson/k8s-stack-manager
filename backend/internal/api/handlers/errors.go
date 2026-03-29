package handlers

import (
	"errors"
	"net/http"
	"strings"

	"backend/pkg/dberrors"
)

// Common error messages shared across handlers.
const (
	msgInternalServerError  = "Internal server error"
	msgInvalidRequestFormat = "Invalid request format"
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
