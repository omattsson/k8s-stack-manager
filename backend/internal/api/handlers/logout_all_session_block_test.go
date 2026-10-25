package handlers

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"backend/internal/config"
	"backend/internal/models"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Tests for #706 items 1 and 2: logout-all lists the recent session families
// of the user (revoked rows included) and blocks each of them, with the long
// block when the revoke fails.

func TestLogoutAllSessionBlock_BlocksEveryActiveSession(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		revokeErr  error
		listErr    error
		wantStatus int
		// wantBlocked: whether the sessions a, b and c are blocked.
		wantBlocked []bool
		// wantLong: the block lasts until SessionMaxLifetime + 1m instead of
		// the access token lifetime + 1m.
		wantLong bool
	}{
		{name: "success: short block for every family", wantStatus: http.StatusOK, wantBlocked: []bool{true, true, true}},
		{name: "revoke fails: long block for every family", revokeErr: errors.New("db down"), wantStatus: http.StatusInternalServerError, wantBlocked: []bool{true, true, true}, wantLong: true},
		{name: "listing fails, revoke succeeds: still 200, only the presented session is blocked", listErr: errors.New("db down"), wantStatus: http.StatusOK, wantBlocked: []bool{true, false, false}},
		{name: "listing and revoke fail: 500, the presented session gets the long block", listErr: errors.New("db down"), revokeErr: errors.New("db down"), wantStatus: http.StatusInternalServerError, wantBlocked: []bool{true, false, false}, wantLong: true},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			env := newSIDEnv(t, nil, nil)
			a := env.loginSession(t)
			b := env.loginSession(t)
			c := env.loginSession(t)
			env.refresh.mu.Lock()
			env.refresh.revokeAllErr = tt.revokeErr
			env.refresh.listErr = tt.listErr
			env.refresh.mu.Unlock()

			w := env.doLogoutAll(a.token)
			require.Equal(t, tt.wantStatus, w.Code, w.Body.String())
			assertCookieCleared(t, w)

			wantTTL := env.cfg.AccessTokenExpiration + time.Minute
			if tt.wantLong {
				wantTTL = env.cfg.SessionMaxLifetime + time.Minute
			}
			for i, s := range []sidSession{a, b, c} {
				assert.Equal(t, tt.wantBlocked[i], env.sessionBlocked(t, s.family), "session %d blocked", i)
				if tt.wantBlocked[i] {
					until, ok := env.guarded.untilOf(s.family)
					require.True(t, ok)
					assert.WithinDuration(t, time.Now().Add(wantTTL), until, 10*time.Second, "session %d block expiry", i)
				}
			}

			// All sessions share one block lifetime here, so they go to the
			// store in one batched write, not one write per session.
			env.guarded.mu.Lock()
			batches := env.guarded.batches
			env.guarded.mu.Unlock()
			assert.Equal(t, 1, batches, "session blocks are written in one BlockSessions call")

			// The user block is written in every case.
			blocked, err := env.store.IsUserBlocked(context.Background(), "uid-1", time.Now().Add(-time.Hour))
			require.NoError(t, err)
			assert.True(t, blocked, "user block")

			if tt.revokeErr == nil {
				for _, s := range []sidSession{a, b, c} {
					assert.Equal(t, int64(0), env.activeIn(t, s.family), "refresh tokens are revoked")
				}
			}
			users, _ := env.revoker.calls()
			assert.Equal(t, []string{"uid-1"}, users)
		})
	}
}

// A refresh after a failed logout-all is refused: the family is blocked, so
// Refresh answers 401 "Session revoked", revokes the family and clears the
// cookie.
func TestLogoutAllSessionBlock_RefreshAfterFailedRevokeIsRefused(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		// rotateFirst makes the cookie the consumed token of a rotation inside
		// the reuse grace window (the grace path).
		rotateFirst bool
	}{
		{name: "active refresh token"},
		{name: "reuse grace path", rotateFirst: true},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			env := newSIDEnv(t, nil, nil)
			a := env.loginSession(t)
			b := env.loginSession(t)
			raw := b.raw
			if tt.rotateFirst {
				_, newRaw := env.refreshOK(t, b.raw)
				require.NotEmpty(t, newRaw)
				raw = b.raw // the consumed token, within the grace window
			}

			env.refresh.mu.Lock()
			env.refresh.revokeAllErr = errors.New("db down")
			env.refresh.mu.Unlock()
			require.Equal(t, http.StatusInternalServerError, env.doLogoutAll(a.token).Code)
			assert.Equal(t, int64(1), env.activeIn(t, b.family), "the failed revoke left the tokens active")

			// The database works again for the revoke inside Refresh.
			env.refresh.mu.Lock()
			env.refresh.revokeAllErr = nil
			env.refresh.mu.Unlock()

			w := env.postRefresh(raw)
			assertRejected(t, w, "Session revoked", "refresh after a failed logout-all")
			assertCookieCleared(t, w)
			assert.NotContains(t, w.Body.String(), `"token"`)
			assert.Equal(t, int64(0), env.activeIn(t, b.family), "the blocked family is revoked")
		})
	}
}

// A login after logout-all starts a new family that no block covers.
func TestLogoutAllSessionBlock_NewLoginIsNotBlocked(t *testing.T) {
	t.Parallel()
	env := newSIDEnv(t, nil, nil)
	a := env.loginSession(t)
	require.Equal(t, http.StatusOK, env.doLogoutAll(a.token).Code)

	// A user block covers tokens issued up to its millisecond.
	time.Sleep(2 * time.Millisecond)
	fresh := env.loginSession(t)
	assert.False(t, env.sessionBlocked(t, fresh.family))
	assert.Equal(t, http.StatusOK, env.ping(fresh.token).Code)
	_, _ = env.refreshOK(t, fresh.raw)
}

