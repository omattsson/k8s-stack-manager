//go:build integration

package sessionstore

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// Benchmarks for #706 item 5: do the three revocation lookups of
// middleware.RevocationStatus pay off as one query? DSN: TEST_MYSQL_DSN, as in
// mysql_integration_test.go.
//
// Result and decision: three PK lookups take about 1.3 ms and one query about
// 0.49 ms, sequential, on localhost Docker MySQL 8.4.4 (M2 Pro). That saves
// about 0.8 ms per request. Not implemented.
//
// oneQueryRevocation is a measurement prototype: it does not apply the
// IsUserBlocked iat rule, and only the miss path is measured.

func seedRevocationBench(b *testing.B) *MySQLStore {
	b.Helper()
	s := setupBenchStore(b)
	until := time.Now().Add(time.Hour).Unix()
	rows := make([]SessionEntry, 0, 12000)
	for i := 0; i < 10000; i++ {
		rows = append(rows, SessionEntry{EntryKey: fmt.Sprintf("other-jti-%d", i), Kind: kindTokenBlock, ExpiresAt: until})
	}
	for i := 0; i < 1000; i++ {
		rows = append(rows,
			SessionEntry{EntryKey: fmt.Sprintf("other-sid-%d", i), Kind: kindSessionBlock, ExpiresAt: until},
			SessionEntry{EntryKey: fmt.Sprintf("other-user-%d", i), Kind: kindUserBlock, Data: "1", ExpiresAt: until},
			SessionEntry{EntryKey: fmt.Sprintf("other-user-%d", i), Kind: kindUserBlockMs, Data: "1000", ExpiresAt: until},
		)
	}
	require.NoError(b, s.db.CreateInBatches(&rows, 1000).Error)
	return s
}

func setupBenchStore(b *testing.B) *MySQLStore {
	b.Helper()
	// setupMySQLSessionStore takes *testing.T; replicate with the same DSN.
	dsn := os.Getenv("TEST_MYSQL_DSN")
	if dsn == "" {
		dsn = "root:rootpassword@tcp(localhost:3306)/app?charset=utf8mb4&parseTime=True&loc=Local"
	}
	db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(b, err, "is the MySQL container running?")
	require.NoError(b, db.AutoMigrate(&SessionEntry{}))
	require.NoError(b, db.Exec("TRUNCATE TABLE session_entries").Error)
	s := NewMySQLStore(db)
	b.Cleanup(func() {
		s.Stop()
		db.Exec("TRUNCATE TABLE session_entries")
	})
	return s
}

func BenchmarkMySQLRevocationThreeLookups(b *testing.B) {
	s := seedRevocationBench(b)
	ctx := context.Background()
	iat := time.Now()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := s.IsTokenBlocked(ctx, "jti-x"); err != nil {
			b.Fatal(err)
		}
		if _, err := s.IsSessionBlocked(ctx, "sid-x"); err != nil {
			b.Fatal(err)
		}
		if _, err := s.IsUserBlocked(ctx, "user-x", iat); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkMySQLRevocationThreeLookupsParallel(b *testing.B) {
	s := seedRevocationBench(b)
	ctx := context.Background()
	iat := time.Now()
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_, _ = s.IsTokenBlocked(ctx, "jti-x")
			_, _ = s.IsSessionBlocked(ctx, "sid-x")
			_, _ = s.IsUserBlocked(ctx, "user-x", iat)
		}
	})
}

func oneQueryRevocation(ctx context.Context, s *MySQLStore, jti, sid, user string) (int, error) {
	var entries []SessionEntry
	err := s.db.WithContext(ctx).
		Select("entry_key, kind, data").
		Where("(entry_key, kind) IN ((?,?),(?,?),(?,?),(?,?)) AND expires_at > ?",
			jti, kindTokenBlock, sid, kindSessionBlock, user, kindUserBlock, user, kindUserBlockMs, time.Now().Unix()).
		Find(&entries).Error
	return len(entries), err
}

func BenchmarkMySQLRevocationOneQuery(b *testing.B) {
	s := seedRevocationBench(b)
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := oneQueryRevocation(ctx, s, "jti-x", "sid-x", "user-x"); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkMySQLRevocationOneQueryParallel(b *testing.B) {
	s := seedRevocationBench(b)
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_, _ = oneQueryRevocation(ctx, s, "jti-x", "sid-x", "user-x")
		}
	})
}

// BenchmarkMySQLRevocationSingleLookup gives the latency of one PK lookup.
func BenchmarkMySQLRevocationSingleLookup(b *testing.B) {
	s := seedRevocationBench(b)
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := s.IsTokenBlocked(ctx, "jti-x"); err != nil {
			b.Fatal(err)
		}
	}
}
