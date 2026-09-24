// Package netguard keeps agent-driven network access away from local and private
// addresses (SSRF). The dialer checks the address it actually connects to, so a
// hostname that resolves differently between a check and the dial (DNS rebinding)
// cannot slip through; CheckURL covers paths where the dial is not ours (an HTTP
// proxy, or a browser).
package netguard

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"syscall"
	"time"
)

// cgnat is the carrier-grade NAT range (Tailscale and friends): reachable, private, not covered by IsPrivate.
var cgnat = &net.IPNet{IP: net.IPv4(100, 64, 0, 0), Mask: net.CIDRMask(10, 32)}

// Private reports whether ip is loopback, private, link-local (cloud metadata lives at 169.254.169.254),
// CGNAT, multicast or unspecified.
func Private(ip net.IP) bool {
	if ip4 := ip.To4(); ip4 != nil {
		ip = ip4
	}
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsMulticast() || ip.IsUnspecified() || cgnat.Contains(ip)
}

func privateName(host string) bool {
	h := strings.ToLower(strings.TrimSuffix(host, "."))
	return h == "localhost" || strings.HasSuffix(h, ".localhost") || strings.HasSuffix(h, ".local") || strings.HasSuffix(h, ".internal") || strings.HasSuffix(h, ".lan")
}

// CheckURL rejects URLs whose host is (or resolves to) a private address. Lookup failures are treated
// as unsafe: a host we cannot resolve is not one we can vouch for.
func CheckURL(ctx context.Context, raw string) error {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Hostname() == "" {
		return fmt.Errorf("invalid URL %q", raw)
	}
	host := u.Hostname()
	if privateName(host) {
		return fmt.Errorf("refusing to reach private address %s (enable “allow private” in Settings → Web to permit)", host)
	}
	if ip := net.ParseIP(host); ip != nil {
		if Private(ip) {
			return fmt.Errorf("refusing to reach private address %s (enable “allow private” in Settings → Web to permit)", host)
		}
		return nil
	}
	ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return fmt.Errorf("cannot resolve %s: %w", host, err)
	}
	for _, a := range ips {
		if Private(a.IP) {
			return fmt.Errorf("refusing to reach %s: it resolves to private address %s", host, a.IP)
		}
	}
	return nil
}

// Dialer returns a dialer that refuses private destinations at connect time (unless allowPrivate).
func Dialer(allowPrivate bool) *net.Dialer {
	d := &net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}
	if !allowPrivate {
		d.Control = func(network, address string, _ syscall.RawConn) error {
			host, _, err := net.SplitHostPort(address)
			if err != nil {
				return err
			}
			if ip := net.ParseIP(host); ip != nil && Private(ip) {
				return fmt.Errorf("refusing to connect to private address %s", host)
			}
			return nil
		}
	}
	return d
}

// Client returns an HTTP client bound to the guard. Every request and redirect target is also checked
// by name (needed when an environment proxy is in play: the dial then goes to the proxy, not the target).
func Client(allowPrivate bool, timeout time.Duration) *http.Client {
	d := Dialer(allowPrivate)
	return &http.Client{
		Timeout:   timeout,
		Transport: &http.Transport{Proxy: http.ProxyFromEnvironment, DialContext: d.DialContext, TLSHandshakeTimeout: 15 * time.Second},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 8 {
				return fmt.Errorf("too many redirects")
			}
			if !allowPrivate {
				return CheckURL(req.Context(), req.URL.String())
			}
			return nil
		},
	}
}
