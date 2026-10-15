package config

import (
	"fmt"
	"net"
	"strings"
)

// ParseTrustedProxies parses TRUSTED_PROXIES: a comma-separated list of IP
// addresses or CIDR ranges (for example "10.0.0.0/8,192.168.1.10"). Only a
// request from one of them may set the client IP with X-Forwarded-For or
// X-Real-IP; the per-IP rate limits and the IP of a refresh token use that
// client IP. Empty input returns nil: no proxy is trusted, and the client IP
// is the TCP peer.
//
// A range that covers every address (0.0.0.0/0, ::/0) is refused: every
// client could then choose its IP. An IPv4-mapped IPv6 entry is refused too,
// because Gin compares it as IPv6 and it never matches an IPv4 peer.
func ParseTrustedProxies(raw string) ([]string, error) {
	var out []string
	for _, part := range strings.Split(raw, ",") {
		entry := strings.TrimSpace(part)
		if entry == "" {
			continue
		}
		var ip net.IP
		if strings.Contains(entry, "/") {
			cidrIP, network, err := net.ParseCIDR(entry)
			if err != nil {
				return nil, fmt.Errorf("TRUSTED_PROXIES: %q is not a valid CIDR range", entry)
			}
			if ones, _ := network.Mask.Size(); ones == 0 {
				return nil, fmt.Errorf("TRUSTED_PROXIES: %q trusts every address; list the proxy addresses", entry)
			}
			ip = cidrIP
		} else if ip = net.ParseIP(entry); ip == nil {
			return nil, fmt.Errorf("TRUSTED_PROXIES: %q is not a valid IP address or CIDR range", entry)
		}
		if strings.Contains(entry, ":") && ip.To4() != nil {
			return nil, fmt.Errorf("TRUSTED_PROXIES: %q is an IPv4-mapped IPv6 address; use the IPv4 form", entry)
		}
		out = append(out, entry)
	}
	return out, nil
}
