package k8s

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"go.opentelemetry.io/otel/attribute"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/httpstream"
	httpstreamspdy "k8s.io/apimachinery/pkg/util/httpstream/spdy"
	"k8s.io/apimachinery/pkg/util/portforward"
	"k8s.io/client-go/rest"
	cgportforward "k8s.io/client-go/tools/portforward"
	"k8s.io/client-go/transport/spdy"
	wstransport "k8s.io/client-go/transport/websocket"
)

// Bounds of the dial to a pod. client-go's dialers take no context and set no
// deadline of their own: the WebSocket tunnel dialer uses a background
// context, and the SPDY round tripper reads the upgrade response without any
// deadline. A stalled API server (or a load balancer that accepts the
// connection and never answers) would hold the dial goroutine and its
// connection for good. So:
//   - Both dialers run with our own request context, which carries the bound
//     podUpgradeBound (TCP connect, TLS handshake and the upgrade response
//     for the WebSocket path: gorilla sets the connection deadline from the
//     context deadline).
//   - The SPDY path (no proxy configured) dials through an UpgradeTransport
//     whose connection gets the same deadline until the upgrade completed,
//     and is closed at once when the context ends. With a proxy (kubeconfig
//     proxy-url or the proxy environment) the stock round tripper is used:
//     the connect and the TLS handshake follow the context, the upgrade
//     response read is not bounded by client-go.
//   - A dial that completes after the caller gave up is closed (dialPodConn).
//
// These are variables so that tests can shorten them.
var (
	podDialTimeout  = 10 * time.Second // TCP connect
	podUpgradeBound = 20 * time.Second // connect, TLS and upgrade together
	podKeepAlive    = 30 * time.Second // TCP keepalive of the connection
)

// podConnDialer opens an upgraded connection to the pod, honouring ctx.
type podConnDialer func(ctx context.Context, protocols ...string) (httpstream.Connection, string, error)

// DialPodPort opens the error and data stream of a port-forward to a port of
// a pod, without the local listener of portforward.New. It tries the
// WebSocket tunnel first and falls back to SPDY, as kubectl does. ErrDialFailed
// wraps a failed dial; ErrRESTConfigUnavailable means the client has no REST
// config (fake clientset). The whole dial is bound by podUpgradeBound and by
// ctx (see the comment on the bounds).
func (c *Client) DialPodPort(ctx context.Context, namespace, pod string, port int32) (ps PodPortStream, err error) {
	ctx, span := startSpan(ctx, "k8s.DialPodPort",
		attribute.String("k8s.namespace", namespace), attribute.String("k8s.pod", pod), attribute.Int("k8s.port", int(port)))
	defer func() { endSpan(span, err) }()

	if c.restConfig == nil {
		return nil, ErrRESTConfigUnavailable
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	u := c.clientset.CoreV1().RESTClient().Post().
		Resource("pods").Namespace(namespace).Name(pod).SubResource("portforward").URL()
	dial, err := newPodConnDialer(rest.CopyConfig(c.restConfig), u)
	if err != nil {
		return nil, err
	}
	conn, err := dialPodConn(ctx, dial)
	if err != nil {
		return nil, err
	}
	return OpenPodPortStream(conn, port)
}

// newPodConnDialer returns the WebSocket tunnel dialer with the SPDY dialer
// as fallback (an upgrade failure or an HTTPS proxy error), as kubectl does.
func newPodConnDialer(cfg *rest.Config, u *url.URL) (podConnDialer, error) {
	wsDial := wsTunnelDialer(cfg, u)
	spdyDial, err := spdyPodDialer(cfg, u)
	if err != nil {
		return nil, err
	}
	return func(ctx context.Context, protocols ...string) (httpstream.Connection, string, error) {
		conn, proto, err := wsDial(ctx, protocols...)
		if err != nil && (httpstream.IsUpgradeFailure(err) || httpstream.IsHTTPSProxyError(err)) {
			return spdyDial(ctx, protocols...)
		}
		return conn, proto, err
	}, nil
}

// wsTunnelDialer is the SPDY-over-WebSocket dialer of client-go
// (portforward.NewSPDYOverWebsocketDialer) with the context of the caller
// instead of a background context.
func wsTunnelDialer(cfg *rest.Config, u *url.URL) podConnDialer {
	return func(ctx context.Context, protocols ...string) (httpstream.Connection, string, error) {
		transport, holder, err := wstransport.RoundTripperFor(cfg)
		if err != nil {
			return nil, "", err
		}
		// WebSockets need GET (RFC 6455).
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
		if err != nil {
			return nil, "", err
		}
		tunnelProtocols := make([]string, 0, len(protocols))
		for _, p := range protocols {
			tunnelProtocols = append(tunnelProtocols, portforward.WebsocketsSPDYTunnelingPrefix+p)
		}
		wsConn, err := wstransport.Negotiate(transport, holder, req, tunnelProtocols...)
		if err != nil {
			return nil, "", err
		}
		if wsConn == nil {
			return nil, "", fmt.Errorf("negotiated websocket connection is nil")
		}
		proto := strings.TrimPrefix(wsConn.Subprotocol(), portforward.WebsocketsSPDYTunnelingPrefix)
		// The SPDY connection runs inside the WebSocket tunnel.
		spdyConn, err := httpstreamspdy.NewClientConnectionWithPings(cgportforward.NewTunnelingConnection("client", wsConn), cgportforward.PingPeriod)
		if err != nil {
			_ = wsConn.Close()
			return nil, "", err
		}
		return spdyConn, proto, nil
	}
}

// spdyPodDialer dials a SPDY upgrade with the context of the caller.
func spdyPodDialer(cfg *rest.Config, u *url.URL) (podConnDialer, error) {
	tlsConfig, err := rest.TLSConfigFor(cfg)
	if err != nil {
		return nil, fmt.Errorf("build SPDY TLS config: %w", err)
	}
	bounded := cfg.Proxy == nil && !envProxyFor(u)
	return func(ctx context.Context, protocols ...string) (httpstream.Connection, string, error) {
		var (
			rt       http.RoundTripper
			upgrader spdy.Upgrader
			guard    *connGuard
		)
		if bounded {
			guard = &connGuard{}
			tr := &http.Transport{TLSClientConfig: tlsConfig, DialContext: guard.dial}
			srt, err := httpstreamspdy.NewRoundTripperWithConfig(httpstreamspdy.RoundTripperConfig{
				UpgradeTransport: tr, PingPeriod: 5 * time.Second,
			})
			if err != nil {
				return nil, "", fmt.Errorf("build SPDY round tripper: %w", err)
			}
			if rt, err = rest.HTTPWrappersForConfig(cfg, srt); err != nil {
				return nil, "", fmt.Errorf("build SPDY round tripper: %w", err)
			}
			upgrader = srt
		} else {
			var err error
			if rt, upgrader, err = spdy.RoundTripperFor(cfg); err != nil {
				return nil, "", fmt.Errorf("build SPDY round tripper: %w", err)
			}
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), nil)
		if err != nil {
			return nil, "", err
		}
		stop := make(chan struct{})
		defer close(stop)
		if guard != nil {
			// The upgrade response read ignores the context: close the
			// connection when the context ends first.
			go func() {
				select {
				case <-ctx.Done():
					guard.close()
				case <-stop:
				}
			}()
		}
		conn, proto, err := spdy.Negotiate(upgrader, &http.Client{Transport: rt}, req, protocols...)
		if err == nil && guard != nil {
			guard.clearDeadline()
		}
		return conn, proto, err
	}, nil
}

