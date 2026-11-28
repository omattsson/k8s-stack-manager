package imagedigest

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"syscall"
	"time"
)

// SSRF guard of the registry requests (#811).
//
// The allowed hosts (the cluster registry host and IMAGE_DIGEST_REGISTRIES)
// may be internal registries on private addresses: their requests use the
// base transport as is. Every other host is reached only through a redirect
// or a token realm of an allowed registry. Those requests go through the
// guarded transport, which refuses an address that is loopback, private
// (RFC 1918, IPv6 ULA), link-local, CGNAT (100.64.0.0/10), 0.0.0.0/8 or
// unspecified. The check runs on the resolved IP at dial time, and the dial
// goes to exactly the checked IP, so DNS rebinding (a name that resolves to a
// public address for a check and to a private one for the connection) cannot
// pass it.
//
// Limit: when an HTTP proxy is in use (HTTPS_PROXY and friends, the
// ProxyFromEnvironment of the base transport), the backend dials the proxy,
// not the target, and the proxy resolves the target name. The dial to the
// proxy is allowed (the proxy may be on a private address), so the guard
// cannot check the target address: the proxy must refuse internal targets.
// The hostname check of guardTransport (IP literals, localhost) still applies.

var (
	errBlockedHost = errors.New("request to a private or loopback address refused")
	errPlainHTTP   = errors.New("plain http registry request refused, https is required")
)

// guardTransport refuses every request that is not https (unless
// WithPlainHTTPForTests), so credentials never go in clear text:
// go-containerregistry tries plain http for localhost, private addresses and
// *.local. A request to an allowed host goes to base. A request to another
// host (a redirect or token realm) is refused when its host is an IP literal
// that blockedIP refuses or localhost, and otherwise goes to guarded, which
// checks the resolved address at dial time.
type guardTransport struct {
	base    http.RoundTripper
	guarded http.RoundTripper // nil: base (base is not an *http.Transport)
	allowed func(host string) bool
	// plainHTTP allows http (tests only).
	plainHTTP bool
}

func (t *guardTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if !strings.EqualFold(req.URL.Scheme, "https") && !t.plainHTTP {
		return nil, fmt.Errorf("%w: %s", errPlainHTTP, req.URL.Host)
	}
	if t.allowed(req.URL.Host) {
		return t.base.RoundTrip(req)
	}
	if blockedHostname(req.URL.Hostname()) {
		return nil, fmt.Errorf("%w: %s", errBlockedHost, req.URL.Host)
	}
	if t.guarded != nil {
		return t.guarded.RoundTrip(req)
	}
	return t.base.RoundTrip(req)
}

