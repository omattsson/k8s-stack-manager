package sessionstore

import (
	"context"
	"time"
)

type OIDCStateData struct {
	CodeVerifier string `json:"code_verifier"`
	RedirectURL  string `json:"redirect_url"`
}

type SessionStore interface {
	BlockToken(ctx context.Context, jti string, expiresAt time.Time) error
	IsTokenBlocked(ctx context.Context, jti string) (bool, error)
	SaveOIDCState(ctx context.Context, state string, data OIDCStateData, ttl time.Duration) error
	ConsumeOIDCState(ctx context.Context, state string) (*OIDCStateData, error)
	Cleanup(ctx context.Context) error
	Stop()
}
