//go:build integration

package database

import (
	"log/slog"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"backend/internal/config"
	"backend/internal/models"

	mysqldriver "github.com/go-sql-driver/mysql"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// TestDatabaseMySQL_GormLoggerValues opens MySQL through NewFromAppConfig
// and checks two paths of #653 in release and debug mode: db.Scan (logged
// through logger.Recorder) and a duplicate entry error (1062).
func TestDatabaseMySQL_GormLoggerValues(t *testing.T) {
	setupMySQLTestDB(t)
	dsn := os.Getenv("TEST_MYSQL_DSN")
	if dsn == "" {
		dsn = "root:rootpassword@tcp(localhost:3306)/app?charset=utf8mb4&parseTime=True&loc=Local"
	}
	dsnCfg, err := mysqldriver.ParseDSN(dsn)
	require.NoError(t, err)
	host, port, err := net.SplitHostPort(dsnCfg.Addr)
	require.NoError(t, err)

	tests := []struct {
		name  string
		user  string
		debug bool
	}{
		{name: "release", user: "dup-secret-release"},
		{name: "debug", user: "dup-secret-debug", debug: true},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			d, err := NewFromAppConfig(&config.Config{
				App: config.AppConfig{Debug: tt.debug},
				Database: config.DatabaseConfig{
					Host: host, Port: port, User: dsnCfg.User, Password: dsnCfg.Passwd, DBName: dsnCfg.DBName,
					MaxOpenConns: 2, MaxIdleConns: 1, ConnMaxLifetime: time.Minute,
				},
			})
			require.NoError(t, err)
			sqlDB, err := d.DB.DB()
			require.NoError(t, err)
			t.Cleanup(func() { _ = sqlDB.Close() })

			log, h := newRecordingLogger(slog.LevelInfo)
			db := d.DB.Session(&gorm.Session{Logger: newSlogGormLogger(log, tt.debug)})

			var out []map[string]any
			require.Error(t, db.Raw("SELECT id FROM missing_table WHERE name = ?", "scan-secret").Scan(&out).Error)
			require.NoError(t, db.Create(&models.User{ID: uuid.New().String(), Username: tt.user, Role: "user"}).Error)
			require.Error(t, db.Create(&models.User{ID: uuid.New().String(), Username: tt.user, Role: "user"}).Error)

			var scanSQL, dupSQL, dupErr string
			for _, r := range h.all() {
				attrs := recordAttrs(r)
				switch {
				case strings.Contains(attrs["sql"].String(), "missing_table"):
					scanSQL = attrs["sql"].String()
				case r.Message == "SQL duplicate key":
					assert.Equal(t, slog.LevelWarn, r.Level)
					dupSQL, dupErr = attrs["sql"].String(), attrs["error"].String()
				}
			}
			require.NotEmpty(t, scanSQL, "the Scan error is logged")
			require.NotEmpty(t, dupErr, "the duplicate entry is logged")
			assert.Contains(t, dupErr, "Error 1062")
			assert.Contains(t, dupErr, "idx_users_username", "the key name stays")

			if tt.debug {
				assert.Contains(t, scanSQL, "scan-secret")
				assert.Contains(t, dupErr, tt.user)
				return
			}
			assert.Contains(t, scanSQL, "name = ?")
			assert.NotContains(t, scanSQL, "scan-secret")
			assert.NotContains(t, dupSQL, tt.user)
			assert.NotContains(t, dupErr, tt.user)
		})
	}
}
