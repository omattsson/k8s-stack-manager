package auth

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"backend/internal/config"

	jwtlib "github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---- shared RSA key for OIDC tests (generated once per test binary) ----

var (
	oidcTestPrivKey     *rsa.PrivateKey
	oidcTestPrivKeyOnce sync.Once
)

func getOIDCTestPrivKey() *rsa.PrivateKey {
	oidcTestPrivKeyOnce.Do(func() {
		var err error
		oidcTestPrivKey, err = rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			panic(fmt.Sprintf("failed to generate RSA test key: %v", err))
		}
	})
	return oidcTestPrivKey
}

// ---- mock OIDC identity provider ----

// oidcTestServer is a minimal httptest server acting as an OIDC identity provider.
type oidcTestServer struct {
	server    *httptest.Server
	clientID  string
	kid       string
	failToken bool // when true the /token endpoint returns HTTP 500
}

// newOIDCTestServer starts a mock OIDC server and registers t.Cleanup to close it.
// When failToken is true the /token endpoint will always return HTTP 500.
func newOIDCTestServer(t *testing.T, clientID string, failToken bool) *oidcTestServer {
	t.Helper()

	priv := getOIDCTestPrivKey()
	ts := &oidcTestServer{
		clientID:  clientID,
		kid:       "test-key-1",
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

// newTestProvider creates a real auth.Provider backed by the mock OIDC server.
func newTestProvider(t *testing.T, ts *oidcTestServer, extraCfg ...func(*config.OIDCConfig)) *Provider {
	t.Helper()
	cfg := &config.OIDCConfig{
		ProviderURL:  ts.server.URL,
		ClientID:     ts.clientID,
		ClientSecret: "",
		RedirectURL:  "http://localhost:3000/auth/callback",
		Scopes:       []string{"openid", "profile", "email"},
		RoleClaim:    "roles",
		AdminRoles:   []string{"k8s-stack-admin"},
		DevOpsRoles:  []string{"k8s-stack-devops"},
	}
	for _, fn := range extraCfg {
		fn(cfg)
	}
	p, err := NewProvider(context.Background(), cfg)
	require.NoError(t, err)
	return p
}

// ---- GenerateCodeVerifier ----

func TestGenerateCodeVerifier(t *testing.T) {
	t.Parallel()

	verifier, challenge, err := GenerateCodeVerifier()
	require.NoError(t, err)
	assert.NotEmpty(t, verifier)
	assert.NotEmpty(t, challenge)

	// Challenge must be base64url(SHA256(verifier)).
	h := sha256.Sum256([]byte(verifier))
	expected := base64.RawURLEncoding.EncodeToString(h[:])
	assert.Equal(t, expected, challenge, "challenge must equal base64url(SHA256(verifier))")

	// Both values must be valid base64url (no +, /, = characters).
	_, err = base64.RawURLEncoding.DecodeString(verifier)
	assert.NoError(t, err, "verifier must be valid base64url")
	_, err = base64.RawURLEncoding.DecodeString(challenge)
	assert.NoError(t, err, "challenge must be valid base64url")

	// Each call must produce distinct values.
	v2, c2, err := GenerateCodeVerifier()
	require.NoError(t, err)
	assert.NotEqual(t, verifier, v2, "each call must yield a unique verifier")
	assert.NotEqual(t, challenge, c2, "each call must yield a unique challenge")
}

// ---- GenerateState ----

func TestGenerateState(t *testing.T) {
	t.Parallel()

	s1, err := GenerateState()
	require.NoError(t, err)
	assert.NotEmpty(t, s1)
	// Base64url of 32 random bytes produces at least 43 characters (ceiling(32*4/3)).
	assert.GreaterOrEqual(t, len(s1), 43, "state must be at least 43 characters long")

	_, err = base64.RawURLEncoding.DecodeString(s1)
	assert.NoError(t, err, "state must be valid base64url")

	// Each call must return a distinct value.
	s2, err := GenerateState()
	require.NoError(t, err)
	assert.NotEqual(t, s1, s2, "each call must produce a unique state")
}

// ---- MapRole ----

func TestMapRole(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		adminRoles  []string
		devopsRoles []string
		idpRoles    []string
		wantRole    string
	}{
		{
			name:     "no configured roles — defaults to user",
			idpRoles: []string{"some-other-role"},
			wantRole: "user",
		},
		{
			name:       "empty idp roles — defaults to user",
			adminRoles: []string{"k8s-stack-admin"},
			idpRoles:   []string{},
			wantRole:   "user",
		},
		{
			name:       "nil idp roles — defaults to user",
			adminRoles: []string{"k8s-stack-admin"},
			idpRoles:   nil,
			wantRole:   "user",
		},
		{
			name:       "idp role matches admin",
			adminRoles: []string{"k8s-stack-admin"},
			idpRoles:   []string{"k8s-stack-admin"},
			wantRole:   "admin",
		},
		{
			name:        "idp role matches devops",
			devopsRoles: []string{"k8s-stack-devops"},
			idpRoles:    []string{"k8s-stack-devops"},
			wantRole:    "devops",
		},
		{
			name:        "admin takes priority over devops when both match",
			adminRoles:  []string{"k8s-stack-admin"},
			devopsRoles: []string{"k8s-stack-devops"},
			idpRoles:    []string{"k8s-stack-admin", "k8s-stack-devops"},
			wantRole:    "admin",
		},
		{
			name:       "case-insensitive admin match (EqualFold)",
			adminRoles: []string{"k8s-stack-admin"},
			idpRoles:   []string{"K8S-STACK-ADMIN"},
			wantRole:   "admin",
		},
		{
			name:        "case-insensitive devops match (EqualFold)",
			devopsRoles: []string{"k8s-stack-devops"},
			idpRoles:    []string{"K8S-STACK-DEVOPS"},
			wantRole:    "devops",
		},
		{
			name:        "no match against either list — defaults to user",
			adminRoles:  []string{"k8s-stack-admin"},
			devopsRoles: []string{"k8s-stack-devops"},
			idpRoles:    []string{"unrelated-role"},
			wantRole:    "user",
		},
		{
			name:       "multiple idp roles — first admin match wins",
			adminRoles: []string{"k8s-stack-admin"},
			idpRoles:   []string{"viewer", "k8s-stack-admin", "developer"},
			wantRole:   "admin",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			p := &Provider{
				config: &config.OIDCConfig{
					AdminRoles:  tt.adminRoles,
					DevOpsRoles: tt.devopsRoles,
				},
			}
			got := p.MapRole(tt.idpRoles)
			assert.Equal(t, tt.wantRole, got)
		})
	}
}

