package middleware

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/assert"
)

func TestRevocationStatus_SessionBlock(t *testing.T) {
	t.Parallel()

	dbDown := errors.New("db down")
	tests := []struct {
		name           string
		sessionID      string
		setup          func(*mockSessionStore)
		wantRevoked    error
		wantLookup     bool
		wantSessionHit int
	}{
		{name: "session blocked", sessionID: "fam-1", setup: func(m *mockSessionStore) { m.blockedSessions["fam-1"] = true }, wantRevoked: ErrSessionRevoked, wantSessionHit: 1},
		{name: "other session passes", sessionID: "fam-2", setup: func(m *mockSessionStore) { m.blockedSessions["fam-1"] = true }, wantSessionHit: 1},
		{name: "empty sid skips the lookup", sessionID: "", setup: func(m *mockSessionStore) { m.blockedSessions[""] = true; m.sessionBlockErr = dbDown }},
		{name: "store error fails open", sessionID: "fam-1", setup: func(m *mockSessionStore) { m.sessionBlockErr = dbDown }, wantLookup: true, wantSessionHit: 1},
		{name: "store error, user still checked", sessionID: "fam-1", setup: func(m *mockSessionStore) { m.sessionBlockErr = dbDown; m.blockedUsers["u1"] = true }, wantRevoked: ErrSessionRevoked, wantLookup: true, wantSessionHit: 1},
		{name: "token block wins before the session lookup", sessionID: "fam-1", setup: func(m *mockSessionStore) { m.blockedTokens["jti-1"] = true }, wantRevoked: ErrTokenRevoked},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			m := newMockSessionStore()
			tt.setup(m)
			claims := &Claims{
				UserID:           "u1",
				SessionID:        tt.sessionID,
				RegisteredClaims: jwt.RegisteredClaims{ID: "jti-1", IssuedAt: jwt.NewNumericDate(time.Now())},
			}
			revoked, lookupErr := RevocationStatus(context.Background(), m, claims)
			assert.Equal(t, tt.wantRevoked, revoked)
			if tt.wantLookup {
				assert.ErrorIs(t, lookupErr, dbDown)
			} else {
				assert.NoError(t, lookupErr)
			}
			assert.Equal(t, tt.wantSessionHit, m.sessionChecks)
		})
	}
}
