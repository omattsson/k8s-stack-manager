package handlers

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"backend/internal/auth"
	"backend/internal/config"
	"backend/internal/models"
	"backend/internal/sessionstore"
	"backend/pkg/dberrors"

	"github.com/gin-gonic/gin"
	jwtlib "github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func stringPtr(s string) *string { return &s }

var (
	handlerOIDCPrivKey     *rsa.PrivateKey
	handlerOIDCPrivKeyOnce sync.Once
)

func getHandlerOIDCPrivKey() *rsa.PrivateKey {
	handlerOIDCPrivKeyOnce.Do(func() {
		var err error
		handlerOIDCPrivKey, err = rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			panic(fmt.Sprintf("failed to generate RSA test key: %v", err))
		}
	})
	return handlerOIDCPrivKey
}

// ---- minimal mock OIDC server for handler tests ----

// oidcHandlerTestServer is a self-contained httptest.Server acting as a mock IdP.
type oidcHandlerTestServer struct {
	server    *httptest.Server
	clientID  string
	kid       string
	failToken bool
}

// newOIDCHandlerTestServer starts a mock OIDC server.
// When failToken is true the /token endpoint always returns HTTP 500.
func newOIDCHandlerTestServer(t *testing.T, clientID string, failToken bool) *oidcHandlerTestServer {
	t.Helper()

	priv := getHandlerOIDCPrivKey()
	ts := &oidcHandlerTestServer{
		clientID:  clientID,
		kid:       "handler-test-key",
		failToken: failToken,
	}

	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	ts.server = srv
	t.Cleanup(srv.Close)

	issuer := srv.URL

	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer":                                issuer,
			"authorization_endpoint":                issuer + "/authorize",
			"token_endpoint":                        issuer + "/token",
			"jwks_uri":                              issuer + "/keys",
			"response_types_supported":              []string{"code"},
			"subject_types_supported":               []string{"public"},
			"id_token_signing_alg_values_supported": []string{"RS256"},
		})
	})

	mux.HandleFunc("/keys", func(w http.ResponseWriter, r *http.Request) {
		pub := &priv.PublicKey
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"keys": []any{map[string]any{
				"kty": "RSA",
				"alg": "RS256",
				"use": "sig",
				"kid": ts.kid,
				"n":   base64.RawURLEncoding.EncodeToString(pub.N.Bytes()),
				"e":   base64.RawURLEncoding.EncodeToString(big.NewInt(int64(pub.E)).Bytes()),
			}},
		})
	})

	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		if ts.failToken {
			http.Error(w, "token endpoint error", http.StatusInternalServerError)
			return
		}

		claims := jwtlib.MapClaims{
			"iss":                issuer,
			"sub":                "oidc-user-001",
			"aud":                clientID,
			"email":              "oidcuser@example.com",
			"preferred_username": "oidcuser",
			"name":               "OIDC User",
			"roles":              []string{"k8s-stack-admin"},
			"exp":                jwtlib.NewNumericDate(time.Now().Add(time.Hour)),
			"iat":                jwtlib.NewNumericDate(time.Now()),
		}
		tok := jwtlib.NewWithClaims(jwtlib.SigningMethodRS256, claims)
		tok.Header["kid"] = ts.kid

		signed, err := tok.SignedString(priv)
		if err != nil {
			http.Error(w, "signing error", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "mock-access-token",
			"token_type":   "Bearer",
			"expires_in":   3600,
			"id_token":     signed,
		})
	})

	return ts
}

// newOIDCHandlerSetup creates a fully wired OIDCHandler using a mock OIDC server.
// The same *config.OIDCConfig is passed to both auth.NewProvider and NewOIDCHandler
// to match production wiring (handler and provider share the same config pointer).
// When failToken is true the mock token endpoint returns HTTP 500.
// Optional funcs in cfgOverrides are applied to the OIDCConfig before provider creation.
func newOIDCHandlerSetup(t *testing.T, failToken bool, cfgOverrides ...func(*config.OIDCConfig)) (*OIDCHandler, sessionstore.SessionStore, *MockUserRepository) {
	t.Helper()

	const clientID = "oidc-test-client"
	oidcSvr := newOIDCHandlerTestServer(t, clientID, failToken)

	cfg := &config.OIDCConfig{
		Enabled:       true,
		ProviderURL:   oidcSvr.server.URL,
		ClientID:      clientID,
		ClientSecret:  "",
		RedirectURL:   "http://localhost:3000/api/v1/auth/oidc/callback",
		Scopes:        []string{"openid", "profile", "email"},
		RoleClaim:     "roles",
		AdminRoles:    []string{"k8s-stack-admin"},
		DevOpsRoles:   []string{"k8s-stack-devops"},
		AutoProvision: true,
		StateTTL:      5 * time.Minute,
	}
	for _, fn := range cfgOverrides {
		fn(cfg)
	}

	provider, err := auth.NewProvider(context.Background(), cfg)
	require.NoError(t, err, "newOIDCHandlerSetup: auth.NewProvider must not fail")

	store := sessionstore.NewMemoryStore()
	t.Cleanup(store.Stop)

	userRepo := NewMockUserRepository()
	h := NewOIDCHandler(provider, store, userRepo, cfg, testAuthConfig(false))
	return h, store, userRepo
}

