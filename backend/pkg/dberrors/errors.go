package dberrors

import (
	"errors"
	"fmt"
	"strings"
)

// Common database errors
var (
	ErrNotFound         = errors.New("record not found")
	ErrDuplicateKey     = errors.New("duplicate key violation")
	ErrValidation       = errors.New("validation error")
	ErrConnectionFailed = errors.New("database connection failed")
	ErrNotImplemented   = errors.New("not implemented")
)

// DatabaseError wraps database-specific errors with additional context
type DatabaseError struct {
	Op  string
	Err error
}

func (e *DatabaseError) Error() string {
	if e.Op != "" {
		return fmt.Sprintf("%s: %v", e.Op, e.Err)
	}
	return e.Err.Error()
}

func (e *DatabaseError) Unwrap() error {
	return e.Err
}

// NewDatabaseError creates a new database error with operation context
func NewDatabaseError(op string, err error) error {
	if err == nil {
		return nil
	}
	return &DatabaseError{Op: op, Err: err}
}

// HandleGormError translates GORM/MySQL errors into our custom error types
func HandleGormError(op string, err error) error {
	if err == nil {
		return nil
	}

	if strings.Contains(err.Error(), "record not found") {
		return NewDatabaseError(op, ErrNotFound)
	}

	// Check for duplicate key violations
	if strings.Contains(err.Error(), "Duplicate entry") {
		return NewDatabaseError(op, ErrDuplicateKey)
	}

	// Handle validation errors
	if strings.Contains(err.Error(), "validation failed") {
		return NewDatabaseError(op, ErrValidation)
	}

	return NewDatabaseError(op, err)
}
