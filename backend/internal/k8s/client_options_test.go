package k8s

import (
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewClientWithOptions_RequestLimit(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		opts      ClientOptions
		wantQPS   float32
		wantBurst int
	}{
		{name: "zero uses the defaults", opts: ClientOptions{}, wantQPS: DefaultClientQPS, wantBurst: DefaultClientBurst},
		{name: "explicit values", opts: ClientOptions{QPS: 20, Burst: 40}, wantQPS: 20, wantBurst: 40},
		{name: "negative uses the defaults", opts: ClientOptions{QPS: -1, Burst: -1}, wantQPS: DefaultClientQPS, wantBurst: DefaultClientBurst},
		{name: "NaN uses the default", opts: ClientOptions{QPS: float32(math.NaN()), Burst: 5}, wantQPS: DefaultClientQPS, wantBurst: 5},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			path := writeHomeKubeconfig(t, t.TempDir())

			client, err := NewClientWithOptions(path, tt.opts)
			require.NoError(t, err)
			assert.Equal(t, tt.wantQPS, client.RESTConfig().QPS)
			assert.Equal(t, tt.wantBurst, client.RESTConfig().Burst)
		})
	}
}

// NewClient must not keep the client-go default (5 QPS, burst 10).
func TestNewClient_UsesDefaultRequestLimit(t *testing.T) {
	t.Parallel()

	client, err := NewClient(writeHomeKubeconfig(t, t.TempDir()))
	require.NoError(t, err)
	assert.Equal(t, DefaultClientQPS, client.RESTConfig().QPS)
	assert.Equal(t, DefaultClientBurst, client.RESTConfig().Burst)
}
