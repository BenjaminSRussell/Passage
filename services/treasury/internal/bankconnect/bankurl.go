package bankconnect

import (
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"syscall"
	"time"
)

var specialPurpose = func() []netip.Prefix {
	var out []netip.Prefix
	for _, c := range []string{
		"0.0.0.0/8", "10.0.0.0/8", "100.64.0.0/10", "127.0.0.0/8", "169.254.0.0/16", "172.16.0.0/12",
		"192.0.0.0/24", "192.0.2.0/24", "192.88.99.0/24", "192.168.0.0/16", "198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24",
		"224.0.0.0/4", "240.0.0.0/4", "255.255.255.255/32",
		"::/96", "::ffff:0:0:0/96", "64:ff9b::/96", "64:ff9b:1::/48", "2002::/16", "100::/64", "2001::/32", "2001:20::/28", "2001:db8::/32",
		"fc00::/7", "fe80::/10", "ff00::/8",
	} {
		out = append(out, netip.MustParsePrefix(c))
	}
	return out
}()

// blockedAddr is an allowlist: only a global unicast address outside every
// special-purpose block passes. IPv4-mapped addresses are judged as the IPv4
// address inside; the prefixes that embed an IPv4 address (IPv4-compatible,
// IPv4-translated, NAT64, 6to4) are refused outright, so no embedded 127.0.0.1
// or 10.0.0.1 gets through. Loopback passes only when allowLoopback is set.
func blockedAddr(a netip.Addr, allowLoopback bool) bool {
	a = a.Unmap().WithZone("")
	if a.IsLoopback() && allowLoopback {
		return false
	}
	if !a.IsGlobalUnicast() {
		return true
	}
	for _, p := range specialPurpose {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

func numericHost(host string) bool {
	last := host[strings.LastIndexByte(host, '.')+1:]
	if rest, ok := strings.CutPrefix(last, "0x"); ok {
		return strings.TrimLeft(rest, "0123456789abcdef") == ""
	}
	return last != "" && strings.TrimLeft(last, "0123456789") == ""
}

// CheckURL accepts the https URL of a host that is not a special-purpose
// address; it is the rule for a bank URL that is stored. A hostname is only
// judged here by name (BankHTTP judges what it resolves to when it connects):
// localhost, *.localhost and names whose last label is a number (inet_aton
// forms such as 2130706433 or 0x7f.1) are refused.
func CheckURL(raw string) error { return checkURL(raw, false) }

func checkURL(raw string, allowLoopback bool) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil {
		return fmt.Errorf("invalid url %q", raw)
	}
	host := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
	if host == "" {
		return fmt.Errorf("invalid url %q", raw)
	}
	loopback := host == "localhost" || strings.HasSuffix(host, ".localhost")
	if a, err := netip.ParseAddr(host); err == nil {
		if blockedAddr(a, allowLoopback) {
			return fmt.Errorf("url %q points at a loopback, private or other special-purpose address", raw)
		}
		loopback = a.Unmap().IsLoopback()
	} else if numericHost(host) {
		return fmt.Errorf("url %q has a numeric host name; give the address in its standard form", raw)
	}
	switch {
	case loopback && !allowLoopback:
		return fmt.Errorf("url %q points at a loopback address", raw)
	case u.Scheme == "https", u.Scheme == "http" && loopback:
		return nil
	}
	return fmt.Errorf("url %q must use https", raw)
}

func guardDial(allowLoopback bool) func(string, string, syscall.RawConn) error {
	return func(_, address string, _ syscall.RawConn) error {
		ap, err := netip.ParseAddrPort(address)
		if err != nil {
			return fmt.Errorf("bank address %q is not an IP address", address)
		}
		if blockedAddr(ap.Addr(), allowLoopback) {
			return fmt.Errorf("bank host resolves to %s, a loopback, private or other special-purpose address", ap.Addr())
		}
		return nil
	}
}

// BankHTTP makes the calls to a bank URL a caller supplied. Its HTTP client is
// not reachable from outside the package, so every request passes the URL check
// and the dialer's check of the address actually connected to: no connection to
// a non-global or special-purpose address whatever the hostname, proxy settings
// ignored so the check sees the real peer, and a redirect is returned as the
// response instead of being followed with the signed body. Plain http to
// loopback is possible only when allowLoopback is set (local development).
type BankHTTP struct {
	client        *http.Client
	allowLoopback bool
}

func NewBankHTTP(timeout time.Duration, allowLoopback bool) *BankHTTP {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.Proxy = nil
	tr.DialContext = (&net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second, Control: guardDial(allowLoopback)}).DialContext
	return &BankHTTP{
		allowLoopback: allowLoopback,
		client: &http.Client{
			Timeout:       timeout,
			Transport:     tr,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	}
}

func (b *BankHTTP) Check(raw string) error { return checkURL(raw, b.allowLoopback) }

func (b *BankHTTP) Do(req *http.Request) (*http.Response, error) {
	if err := b.Check(req.URL.String()); err != nil {
		return nil, err
	}
	return b.client.Do(req)
}
