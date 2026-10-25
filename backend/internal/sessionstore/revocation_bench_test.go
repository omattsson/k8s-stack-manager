package sessionstore

import (
	"context"
	"strconv"
	"testing"
	"time"
)

// BenchmarkMemoryRevocationThreeLookups measures the three revocation checks
// of middleware.RevocationStatus on the memory store (#706).
func BenchmarkMemoryRevocationThreeLookups(b *testing.B) {
	s := NewMemoryStore()
	defer s.Stop()
	ctx := context.Background()
	until := time.Now().Add(time.Hour)
	for i := 0; i < 1000; i++ {
		_ = s.BlockToken(ctx, "other-jti-"+strconv.Itoa(i), until)
		_ = s.BlockSession(ctx, "other-sid-"+strconv.Itoa(i), until)
		_ = s.BlockUser(ctx, "other-user-"+strconv.Itoa(i), until)
	}
	iat := time.Now()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = s.IsTokenBlocked(ctx, "jti-x")
		_, _ = s.IsSessionBlocked(ctx, "sid-x")
		_, _ = s.IsUserBlocked(ctx, "user-x", iat)
	}
}
