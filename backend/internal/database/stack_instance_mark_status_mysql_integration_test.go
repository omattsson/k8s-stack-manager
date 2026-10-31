//go:build integration

package database

import (
	"testing"
)

// MySQL counts a row whose values did not change as not affected: the
// stabilizing-to-stabilizing case reads the status again.
func TestDatabaseMySQL_MarkStatus(t *testing.T) {
	db := setupMySQLTestDB(t)
	repo := NewGORMStackInstanceRepository(db)
	for _, tt := range markStatusCases {
		t.Run(tt.name, func(t *testing.T) {
			db.Exec("DELETE FROM stack_instances")
			runMarkStatusCase(t, repo, tt.status, tt.wantMarked, tt.wantStatus)
		})
	}
}
