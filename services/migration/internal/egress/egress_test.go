package egress

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"sync/atomic"
	"testing"
	"time"
)

// The cases below are the cases of services/integration-hub-ts/test/ssrf.test.ts, one for one, against the Go guard.

func resolveTo(addrs ...string) Resolver {
	return func(context.Context, string) ([]netip.Addr, error) {
		out := make([]netip.Addr, len(addrs))
		for i, a := range addrs {
			out[i] = netip.MustParseAddr(a)
		}
		return out, nil
	}
}

func publicResolver() Resolver { return resolveTo("93.184.216.34") }

func TestCheckURL_HTTPRejectedUnlessAllowed(t *testing.T) {
	g := &Guard{Resolver: publicResolver()}
	if _, err := g.CheckURL(context.Background(), "http://example.com/h", false); !errors.Is(err, ErrDenied) {
		t.Fatalf("http without allowHTTP = %v", err)
	}
	ok, err := g.CheckURL(context.Background(), "http://example.com/h", true)
	if err != nil || ok.Address.String() != "93.184.216.34" {
		t.Fatalf("http with allowHTTP = %v %v", ok, err)
	}
}

func TestCheckURL_NonHTTPSchemesCredentialsAndGarbageRejected(t *testing.T) {
	g := &Guard{Resolver: publicResolver()}
	for _, u := range []string{"ftp://example.com/", "file:///etc/passwd", "https://user:pw@example.com/", "not a url", "gopher://example.com/"} {
		if _, err := g.CheckURL(context.Background(), u, true); !errors.Is(err, ErrDenied) {
			t.Errorf("%q = %v, want denied", u, err)
		}
	}
}

// blockedAddresses is the TS list plus the cases the Go port adds (metadata, embedded forms).
var blockedAddresses = []string{
	"127.0.0.1", "127.1.2.3", "10.0.0.1", "10.255.255.255", "172.16.0.1", "172.31.255.255", "192.168.1.1",
	"169.254.169.254", "169.254.170.2", "100.64.0.1", "100.127.255.255", "0.0.0.0", "224.0.0.1", "255.255.255.255",
	"::", "::1", "fc00::1", "fd00:ec2::254", "fe80::1", "ff02::1", "::ffff:127.0.0.1", "::ffff:169.254.169.254", "::ffff:7f00:1",
	"64:ff9b::a00:1", "64:ff9b::7f00:1", "64:ff9b::a9fe:a9fe", "2002:7f00:0001::", "2002:a9fe:a9fe::1", "2002:0a00:0001::",
	"64:ff9b:1::a9fe:a9fe", "::127.0.0.1", "::7f00:1", "::a9fe:a9fe", "::ffff:0:a9fe:a9fe", "fe80::1%eth0", "FE80::1%en0",
	"FD00:EC2::254", "::FFFF:7F00:1", "2002:7F00:0001::", "2001:db8::1", "2001:2::1", "2001:10::1", "2001:20::1", "2001:1ff::1",
	"::ffff:0:127.0.0.1", "2001:0:4136:e378:8000:63bf:3fff:fdd2", "2001::1", "::ffff:10.0.0.1",
}

func classOf(s string) Class {
	a, err := netip.ParseAddr(s)
	if err != nil {
		return Denied
	}
	return Classify(a)
}

func TestClassify_EveryNonPublicAddressIsNotPublic(t *testing.T) {
	for _, a := range blockedAddresses {
		if classOf(a) == Public {
			t.Errorf("%s classified public", a)
		}
	}
}

func TestClassify_AlwaysDeniedNeverPrivate(t *testing.T) {
	for _, a := range []string{"127.0.0.1", "169.254.169.254", "::1", "fe80::1", "0.0.0.0", "::", "224.0.0.1", "ff02::1", "255.255.255.255",
		"::ffff:127.0.0.1", "::ffff:169.254.169.254", "64:ff9b::a00:1", "2002:7f00:1::", "2002:a9fe:a9fe::1", "2002:0a00:0001::", "100.64.0.1"} {
		if classOf(a) != Denied {
			t.Errorf("%s = %v, want Denied", a, classOf(a))
		}
	}
	for _, a := range []string{"10.0.0.1", "172.16.5.5", "172.31.255.255", "192.168.1.1", "fc00::1", "fd00:ec2::254", "::ffff:10.0.0.1"} {
		if classOf(a) != Private {
			t.Errorf("%s = %v, want Private", a, classOf(a))
		}
	}
}

func TestClassify_PublicAddressesAreNotBlocked(t *testing.T) {
	for _, a := range []string{"93.184.216.34", "8.8.8.8", "172.15.255.255", "172.32.0.1", "100.63.255.255", "2606:4700::1111", "::ffff:8.8.8.8", "2002:0808:0808::1", "2a00:1450:4001::200e"} {
		if classOf(a) != Public {
			t.Errorf("%s = %v, want Public", a, classOf(a))
		}
	}
}

