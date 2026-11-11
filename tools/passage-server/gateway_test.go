package main

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"testing/fstest"
)

func testGateway(t *testing.T, ready bool) (http.Handler, *[]http.Header) {
	t.Helper()
	var seen []http.Header
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Header.Clone())
		_, _ = w.Write([]byte(r.URL.Path))
	}))
	t.Cleanup(up.Close)
	u, _ := url.Parse(up.URL)
	web := fstest.MapFS{"index.html": {Data: []byte("<html>app</html>")}, "assets/app.js": {Data: []byte("js")}}
	passThrough := func(next http.Handler) http.Handler { return next }
	return NewGateway(passThrough, map[string]*url.URL{"party": u}, web, func() (bool, []Status) { return ready, nil }, "gw-secret"), &seen
}

func do(h http.Handler, method, target string, hdr map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, nil)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestGatewayServesWebAndSPAFallback(t *testing.T) {
	g, _ := testGateway(t, true)
	if rec := do(g, "GET", "/", nil); rec.Code != 200 || !strings.Contains(rec.Body.String(), "app") {
		t.Fatalf("/ = %d %s", rec.Code, rec.Body)
	}
	if rec := do(g, "GET", "/billing/invoices", nil); rec.Code != 200 || !strings.Contains(rec.Body.String(), "app") {
		t.Fatalf("spa route = %d", rec.Code)
	}
	if rec := do(g, "GET", "/assets/app.js", nil); rec.Code != 200 || rec.Body.String() != "js" {
		t.Fatalf("asset = %d %s", rec.Code, rec.Body)
	}
	if rec := do(g, "GET", "/assets/missing.js", nil); rec.Code != 404 {
		t.Fatalf("missing asset = %d", rec.Code)
	}
}

func TestGatewayRoutesAPIAndStripsClientIdentityHeaders(t *testing.T) {
	g, seen := testGateway(t, true)
	rec := do(g, "GET", "/api/v1/party/vendors", map[string]string{"X-Passage-User": "forged", "X-Passage-Duties": "everything"})
	if rec.Code != 200 || rec.Body.String() != "/v1/party/vendors" {
		t.Fatalf("api = %d %q", rec.Code, rec.Body)
	}
	if len(*seen) == 1 && (*seen)[0].Get("X-Passage-Gateway-Secret") != "gw-secret" {
		t.Fatalf("gateway secret not forwarded: %v", *seen)
	}
	if len(*seen) != 1 || (*seen)[0].Get("X-Passage-User") != "" || (*seen)[0].Get("X-Passage-Duties") != "" {
		t.Fatalf("client identity headers reached the service: %v", *seen)
	}
}

func TestGatewayUnknownRouteAndNonCanonicalPath(t *testing.T) {
	g, _ := testGateway(t, true)
	if rec := do(g, "GET", "/api/v1/nowhere/x", nil); rec.Code != 404 {
		t.Fatalf("unknown route = %d", rec.Code)
	}
	if rec := do(g, "GET", "/api/v1/party/../ledger/x", nil); rec.Code != 400 {
		t.Fatalf("non-canonical = %d", rec.Code)
	}
}

func TestGatewayLegalEntitySelectionMustMatchToken(t *testing.T) {
	g, _ := testGateway(t, true)
	rec := do(g, "GET", "/api/v1/party/vendors", map[string]string{"X-Passage-LegalEntity": "le-2"})
	// No token claims in the pass-through guard: a selection that the token does not carry is refused.
	if rec.Code != 403 {
		t.Fatalf("selection without matching token = %d", rec.Code)
	}
}

func TestGatewayReadiness(t *testing.T) {
	g, _ := testGateway(t, false)
	rec := do(g, "GET", "/readyz", nil)
	if rec.Code != 503 || strings.Contains(rec.Body.String(), "services") {
		t.Fatalf("readyz = %d %s (must not list services)", rec.Code, rec.Body)
	}
	if rec := do(g, "GET", "/healthz", nil); rec.Code != 200 {
		t.Fatalf("healthz = %d", rec.Code)
	}
}
