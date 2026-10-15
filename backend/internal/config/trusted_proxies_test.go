package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseTrustedProxies(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		raw     string
		want    []string
		wantErr bool
	}{
		{name: "empty trusts no proxy", raw: "", want: nil},
		{name: "blanks only", raw: " , ", want: nil},
		{name: "private ranges", raw: "10.0.0.0/8, 172.16.0.0/12,192.168.0.0/16", want: []string{"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16"}},
		{name: "single IPv4 and IPv6", raw: "192.168.1.10,::1,fd00::/8", want: []string{"192.168.1.10", "::1", "fd00::/8"}},
		{name: "host name", raw: "10.0.0.0/8,traefik", wantErr: true},
		{name: "bad CIDR", raw: "10.0.0.0/33", wantErr: true},
		{name: "wildcard", raw: "*", wantErr: true},
		{name: "every IPv4 address", raw: "0.0.0.0/0", wantErr: true},
		{name: "every IPv6 address", raw: "10.0.0.0/8,::/0", wantErr: true},
		{name: "IPv4-mapped IPv6 address", raw: "::ffff:10.0.0.1", wantErr: true},
		{name: "IPv4-mapped IPv6 range", raw: "::ffff:10.0.0.0/104", wantErr: true},
		{name: "zone ID", raw: "fe80::1%eth0", wantErr: true},
		{name: "single host CIDR and unique local range", raw: "10.0.0.1/32,fc00::/7", want: []string{"10.0.0.1/32", "fc00::/7"}},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := ParseTrustedProxies(tt.raw)
			if tt.wantErr {
				assert.ErrorContains(t, err, "TRUSTED_PROXIES")
				return
			}
			assert.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

// TestLoadConfig_TrustedProxies checks that LoadConfig parses TRUSTED_PROXIES
// and refuses an invalid value at startup. Not parallel: t.Setenv.
func TestLoadConfig_TrustedProxies(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		want    []string
		wantErr string
	}{
		{name: "unset trusts no proxy", raw: "", want: nil},
		{name: "list", raw: "10.0.0.0/8,192.168.0.0/16", want: []string{"10.0.0.0/8", "192.168.0.0/16"}},
		{name: "invalid is refused at startup", raw: "10.0.0.0/8,proxy.example.com", wantErr: "invalid configuration: TRUSTED_PROXIES: "},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("ENV_FILE", "/nonexistent/.env")
			t.Setenv("TRUSTED_PROXIES", tt.raw)
			cfg, err := LoadConfig()
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, cfg.Server.TrustedProxies)
		})
	}
}
