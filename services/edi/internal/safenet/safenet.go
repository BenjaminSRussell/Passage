// Package safenet builds the HTTP clients that call URLs a partner supplied (a cXML network, a punchout supplier, an AS2 endpoint). A
// partner's URL is data an attacker may control, so the connection is refused at dial time, after DNS, when the address it resolved
// to is one a partner has no business reaching: loopback, private (RFC 1918 and unique-local), link-local (the cloud metadata address
// 169.254.169.254 included), carrier-grade NAT, unspecified, multicast and the IPv4 forms of these inside IPv6. Checking at dial time
// also covers DNS rebinding and redirects (which these clients do not follow anyway).
package safenet

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"syscall"
	"time"
)

// ErrForbiddenAddress is returned when a partner URL resolves to an address that is not public.
var ErrForbiddenAddress = errors.New("the address is not a public one")

var blocked = mustPrefixes(
	"0.0.0.0/8", "10.0.0.0/8", "100.64.0.0/10", "127.0.0.0/8", "169.254.0.0/16", "172.16.0.0/12", "192.0.0.0/24", "192.168.0.0/16",
	"198.18.0.0/15", "224.0.0.0/4", "240.0.0.0/4", "::/128", "::1/128", "fc00::/7", "fe80::/10", "ff00::/8", "64:ff9b::/96", "100::/64", "2001:db8::/32",
)

func mustPrefixes(list ...string) []netip.Prefix {
	out := make([]netip.Prefix, 0, len(list))
	for _, s := range list {
		out = append(out, netip.MustParsePrefix(s))
	}
	return out
}

// Public reports whether the address may be dialed for a partner.
func Public(ip netip.Addr) bool {
	ip = ip.Unmap()
	if !ip.IsValid() || ip.IsUnspecified() || ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsMulticast() {
		return false
	}
	for _, p := range blocked {
		if p.Contains(ip) {
			return false
		}
	}
	return true
}

// Control is a net.Dialer Control hook that refuses a connection to a non-public address.
func Control(_, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrForbiddenAddress, err)
	}
	ip, err := netip.ParseAddr(host)
	if err != nil || !Public(ip) {
		return fmt.Errorf("%w: %s", ErrForbiddenAddress, host)
	}
	return nil
}

// Client returns an HTTP client for partner URLs: dials only public addresses (unless allowPrivate, for development and tests),
// follows no redirect and goes direct (no proxy from the environment). timeout is the whole request.
func Client(timeout time.Duration, allowPrivate bool) *http.Client {
	d := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
	if !allowPrivate {
		d.Control = Control
	}
	tr := &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			return d.DialContext(ctx, network, addr)
		},
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: timeout,
		MaxIdleConns:          10,
		IdleConnTimeout:       30 * time.Second,
	}
	return &http.Client{Timeout: timeout, Transport: tr, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}