// A legacy refresh token (empty family_id) is its own family, and its token ID
// is the session that logout-all blocks.
func TestLogoutAllSessionBlock_LegacyFamilyIsBlockedByTokenID(t *testing.T) {
	t.Parallel()
	env := newSIDEnv(t, nil, nil)
	a := env.loginSession(t)
	now := time.Now().UTC()
	require.NoError(t, env.refresh.Create(context.Background(), &models.RefreshToken{
		ID: "legacy-token", UserID: "uid-1", TokenHash: "legacy-hash",
		ExpiresAt: now.Add(time.Hour), LastActivity: now, CreatedAt: now,
	}))

	families, err := env.refresh.ListRecentFamiliesForUser(context.Background(), "uid-1", time.Now().Add(-time.Hour))
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{a.family, "legacy-token"}, families)

	require.Equal(t, http.StatusOK, env.doLogoutAll(a.token).Code)
	assert.True(t, env.sessionBlocked(t, "legacy-token"))
	assert.True(t, env.sessionBlocked(t, a.family))
}

// The sessions of other users are never listed or blocked.
func TestLogoutAllSessionBlock_OtherUsersSessionsUntouched(t *testing.T) {
	t.Parallel()
	env := newSIDEnv(t, nil, nil)
	a := env.loginSession(t)
	now := time.Now().UTC()
	require.NoError(t, env.refresh.Create(context.Background(), &models.RefreshToken{
		ID: "other-1", UserID: "uid-2", FamilyID: "other-1", TokenHash: "other-hash",
		ExpiresAt: now.Add(time.Hour), LastActivity: now, CreatedAt: now,
	}))

	require.Equal(t, http.StatusOK, env.doLogoutAll(a.token).Code)
	assert.False(t, env.sessionBlocked(t, "other-1"))
	assert.Equal(t, int64(1), env.activeIn(t, "other-1"))
}

// A family whose refresh token was revoked earlier (idle timeout, token cap)
// can still hold a live access token: logout-all blocks it too. Without the
// listing that ignores the revoked flag this session would stay valid.
func TestLogoutAllSessionBlock_BlocksRecentlyRevokedFamily(t *testing.T) {
	t.Parallel()

	// window is how far back logout-all lists: a row that expired less than
	// this long ago can belong to a session with a live access token.
	tests := []struct {
		name      string
		expiresIn func(window time.Duration) time.Duration // expiry of the revoked row, from now
		wantBlock bool
	}{
		{name: "revoked, expiry ahead", expiresIn: func(time.Duration) time.Duration { return time.Hour }, wantBlock: true},
		{name: "revoked, expired 5 minutes ago", expiresIn: func(time.Duration) time.Duration { return -5 * time.Minute }, wantBlock: true},
		{name: "revoked, expired just inside the window", expiresIn: func(w time.Duration) time.Duration { return -w + 30*time.Second }, wantBlock: true},
		{name: "revoked, expired just outside the window", expiresIn: func(w time.Duration) time.Duration { return -w - 30*time.Second }, wantBlock: false},
		{name: "revoked, expired long ago", expiresIn: func(time.Duration) time.Duration { return -24 * time.Hour }, wantBlock: false},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			env := newSIDEnv(t, nil, nil)
			a := env.loginSession(t)
			idle := env.loginSession(t)
			window := env.cfg.AccessTokenExpiration + sessionBlockSkew + env.cfg.RefreshReuseGrace

			// Idle timeout revoke (RevokeByID) of the only token of the family.
			require.NoError(t, env.refresh.RevokeByID(context.Background(), idle.family))
			env.refresh.mu.Lock()
			env.refresh.tokens[idle.family].ExpiresAt = time.Now().Add(tt.expiresIn(window))
			env.refresh.mu.Unlock()
			require.Equal(t, int64(0), env.activeIn(t, idle.family))

			require.Equal(t, http.StatusOK, env.doLogoutAll(a.token).Code)
			assert.Equal(t, tt.wantBlock, env.sessionBlocked(t, idle.family))
			assert.True(t, env.sessionBlocked(t, a.family))
		})
	}
}

// TestBlockSessions_GroupsByBlockLifetime checks that blockSessions writes a
// mix of normal and long blocks in exactly two batched calls, each with its
// own end time (#706).
func TestBlockSessions_GroupsByBlockLifetime(t *testing.T) {
	t.Parallel()
	env := newSIDEnv(t, nil, nil)
	h := NewAuthHandler(env.users, env.cfg, &config.OIDCConfig{})
	h.SetSessionStore(env.guarded)

	h.blockSessions(context.Background(), "uid-1", []sessionToBlock{
		{id: "fam-a"},
		{id: "fam-b", revokeFailed: true},
		{id: "fam-c"},
	})

	env.guarded.mu.Lock()
	batches := env.guarded.batches
	env.guarded.mu.Unlock()
	assert.Equal(t, 2, batches, "one call for the normal blocks, one for the long blocks")

	for _, tt := range []struct {
		id  string
		ttl time.Duration
	}{
		{id: "fam-a", ttl: env.cfg.AccessTokenExpiration + sessionBlockSkew},
		{id: "fam-b", ttl: env.cfg.SessionMaxLifetime + sessionBlockSkew},
		{id: "fam-c", ttl: env.cfg.AccessTokenExpiration + sessionBlockSkew},
	} {
		until, ok := env.guarded.untilOf(tt.id)
		require.True(t, ok, tt.id)
		assert.WithinDuration(t, time.Now().Add(tt.ttl), until, 10*time.Second, tt.id)
	}
}