func TestCheckURL_IPLiteralsAreValidatedWithoutResolving(t *testing.T) {
	g := &Guard{Resolver: func(context.Context, string) ([]netip.Addr, error) { t.Error("must not resolve"); return nil, nil }}
	for _, u := range []string{"https://127.0.0.1/", "https://[::1]/", "https://169.254.169.254/latest/meta-data", "https://10.1.2.3/", "https://[::ffff:127.0.0.1]/", "https://[64:ff9b::a00:1]/", "https://[2002:7f00:1::]/"} {
		if _, err := g.CheckURL(context.Background(), u, false); !errors.Is(err, ErrDenied) {
			t.Errorf("%s = %v, want denied", u, err)
		}
	}
}

func TestCheckURL_HostResolvingToPrivateIsRejectedEvenWithAPublicRecord(t *testing.T) {
	for _, rs := range [][]string{{"10.0.0.5"}, {"93.184.216.34", "169.254.169.254"}, {"93.184.216.34", "127.0.0.1"}} {
		g := &Guard{Resolver: resolveTo(rs...)}
		if _, err := g.CheckURL(context.Background(), "https://evil.example/", false); !errors.Is(err, ErrDenied) {
			t.Errorf("%v = %v, want denied", rs, err)
		}
	}
}

func TestCheckURL_UnresolvableHostIsRejected(t *testing.T) {
	g := &Guard{Resolver: func(context.Context, string) ([]netip.Addr, error) { return nil, errors.New("ENOTFOUND") }}
	if _, err := g.CheckURL(context.Background(), "https://nx.example/", false); !errors.Is(err, ErrDenied) {
		t.Fatalf("got %v", err)
	}
}

func TestCheckURL_PublicHTTPSIsPinnedToTheFirstResolvedAddress(t *testing.T) {
	g := &Guard{Resolver: resolveTo("2606:4700::1111", "93.184.216.34")}
	tgt, err := g.CheckURL(context.Background(), "https://hooks.example.com/a", false)
	if err != nil || tgt.Address.String() != "2606:4700::1111" || !tgt.Address.Is6() || tgt.URL.Hostname() != "hooks.example.com" {
		t.Fatalf("got %+v %v", tgt, err)
	}
}

// The allowlist: private ranges only when the operator lists them; the always-denied ranges never.

func TestPolicy_PrivateAllowedOnlyWhenListed(t *testing.T) {
	cases := []struct {
		allow, host, port string
		resolved          string
		ok                bool
	}{
		{"", "erp.corp", "1433", "10.20.30.40", false},
		{"10.20.0.0/16", "erp.corp", "1433", "10.20.30.40", true},
		{"10.20.0.0/16", "erp.corp", "1433", "10.21.30.40", false},
		{"erp.corp:1433", "erp.corp", "1433", "10.20.30.40", true},
		{"erp.corp:1433", "ERP.corp", "1433", "10.20.30.40", true},
		{"erp.corp:1433", "erp.corp", "1434", "10.20.30.40", false},
		{"10.20.30.40:1433", "10.20.30.40", "1433", "10.20.30.40", true},
		{"10.20.30.40:1433", "10.20.30.40", "443", "10.20.30.40", false},
		{"fd00::/8", "erp.corp", "443", "fd00::5", true},
		{"[fd00::5]:443", "fd00::5", "443", "fd00::5", true},
		{"192.168.0.0/16,10.0.0.0/8", "x", "80", "192.168.1.1", true},
		// Always denied whatever the list says.
		{"127.0.0.0/8", "localhost", "80", "127.0.0.1", false},
		{"0.0.0.0/0", "localhost", "80", "127.0.0.1", false},
		{"169.254.0.0/16", "meta", "80", "169.254.169.254", false},
		{"::/0", "meta", "80", "fe80::1", false},
		{"meta:80", "meta", "80", "169.254.169.254", false},
		{"0.0.0.0/0", "x", "80", "224.0.0.1", false},
		{"0.0.0.0/0", "x", "80", "::ffff:127.0.0.1", false},
		{"10.0.0.0/8", "x", "80", "64:ff9b::a00:1", false},
		{"10.0.0.0/8", "x", "80", "2002:0a00:1::", false},
		// IPv4-mapped is the IPv4 it connects to.
		{"10.0.0.0/8", "x", "80", "::ffff:10.0.0.1", true},
	}
	for _, c := range cases {
		pol, err := ParsePolicy(c.allow)
		if err != nil {
			t.Fatalf("%q: %v", c.allow, err)
		}
		g := &Guard{Policy: pol, Resolver: resolveTo(c.resolved)}
		_, err = g.Check(context.Background(), c.host, c.port)
		if (err == nil) != c.ok {
			t.Errorf("allow=%q host=%s:%s -> %s: err=%v, want ok=%v", c.allow, c.host, c.port, c.resolved, err, c.ok)
		}
		if err != nil && !errors.Is(err, ErrDenied) {
			t.Errorf("%v is not ErrDenied", err)
		}
	}
}

