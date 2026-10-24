package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"backend/internal/api/middleware"
	"backend/internal/models"
	"backend/pkg/dberrors"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// More tests for POST /auth/logout (#673), next to logout_session_test.go.
// They use sessionTestEnv (session_lifecycle_test.go), so a logout and the
// refresh replays that follow it run through the real handler wiring.

// logoutAccessToken signs an access token of uid-1 and returns it with its
// jti.
func (e *sessionTestEnv) logoutAccessToken(t *testing.T) (string, string) {
	t.Helper()
	token, err := middleware.GenerateTokenWithOpts(middleware.GenerateTokenOptions{
		UserID:     "uid-1",
		Username:   "alice",
		Role:       "user",
		Secret:     e.cfg.JWTSecret,
		Expiration: e.cfg.AccessTokenExpiration,
	})
	require.NoError(t, err)
	claims, err := middleware.ValidateJWT(token, e.cfg.JWTSecret)
	require.NoError(t, err)
	require.NotEmpty(t, claims.ID)
	return token, claims.ID
}

// assertLogoutSideEffects checks the steps that must run on every logout:
// cookie cleared, jti blocklisted, sockets of the jti closed.
func (e *sessionTestEnv) assertLogoutSideEffects(t *testing.T, w *httptest.ResponseRecorder, jti string) {
	t.Helper()

	cookie := findRefreshCookie(w)
	require.NotNil(t, cookie, "the refresh cookie must be cleared")
	assert.True(t, cookie.MaxAge < 0 && cookie.Value == "", "the refresh cookie must be cleared")

	blocked, err := e.store.IsTokenBlocked(context.Background(), jti)
	require.NoError(t, err)
	assert.True(t, blocked, "the access token jti must be blocklisted")

	_, tokens := e.revoker.calls()
	assert.Equal(t, []string{jti}, tokens, "the sockets of the access token must be closed")
}

// TestLogout_RevokesSessionFamily checks that logout revokes the active
// token of the session family, and that no token of the family can refresh
// afterwards (#673):
//   - a cookie with a token that a rotation consumed inside
//     REFRESH_REUSE_GRACE, which would still get an access token without the
//     logout;
//   - a cookie with the active token, the common logout;
//   - a legacy root token without a family ID, whose successor carries the
//     root ID as its family (continuedSession). A revoke by FamilyID alone
//     would miss the successor.
func TestLogout_RevokesSessionFamily(t *testing.T) {
	t.Parallel()

	tests := []struct {
		// rotatedAgo is how long ago rt-old was rotated.
		rotatedAgo time.Duration
		name       string
		// cookie is the ID of the token whose raw value goes in the cookie.
		cookie string
		// legacy makes rt-old a legacy root (no family ID, no session start)
		// and rt-new its successor in family "rt-old".
		legacy bool
		// inGrace: rt-old can still refresh before the logout (control).
		inGrace bool
	}{
		{name: "rotated token inside the reuse grace", rotatedAgo: 5 * time.Second, cookie: "rt-old", inGrace: true},
		{name: "active token", rotatedAgo: time.Minute, cookie: "rt-new"},
		{name: "legacy root token revokes its successor", rotatedAgo: time.Minute, cookie: "rt-old", legacy: true},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			env := newSessionTestEnv(t)
			env.seedUser(t, "", "")

			rotatedAt := time.Now().UTC().Add(-tt.rotatedAgo)
			raws := map[string]string{
				// First token of the family, consumed by a rotation.
				"rt-old": env.seedToken(t, "rt-old", func(rt *models.RefreshToken) {
					rt.Revoked = true
					rt.RotatedAt = &rotatedAt
					if tt.legacy {
						rt.FamilyID = ""
						rt.SessionStartedAt = time.Time{}
					}
				}),
				// Its successor: the active token of the session.
				"rt-new": env.seedToken(t, "rt-new", func(rt *models.RefreshToken) {
					if tt.legacy {
						rt.FamilyID = "rt-old"
					}
				}),
			}
			// Another session of the same user must survive (logout is not
			// logout-all).
			env.seedToken(t, "rt-other", func(rt *models.RefreshToken) { rt.FamilyID = "fam-2" })

			if tt.inGrace {
				// Control: inside the grace the rotated token gets an access
				// token, without a cookie or a revoke, so the replay after the
				// logout below tests the grace path.
				w := env.postRefresh(raws["rt-old"])
				require.Equal(t, http.StatusOK, w.Code, "control: grace refresh before logout: %s", w.Body.String())
				require.False(t, env.tokenByID(t, "rt-new").Revoked)
			}

			bearer, jti := env.logoutAccessToken(t)
			w := env.postLogoutAs(bearer, raws[tt.cookie])

			assert.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())
			env.assertLogoutSideEffects(t, w, jti)
			assert.True(t, env.tokenByID(t, "rt-new").Revoked, "the active token of the family must be revoked")
			assert.False(t, env.tokenByID(t, "rt-other").Revoked, "another session of the user must stay active")

			// Neither token of the family can refresh. rt-old first: its
			// replay inside the grace must see the state right after the
			// logout (a replay of rt-new revokes the family again).
			for _, id := range []string{"rt-old", "rt-new"} {
				rw := env.postRefresh(raws[id])
				assert.Equal(t, http.StatusUnauthorized, rw.Code,
					"%s must not refresh after logout: %s", id, rw.Body.String())
				var body map[string]any
				require.NoError(t, json.Unmarshal(rw.Body.Bytes(), &body))
				assert.NotContains(t, body, "token", "%s must get no access token after logout", id)
			}
		})
	}
}

// opaqueNotFound wraps dberrors.ErrNotFound with a message that does not
// contain "not found", so only the errors.Is branch of isNotFoundError
// matches it.
type opaqueNotFound struct{}

func (opaqueNotFound) Error() string { return "lookup failed" }
func (opaqueNotFound) Unwrap() error { return dberrors.ErrNotFound }

// TestLogout_WrappedNotFound checks that a lookup error that wraps
// dberrors.ErrNotFound is an unknown token, not a store failure: 200, and
// every other step runs.
func TestLogout_WrappedNotFound(t *testing.T) {
	t.Parallel()
	env := newSessionTestEnv(t)
	env.seedToken(t, "rt-other", nil)
	env.refresh.SetFindError(opaqueNotFound{})

	bearer, jti := env.logoutAccessToken(t)
	w := env.postLogoutAs(bearer, "raw-unknown")

	assert.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())
	env.assertLogoutSideEffects(t, w, jti)
	assert.False(t, env.tokenByID(t, "rt-other").Revoked, "an unrelated session must stay active")
}
