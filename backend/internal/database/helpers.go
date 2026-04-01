package database

import "strings"

// isDuplicateKeyError returns true if the error message indicates a duplicate key
// constraint violation. It handles both MySQL ("Duplicate entry") and SQLite
// ("UNIQUE constraint failed") dialects.
func isDuplicateKeyError(err error) bool {
	msg := err.Error()
	return strings.Contains(msg, "Duplicate entry") || strings.Contains(msg, "UNIQUE constraint failed")
}
