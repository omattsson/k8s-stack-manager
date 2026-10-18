package middleware

import (
	"net/http"
	"testing"
	"time"

	"backend/internal/models"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestAuthenticateAPIKey_VerifyLimiter checks the cap of concurrent Argon2id
// verifies: without a free slot an Argon2id key gets 503 within the wait
// time; a SHA-256 key does not need a slot.
func TestAuthenticateAPIKey_VerifyLimiter(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		argon2     bool
		full       bool
		wantStatus int // 0: success
	}{
		{name: "argon2 key with a free slot", argon2: true},
		{name: "argon2 key without a free slot", argon2: true, full: true, wantStatus: http.StatusServiceUnavailable},
		{name: "sha256 key without a free slot", full: true},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			raw, prefix, hash, err := models.GenerateAPIKey()
			require.NoError(t, err)
			if tt.argon2 {
				hash, err = models.HashAPIKeyWithSalt(raw)
				require.NoError(t, err)
			}
			keys := newTestAPIKeyRepo()
			keys.addKey(&models.APIKey{ID: "k1", UserID: "u1", Prefix: prefix, KeyHash: hash})
			users := newTestUserRepo()
			users.addUser(&models.User{ID: "u1", Username: "one", Role: "developer"})

			limiter := newVerifyLimiter(1, 20*time.Millisecond)
			if tt.full {
				require.True(t, limiter.acquire())
			}

			start := time.Now()
			key, user, failure := authenticateAPIKey(keys, users, "sk_"+raw, limiter)
			if tt.wantStatus != 0 {
				require.NotNil(t, failure)
				assert.Equal(t, tt.wantStatus, failure.Status)
				assert.Equal(t, "busy", failure.Outcome)
				assert.Equal(t, "Server busy, retry later", failure.Message)
				assert.Less(t, time.Since(start), time.Second, "a busy verify must not queue")
				return
			}
			require.Nil(t, failure)
			assert.Equal(t, "k1", key.ID)
			assert.Equal(t, "u1", user.ID)
			if !tt.full {
				assert.True(t, limiter.acquire(), "the slot is released after the verify")
			}
		})
	}
}

func TestVerifyLimiter_AcquireRelease(t *testing.T) {
	t.Parallel()

	l := newVerifyLimiter(2, 10*time.Millisecond)
	assert.True(t, l.acquire())
	assert.True(t, l.acquire())
	assert.False(t, l.acquire(), "no third slot")
	l.release()
	assert.True(t, l.acquire(), "a released slot is free again")
}