// blockedHostname reports whether h is localhost or an IP literal that
// blockedIP refuses.
func blockedHostname(h string) bool {
	h = strings.ToLower(strings.TrimSuffix(h, "."))
	if h == "localhost" || strings.HasSuffix(h, ".localhost") {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && blockedIP(ip)
}

// cgnat is the shared address space of carrier-grade NAT (RFC 6598).
var cgnat = &net.IPNet{IP: net.IPv4(100, 64, 0, 0).To4(), Mask: net.CIDRMask(10, 32)}

// blockedIP reports whether a host that is not allowed may not be dialed at
// ip: loopback, private (RFC 1918, fc00::/7), link-local, CGNAT, 0.0.0.0/8,
// unspecified, or interface-local multicast. An IPv6 address that carries an
// IPv4 address (IPv4-mapped, IPv4-compatible, NAT64 64:ff9b::/96, 6to4
// 2002::/16) is blocked when the IPv4 address is. The local-use NAT64 prefix
// 64:ff9b:1::/48 is blocked as a whole: where it holds the IPv4 address
// depends on the prefix length of the network.
func blockedIP(ip net.IP) bool {
	if ip4 := ip.To4(); ip4 != nil {
		return blockedIPv4(ip4)
	}
	ip16 := ip.To16()
	if ip16 == nil {
		return true
	}
	if nat64Local.Contains(ip16) {
		return true
	}
	if v4 := embeddedIPv4(ip16); v4 != nil && blockedIPv4(v4) {
		return true
	}
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() || ip.IsUnspecified() || ip.IsInterfaceLocalMulticast()
}

func blockedIPv4(ip4 net.IP) bool {
	return ip4[0] == 0 || cgnat.Contains(ip4) || ip4.IsLoopback() || ip4.IsPrivate() ||
		ip4.IsLinkLocalUnicast() || ip4.IsLinkLocalMulticast() || ip4.IsUnspecified()
}

var (
	nat64WellKnown = mustCIDR("64:ff9b::/96")
	nat64Local     = mustCIDR("64:ff9b:1::/48")
	sixToFour      = mustCIDR("2002::/16")
	ipv4Compatible = mustCIDR("::/96")
)

func mustCIDR(s string) *net.IPNet {
	_, n, err := net.ParseCIDR(s)
	if err != nil {
		panic(err)
	}
	return n
}

// embeddedIPv4 returns the IPv4 address that an IPv6 address carries by a
// fixed rule (NAT64 64:ff9b::/96 and IPv4-compatible ::a.b.c.d: the last 32
// bits; 6to4 2002::/16: bits 16 to 47), or nil.
func embeddedIPv4(ip16 net.IP) net.IP {
	switch {
	case nat64WellKnown.Contains(ip16), ipv4Compatible.Contains(ip16):
		return net.IPv4(ip16[12], ip16[13], ip16[14], ip16[15]).To4()
	case sixToFour.Contains(ip16):
		return net.IPv4(ip16[2], ip16[3], ip16[4], ip16[5]).To4()
	}
	return nil
}

// newGuardedTransport returns a clone of base whose dials refuse a blocked
// address (guardDialer), or nil when base is not an *http.Transport. lookup
// resolves a host name (net.DefaultResolver; tests inject one).
func newGuardedTransport(base http.RoundTripper, lookup func(ctx context.Context, host string) ([]net.IPAddr, error)) http.RoundTripper {
	bt, ok := base.(*http.Transport)
	if !ok {
		return nil
	}
	tr := bt.Clone()
	d := &guardDialer{
		lookup:  lookup,
		plain:   &net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second},
		checked: &net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second, Control: controlBlocked},
	}
	if tr.Proxy != nil {
		proxy := tr.Proxy
		// Remember the proxy addresses (host:port, as the transport dials
		// them), so the dial to a proxy is not checked (see the limit
		// above). Another port of the proxy host is checked.
		tr.Proxy = func(req *http.Request) (*url.URL, error) {
			u, err := proxy(req)
			if u != nil {
				d.proxies.Store(proxyAddr(u), struct{}{})
			}
			return u, err
		}
	}
	tr.DialContext = d.DialContext
	tr.DialTLSContext = nil
	return tr
}

// guardDialer resolves the target host itself, refuses it when any of its
// addresses is blocked, and dials the checked IP (never the name again).
type guardDialer struct {
	lookup  func(ctx context.Context, host string) ([]net.IPAddr, error)
	plain   *net.Dialer // dials to the HTTP proxy
	checked *net.Dialer // dials to a checked IP; Control checks again
	proxies sync.Map    // lower-case proxy addresses host:port (proxyAddr)
}

// proxyAddr returns the lower-case host:port that http.Transport dials for
// proxy u (the default port of the proxy scheme when u has none).
func proxyAddr(u *url.URL) string {
	port := u.Port()
	if port == "" {
		switch strings.ToLower(u.Scheme) {
		case "https":
			port = "443"
		case "socks5", "socks5h":
			port = "1080"
		default:
			port = "80"
		}
	}
	return strings.ToLower(net.JoinHostPort(u.Hostname(), port))
}

func (d *guardDialer) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	if _, ok := d.proxies.Load(strings.ToLower(net.JoinHostPort(host, port))); ok {
		return d.plain.DialContext(ctx, network, addr)
	}
	var ips []net.IPAddr
	if ip := net.ParseIP(host); ip != nil {
		ips = []net.IPAddr{{IP: ip}}
	} else {
		ips, err = d.lookup(ctx, host)
		if err != nil {
			return nil, err
		}
	}
	if len(ips) == 0 {
		return nil, fmt.Errorf("no address for %s", host)
	}
	for _, ip := range ips {
		if blockedIP(ip.IP) {
			return nil, fmt.Errorf("%w: %s", errBlockedHost, host)
		}
	}
	var firstErr error
	for _, ip := range ips {
		conn, err := d.checked.DialContext(ctx, network, net.JoinHostPort(ip.IP.String(), port))
		if err == nil {
			return conn, nil
		}
		if firstErr == nil {
			firstErr = err
		}
	}
	return nil, firstErr
}

// controlBlocked refuses a connection to a blocked address right before the
// connect, as a second check of the IP that guardDialer chose.
func controlBlocked(_, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	ip := net.ParseIP(host)
	if ip == nil || blockedIP(ip) {
		return fmt.Errorf("%w: %s", errBlockedHost, host)
	}
	return nil
}