// setupOIDCRouter builds a gin.Engine with the provided handler method registered
// at the given path using the given HTTP method.
func setupOIDCRouter(handler gin.HandlerFunc, method, path string) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Handle(method, path, handler)
	return r
}

// ---- GetConfig ----

func TestOIDCGetConfig(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name             string
		cfg              *config.OIDCConfig
		wantEnabled      bool
		wantProviderName string
		wantLocalAuth    bool
	}{
		{
			name:             "OIDC disabled returns local_auth_enabled:true",
			cfg:              &config.OIDCConfig{Enabled: false},
			wantEnabled:      false,
			wantProviderName: "SSO Provider",
			wantLocalAuth:    true,
		},
		{
			name: "OIDC enabled with Microsoft provider hides local auth",
			cfg: &config.OIDCConfig{
				Enabled:     true,
				ProviderURL: "https://login.microsoftonline.com/tenant-id/v2.0",
			},
			wantEnabled:      true,
			wantProviderName: "Microsoft",
			wantLocalAuth:    false,
		},
		{
			name: "OIDC enabled with Okta provider hides local auth",
			cfg: &config.OIDCConfig{
				Enabled:     true,
				ProviderURL: "https://company.okta.com/oauth2/default",
			},
			wantEnabled:      true,
			wantProviderName: "Okta",
			wantLocalAuth:    false,
		},
		{
			name: "OIDC enabled with Google provider hides local auth",
			cfg: &config.OIDCConfig{
				Enabled:     true,
				ProviderURL: "https://accounts.google.com",
			},
			wantEnabled:      true,
			wantProviderName: "Google",
			wantLocalAuth:    false,
		},
		{
			name: "OIDC enabled with Keycloak provider hides local auth",
			cfg: &config.OIDCConfig{
				Enabled:     true,
				ProviderURL: "https://auth.example.com/keycloak/realms/myrealm",
			},
			wantEnabled:      true,
			wantProviderName: "Keycloak",
			wantLocalAuth:    false,
		},
		{
			name: "OIDC enabled with unknown provider hides local auth",
			cfg: &config.OIDCConfig{
				Enabled:     true,
				ProviderURL: "https://custom-sso.example.com",
			},
			wantEnabled:      true,
			wantProviderName: "SSO Provider",
			wantLocalAuth:    false,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			h := NewOIDCHandler(nil, nil, nil, tt.cfg, testAuthConfig(false))
			r := setupOIDCRouter(h.GetConfig, http.MethodGet, "/api/v1/auth/oidc/config")

			w := httptest.NewRecorder()
			req, err := http.NewRequest(http.MethodGet, "/api/v1/auth/oidc/config", nil)
			require.NoError(t, err)
			r.ServeHTTP(w, req)

			assert.Equal(t, http.StatusOK, w.Code)

			var resp map[string]any
			require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
			assert.Equal(t, tt.wantEnabled, resp["enabled"])
			assert.Equal(t, tt.wantProviderName, resp["provider_name"])
			assert.Equal(t, tt.wantLocalAuth, resp["local_auth_enabled"])
		})
	}
}

// ---- Authorize ----

