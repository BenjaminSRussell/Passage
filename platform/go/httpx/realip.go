package httpx

import (
	"net"
	"net/http"
	"net/netip"
	"os"
	"strings"
)

// TrustedProxiesEnv names the comma-separated list of CIDRs (or bare IPs) of the
// ingress proxies whose forwarded headers are honoured. Empty means none: the
// client IP is always the TCP peer address.
const TrustedProxiesEnv = "PASSAGE_TRUSTED_PROXIES"

// ParseTrustedProxies parses a comma-separated CIDR/IP list, ignoring blanks and
// invalid entries.
func ParseTrustedProxies(s string) []netip.Prefix {
	var out []netip.Prefix
	for _, f := range strings.Split(s, ",") {
		f = strings.TrimSpace(f)
		if f == "" {
			continue
		}
		if p, err := netip.ParsePrefix(f); err == nil {
			out = append(out, p.Masked())
		} else if a, err := netip.ParseAddr(f); err == nil {
			out = append(out, netip.PrefixFrom(a.Unmap(), a.Unmap().BitLen()))
		}
	}
	return out
}

func trusted(a netip.Addr, proxies []netip.Prefix) bool {
	a = a.Unmap()
	for _, p := range proxies {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

// TrustedRealIP sets r.RemoteAddr to the client IP. Forwarded headers are read
// only when the TCP peer is a trusted proxy; X-Forwarded-For is walked right to
// left and the first address that is not itself a trusted proxy wins, so a
// client cannot inject an address by prepending entries.
func TrustedRealIP(proxies []netip.Prefix) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if ip := clientIP(r, proxies); ip.IsValid() {
				r.RemoteAddr = ip.String()
			}
			next.ServeHTTP(w, r)
		})
	}
}

func clientIP(r *http.Request, proxies []netip.Prefix) netip.Addr {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	peer, err := netip.ParseAddr(host)
	if err != nil {
		return netip.Addr{}
	}
	peer = peer.Unmap()
	if len(proxies) == 0 || !trusted(peer, proxies) {
		return peer
	}
	if xff := r.Header.Values("X-Forwarded-For"); len(xff) > 0 {
		parts := strings.Split(strings.Join(xff, ","), ",")
		for i := len(parts) - 1; i >= 0; i-- {
			a, err := netip.ParseAddr(strings.TrimSpace(parts[i]))
			if err != nil {
				return peer // malformed hop: stop trusting the chain
			}
			if !trusted(a, proxies) {
				return a.Unmap()
			}
		}
		return peer
	}
	if a, err := netip.ParseAddr(strings.TrimSpace(r.Header.Get("X-Real-IP"))); err == nil {
		return a.Unmap()
	}
	return peer
}

func trustedProxiesFromEnv() []netip.Prefix { return ParseTrustedProxies(os.Getenv(TrustedProxiesEnv)) }