// ---- extractRoles ----

func TestExtractRoles(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		claimKey  string
		claims    map[string]any
		wantRoles []string
	}{
		{
			name:      "claim is a []interface{} (standard JSON array)",
			claimKey:  "roles",
			claims:    map[string]any{"roles": []any{"admin", "user"}},
			wantRoles: []string{"admin", "user"},
		},
		{
			name:      "claim is a single string",
			claimKey:  "roles",
			claims:    map[string]any{"roles": "admin"},
			wantRoles: []string{"admin"},
		},
		{
			name:      "claim is missing — returns nil",
			claimKey:  "roles",
			claims:    map[string]any{"email": "user@example.com"},
			wantRoles: nil,
		},
		{
			name:      "empty claims map — returns nil",
			claimKey:  "roles",
			claims:    map[string]any{},
			wantRoles: nil,
		},
		{
			name:      "custom claim path (non-default key)",
			claimKey:  "groups",
			claims:    map[string]any{"groups": []any{"devops"}},
			wantRoles: []string{"devops"},
		},
		{
			name:      "non-string items in array are filtered out",
			claimKey:  "roles",
			claims:    map[string]any{"roles": []any{"admin", 42, "user", true}},
			wantRoles: []string{"admin", "user"},
		},
		{
			name:      "empty array returns empty (non-nil) slice",
			claimKey:  "roles",
			claims:    map[string]any{"roles": []any{}},
			wantRoles: []string{},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			p := &Provider{
				config: &config.OIDCConfig{RoleClaim: tt.claimKey},
			}
			got := p.extractRoles(tt.claims)
			assert.Equal(t, tt.wantRoles, got)
		})
	}
}