func TestOIDCAuthorize(t *testing.T) {
	t.Parallel()

	t.Run("OIDC not enabled returns 404", func(t *testing.T) {
		t.Parallel()

		h := NewOIDCHandler(nil, nil, nil, &config.OIDCConfig{Enabled: false}, testAuthConfig(false))
		r := setupOIDCRouter(h.Authorize, http.MethodGet, "/api/v1/auth/oidc/authorize")

		w := httptest.NewRecorder()
		req, err := http.NewRequest(http.MethodGet, "/api/v1/auth/oidc/authorize", nil)
		require.NoError(t, err)
		r.ServeHTTP(w, req)

		assert.Equal(t, http.StatusNotFound, w.Code)
	})

	t.Run("returns redirect_url with state and PKCE parameters", func(t *testing.T) {
		t.Parallel()

		h, _, _ := newOIDCHandlerSetup(t, false)
		r := setupOIDCRouter(h.Authorize, http.MethodGet, "/api/v1/auth/oidc/authorize")

		w := httptest.NewRecorder()
		req, err := http.NewRequest(http.MethodGet, "/api/v1/auth/oidc/authorize", nil)
		require.NoError(t, err)
		r.ServeHTTP(w, req)

		require.Equal(t, http.StatusOK, w.Code)
		var resp map[string]string
		require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))

		redirectURL := resp["redirect_url"]
		assert.NotEmpty(t, redirectURL, "redirect_url must be non-empty")
		assert.Contains(t, redirectURL, "state=", "redirect_url must contain state parameter")
		assert.Contains(t, redirectURL, "code_challenge=", "redirect_url must contain PKCE code_challenge")
		assert.Contains(t, redirectURL, "code_challenge_method=S256", "redirect_url must specify S256 method")
	})

	t.Run("redirect query param is stored in state entry", func(t *testing.T) {
		t.Parallel()

		h, stateStore, _ := newOIDCHandlerSetup(t, false)
		r := setupOIDCRouter(h.Authorize, http.MethodGet, "/api/v1/auth/oidc/authorize")

		w := httptest.NewRecorder()
		req, err := http.NewRequest(http.MethodGet, "/api/v1/auth/oidc/authorize?redirect=/dashboard", nil)
		require.NoError(t, err)
		r.ServeHTTP(w, req)

		require.Equal(t, http.StatusOK, w.Code)
		var resp map[string]string
		require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))

		// Extract the state nonce from the redirect_url so we can look it up.
		parsedURL, err := url.Parse(resp["redirect_url"])
		require.NoError(t, err)
		stateVal := parsedURL.Query().Get("state")
		require.NotEmpty(t, stateVal, "redirect_url must contain state param")

		// Consume (one-time use) to verify the stored redirect path.
		authState, consumeErr := stateStore.ConsumeOIDCState(context.Background(), stateVal)
		require.NoError(t, consumeErr)
		require.NotNil(t, authState, "state must be present in the session store")
		assert.Equal(t, "/dashboard", authState.RedirectURL)
	})

	t.Run("default redirect stored as / when no redirect param given", func(t *testing.T) {
		t.Parallel()

		h, stateStore, _ := newOIDCHandlerSetup(t, false)
		r := setupOIDCRouter(h.Authorize, http.MethodGet, "/api/v1/auth/oidc/authorize")

		w := httptest.NewRecorder()
		req, err := http.NewRequest(http.MethodGet, "/api/v1/auth/oidc/authorize", nil)
		require.NoError(t, err)
		r.ServeHTTP(w, req)

		require.Equal(t, http.StatusOK, w.Code)
		var resp map[string]string
		require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))

		parsedURL, err := url.Parse(resp["redirect_url"])
		require.NoError(t, err)
		stateVal := parsedURL.Query().Get("state")
		require.NotEmpty(t, stateVal)

		authState, consumeErr := stateStore.ConsumeOIDCState(context.Background(), stateVal)
		require.NoError(t, consumeErr)
		require.NotNil(t, authState)
		assert.Equal(t, "/", authState.RedirectURL, "default redirect must be /")
	})
}

// ---- Callback ----

