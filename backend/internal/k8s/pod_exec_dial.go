package k8s

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"k8s.io/apimachinery/pkg/util/httpstream"
	httpstreamspdy "k8s.io/apimachinery/pkg/util/httpstream/spdy"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/remotecommand"
	"k8s.io/client-go/transport/spdy"
	wstransport "k8s.io/client-go/transport/websocket"
)

// The upgrade of an exec request (the WebSocket handshake or the SPDY upgrade)
// must end when the session ends. client-go does not give that: the WebSocket
// dialer only applies the deadline of the context, never its cancellation, and
// the SPDY round tripper reads the upgrade response without any deadline. A
// stalled API server (or a load balancer that accepts and never answers) would
// hold the dial for good, and an upgrade that completes after a revoke would
// start the command after the session was audited as ended. So:
//   - boundedRoundTripper wraps the upgrade round trip of both legs. It gives
//     the round trip a deadline (podUpgradeBound, which gorilla applies to the
//     connect, the TLS handshake and the handshake read), returns at once when
//     the context of the caller ends, and closes a connection that completes
//     after that.
//   - The SPDY leg (no proxy) also dials through a connGuard, as the
//     port-forward dial does: the raw connection has a deadline until the
//     upgrade is done and is closed when the context ends.
//
// The TLS settings stay those of client-go: the WebSocket leg uses the stock
// websocket.RoundTripperFor through NewWebSocketExecutor (a wrapper is added
// with WrapTransport); the SPDY leg uses spdyTLSConfig, so a nil TLS config
// never reaches an UpgradeTransport (that would skip the certificate check).

// boundedRoundTripper bounds one upgrade round trip. See above.
type boundedRoundTripper struct {
	inner http.RoundTripper
	// late closes the connection of a round trip that completed after the
	// caller gave up. May be nil.
	late func(*http.Response)
	// cancel runs when the context of the caller ends first (closes a raw
	// connection). May be nil.
	cancel func()
}

func (b *boundedRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	ctx := req.Context()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	type result struct {
		resp *http.Response
		err  error
	}
	dctx, cancel := context.WithTimeout(ctx, podUpgradeBound)
	ch := make(chan result, 1)
	go func() {
		defer cancel()
		resp, err := b.inner.RoundTrip(req.WithContext(dctx))
		ch <- result{resp, err}
	}()
	select {
	case r := <-ch:
		return r.resp, r.err
	case <-ctx.Done():
		if b.cancel != nil {
			b.cancel()
		}
		go func() {
			if r := <-ch; r.err == nil && b.late != nil {
				b.late(r.resp)
			}
		}()
		return nil, ctx.Err()
	}
}

// guardedUpgrader clears the deadline of the raw connection once the upgrade
// is done.
type guardedUpgrader struct {
	inner spdy.Upgrader
	guard *connGuard
}

func (g guardedUpgrader) NewConnection(resp *http.Response) (httpstream.Connection, error) {
	conn, err := g.inner.NewConnection(resp)
	if err == nil {
		g.guard.clearDeadline()
	}
	return conn, err
}

// newExecExecutor returns the WebSocket executor with the SPDY executor as the
// fallback (an upgrade failure or an HTTPS proxy error), as kubectl does. TLS
// settings (CA, certificate checks, client certificates, proxy) are those of
// the REST config as client-go builds them. Both upgrades are bounded and end
// with the context of StreamWithContext (see the comment above).
func newExecExecutor(cfg *rest.Config, u *url.URL) (remotecommand.Executor, error) {
	spdyExec, err := newBoundedSPDYExecutor(cfg, u)
	if err != nil {
		return nil, err
	}
	wsExec, err := newBoundedWebSocketExecutor(cfg, u)
	if err != nil {
		return nil, err
	}
	return remotecommand.NewFallbackExecutor(wsExec, spdyExec, func(err error) bool {
		return httpstream.IsUpgradeFailure(err) || httpstream.IsHTTPSProxyError(err)
	})
}

func newBoundedWebSocketExecutor(cfg *rest.Config, u *url.URL) (remotecommand.Executor, error) {
	wcfg := rest.CopyConfig(cfg)
	prev := wcfg.WrapTransport
	// The stock WebSocket round tripper is the innermost transport: wrap it
	// first, so the wrapper still reaches its connection holder.
	wcfg.WrapTransport = func(rt http.RoundTripper) http.RoundTripper {
		b := &boundedRoundTripper{inner: rt}
		if holder, ok := rt.(wstransport.ConnectionHolder); ok {
			b.late = func(*http.Response) {
				if c := holder.Connection(); c != nil {
					_ = c.Close()
				}
			}
		}
		if prev != nil {
			return prev(b)
		}
		return b
	}
	ex, err := remotecommand.NewWebSocketExecutor(wcfg, "GET", u.String())
	if err != nil {
		return nil, fmt.Errorf("build WebSocket exec: %w", err)
	}
	return ex, nil
}

func newBoundedSPDYExecutor(cfg *rest.Config, u *url.URL) (remotecommand.Executor, error) {
	var (
		rt       http.RoundTripper
		upgrader spdy.Upgrader
		guard    *connGuard
	)
	if cfg.Proxy == nil && !envProxyFor(u) {
		tlsConfig, err := spdyTLSConfig(cfg, u)
		if err != nil {
			return nil, err
		}
		guard = &connGuard{}
		tr := &http.Transport{TLSClientConfig: tlsConfig, DialContext: guard.dial}
		srt, err := httpstreamspdy.NewRoundTripperWithConfig(httpstreamspdy.RoundTripperConfig{
			UpgradeTransport: tr, PingPeriod: 5 * time.Second,
		})
		if err != nil {
			return nil, fmt.Errorf("build SPDY exec: %w", err)
		}
		if rt, err = rest.HTTPWrappersForConfig(cfg, srt); err != nil {
			return nil, fmt.Errorf("build SPDY exec: %w", err)
		}
		upgrader = guardedUpgrader{inner: srt, guard: guard}
	} else {
		var err error
		if rt, upgrader, err = spdy.RoundTripperFor(cfg); err != nil {
			return nil, fmt.Errorf("build SPDY exec: %w", err)
		}
	}
	b := &boundedRoundTripper{inner: rt, late: func(resp *http.Response) {
		if conn, err := upgrader.NewConnection(resp); err == nil {
			_ = conn.Close()
		}
	}}
	if guard != nil {
		b.cancel = guard.close
	}
	ex, err := remotecommand.NewSPDYExecutorForTransports(b, upgrader, "POST", u)
	if err != nil {
		return nil, fmt.Errorf("build SPDY exec: %w", err)
	}
	return ex, nil
}
