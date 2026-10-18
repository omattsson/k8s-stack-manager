package database

import (
	"errors"
	"strings"

	"backend/pkg/dberrors"

	"github.com/go-sql-driver/mysql"
	"gorm.io/gorm"
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

// namesByIDs returns the name column of the rows of model whose id is in ids,
// keyed by id. It runs one query and selects only id and name. An empty ids
// gives an empty map without a query. IDs that do not exist are not in the map.
func namesByIDs(db *gorm.DB, model any, ids []string) (map[string]string, error) {
	result := make(map[string]string, len(ids))
	if len(ids) == 0 {
		return result, nil
	}
	var rows []struct {
		ID   string
		Name string
	}
	if err := db.Model(model).Select("id, name").Where("id IN ?", ids).Find(&rows).Error; err != nil {
		return nil, dberrors.NewDatabaseError("names_by_ids", err)
	}
	for _, row := range rows {
		result[row.ID] = row.Name
	}
	return result, nil
}