func TestOIDCCallback(t *testing.T) {
	t.Parallel()

	t.Run("missing state redirects to login?error=invalid_state", func(t *testing.T) {
		t.Parallel()

		stateStore := sessionstore.NewMemoryStore()
		t.Cleanup(stateStore.Stop)

		h := NewOIDCHandler(nil, stateStore, NewMockUserRepository(), &config.OIDCConfig{Enabled: true}, testAuthConfig(false))
		r := setupOIDCRouter(h.Callback, http.MethodGet, "/api/v1/auth/oidc/callback")

		w := httptest.NewRecorder()
		req, err := http.NewRequest(http.MethodGet, "/api/v1/auth/oidc/callback?code=some-code", nil)
		require.NoError(t, err)
		r.ServeHTTP(w, req)

		assert.Equal(t, http.StatusFound, w.Code)
		assert.Contains(t, w.Header().Get("Location"), "/login?error=invalid_state")
	})

	t.Run("invalid state param redirects to login?error=invalid_state", func(t *testing.T) {
		t.Parallel()

		stateStore := sessionstore.NewMemoryStore()
		t.Cleanup(stateStore.Stop)

		h := NewOIDCHandler(nil, stateStore, NewMockUserRepository(), &config.OIDCConfig{Enabled: true}, testAuthConfig(false))
		r := setupOIDCRouter(h.Callback, http.MethodGet, "/api/v1/auth/oidc/callback")

		w := httptest.NewRecorder()
		req, err := http.NewRequest(http.MethodGet, "/api/v1/auth/oidc/callback?code=any-code&state=not-a-real-state", nil)
		require.NoError(t, err)
		r.ServeHTTP(w, req)

		assert.Equal(t, http.StatusFound, w.Code)
		assert.Contains(t, w.Header().Get("Location"), "/login?error=invalid_state")
	})

	t.Run("expired state redirects to login?error=invalid_state", func(t *testing.T) {
		t.Parallel()

		stateStore := sessionstore.NewMemoryStore()
		t.Cleanup(stateStore.Stop)

		// Store a state entry with a near-zero TTL so it is immediately expired.
		_ = stateStore.SaveOIDCState(context.Background(), "expired-state", sessionstore.OIDCStateData{
			CodeVerifier: "verifier",
			RedirectURL:  "/",
		}, time.Millisecond)
		time.Sleep(2 * time.Millisecond)

		h := NewOIDCHandler(nil, stateStore, NewMockUserRepository(), &config.OIDCConfig{Enabled: true}, testAuthConfig(false))
		r := setupOIDCRouter(h.Callback, http.MethodGet, "/api/v1/auth/oidc/callback")

		w := httptest.NewRecorder()
		req, err := http.NewRequest(http.MethodGet, "/api/v1/auth/oidc/callback?code=some-code&state=expired-state", nil)
		require.NoError(t, err)
		r.ServeHTTP(w, req)

		assert.Equal(t, http.StatusFound, w.Code)
		assert.Contains(t, w.Header().Get("Location"), "/login?error=invalid_state")
	})

	t.Run("code exchange failure redirects to login?error=auth_failed", func(t *testing.T) {
		t.Parallel()

		h, stateStore, _ := newOIDCHandlerSetup(t, true /* failToken */)

		_ = stateStore.SaveOIDCState(context.Background(), "valid-state-fail-exchange", sessionstore.OIDCStateData{
			CodeVerifier: "test-verifier",
			RedirectURL:  "/",
		}, 5*time.Minute)

		r := setupOIDCRouter(h.Callback, http.MethodGet, "/api/v1/auth/oidc/callback")

		w := httptest.NewRecorder()
		req, err := http.NewRequest(http.MethodGet, "/api/v1/auth/oidc/callback?code=any-code&state=valid-state-fail-exchange", nil)
		require.NoError(t, err)
		r.ServeHTTP(w, req)

		assert.Equal(t, http.StatusFound, w.Code)
		assert.Contains(t, w.Header().Get("Location"), "/login?error=auth_failed")
	})

	t.Run("happy path provisions new user and redirects with JWT", func(t *testing.T) {
		t.Parallel()

		h, stateStore, userRepo := newOIDCHandlerSetup(t, false)

		_ = stateStore.SaveOIDCState(context.Background(), "valid-state-happy", sessionstore.OIDCStateData{
			CodeVerifier: "test-verifier",
			RedirectURL:  "/dashboard",
			}, 5*time.Minute)

		r := setupOIDCRouter(h.Callback, http.MethodGet, "/api/v1/auth/oidc/callback")

		w := httptest.NewRecorder()
		req, err := http.NewRequest(http.MethodGet, "/api/v1/auth/oidc/callback?code=test-code&state=valid-state-happy", nil)
		require.NoError(t, err)
		r.ServeHTTP(w, req)

		require.Equal(t, http.StatusFound, w.Code)
		location := w.Header().Get("Location")
		assert.Contains(t, location, "/auth/callback#", "must redirect to callback")
		assert.Contains(t, location, "token=", "must include JWT token")
		assert.Contains(t, location, "redirect=", "must include redirect path")

		// Verify the user was provisioned in the repo.
		users, err := userRepo.List()
		require.NoError(t, err)
		require.Len(t, users, 1, "exactly one user must be provisioned")
		assert.Equal(t, "oidc", users[0].AuthProvider)
		assert.Equal(t, "oidc-user-001", *users[0].ExternalID)
		assert.Equal(t, "oidcuser@example.com", users[0].Email)
	})

	t.Run("admin role is mapped correctly on provisioned user", func(t *testing.T) {
		t.Parallel()

		h, stateStore, userRepo := newOIDCHandlerSetup(t, false)

		_ = stateStore.SaveOIDCState(context.Background(), "valid-state-role", sessionstore.OIDCStateData{
			CodeVerifier: "test-verifier",
			RedirectURL:  "/",
			}, 5*time.Minute)

		r := setupOIDCRouter(h.Callback, http.MethodGet, "/api/v1/auth/oidc/callback")

		w := httptest.NewRecorder()
		req, err := http.NewRequest(http.MethodGet, "/api/v1/auth/oidc/callback?code=test-code&state=valid-state-role", nil)
		require.NoError(t, err)
		r.ServeHTTP(w, req)

		require.Equal(t, http.StatusFound, w.Code)

		users, err := userRepo.List()
		require.NoError(t, err)
		require.Len(t, users, 1)
		// Mock token returns roles:["k8s-stack-admin"], config AdminRoles:["k8s-stack-admin"] → "admin".
		assert.Equal(t, "admin", users[0].Role, "IdP admin role must be mapped to app role admin")
	})

	t.Run("auto-provision disabled with unknown user redirects to login?error=no_account", func(t *testing.T) {
		t.Parallel()

		h, stateStore, _ := newOIDCHandlerSetup(t, false, func(cfg *config.OIDCConfig) {
			cfg.AutoProvision = false
		})

		_ = stateStore.SaveOIDCState(context.Background(), "valid-state-noprov", sessionstore.OIDCStateData{
			CodeVerifier: "test-verifier",
			RedirectURL:  "/",
			}, 5*time.Minute)

		r := setupOIDCRouter(h.Callback, http.MethodGet, "/api/v1/auth/oidc/callback")

		w := httptest.NewRecorder()
		req, err := http.NewRequest(http.MethodGet, "/api/v1/auth/oidc/callback?code=test-code&state=valid-state-noprov", nil)
		require.NoError(t, err)
		r.ServeHTTP(w, req)

		assert.Equal(t, http.StatusFound, w.Code)
		assert.Contains(t, w.Header().Get("Location"), "/login?error=no_account")
	})

	t.Run("existing OIDC user is updated and redirected with JWT", func(t *testing.T) {
		t.Parallel()

		h, stateStore, userRepo := newOIDCHandlerSetup(t, false)

		// Pre-seed a user that matches the mock token's sub claim.
		existingUser := &models.User{
			ID:           "existing-user-001",
			Username:     "oidcuser@example.com",
			DisplayName:  "Old Name",
			Role:         "user",
			AuthProvider: "oidc",
			ExternalID:   stringPtr("oidc-user-001"), // matches mock token "sub"
			Email:        "old@example.com",
		}
		require.NoError(t, userRepo.Create(existingUser))

		_ = stateStore.SaveOIDCState(context.Background(), "valid-state-existing", sessionstore.OIDCStateData{
			CodeVerifier: "test-verifier",
			RedirectURL:  "/",
			}, 5*time.Minute)

		r := setupOIDCRouter(h.Callback, http.MethodGet, "/api/v1/auth/oidc/callback")

		w := httptest.NewRecorder()
		req, err := http.NewRequest(http.MethodGet, "/api/v1/auth/oidc/callback?code=test-code&state=valid-state-existing", nil)
		require.NoError(t, err)
		r.ServeHTTP(w, req)

		require.Equal(t, http.StatusFound, w.Code)
		assert.Contains(t, w.Header().Get("Location"), "/auth/callback#", "must redirect to callback")
		assert.Contains(t, w.Header().Get("Location"), "token=", "must include JWT token")

		// Verify the existing user was updated in the repo.
		updated, err := userRepo.FindByID("existing-user-001")
		require.NoError(t, err)
		assert.Equal(t, "oidcuser@example.com", updated.Email, "email should be updated from token claims")
		assert.Equal(t, "admin", updated.Role, "role should be updated via role mapping")
	})

	t.Run("state is consumed (one-time use) after successful callback", func(t *testing.T) {
		t.Parallel()

		h, stateStore, _ := newOIDCHandlerSetup(t, false)

		_ = stateStore.SaveOIDCState(context.Background(), "valid-state-consumed", sessionstore.OIDCStateData{
			CodeVerifier: "test-verifier",
			RedirectURL:  "/",
			}, 5*time.Minute)

		r := setupOIDCRouter(h.Callback, http.MethodGet, "/api/v1/auth/oidc/callback")

		// First request: succeeds.
		w1 := httptest.NewRecorder()
		req1, err := http.NewRequest(http.MethodGet, "/api/v1/auth/oidc/callback?code=code1&state=valid-state-consumed", nil)
		require.NoError(t, err)
		r.ServeHTTP(w1, req1)
		assert.Equal(t, http.StatusFound, w1.Code)
		assert.Contains(t, w1.Header().Get("Location"), "/auth/callback#")
		assert.Contains(t, w1.Header().Get("Location"), "token=")

		// Second request with same state: state was consumed, must fail with invalid_state.
		w2 := httptest.NewRecorder()
		req2, err := http.NewRequest(http.MethodGet, "/api/v1/auth/oidc/callback?code=code2&state=valid-state-consumed", nil)
		require.NoError(t, err)
		r.ServeHTTP(w2, req2)
		assert.Equal(t, http.StatusFound, w2.Code)
		assert.Contains(t, w2.Header().Get("Location"), "/login?error=invalid_state", "replayed state must be rejected")
	})
}

