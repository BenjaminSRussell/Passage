package safenet

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"
	"time"
)

func TestPublic(t *testing.T) {
	for _, bad := range []string{"127.0.0.1", "10.1.2.3", "172.16.0.1", "172.31.255.255", "192.168.1.1", "169.254.169.254", "100.64.0.1", "0.0.0.0", "::1", "fe80::1", "fc00::1", "fd12::1",
		"::ffff:127.0.0.1", "::ffff:169.254.169.254", "224.0.0.1", "198.18.0.1", "64:ff9b::7f00:1"} {
		if Public(netip.MustParseAddr(bad)) {
			t.Errorf("%s must be refused", bad)
		}
	}
	for _, good := range []string{"8.8.8.8", "93.184.216.34", "2606:4700::1111", "172.32.0.1"} {
		if !Public(netip.MustParseAddr(good)) {
			t.Errorf("%s must be allowed", good)
		}
	}
}

func TestClientRefusesLoopbackAndMetadata(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	defer srv.Close()
	for _, url := range []string{srv.URL, "http://169.254.169.254/latest/meta-data/", "http://10.0.0.5:9/"} {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		resp, err := Client(5*time.Second, false).Do(req)
		cancel()
		if err == nil {
			_ = resp.Body.Close()
			t.Fatalf("%s was reached", url)
		}
		if !errors.Is(err, ErrForbiddenAddress) {
			t.Fatalf("%s: want the address refusal, got %v", url, err)
		}
	}
	// Development mode reaches the loopback test server.
	resp, err := Client(5*time.Second, true).Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
}
