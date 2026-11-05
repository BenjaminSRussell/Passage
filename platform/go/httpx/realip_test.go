package httpx

import (
	"net/http/httptest"
	"testing"
)

func TestClientIP(t *testing.T) {
	proxies := ParseTrustedProxies("10.0.0.0/8, 192.168.1.5, bogus")
	cases := []struct {
		name, remote, xff, xri, want string
		proxies                      bool
	}{
		{"no proxies ignores headers", "203.0.113.9:1234", "1.2.3.4", "5.6.7.8", "203.0.113.9", false},
		{"untrusted peer ignores headers", "203.0.113.9:1234", "1.2.3.4", "", "203.0.113.9", true},
		{"trusted peer uses xff", "10.1.2.3:80", "198.51.100.7", "", "198.51.100.7", true},
		{"spoofed leftmost entry ignored", "10.1.2.3:80", "1.1.1.1, 198.51.100.7, 10.9.9.9", "", "198.51.100.7", true},
		{"all hops trusted falls back to peer", "10.1.2.3:80", "10.2.2.2", "", "10.1.2.3", true},
		{"malformed hop stops", "10.1.2.3:80", "1.1.1.1, junk", "", "10.1.2.3", true},
		{"x-real-ip from trusted peer", "192.168.1.5:80", "", "198.51.100.8", "198.51.100.8", true},
		{"ipv6 peer", "[2001:db8::1]:443", "1.2.3.4", "", "2001:db8::1", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := httptest.NewRequest("GET", "/", nil)
			r.RemoteAddr = c.remote
			if c.xff != "" {
				r.Header.Set("X-Forwarded-For", c.xff)
			}
			if c.xri != "" {
				r.Header.Set("X-Real-IP", c.xri)
			}
			p := proxies
			if !c.proxies {
				p = nil
			}
			if got := clientIP(r, p).String(); got != c.want {
				t.Fatalf("got %s want %s", got, c.want)
			}
		})
	}
	if len(proxies) != 2 {
		t.Fatalf("expected 2 parsed proxies, got %d", len(proxies))
	}
}