// ---- provisionUser race condition tests ----

func TestCallback_DuplicateKeyRaceCondition(t *testing.T) {
	t.Parallel()

	h, stateStore, userRepo := newOIDCHandlerSetup(t, false)

	// Simulate a race: Create returns duplicate key, but on retry FindByExternalID
	// succeeds because the "other" goroutine's user is now visible.
	raceUser := &models.User{
		ID:           "race-winner-001",
		Username:     "oidcuser@example.com",
		DisplayName:  "OIDC User",
		Role:         "admin",
		AuthProvider: "oidc",
		ExternalID:   stringPtr("oidc-user-001"), // matches mock token "sub"
		Email:        "oidcuser@example.com",
	}

	userRepo.SetCreateFunc(func(user *models.User) error {
		// Simulate the race winner inserting the user just before us.
		// createFunc runs under the mock's lock, so manipulate maps directly.
		userRepo.users[raceUser.ID] = raceUser
		userRepo.byName[raceUser.Username] = raceUser
		return errors.New("duplicate key")
	})

	_ = stateStore.SaveOIDCState(context.Background(), "valid-state-race", sessionstore.OIDCStateData{
		CodeVerifier: "test-verifier",
		RedirectURL:  "/dashboard",
		}, 5*time.Minute)

	r := setupOIDCRouter(h.Callback, http.MethodGet, "/api/v1/auth/oidc/callback")

	w := httptest.NewRecorder()
	req, err := http.NewRequest(http.MethodGet, "/api/v1/auth/oidc/callback?code=test-code&state=valid-state-race", nil)
	require.NoError(t, err)
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusFound, w.Code)
	location := w.Header().Get("Location")
	assert.Contains(t, location, "/auth/callback#", "must redirect to callback on race-condition recovery")
	assert.Contains(t, location, "token=", "must include JWT token")
}

