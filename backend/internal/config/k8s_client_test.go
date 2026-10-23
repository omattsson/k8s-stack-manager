package config

import (
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// No t.Parallel: the cases set environment variables (t.Setenv).
func TestLoadDeploymentConfig_K8sClientLimit(t *testing.T) {
	tests := []struct {
		name      string
		qps       string
		burst     string
		wantQPS   float64
		wantBurst int
		wantNaN   bool
		wantErr   bool
	}{
		{name: "defaults", wantQPS: 50, wantBurst: 100},
		{name: "explicit", qps: "20.5", burst: "40", wantQPS: 20.5, wantBurst: 40},
		{name: "zero uses the client default", qps: "0", burst: "0"},
		{name: "negative QPS", qps: "-1", burst: "10", wantQPS: -1, wantBurst: 10, wantErr: true},
		{name: "negative burst", qps: "10", burst: "-1", wantQPS: 10, wantBurst: -1, wantErr: true},
		{name: "NaN QPS", qps: "NaN", burst: "10", wantNaN: true, wantBurst: 10, wantErr: true},
		{name: "infinite QPS", qps: "+Inf", burst: "10", wantQPS: math.Inf(1), wantBurst: 10, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("K8S_CLIENT_QPS", tt.qps)
			t.Setenv("K8S_CLIENT_BURST", tt.burst)

			cfg := validBaseConfig()
			cfg.Deployment = loadDeploymentConfig()
			if tt.wantNaN {
				assert.True(t, math.IsNaN(cfg.Deployment.K8sClientQPS))
			} else {
				assert.Equal(t, tt.wantQPS, cfg.Deployment.K8sClientQPS)
			}
			assert.Equal(t, tt.wantBurst, cfg.Deployment.K8sClientBurst)

			err := cfg.Validate()
			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), "K8S_CLIENT_QPS")
				return
			}
			assert.NoError(t, err)
		})
	}
}

func validBaseConfig() *Config {
	return &Config{
		App:      AppConfig{Name: "myapp", Environment: "production"},
		Database: DatabaseConfig{Host: "localhost", Port: "3306", User: "user", Password: "pass", DBName: "dbname", MaxOpenConns: 10, MaxIdleConns: 5, ConnMaxLifetime: time.Minute},
		Server:   ServerConfig{Host: "127.0.0.1", Port: "8080", ReadTimeout: 5 * time.Second, WriteTimeout: 5 * time.Second, IdleTimeout: 30 * time.Second, ShutdownTimeout: 10 * time.Second},
	}
}
