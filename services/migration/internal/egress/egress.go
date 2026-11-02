// Package egress guards every outbound connection a tenant-configured connection profile can cause. It is a Go port of
// the webhook guard in services/integration-hub-ts/src/ssrf.ts, applied at dial time:
//
//   - loopback, link-local (169.254.0.0/16 with the cloud metadata address, fe80::/10), unspecified, multicast and
//     reserved addresses are always denied, and so is IPv4 embedded in IPv6: ::ffff: is judged as the IPv4 it is,
//     6to4 (2002::/16) passes only when the embedded IPv4 is public, and NAT64 (64:ff9b::/96) is denied outright;
//   - private ranges (10.0.0.0/8, 172.16.0.0/12, 192.168.0.0/16, fc00::/7) are denied unless the operator lists them in
//     MIG_EGRESS_ALLOW (CIDRs or host:port); the list is deployment configuration, never tenant-settable;
//   - a host name is resolved once, every address is checked, and the connection is pinned to a checked address, so a
//     second DNS answer (rebinding) cannot reach another one; net.Dialer.Control checks the address actually connected;
//   - redirects are never followed.
package egress

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// inTestBinary reports whether the process is a go test binary; a variable so the guard's own test can play production.
var inTestBinary = testing.Testing

// ErrDenied is wrapped by every refusal so callers can tell a policy refusal from a network failure.
var ErrDenied = errors.New("egress denied")

func denied(format string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrDenied, fmt.Sprintf(format, a...))
}

// Class says how the guard treats an address.
type Class int

const (
	Public  Class = iota // reachable
	Private              // reachable only when the operator allows it
	Denied               // never reachable
)

var (
	always4 = mustPrefixes("0.0.0.0/8", "100.64.0.0/10", "127.0.0.0/8", "169.254.0.0/16", "192.0.0.0/24", "198.18.0.0/15", "224.0.0.0/4", "240.0.0.0/4")
	priv4   = mustPrefixes("10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16")
	fc00    = netip.MustParsePrefix("fc00::/7")
	global6 = netip.MustParsePrefix("2000::/3")
	nat64   = netip.MustParsePrefix("64:ff9b::/96")
	sixToFo = netip.MustParsePrefix("2002::/16")
	// 2001::/23 (IETF protocol assignments, Teredo among them) and 2001:db8::/32 (documentation).
	ietf6 = mustPrefixes("2001::/23", "2001:db8::/32")
)

func mustPrefixes(s ...string) []netip.Prefix {
	out := make([]netip.Prefix, len(s))
	for i, p := range s {
		out[i] = netip.MustParsePrefix(p)
	}
	return out
}