func TestCallback_DuplicateKeyRetryFails(t *testing.T) {
	t.Parallel()

	h, stateStore, userRepo := newOIDCHandlerSetup(t, false)

	// Simulate: Create returns duplicate key, but retry FindByExternalID also fails
	// (e.g., transient DB error on the second lookup).
	userRepo.SetCreateFunc(func(user *models.User) error {
		// After this returns, FindByExternalID will still find no user (empty repo)
		// AND we set findErr so the retry also fails.
		// createFunc runs under the mock's lock, so set the field directly.
		userRepo.findErr = errors.New("transient database error")
		return errors.New("duplicate key")
	})

	_ = stateStore.SaveOIDCState(context.Background(), "valid-state-retry-fail", sessionstore.OIDCStateData{
		CodeVerifier: "test-verifier",
		RedirectURL:  "/",
		}, 5*time.Minute)

	r := setupOIDCRouter(h.Callback, http.MethodGet, "/api/v1/auth/oidc/callback")

	w := httptest.NewRecorder()
	req, err := http.NewRequest(http.MethodGet, "/api/v1/auth/oidc/callback?code=test-code&state=valid-state-retry-fail", nil)
	require.NoError(t, err)
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusFound, w.Code)
	assert.Contains(t, w.Header().Get("Location"), "/login?error=auth_failed",
		"must redirect to login with error when retry also fails")
}

// ---- deriveOIDCUsername ----

func TestDeriveOIDCUsername(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		user     *auth.OIDCUser
		expected string
	}{
		{
			name:     "prefers email when present",
			user:     &auth.OIDCUser{Email: "alice@example.com", Name: "Alice", Subject: "sub-001"},
			expected: "alice@example.com",
		},
		{
			name:     "falls back to name when email is empty",
			user:     &auth.OIDCUser{Email: "", Name: "Alice", Subject: "sub-001"},
			expected: "Alice",
		},
		{
			name:     "falls back to subject when email and name are empty",
			user:     &auth.OIDCUser{Email: "", Name: "", Subject: "sub-001"},
			expected: "sub-001",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.expected, deriveOIDCUsername(tt.user))
		})
	}
}

// ---- isSafeRedirect ----

func TestIsSafeRedirect(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		target string
		want   bool
	}{
		{name: "empty string", target: "", want: false},
		{name: "root path", target: "/", want: true},
		{name: "relative path", target: "/dashboard", want: true},
		{name: "nested relative path", target: "/admin/users", want: true},
		{name: "protocol-relative URL", target: "//evil.com", want: false},
		{name: "absolute URL with scheme", target: "https://evil.com/path", want: false},
		{name: "javascript scheme", target: "javascript:alert(1)", want: false},
		{name: "no leading slash", target: "dashboard", want: false},
		{name: "path with query params", target: "/page?foo=bar", want: true},
		{name: "path with fragment", target: "/page#section", want: true},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, isSafeRedirect(tt.target))
		})
	}
}

// ---- Authorize unsafe redirect ----

func TestOIDCAuthorize_UnsafeRedirectFallsBackToRoot(t *testing.T) {
	t.Parallel()

	h, stateStore, _ := newOIDCHandlerSetup(t, false)
	r := setupOIDCRouter(h.Authorize, http.MethodGet, "/api/v1/auth/oidc/authorize")

	w := httptest.NewRecorder()
	req, err := http.NewRequest(http.MethodGet, "/api/v1/auth/oidc/authorize?redirect=//evil.com", nil)
	require.NoError(t, err)
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var resp map[string]string
	require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))

	// Extract the state nonce to verify stored redirect was sanitized to "/".
	parsedURL, err := url.Parse(resp["redirect_url"])
	require.NoError(t, err)
	stateVal := parsedURL.Query().Get("state")
	require.NotEmpty(t, stateVal)

	authState, consumeErr := stateStore.ConsumeOIDCState(context.Background(), stateVal)
	require.NoError(t, consumeErr)
	require.NotNil(t, authState)
	assert.Equal(t, "/", authState.RedirectURL, "unsafe redirect must be sanitized to /")
}

// ---- provisionUser error paths ----

func TestCallback_FindByExternalIDReturnsUnexpectedError(t *testing.T) {
	t.Parallel()

	h, stateStore, userRepo := newOIDCHandlerSetup(t, false)

	// Make FindByExternalID return a non-not-found error.
	userRepo.findErr = errors.New("database connection lost")

	_ = stateStore.SaveOIDCState(context.Background(), "valid-state-dberr", sessionstore.OIDCStateData{
		CodeVerifier: "test-verifier",
		RedirectURL:  "/",
		}, 5*time.Minute)

	r := setupOIDCRouter(h.Callback, http.MethodGet, "/api/v1/auth/oidc/callback")

	w := httptest.NewRecorder()
	req, err := http.NewRequest(http.MethodGet, "/api/v1/auth/oidc/callback?code=test-code&state=valid-state-dberr", nil)
	require.NoError(t, err)
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusFound, w.Code)
	assert.Contains(t, w.Header().Get("Location"), "/login?error=auth_failed",
		"unexpected DB error during lookup should redirect with auth_failed")
}

