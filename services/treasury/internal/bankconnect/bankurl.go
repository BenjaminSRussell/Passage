package bankconnect

import (
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"sync/atomic"
	"syscall"
	"time"
)

var loopbackAllowed atomic.Bool

// AllowLoopback lets bank URLs point at a loopback host over plain http. It is
// set from TREASURY_ENV=dev at start-up and by tests; production leaves it off.
func AllowLoopback(on bool) { loopbackAllowed.Store(on) }

var specialPurpose = func() []netip.Prefix {
	var out []netip.Prefix
	for _, c := range []string{
		"0.0.0.0/8", "10.0.0.0/8", "100.64.0.0/10", "127.0.0.0/8", "169.254.0.0/16", "172.16.0.0/12",
		"192.0.0.0/24", "192.0.2.0/24", "192.168.0.0/16", "198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24",
		"224.0.0.0/4", "240.0.0.0/4", "255.255.255.255/32",
		"::/96", "64:ff9b::/96", "64:ff9b:1::/48", "2002::/16", "100::/64", "2001::/32", "2001:db8::/32",
		"fc00::/7", "fe80::/10", "ff00::/8",
	} {
		out = append(out, netip.MustParsePrefix(c))
	}
	return out
}()

// blockedAddr is an allowlist: only a global unicast address outside every
// special-purpose block passes. IPv4-mapped addresses are judged as the IPv4
// address inside; the prefixes that embed an IPv4 address (IPv4-compatible,
// NAT64, 6to4) are refused outright, so no embedded 127.0.0.1 or 10.0.0.1
// gets through. Loopback passes only after AllowLoopback.
func blockedAddr(a netip.Addr) bool {
	a = a.Unmap().WithZone("")
	if a.IsLoopback() && loopbackAllowed.Load() {
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

// CheckURL accepts https to a host that is not a special-purpose address.
// A hostname is only judged here by name; BankHTTPClient judges what it resolves
// to when it connects. Plain http to loopback is accepted only after AllowLoopback.
func CheckURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil {
		return fmt.Errorf("invalid url %q", raw)
	}
	host := u.Hostname()
	loopback := host == "localhost"
	if a, err := netip.ParseAddr(host); err == nil {
		if blockedAddr(a) {
			return fmt.Errorf("url %q points at a loopback, private or other special-purpose address", raw)
		}
		loopback = a.Unmap().IsLoopback()
	}
	switch {
	case loopback && !loopbackAllowed.Load():
		return fmt.Errorf("url %q points at a loopback address", raw)
	case u.Scheme == "https", u.Scheme == "http" && loopback:
		return nil
	}
	return fmt.Errorf("url %q must use https", raw)
}

func guardDial(_, address string, _ syscall.RawConn) error {
	ap, err := netip.ParseAddrPort(address)
	if err != nil {
		return fmt.Errorf("bank address %q is not an IP address", address)
	}
	if blockedAddr(ap.Addr()) {
		return fmt.Errorf("bank host resolves to %s, a loopback, private or other special-purpose address", ap.Addr())
	}
	return nil
}

// BankHTTPClient is the client for calls to a bank URL a caller supplied: it
// refuses to connect to a non-global or special-purpose address whatever the
// hostname, ignores proxy settings so the check sees the real peer, and returns
// a redirect as the response instead of following it with the signed body.
func BankHTTPClient(timeout time.Duration) *http.Client {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.Proxy = nil
	tr.DialContext = (&net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second, Control: guardDial}).DialContext
	return &http.Client{
		Timeout:       timeout,
		Transport:     tr,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}
