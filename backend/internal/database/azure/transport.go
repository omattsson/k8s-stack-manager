package azure

import (
	"net"
	"net/http"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/data/aztables"
)

// TransportConfig controls HTTP connection pooling for Azure Table Storage clients.
type TransportConfig struct {
	MaxIdleConns        int
	MaxIdleConnsPerHost int
	MaxConnsPerHost     int
	IdleConnTimeout     time.Duration
}

// DefaultTransportConfig returns tuned defaults for concurrent Azure Table access.
// The stdlib default MaxIdleConnsPerHost is 2, which causes excessive TCP churn
// under load (45% of CPU time in net.Dial during spike tests).
func DefaultTransportConfig() TransportConfig {
	return TransportConfig{
		MaxIdleConns:        100,
		MaxIdleConnsPerHost: 50,
		MaxConnsPerHost:     100,
		IdleConnTimeout:     90 * time.Second,
	}
}

// httpTransporter adapts *http.Client to the Azure SDK's policy.Transporter interface.
type httpTransporter struct {
	client *http.Client
}

func (t *httpTransporter) Do(req *http.Request) (*http.Response, error) { //nolint:gosec // G704: URL constructed from validated config, not user input
	return t.client.Do(req)
}

// sharedClientOptions is the singleton aztables.ClientOptions with a tuned HTTP
// transport. Initialized lazily by SharedClientOptions.
var sharedClientOptions *aztables.ClientOptions

// SharedClientOptions returns aztables.ClientOptions configured with a connection-
// pooled HTTP transport. The same options (and underlying transport) are shared
// across all repository instances to maximize connection reuse.
func SharedClientOptions(cfg TransportConfig) *aztables.ClientOptions {
	if sharedClientOptions != nil {
		return sharedClientOptions
	}
	transport := &http.Transport{
		MaxIdleConns:        cfg.MaxIdleConns,
		MaxIdleConnsPerHost: cfg.MaxIdleConnsPerHost,
		MaxConnsPerHost:     cfg.MaxConnsPerHost,
		IdleConnTimeout:     cfg.IdleConnTimeout,
		DialContext: (&net.Dialer{
			Timeout:   10 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		TLSHandshakeTimeout: 10 * time.Second,
	}
	sharedClientOptions = &aztables.ClientOptions{}
	sharedClientOptions.Transport = &httpTransporter{
		client: &http.Client{Transport: transport},
	}
	return sharedClientOptions
}