func TestCallback_UpdateExistingUserFails(t *testing.T) {
	t.Parallel()

	h, stateStore, userRepo := newOIDCHandlerSetup(t, false)

	// Pre-seed an existing user with outdated fields to trigger an update.
	existingUser := &models.User{
		ID:           "update-fail-001",
		Username:     "oidcuser@example.com",
		DisplayName:  "Old Name",
		Role:         "user",
		AuthProvider: "oidc",
		ExternalID:   stringPtr("oidc-user-001"),
		Email:        "old@example.com",
	}
	require.NoError(t, userRepo.Create(existingUser))

	// Make Update fail.
	userRepo.updateErr = errors.New("db write failed")

	_ = stateStore.SaveOIDCState(context.Background(), "valid-state-update-fail", sessionstore.OIDCStateData{
		CodeVerifier: "test-verifier",
		RedirectURL:  "/",
		}, 5*time.Minute)

	r := setupOIDCRouter(h.Callback, http.MethodGet, "/api/v1/auth/oidc/callback")

	w := httptest.NewRecorder()
	req, err := http.NewRequest(http.MethodGet, "/api/v1/auth/oidc/callback?code=test-code&state=valid-state-update-fail", nil)
	require.NoError(t, err)
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusFound, w.Code)
	assert.Contains(t, w.Header().Get("Location"), "/login?error=auth_failed",
		"update failure should redirect with auth_failed")
}

// ---- helper function tests ----

func TestIsDuplicateError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "nil error", err: nil, want: false},
		{name: "dberrors ErrDuplicateKey", err: dberrors.NewDatabaseError("create", dberrors.ErrDuplicateKey), want: true},
		{name: "duplicate key lowercase", err: errors.New("duplicate key"), want: true},
		{name: "Duplicate entry MySQL", err: errors.New("Duplicate entry '1' for key 'PRIMARY'"), want: true},
		{name: "wrapped duplicate key", err: fmt.Errorf("create failed: %w", errors.New("duplicate key violation")), want: true},
		{name: "unrelated error", err: errors.New("connection timeout"), want: false},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, isDuplicateError(tt.err))
		})
	}
}

// ---- CLIAuth ----

func TestOIDCCLIAuth_Disabled(t *testing.T) {
	t.Parallel()

	h := NewOIDCHandler(nil, nil, nil, &config.OIDCConfig{Enabled: false}, testAuthConfig(false))
	r := setupOIDCRouter(h.CLIAuth, http.MethodPost, "/api/v1/auth/oidc/cli-auth")

	w := httptest.NewRecorder()
	req, err := http.NewRequest(http.MethodPost, "/api/v1/auth/oidc/cli-auth", nil)
	require.NoError(t, err)
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusNotFound, w.Code)
}

func TestOIDCCLIAuth_ReturnsSessionAndURL(t *testing.T) {
	t.Parallel()

	h, _, _ := newOIDCHandlerSetup(t, false)
	r := setupOIDCRouter(h.CLIAuth, http.MethodPost, "/api/v1/auth/oidc/cli-auth")

	w := httptest.NewRecorder()
	req, err := http.NewRequest(http.MethodPost, "/api/v1/auth/oidc/cli-auth", nil)
	require.NoError(t, err)
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var resp map[string]any
	require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))

	assert.NotEmpty(t, resp["session_id"], "session_id must be non-empty")
	assert.NotEmpty(t, resp["login_url"], "login_url must be non-empty")
	assert.Equal(t, float64(300), resp["expires_in"])

	loginURL, ok := resp["login_url"].(string)
	require.True(t, ok)
	assert.Contains(t, loginURL, "state=")
	assert.Contains(t, loginURL, "code_challenge=")
}

// ---- CLIToken ----

