package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"backend/internal/models"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Tests for the session block that Refresh reads and for the long block after
// a failed family revoke (#699 review).

// TestRefreshSID_BlockedSessionIsRejected checks that a refresh of a blocked
// family gets 401 "Session revoked", clears the cookie and revokes the family,
// in the normal path and in the reuse grace path.
func TestRefreshSID_BlockedSessionIsRejected(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		// rotateFirst rotates the token once, so the cookie of the request is
		// a consumed token inside the reuse grace.
		rotateFirst bool
	}{
		{name: "normal path"},
		{name: "reuse grace path", rotateFirst: true},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			env := newSIDEnv(t, nil, nil)
			a := env.loginSession(t)
			b := env.loginSession(t)
			if tt.rotateFirst {
				_, _ = env.refreshOK(t, a.raw)
			}
			require.Greater(t, env.activeIn(t, a.family), int64(0))

			// The family is still active, only the block exists.
			require.NoError(t, env.guarded.BlockSession(context.Background(), a.family, time.Now().Add(time.Hour)))

			w := env.postRefresh(a.raw)
			assertRejected(t, w, "Session revoked", "refresh of a blocked session")
			assert.NotContains(t, w.Body.String(), `"token"`)
			assertCookieCleared(t, w)
			assert.Equal(t, int64(0), env.activeIn(t, a.family), "the blocked family is revoked")

			// Another session of the user is not affected.
			_, _ = env.refreshOK(t, b.raw)
		})
	}
}

func TestRefreshSID_SessionBlockStoreErrorFailsOpen(t *testing.T) {
	t.Parallel()
	env := newSIDEnv(t, nil, nil)
	a := env.loginSession(t)
	env.guarded.isBlockedErr = errors.New("store down")

	w := env.postRefresh(a.raw)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.NotEmpty(t, tokenOfRefresh(t, w))
}

// TestLogoutSID_FailedRevokeWritesLongBlock checks the length of the block:
// the access-token lifetime plus the margin after a successful revoke, the
// session lifetime plus the margin after a failed revoke. The refresh token of
// a session whose revoke failed is refused by the block.
func TestLogoutSID_FailedRevokeWritesLongBlock(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		revokeErr   error
		wantStatus  int
		wantTTL     func(env *sidEnv) time.Duration
		wantRefresh int
	}{
		{
			name:        "revoke ok: short block",
			wantStatus:  http.StatusOK,
			wantTTL:     func(env *sidEnv) time.Duration { return env.cfg.AccessTokenExpiration + sessionBlockSkew },
			wantRefresh: http.StatusUnauthorized,
		},
		{
			name:        "revoke failed: long block",
			revokeErr:   errors.New("db down"),
			wantStatus:  http.StatusInternalServerError,
			wantTTL:     func(env *sidEnv) time.Duration { return env.cfg.SessionMaxLifetime + sessionBlockSkew },
			wantRefresh: http.StatusUnauthorized,
		},
	}
	for _, tt := range tests {
		tt := tt
		for _, via := range []string{"cookie", "bearer"} {
			via := via
			t.Run(tt.name+" via "+via, func(t *testing.T) {
				t.Parallel()
				env := newSIDEnv(t, nil, nil)
				a := env.loginSession(t)
				env.refresh.mu.Lock()
				env.refresh.revokeAllErr = tt.revokeErr
				env.refresh.mu.Unlock()

				var w *httptest.ResponseRecorder
				if via == "cookie" {
					w = env.doLogout("", a.raw)
				} else {
					w = env.doLogout(a.token, "")
				}
				assert.Equal(t, tt.wantStatus, w.Code, w.Body.String())

				until, ok := env.guarded.untilOf(a.family)
				require.True(t, ok, "the session must be blocked")
				assert.WithinDuration(t, time.Now().Add(tt.wantTTL(env)), until, 10*time.Second)

				// The revoke works again; the block alone refuses the refresh
				// and the refresh revokes the family.
				env.refresh.mu.Lock()
				env.refresh.revokeAllErr = nil
				env.refresh.mu.Unlock()
				rw := env.postRefresh(a.raw)
				assert.Equal(t, tt.wantRefresh, rw.Code, rw.Body.String())
				assert.Equal(t, int64(0), env.activeIn(t, a.family))
			})
		}
	}
}

// TestLogoutSID_ReplayFailedRevokeWritesLongBlock checks the replay path: the
// family revoke fails, so the session block lasts until the session ends.
func TestLogoutSID_ReplayFailedRevokeWritesLongBlock(t *testing.T) {
	t.Parallel()
	env := newSIDEnv(t, nil, nil)
	a := env.loginSession(t)
	_, _ = env.refreshOK(t, a.raw)

	// Outside the reuse grace the replay revokes the family.
	env.updateToken(t, a.raw, func(rt *models.RefreshToken) {
		old := time.Now().UTC().Add(-time.Hour)
		rt.RotatedAt = &old
	})
	env.refresh.mu.Lock()
	env.refresh.revokeAllErr = errors.New("db down")
	env.refresh.mu.Unlock()

	w := env.postRefresh(a.raw)
	assert.Equal(t, http.StatusUnauthorized, w.Code, w.Body.String())

	until, ok := env.guarded.untilOf(a.family)
	require.True(t, ok)
	assert.WithinDuration(t, time.Now().Add(env.cfg.SessionMaxLifetime+sessionBlockSkew), until, 10*time.Second)
}

// TestLogoutSID_CookieAndBearerOfDifferentUsers checks that a cookie of user A
// and a bearer token of user B end both sessions and leave a third session of
// each user alone.
func TestLogoutSID_CookieAndBearerOfDifferentUsers(t *testing.T) {
	t.Parallel()
	env := newSIDEnv(t, nil, nil)
	seedUser(t, env.users, "uid-2", "bob", "secret", "user")

	loginBob := func() sidSession {
		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodPost, "/api/v1/auth/login",
			bytes.NewBufferString(`{"username":"bob","password":"secret"}`))
		req.Header.Set("Content-Type", "application/json")
		env.router.ServeHTTP(w, req)
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		var resp LoginResponse
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
		cookie := findRefreshCookie(w)
		require.NotNil(t, cookie)
		return sidSession{token: resp.Token, raw: cookie.Value, family: env.tokenByRaw(t, cookie.Value).FamilyID}
	}

	a1, a2 := env.loginSession(t), env.loginSession(t)
	b1, b2 := loginBob(), loginBob()

	w := env.doLogout(b1.token, a1.raw)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	for name, s := range map[string]sidSession{"cookie session of A": a1, "bearer session of B": b1} {
		assert.Equal(t, int64(0), env.activeIn(t, s.family), name)
		assert.True(t, env.sessionBlocked(t, s.family), name)
	}
	assertRejected(t, env.ping(a1.token), "Session revoked", "access token of the cookie session")

	for name, s := range map[string]sidSession{"third session of A": a2, "third session of B": b2} {
		assert.Equal(t, int64(1), env.activeIn(t, s.family), name)
		assert.False(t, env.sessionBlocked(t, s.family), name)
		assert.Equal(t, http.StatusOK, env.ping(s.token).Code, name)
		_, _ = env.refreshOK(t, s.raw)
	}
}