func envProxyFor(u *url.URL) bool {
	p, err := http.ProxyFromEnvironment(&http.Request{URL: u})
	return err != nil || p != nil
}

// connGuard keeps the raw connection of one SPDY dial: it carries a deadline
// until the upgrade is done and can be closed from outside.
type connGuard struct {
	mu   sync.Mutex
	conn net.Conn
}

func (g *connGuard) dial(ctx context.Context, network, addr string) (net.Conn, error) {
	d := net.Dialer{Timeout: podDialTimeout, KeepAlive: podKeepAlive}
	conn, err := d.DialContext(ctx, network, addr)
	if err != nil {
		return nil, err
	}
	_ = conn.SetDeadline(time.Now().Add(podUpgradeBound))
	g.mu.Lock()
	g.conn = conn
	g.mu.Unlock()
	return conn, nil
}

func (g *connGuard) clearDeadline() {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.conn != nil {
		_ = g.conn.SetDeadline(time.Time{})
	}
}

func (g *connGuard) close() {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.conn != nil {
		_ = g.conn.Close()
	}
}

// dialPodConn runs dial with the bound podUpgradeBound and the caller's ctx.
// It returns when the dial ends or the context ends, whichever is first. The
// dial goroutine always ends with the bound; a connection that completes after
// the caller gave up is closed.
func dialPodConn(ctx context.Context, dial podConnDialer) (httpstream.Connection, error) {
	ctx, cancel := context.WithTimeout(ctx, podUpgradeBound)
	defer cancel()

	type dialResult struct {
		conn     httpstream.Connection
		protocol string
		err      error
	}
	resCh := make(chan dialResult, 1)
	go func() {
		conn, protocol, err := dial(ctx, cgportforward.PortForwardProtocolV1Name)
		resCh <- dialResult{conn, protocol, err}
	}()
	var res dialResult
	select {
	case res = <-resCh:
	case <-ctx.Done():
		go func() {
			if r := <-resCh; r.conn != nil {
				_ = r.conn.Close()
			}
		}()
		return nil, ctx.Err()
	}
	if res.err != nil {
		return nil, fmt.Errorf("%w: %v", ErrDialFailed, res.err)
	}
	if res.protocol != cgportforward.PortForwardProtocolV1Name {
		_ = res.conn.Close()
		return nil, fmt.Errorf("%w: unable to negotiate protocol, server returned %q", ErrDialFailed, res.protocol)
	}
	return res.conn, nil
}

// OpenPodPortStream creates the error stream and the data stream of the
// port-forward protocol on an upgraded connection, as kubectl does for the
// first (and only) forwarded port: the error stream first, then the data
// stream, both with the port and request ID 0. The client writes nothing to
// the error stream, so it is closed for writing at once. On failure the
// connection is closed and ErrDialFailed is returned.
func OpenPodPortStream(conn httpstream.Connection, port int32) (PodPortStream, error) {
	headers := http.Header{}
	headers.Set(corev1.StreamType, corev1.StreamTypeError)
	headers.Set(corev1.PortHeader, strconv.Itoa(int(port)))
	headers.Set(corev1.PortForwardRequestIDHeader, "0")
	errStream, err := conn.CreateStream(headers)
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("%w: create error stream: %v", ErrDialFailed, err)
	}
	_ = errStream.Close()

	headers.Set(corev1.StreamType, corev1.StreamTypeData)
	dataStream, err := conn.CreateStream(headers)
	if err != nil {
		_ = errStream.Reset()
		_ = conn.Close()
		return nil, fmt.Errorf("%w: create data stream: %v", ErrDialFailed, err)
	}
	return &podPortStream{conn: conn, data: dataStream, errs: errStream}, nil
}