func within(a netip.Addr, ps []netip.Prefix) bool {
	for _, p := range ps {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

func classify4(a netip.Addr) Class {
	switch {
	case within(a, always4):
		return Denied
	case within(a, priv4):
		return Private
	}
	return Public
}

// Classify reports how the guard treats one address (an IPv4-mapped IPv6 address is judged as the IPv4 it is).
func Classify(a netip.Addr) Class {
	if !a.IsValid() || a.Zone() != "" || nat64.Contains(a) {
		return Denied // NAT64 (64:ff9b::/96) embeds an IPv4 address; this cluster has no NAT64 gateway, so none is reachable
	}
	if a.Is4In6() {
		a = a.Unmap()
	}
	if a.Is4() {
		return classify4(a)
	}
	if fc00.Contains(a) {
		return Private
	}
	if !global6.Contains(a) || within(a, ietf6) {
		return Denied // ::, ::1, fe80::/10, ff00::/8, ::a.b.c.d, 64:ff9b:1::/48 and the rest of the non-global space
	}
	if sixToFo.Contains(a) {
		b := a.As16()
		return embedded(netip.AddrFrom4([4]byte{b[2], b[3], b[4], b[5]}))
	}
	return Public
}

// embedded judges an IPv4 address carried inside a 6to4 address: only a public IPv4 passes, a private one is never
// allowed through a translation prefix.
func embedded(v4 netip.Addr) Class {
	if classify4(v4) == Public {
		return Public
	}
	return Denied
}

// Policy is the operator allowlist for private addresses.
type Policy struct {
	prefixes []netip.Prefix
	hosts    map[string]bool // host:port, host lower-cased (an IP literal or a name)
}

// ParsePolicy reads MIG_EGRESS_ALLOW: comma- or space-separated CIDRs ("10.20.0.0/16", "fd00::/8") and host:port
// entries ("erp.corp.example:1433", "10.1.2.3:443", "[fd00::5]:443"). Empty means no private address is reachable.
// Loopback, link-local and the other always-denied ranges stay denied whatever the list says.
func ParsePolicy(raw string) (*Policy, error) {
	p := &Policy{hosts: map[string]bool{}}
	for _, e := range strings.FieldsFunc(raw, func(r rune) bool { return r == ',' || r == ' ' || r == '\n' || r == '\t' }) {
		if strings.Contains(e, "/") {
			pre, err := netip.ParsePrefix(e)
			if err != nil {
				return nil, fmt.Errorf("MIG_EGRESS_ALLOW entry %q is not a CIDR: %w", e, err)
			}
			p.prefixes = append(p.prefixes, pre.Masked())
			continue
		}
		host, port, err := net.SplitHostPort(e)
		if n, perr := strconv.Atoi(port); err != nil || host == "" || perr != nil || n < 1 || n > 65535 {
			return nil, fmt.Errorf("MIG_EGRESS_ALLOW entry %q is neither a CIDR nor host:port", e)
		}
		p.hosts[hostKey(host, port)] = true
	}
	return p, nil
}

func hostKey(host, port string) string {
	if a, err := netip.ParseAddr(host); err == nil {
		host = a.Unmap().String()
	}
	return strings.ToLower(host) + ":" + port
}

func (p *Policy) allows(a netip.Addr, host, port string) bool {
	if p == nil {
		return false
	}
	if a.Is4In6() {
		a = a.Unmap()
	}
	for _, pre := range p.prefixes {
		if pre.Contains(a) {
			return true
		}
	}
	return p.hosts[hostKey(host, port)] || p.hosts[hostKey(a.String(), port)]
}

// Resolver turns a host name into addresses.
type Resolver func(ctx context.Context, host string) ([]netip.Addr, error)

// Guard dials only addresses the policy permits.
type Guard struct {
	Policy   *Policy
	Resolver Resolver // default: the system resolver
	// TestDialFunc lets a test send the final connection to a checked ip:port to a local server instead, so the policy
	// decides on a pretend private address while the bytes reach httptest. It is honoured only inside a test binary
	// (testing.Testing); in the service binary a guard that carries one refuses every dial. Production dials always use
	// a net.Dialer whose Control re-checks the address being connected.
	TestDialFunc func(ctx context.Context, network, address string) (net.Conn, error)
	// TLS is the client TLS configuration for HTTPS requests; nil uses the system roots.
	TLS     *tls.Config
	Timeout time.Duration
}

// resolve counts name resolution inside the caller's deadline even when a custom resolver ignores its context.
func (g *Guard) resolve(ctx context.Context, host string) ([]netip.Addr, error) {
	if g.Resolver == nil {
		return net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	}
	type answer struct {
		addrs []netip.Addr
		err   error
	}
	ch := make(chan answer, 1)
	go func() {
		a, err := g.Resolver(ctx, host)
		ch <- answer{a, err}
	}()
	select {
	case r := <-ch:
		return r.addrs, r.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Target is a validated URL and the address its connection must be pinned to.
type Target struct {
	URL     *url.URL
	Address netip.Addr
}

// CheckURL parses raw, refuses non-https schemes (http only when allowHTTP), embedded credentials and any host that
// resolves to an address the policy does not allow, and returns the first address, to which the connection is pinned.
func (g *Guard) CheckURL(ctx context.Context, raw string, allowHTTP bool) (Target, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return Target{}, denied("not a valid URL")
	}
	if u.Scheme != "https" && !(allowHTTP && u.Scheme == "http") {
		return Target{}, denied("scheme %s is not allowed", u.Scheme)
	}
	if u.User != nil {
		return Target{}, denied("credentials in the URL are not allowed")
	}
	port := u.Port()
	if port == "" {
		port = map[string]string{"http": "80", "https": "443"}[u.Scheme]
	}
	addrs, err := g.Check(ctx, u.Hostname(), port)
	if err != nil {
		return Target{}, err
	}
	return Target{URL: u, Address: addrs[0]}, nil
}

// Check decides whether host:port may be dialled and returns the addresses to pin the connection to.
func (g *Guard) Check(ctx context.Context, host, port string) ([]netip.Addr, error) {
	host = strings.TrimSuffix(strings.TrimPrefix(host, "["), "]")
	if host == "" {
		return nil, denied("empty host")
	}
	var addrs []netip.Addr
	if a, err := netip.ParseAddr(host); err == nil {
		addrs = []netip.Addr{a}
	} else {
		if strings.Contains(host, "%") {
			return nil, denied("zone identifiers are not allowed")
		}
		rs, err := g.resolve(ctx, host)
		if err != nil || len(rs) == 0 {
			return nil, denied("host %s does not resolve", host)
		}
		addrs = rs
	}
	for _, a := range addrs {
		if err := g.checkAddr(a, host, port); err != nil {
			return nil, err
		}
	}
	return addrs, nil
}

func (g *Guard) checkAddr(a netip.Addr, host, port string) error {
	switch Classify(a) {
	case Public:
		return nil
	case Private:
		if g.Policy.allows(a, host, port) {
			return nil
		}
		return denied("%s is a private address and is not listed in MIG_EGRESS_ALLOW", a)
	}
	return denied("%s is a loopback, link-local, reserved or otherwise non-routable address", a)
}

// DialContext resolves once, checks every address and connects to a checked one, in order, by IP.
func (g *Guard) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	switch network {
	case "tcp", "tcp4", "tcp6":
	default:
		return nil, denied("network %s is not allowed", network)
	}
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, denied("bad address %q", address)
	}
	addrs, err := g.Check(ctx, host, port)
	if err != nil {
		return nil, err
	}
	dial := (&net.Dialer{Timeout: g.timeout(), Control: g.control(host, port)}).DialContext
	if g.TestDialFunc != nil {
		if !inTestBinary() {
			return nil, denied("a test dialer is set outside a test binary")
		}
		dial = g.TestDialFunc
	}
	var last error
	for _, a := range addrs {
		c, err := dial(ctx, network, net.JoinHostPort(a.String(), port))
		if err == nil {
			return c, nil
		}
		last = err
	}
	return nil, last
}

func (g *Guard) timeout() time.Duration {
	if g.Timeout > 0 {
		return g.Timeout
	}
	return 10 * time.Second
}

// control is net.Dialer.Control: it sees the address the socket is about to connect to and refuses it unless allowed,
// whatever resolution produced it (a belt for the braces in DialContext).
func (g *Guard) control(host, port string) func(network, address string, c syscall.RawConn) error {
	return func(_, address string, _ syscall.RawConn) error {
		h, p, err := net.SplitHostPort(address)
		if err != nil {
			return denied("bad address %q", address)
		}
		a, err := netip.ParseAddr(h)
		if err != nil {
			return denied("bad address %q", address)
		}
		return g.checkAddr(a, host, p)
	}
}

// Control is the net.Dialer.Control form of the guard for callers that build their own dialer from IP literals.
func (g *Guard) Control(network, address string, c syscall.RawConn) error {
	h, _, err := net.SplitHostPort(address)
	if err != nil {
		return denied("bad address %q", address)
	}
	return g.control(h, "")(network, address, c)
}

// HTTPClient returns a client that connects only through the guard, never follows a redirect (the redirect response
// itself is returned) and ignores proxy environment variables.
func (g *Guard) HTTPClient() *http.Client {
	tr := &http.Transport{
		Proxy:                 nil,
		DialContext:           g.DialContext,
		TLSClientConfig:       g.TLS,
		TLSHandshakeTimeout:   g.timeout(),
		ResponseHeaderTimeout: g.timeout(),
		DisableKeepAlives:     true,
		ForceAttemptHTTP2:     true,
	}
	return &http.Client{
		Transport:     tr,
		Timeout:       2 * g.timeout(),
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}