func TestParsePolicy_RejectsGarbage(t *testing.T) {
	for _, s := range []string{"10.0.0.0/33", "erp.corp", "erp.corp:0", "erp.corp:99999", ":80", "10.0.0.0/8, nonsense"} {
		if _, err := ParsePolicy(s); err == nil {
			t.Errorf("%q accepted", s)
		}
	}
	if p, err := ParsePolicy(" "); err != nil || p == nil {
		t.Fatalf("empty = %v %v", p, err)
	}
}

func listen(t *testing.T, h http.HandlerFunc) (*httptest.Server, string) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	_, port, _ := net.SplitHostPort(srv.Listener.Addr().String())
	return srv, port
}

// toServer makes the final connection go to the local test server whatever pinned address was checked.
func toServer(srv *httptest.Server, seen *[]string) func(context.Context, string, string) (net.Conn, error) {
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		if seen != nil {
			*seen = append(*seen, address)
		}
		return (&net.Dialer{}).DialContext(ctx, network, srv.Listener.Addr().String())
	}
}

func TestHTTPClient_SendsNothingForABlockedTarget(t *testing.T) {
	var hits atomic.Int32
	srv, port := listen(t, func(http.ResponseWriter, *http.Request) { hits.Add(1) })
	g := &Guard{Timeout: time.Second}
	if _, err := g.HTTPClient().Get("http://127.0.0.1:" + port + "/"); !errors.Is(err, ErrDenied) {
		t.Fatalf("literal = %v", err)
	}
	g = &Guard{Timeout: time.Second, Resolver: resolveTo("127.0.0.1"), TestDialFunc: toServer(srv, nil)}
	if _, err := g.HTTPClient().Get("http://local.example:" + port + "/"); !errors.Is(err, ErrDenied) {
		t.Fatalf("via host = %v", err)
	}
	if hits.Load() != 0 {
		t.Fatalf("server was hit %d times", hits.Load())
	}
}

// The default dialer's own Control hook refuses a loopback socket even if the checks before it were skipped.
func TestControl_RefusesLoopbackAtConnectTime(t *testing.T) {
	srv, _ := listen(t, func(http.ResponseWriter, *http.Request) {})
	g := &Guard{}
	d := &net.Dialer{Timeout: time.Second, Control: g.Control}
	if c, err := d.DialContext(context.Background(), "tcp", srv.Listener.Addr().String()); err == nil || !errors.Is(err, ErrDenied) {
		if c != nil {
			c.Close()
		}
		t.Fatalf("loopback dial = %v", err)
	}
}

// Rebinding: the name answers public first and loopback afterwards. The connection is pinned to the checked answer,
// and the name is not resolved a second time.
func TestDialContext_PinsTheCheckedAddressAgainstDNSRebinding(t *testing.T) {
	srv, port := listen(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	var lookups atomic.Int32
	resolver := func(context.Context, string) ([]netip.Addr, error) {
		if lookups.Add(1) == 1 {
			return []netip.Addr{netip.MustParseAddr("93.184.216.34")}, nil
		}
		return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil
	}
	var dialled []string
	g := &Guard{Resolver: resolver, TestDialFunc: toServer(srv, &dialled), Timeout: time.Second}
	resp, err := g.HTTPClient().Get("http://rebind.example:" + port + "/")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("status %d", resp.StatusCode)
	}
	if lookups.Load() != 1 || len(dialled) != 1 || dialled[0] != net.JoinHostPort("93.184.216.34", port) {
		t.Fatalf("lookups=%d dialled=%v", lookups.Load(), dialled)
	}
	// The second connection re-resolves, gets loopback and is refused.
	if _, err := g.HTTPClient().Get("http://rebind.example:" + port + "/"); !errors.Is(err, ErrDenied) {
		t.Fatalf("second request = %v, want denied", err)
	}
}

func TestHTTPClient_PinsTheValidatedAddressRegardlessOfDNS(t *testing.T) {
	srv, port := listen(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	pol, _ := ParsePolicy("10.9.0.0/16")
	g := &Guard{Policy: pol, Resolver: resolveTo("10.9.0.7"), TestDialFunc: toServer(srv, nil), Timeout: time.Second}
	resp, err := g.HTTPClient().Get("http://does-not-resolve.invalid:" + port + "/")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("status %d", resp.StatusCode)
	}
}

