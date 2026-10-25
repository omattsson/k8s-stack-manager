//go:build integration

package database

import (
	"context"
	"net"
	"os"
	"testing"
	"time"

	"backend/internal/config"

	mysqldrv "github.com/go-sql-driver/mysql"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestDatabaseMySQL_SessionVariablesOnEveryConnection opens the database the
// way production does (NewFromAppConfig) and checks sql_mode, time_zone and
// foreign_key_checks on several pooled connections held at the same time
// (#706). A SET statement after connecting reached only one of them.
//
// The time_zone check fails on the old code: the server default is SYSTEM.
// The sql_mode check only proves the mode is set, because the MySQL 8 server
// default is the same string.
func TestDatabaseMySQL_SessionVariablesOnEveryConnection(t *testing.T) {
	raw := os.Getenv("TEST_MYSQL_DSN")
	if raw == "" {
		raw = "root:rootpassword@tcp(localhost:3306)/app?charset=utf8mb4&parseTime=True&loc=Local"
	}
	parsed, err := mysqldrv.ParseDSN(raw)
	require.NoError(t, err)
	host, port, err := net.SplitHostPort(parsed.Addr)
	require.NoError(t, err)

	const conns = 4
	cfg := &config.Config{
		App: config.AppConfig{Debug: true},
		Database: config.DatabaseConfig{
			Host: host, Port: port, User: parsed.User, Password: parsed.Passwd, DBName: parsed.DBName,
			MaxOpenConns: conns, MaxIdleConns: conns, ConnMaxLifetime: time.Minute,
		},
	}
	db, err := NewFromAppConfig(cfg)
	require.NoError(t, err, "Failed to connect to MySQL — is the container running?")
	sqlDB, err := db.DB.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })

	ctx := context.Background()
	// Hold every connection at once, so each check runs on a different one.
	held := make([]interface{ Close() error }, 0, conns)
	defer func() {
		for _, c := range held {
			_ = c.Close()
		}
	}()
	for i := 0; i < conns; i++ {
		c, err := sqlDB.Conn(ctx)
		require.NoError(t, err)
		held = append(held, c)

		var sqlMode, timeZone string
		var fkChecks int
		require.NoError(t, c.QueryRowContext(ctx, "SELECT @@SESSION.sql_mode, @@SESSION.time_zone, @@SESSION.foreign_key_checks").Scan(&sqlMode, &timeZone, &fkChecks))
		assert.Equal(t, "ONLY_FULL_GROUP_BY,STRICT_TRANS_TABLES,NO_ZERO_IN_DATE,NO_ZERO_DATE,ERROR_FOR_DIVISION_BY_ZERO,NO_ENGINE_SUBSTITUTION", sqlMode, "connection %d sql_mode", i)
		assert.Equal(t, "+00:00", timeZone, "connection %d time_zone", i)
		assert.Equal(t, 1, fkChecks, "connection %d foreign_key_checks", i)
	}
	assert.Equal(t, conns, sqlDB.Stats().OpenConnections, "the checks ran on distinct connections")
}
