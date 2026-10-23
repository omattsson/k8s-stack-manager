package config_test

import (
	"bytes"
	"log/slog"
	"testing"
	"time"

	"backend/internal/config"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Not parallel: it replaces the default slog logger.
func TestValidateEncryptionKeyWarning(t *testing.T) {
	tests := []struct {
		name    string
		key     string
		want    []string
		notWant []string
	}{
		{"no key", "", []string{"is not set", "refuses cluster credentials", "notification channel secrets", "kubeconfig_path"}, []string{"weak"}},
		{"short key", "short-key", []string{"shorter than 16", "weak", "32 or more"}, []string{"refuses", "not be encrypted"}},
		{"long key", "a-long-random-key-0123456789abcdef", nil, []string{"KUBECONFIG_ENCRYPTION_KEY"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			old := slog.Default()
			slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
			defer slog.SetDefault(old)

			cfg := &config.Config{
				App:      config.AppConfig{Name: "myapp", Environment: "production"},
				Database: config.DatabaseConfig{Host: "localhost", Port: "3306", User: "user", Password: "pass", DBName: "dbname", MaxOpenConns: 10, MaxIdleConns: 5, ConnMaxLifetime: time.Minute},
				Server:   config.ServerConfig{Host: "127.0.0.1", Port: "8080", ReadTimeout: 5 * time.Second, WriteTimeout: 5 * time.Second, IdleTimeout: 30 * time.Second, ShutdownTimeout: 10 * time.Second},
			}
			cfg.Deployment.KubeconfigEncryptionKey = tt.key
			require.NoError(t, cfg.Validate(), "a missing or short key never fails the start")

			out := buf.String()
			for _, w := range tt.want {
				assert.Contains(t, out, w)
			}
			for _, w := range tt.notWant {
				assert.NotContains(t, out, w)
			}
		})
	}
}