func TestHTTPClient_RedirectIsNotFollowed(t *testing.T) {
	var targetHits atomic.Int32
	target, tport := listen(t, func(w http.ResponseWriter, _ *http.Request) { targetHits.Add(1) })
	_ = target
	redirector, rport := listen(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://127.0.0.1:"+tport+"/internal", http.StatusFound)
	})
	pol, _ := ParsePolicy("10.9.0.0/16")
	g := &Guard{Policy: pol, Resolver: resolveTo("10.9.0.7"), TestDialFunc: toServer(redirector, nil), Timeout: time.Second}
	resp, err := g.HTTPClient().Get("http://hook.example:" + rport + "/hook")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusFound || targetHits.Load() != 0 {
		t.Fatalf("status %d, target hits %d", resp.StatusCode, targetHits.Load())
	}
}

func TestHTTPClient_TotalDeadlineAgainstSlowDripBody(t *testing.T) {
	srv, port := listen(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		for {
			select {
			case <-r.Context().Done():
				return
			case <-time.After(20 * time.Millisecond):
				w.Write([]byte("x"))
				w.(http.Flusher).Flush()
			}
		}
	})
	pol, _ := ParsePolicy("10.9.0.0/16")
	g := &Guard{Policy: pol, Resolver: resolveTo("10.9.0.7"), TestDialFunc: toServer(srv, nil), Timeout: 150 * time.Millisecond}
	start := time.Now()
	resp, err := g.HTTPClient().Get("http://drip.example:" + port + "/")
	if err == nil {
		_, err = readAll(resp)
	}
	if err == nil || time.Since(start) > 1500*time.Millisecond {
		t.Fatalf("err=%v after %v", err, time.Since(start))
	}
}

func readAll(resp *http.Response) (int, error) {
	defer resp.Body.Close()
	buf := make([]byte, 512)
	n := 0
	for {
		m, err := resp.Body.Read(buf)
		n += m
		if err != nil {
			return n, err
		}
	}
}

func TestHTTPClient_TricklingHeadersFailWithinTheDeadline(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				c.Write([]byte("HTTP/1.1 200 OK\r\n"))
				for {
					time.Sleep(20 * time.Millisecond)
					if _, err := c.Write([]byte("x-a: b\r\n")); err != nil {
						return
					}
				}
			}()
		}
	}()
	pol, _ := ParsePolicy("10.9.0.0/16")
	g := &Guard{Policy: pol, Resolver: resolveTo("10.9.0.7"), Timeout: 150 * time.Millisecond,
		TestDialFunc: func(ctx context.Context, n, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, n, ln.Addr().String())
		}}
	start := time.Now()
	_, err = g.HTTPClient().Get("http://trickle.example:80/")
	if err == nil || time.Since(start) > 1500*time.Millisecond {
		t.Fatalf("err=%v after %v", err, time.Since(start))
	}
}

func TestHTTPClient_SlowDNSCountsInsideTheDeadline(t *testing.T) {
	g := &Guard{Timeout: 100 * time.Millisecond, Resolver: func(context.Context, string) ([]netip.Addr, error) {
		time.Sleep(3 * time.Second) // ignores its context
		return []netip.Addr{netip.MustParseAddr("93.184.216.34")}, nil
	}}
	start := time.Now()
	if _, err := g.HTTPClient().Get("http://slow.example/"); err == nil || time.Since(start) > 1500*time.Millisecond {
		t.Fatalf("err=%v after %v", err, time.Since(start))
	}
}

func TestDialContext_RefusesNonTCPNetworks(t *testing.T) {
	g := &Guard{}
	for _, n := range []string{"unix", "udp", "ip"} {
		if _, err := g.DialContext(context.Background(), n, "93.184.216.34:80"); !errors.Is(err, ErrDenied) {
			t.Errorf("%s = %v", n, err)
		}
	}
}

// The dial redirection is a test-only hook: in the service binary a guard that carries one refuses to dial at all.
func TestDialContext_TestDialerIsRefusedOutsideATestBinary(t *testing.T) {
	prev := inTestBinary
	inTestBinary = func() bool { return false }
	t.Cleanup(func() { inTestBinary = prev })
	called := false
	pol, _ := ParsePolicy("10.0.0.0/8")
	g := &Guard{Policy: pol, Resolver: resolveTo("10.9.0.7"), Timeout: time.Second,
		TestDialFunc: func(context.Context, string, string) (net.Conn, error) {
			called = true
			return nil, errors.New("unreachable")
		}}
	_, err := g.DialContext(context.Background(), "tcp", "erp.acme.example:443")
	if !errors.Is(err, ErrDenied) || called {
		t.Fatalf("err=%v called=%v", err, called)
	}
}
