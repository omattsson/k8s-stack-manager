package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

	"backend/internal/config"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

// OIDCUser represents the user information extracted from an OIDC ID token.
type OIDCUser struct {
	Subject string
	Email   string
	Name    string
	Roles   []string
}

// Provider handles OIDC operations: authorization URL generation, code exchange, and token validation.
type Provider struct {
	config     *config.OIDCConfig
	oidcConfig *oidc.Provider
	oauth2Cfg  *oauth2.Config
	verifier   *oidc.IDTokenVerifier
}

// NewProvider creates a new OIDC provider by performing discovery against the configured provider URL.
func NewProvider(ctx context.Context, cfg *config.OIDCConfig) (*Provider, error) {
	provider, err := oidc.NewProvider(ctx, cfg.ProviderURL)
	if err != nil {
		return nil, fmt.Errorf("OIDC discovery failed: %w", err)
	}

	oauth2Cfg := &oauth2.Config{
		ClientID:     cfg.ClientID,
		ClientSecret: cfg.ClientSecret,
		RedirectURL:  cfg.RedirectURL,
		Endpoint:     provider.Endpoint(),
		Scopes:       cfg.Scopes,
	}

	verifier := provider.Verifier(&oidc.Config{ClientID: cfg.ClientID})

	return &Provider{
		config:     cfg,
		oidcConfig: provider,
		oauth2Cfg:  oauth2Cfg,
		verifier:   verifier,
	}, nil
}

// AuthorizationURL generates the OIDC authorization URL with PKCE code challenge and state.
func (p *Provider) AuthorizationURL(state, codeChallenge string) string {
	return p.oauth2Cfg.AuthCodeURL(state,
		oauth2.SetAuthURLParam("code_challenge", codeChallenge),
		oauth2.SetAuthURLParam("code_challenge_method", "S256"),
	)
}

// Exchange trades an authorization code for tokens and returns the extracted user information.
func (p *Provider) Exchange(ctx context.Context, code, codeVerifier string) (*OIDCUser, error) {
	token, err := p.oauth2Cfg.Exchange(ctx, code, oauth2.SetAuthURLParam("code_verifier", codeVerifier))
	if err != nil {
		return nil, fmt.Errorf("token exchange failed: %w", err)
	}

	rawIDToken, ok := token.Extra("id_token").(string)
	if !ok {
		return nil, fmt.Errorf("no id_token in token response")
	}

	idToken, err := p.verifier.Verify(ctx, rawIDToken)
	if err != nil {
		return nil, fmt.Errorf("id_token verification failed: %w", err)
	}

	var claims map[string]interface{}
	if err := idToken.Claims(&claims); err != nil {
		return nil, fmt.Errorf("failed to parse claims: %w", err)
	}

	user := &OIDCUser{
		Subject: idToken.Subject,
		Email:   claimString(claims, "email"),
		Name:    claimString(claims, "preferred_username"),
		Roles:   p.extractRoles(claims),
	}

	if user.Name == "" {
		user.Name = claimString(claims, "name")
	}

	return user, nil
}

// MapRole maps IdP roles to app roles using the configured admin/devops role lists.
// Returns "admin" if any IdP role matches AdminRoles, "devops" if matches DevOpsRoles, else "user".
func (p *Provider) MapRole(idpRoles []string) string {
	for _, role := range idpRoles {
		for _, adminRole := range p.config.AdminRoles {
			if strings.EqualFold(role, adminRole) {
				return "admin"
			}
		}
	}
	for _, role := range idpRoles {
		for _, devopsRole := range p.config.DevOpsRoles {
			if strings.EqualFold(role, devopsRole) {
				return "devops"
			}
		}
	}
	return "user"
}

// extractRoles extracts role values from the configured role claim path.
func (p *Provider) extractRoles(claims map[string]interface{}) []string {
	val, ok := claims[p.config.RoleClaim]
	if !ok {
		return nil
	}

	switch v := val.(type) {
	case []interface{}:
		roles := make([]string, 0, len(v))
		for _, item := range v {
			if s, ok := item.(string); ok {
				roles = append(roles, s)
			}
		}
		return roles
	case string:
		return []string{v}
	default:
		// Try JSON re-marshal for nested types.
		data, err := json.Marshal(val)
		if err != nil {
			return nil
		}
		var roles []string
		if err := json.Unmarshal(data, &roles); err != nil {
			return nil
		}
		return roles
	}
}

func claimString(claims map[string]interface{}, key string) string {
	if v, ok := claims[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

// GenerateCodeVerifier generates a PKCE code verifier and its S256 challenge.
func GenerateCodeVerifier() (verifier string, challenge string, err error) {
	buf := make([]byte, 32)
	if _, err = rand.Read(buf); err != nil {
		return "", "", fmt.Errorf("failed to generate code verifier: %w", err)
	}
	verifier = base64.RawURLEncoding.EncodeToString(buf)
	h := sha256.Sum256([]byte(verifier))
	challenge = base64.RawURLEncoding.EncodeToString(h[:])
	return verifier, challenge, nil
}

// GenerateState generates a cryptographically random state nonce.
func GenerateState() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("failed to generate state: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}
