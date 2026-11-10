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

func blockedAddr(a netip.Addr) bool {
	a = a.Unmap()
	if a.IsLoopback() {
		return !loopbackAllowed.Load()
	}
	return a.IsPrivate() || a.IsLinkLocalUnicast() || a.IsLinkLocalMulticast() || a.IsMulticast() || a.IsUnspecified() ||
		netip.MustParsePrefix("100.64.0.0/10").Contains(a)
}

// CheckURL accepts https to a host that is not loopback, private or link-local.
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
			return fmt.Errorf("url %q points at a loopback, private or link-local address", raw)
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
		return fmt.Errorf("bank host resolves to %s, a loopback, private or link-local address", ap.Addr())
	}
	return nil
}

// BankHTTPClient is the client for calls to a bank URL a caller supplied: it
// refuses to connect to a loopback, private or link-local address whatever the
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