func TestOIDCCLIToken_Pending(t *testing.T) {
	t.Parallel()

	store := sessionstore.NewMemoryStore()
	t.Cleanup(store.Stop)

	_ = store.SaveCLIAuth(context.Background(), "sess-pending", sessionstore.CLIAuthData{Status: "pending"}, 5*time.Minute)

	h := NewOIDCHandler(nil, store, nil, &config.OIDCConfig{Enabled: true}, testAuthConfig(false))
	r := setupOIDCRouter(h.CLIToken, http.MethodPost, "/api/v1/auth/oidc/cli-token")

	w := httptest.NewRecorder()
	body := `{"session_id":"sess-pending"}`
	req, err := http.NewRequest(http.MethodPost, "/api/v1/auth/oidc/cli-token", strings.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var resp map[string]any
	require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
	assert.Equal(t, "pending", resp["status"])
	assert.Nil(t, resp["token"], "token must not be present when pending")
}

func TestOIDCCLIToken_Completed(t *testing.T) {
	t.Parallel()

	store := sessionstore.NewMemoryStore()
	t.Cleanup(store.Stop)

	_ = store.SaveCLIAuth(context.Background(), "sess-done", sessionstore.CLIAuthData{
		Token:    "jwt-abc",
		UserID:   "uid-1",
		Username: "alice",
		Status:   "completed",
	}, 5*time.Minute)

	h := NewOIDCHandler(nil, store, nil, &config.OIDCConfig{Enabled: true}, testAuthConfig(false))
	r := setupOIDCRouter(h.CLIToken, http.MethodPost, "/api/v1/auth/oidc/cli-token")

	w := httptest.NewRecorder()
	body := `{"session_id":"sess-done"}`
	req, err := http.NewRequest(http.MethodPost, "/api/v1/auth/oidc/cli-token", strings.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var resp map[string]any
	require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
	assert.Equal(t, "completed", resp["status"])
	assert.Equal(t, "jwt-abc", resp["token"])
	assert.Equal(t, "alice", resp["username"])
	assert.Equal(t, "uid-1", resp["user_id"])
}

func TestOIDCCLIToken_NotFound(t *testing.T) {
	t.Parallel()

	store := sessionstore.NewMemoryStore()
	t.Cleanup(store.Stop)

	h := NewOIDCHandler(nil, store, nil, &config.OIDCConfig{Enabled: true}, testAuthConfig(false))
	r := setupOIDCRouter(h.CLIToken, http.MethodPost, "/api/v1/auth/oidc/cli-token")

	w := httptest.NewRecorder()
	body := `{"session_id":"no-such-session"}`
	req, err := http.NewRequest(http.MethodPost, "/api/v1/auth/oidc/cli-token", strings.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusGone, w.Code)
	var resp map[string]any
	require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
	assert.Equal(t, "session expired or not found", resp["error"])
}

func TestOIDCCLIToken_MissingSessionID(t *testing.T) {
	t.Parallel()

	store := sessionstore.NewMemoryStore()
	t.Cleanup(store.Stop)

	h := NewOIDCHandler(nil, store, nil, &config.OIDCConfig{Enabled: true}, testAuthConfig(false))
	r := setupOIDCRouter(h.CLIToken, http.MethodPost, "/api/v1/auth/oidc/cli-token")

	w := httptest.NewRecorder()
	body := `{}`
	req, err := http.NewRequest(http.MethodPost, "/api/v1/auth/oidc/cli-token", strings.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

// ---- Callback CLI auth integration ----

func TestOIDCCallback_CLIAuthSession(t *testing.T) {
	t.Parallel()

	h, stateStore, _ := newOIDCHandlerSetup(t, false)

	sessionID := "cli-session-001"

	// Save OIDC state with CLI redirect prefix.
	_ = stateStore.SaveOIDCState(context.Background(), "valid-state-cli", sessionstore.OIDCStateData{
		CodeVerifier: "test-verifier",
		RedirectURL:  "cli:" + sessionID,
	}, 5*time.Minute)

	// Save pending CLI auth session.
	_ = stateStore.SaveCLIAuth(context.Background(), sessionID, sessionstore.CLIAuthData{
		Status: "pending",
	}, 10*time.Minute)

	r := setupOIDCRouter(h.Callback, http.MethodGet, "/api/v1/auth/oidc/callback")

	w := httptest.NewRecorder()
	req, err := http.NewRequest(http.MethodGet, "/api/v1/auth/oidc/callback?code=test-code&state=valid-state-cli", nil)
	require.NoError(t, err)
	r.ServeHTTP(w, req)

	// CLI callback should return 200 with HTML, not a redirect.
	require.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Header().Get("Content-Type"), "text/html")
	assert.Contains(t, w.Body.String(), "Authentication successful")

	// Verify CLI auth session was updated to completed.
	data, err := stateStore.GetCLIAuth(context.Background(), sessionID)
	require.NoError(t, err)
	require.NotNil(t, data)
	assert.Equal(t, "completed", data.Status)
	assert.NotEmpty(t, data.Token, "token must be set after callback")
	assert.NotEmpty(t, data.UserID, "user_id must be set after callback")
	assert.NotEmpty(t, data.Username, "username must be set after callback")
}

func TestIsNotFoundError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "nil error", err: nil, want: false},
		{name: "dberrors ErrNotFound", err: dberrors.NewDatabaseError("find", dberrors.ErrNotFound), want: true},
		{name: "not found", err: errors.New("not found"), want: true},
		{name: "record not found", err: errors.New("record not found"), want: true},
		{name: "wrapped not found", err: fmt.Errorf("lookup: %w", errors.New("user not found")), want: true},
		{name: "unrelated error", err: errors.New("connection refused"), want: false},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, isNotFoundError(tt.err))
		})
	}
}
