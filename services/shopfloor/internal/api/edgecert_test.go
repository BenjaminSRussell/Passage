package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

type nodeTenants map[string]string

func (n nodeTenants) EdgeNodeTenant(_ context.Context, id, hash string) (string, error) {
	if hash == "revoked" {
		return "", nil
	}
	return n[id], nil
}

func TestEdgeCertIdentityRefusesARevokedCertificate(t *testing.T) {
	nodes := nodeTenants{certNode: certTenant}
	h := seen(t, `By=spiffe://cluster/envoy;Hash=REVOKED;Subject="CN=edge:`+certNode+`"`, nil, nodes)
	if h.Get("X-Passage-Tenant") != "" || h.Get("X-Passage-User") != "" {
		t.Fatalf("a revoked certificate got an identity: %v", h)
	}
	if got := certHash(`Hash=AbC;Subject="CN=x"`); got != "abc" {
		t.Fatalf("hash %q", got)
	}
}

const (
	certNode   = "0195f0a0-0000-7000-8000-0000000000a1"
	certTenant = "0195f0a0-0000-7000-8000-0000000000b1"
)

// seen records the identity headers a request reached the next handler with.
func seen(t *testing.T, xfcc string, headers map[string]string, nodes nodeTenants) http.Header {
	t.Helper()
	var got http.Header
	h := EdgeCertIdentity(nodes)(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { got = r.Header.Clone() }))
	req := httptest.NewRequest(http.MethodPost, "/v1/shopfloor/machine-events", nil)
	if xfcc != "" {
		req.Header.Set("X-Forwarded-Client-Cert", xfcc)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	h.ServeHTTP(httptest.NewRecorder(), req)
	return got
}

func TestEdgeCertIdentityTurnsACertificateIntoTheConnectorIdentity(t *testing.T) {
	nodes := nodeTenants{certNode: certTenant}
	h := seen(t, `By=spiffe://cluster/envoy;Hash=abc123;Subject="CN=edge:`+certNode+`";URI=`, nil, nodes)
	if h.Get("X-Passage-Tenant") != certTenant || h.Get("X-Passage-User") != "edge:"+certNode || h.Get("X-Passage-Duties") != DutyEdgeConnect {
		t.Fatalf("identity headers: %v", h)
	}
	// The node id in a different case still finds the node; more subject attributes are fine.
	h = seen(t, `Hash=abc;Subject="O=Passage,CN=edge:`+certNode+`"`, nil, nodes)
	if h.Get("X-Passage-User") != "edge:"+certNode {
		t.Fatalf("with an organisation: %v", h)
	}
}

func TestEdgeCertIdentityLeavesEverythingElseAlone(t *testing.T) {
	nodes := nodeTenants{certNode: certTenant}
	for name, tc := range map[string]struct {
		xfcc    string
		headers map[string]string
	}{
		"no certificate":               {"", nil},
		"a JWT identity already there": {`Subject="CN=edge:` + certNode + `"`, map[string]string{"X-Passage-Tenant": "0195f0a0-0000-7000-8000-0000000000b9", "X-Passage-User": "alice"}},
		"a certificate of no node":     {`Subject="CN=iot-edge"`, nil},
		"a node that does not exist":   {`Subject="CN=edge:0195f0a0-0000-7000-8000-0000000000ff"`, nil},
		"a chain of proxies":           {`Subject="CN=edge:` + certNode + `",Subject="CN=other"`, nil},
		"not a uuid":                   {`Subject="CN=edge:pump"`, nil},
		"no subject":                   {`Hash=abc`, nil},
	} {
		h := seen(t, tc.xfcc, tc.headers, nodes)
		wantTenant := tc.headers["X-Passage-Tenant"]
		if h.Get("X-Passage-Tenant") != wantTenant || h.Get("X-Passage-Duties") != "" || h.Get("X-Passage-User") != tc.headers["X-Passage-User"] {
			t.Errorf("%s: headers %v", name, h)
		}
	}
}

func TestCertCommonName(t *testing.T) {
	for in, want := range map[string]string{
		`Subject="CN=a,O=b"`:               "a",
		`By=x;Subject="O=b,CN=a b";Hash=h`: "a b",
		`subject="CN=a"`:                   "a",
		`Subject="CN=a;b,O=c"`:             "a;b",
		`Subject="O=b"`:                    "",
		`Subject="CN=a",Subject="CN=b"`:    "",
	} {
		if got, ok := certCommonName(in); got != want || ok != (want != "") {
			t.Errorf("%s: got %q %v, want %q", in, got, ok, want)
		}
	}
}