// ---- NewProvider ----

func TestNewProvider(t *testing.T) {
	t.Parallel()

	const clientID = "test-client"
	oidcSvr := newOIDCTestServer(t, clientID, false)

	cfg := &config.OIDCConfig{
		ProviderURL:  oidcSvr.server.URL,
		ClientID:     clientID,
		ClientSecret: "test-secret",
		RedirectURL:  "http://localhost:3000/auth/callback",
		Scopes:       []string{"openid", "profile", "email"},
		RoleClaim:    "roles",
	}

	provider, err := NewProvider(context.Background(), cfg)
	require.NoError(t, err)
	require.NotNil(t, provider)
	// Ensure the config pointer is stored unchanged.
	assert.Equal(t, clientID, provider.config.ClientID)
	assert.Equal(t, oidcSvr.server.URL, provider.config.ProviderURL)
}

func TestNewProvider_UnreachableEndpoint(t *testing.T) {
	t.Parallel()

	cfg := &config.OIDCConfig{
		ProviderURL: "http://127.0.0.1:1", // nothing listening on port 1
		ClientID:    "test-client",
	}

	_, err := NewProvider(context.Background(), cfg)
	assert.Error(t, err, "discovery against unreachable endpoint must fail")
	assert.Contains(t, err.Error(), "OIDC discovery failed")
}

// ---- Exchange ----

func TestExchange(t *testing.T) {
	t.Parallel()

	const clientID = "test-client"
	oidcSvr := newOIDCTestServer(t, clientID, false)
	provider := newTestProvider(t, oidcSvr)

	verifier, _, err := GenerateCodeVerifier()
	require.NoError(t, err)

	user, err := provider.Exchange(context.Background(), "mock-code", verifier)
	require.NoError(t, err)
	require.NotNil(t, user)

	assert.Equal(t, "oidc-user-001", user.Subject)
	assert.Equal(t, "oidcuser@example.com", user.Email)
	assert.Equal(t, "OIDC User", user.Name, "name claim takes priority over preferred_username")
	assert.Equal(t, []string{"k8s-stack-admin"}, user.Roles)
}

func TestExchange_TokenEndpointError(t *testing.T) {
	t.Parallel()

	const clientID = "test-client"
	oidcSvr := newOIDCTestServer(t, clientID, true /* failToken */)
	provider := newTestProvider(t, oidcSvr)

	verifier, _, err := GenerateCodeVerifier()
	require.NoError(t, err)

	_, err = provider.Exchange(context.Background(), "bad-code", verifier)
	assert.Error(t, err, "exchange against failing token endpoint must return an error")
	assert.Contains(t, err.Error(), "token exchange failed")
}

func TestExchange_NoIDTokenInResponse(t *testing.T) {
	t.Parallel()

	// Build a server where /token returns a valid OAuth2 response but WITHOUT id_token.
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
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
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []any{}})
	})

	// Token endpoint returns only access_token — no id_token field.
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "some-access-token",
			"token_type":   "Bearer",
			"expires_in":   3600,
		})
	})

	cfg := &config.OIDCConfig{
		ProviderURL: issuer,
		ClientID:    "test-client",
		Scopes:      []string{"openid"},
		RoleClaim:   "roles",
	}
	provider, err := NewProvider(context.Background(), cfg)
	require.NoError(t, err)

	_, err = provider.Exchange(context.Background(), "code", "verifier")
	assert.Error(t, err, "missing id_token in response must return an error")
	assert.Contains(t, err.Error(), "no id_token")
}
