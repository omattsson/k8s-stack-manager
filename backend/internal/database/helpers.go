package database

import (
	"errors"
	"strings"

	"github.com/go-sql-driver/mysql"
)

// MySQL error codes.
const mysqlErrDuplicateEntry = 1062

// isDuplicateKeyError returns true if the error indicates a duplicate key
// constraint violation. It checks the typed MySQL error code first (1062),
// then falls back to string matching for SQLite (used in unit tests).
func isDuplicateKeyError(err error) bool {
	var mysqlErr *mysql.MySQLError
	if errors.As(err, &mysqlErr) {
		return mysqlErr.Number == mysqlErrDuplicateEntry
	}
	// SQLite fallback for unit tests.
	return strings.Contains(err.Error(), "UNIQUE constraint failed")
}
