package main

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

const (
	testIssuer = "http://127.0.0.1:8080/realms/passage"
	testTenant = "00000000-0000-0000-0000-000000000001"
	adminUser  = "00000000-0000-0000-0000-0000000000a1"
	clerkUser  = "00000000-0000-0000-0000-0000000000c1"
)

type fixture struct {
	key      *rsa.PrivateKey
	gateway  *httptest.Server
	upstream *httptest.Server
	opaUp    *bool
	seen     chan http.Header
	seenPath chan string
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	jwks := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{{
			"kty": "RSA", "kid": "key-1", "use": "sig", "alg": "RS256",
			"n": base64.RawURLEncoding.EncodeToString(key.N.Bytes()),
			"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes()),
		}}})
	}))
	t.Cleanup(jwks.Close)

	up := true
	opa := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !up {
			http.Error(w, "down", http.StatusServiceUnavailable)
			return
		}
		var in struct {
			Input struct {
				Subject struct {
					UserID string `json:"user_id"`
				} `json:"subject"`
				Resource string `json:"resource"`
			} `json:"input"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		result := map[string]any{"allow": true}
		if in.Input.Subject.UserID == clerkUser && strings.HasPrefix(in.Input.Resource, "/v1/numbering") {
			result = map[string]any{"allow": false, "duty_missing": "numbering.write"}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"result": result})
	}))
	t.Cleanup(opa.Close)

	f := &fixture{key: key, opaUp: &up, seen: make(chan http.Header, 4), seenPath: make(chan string, 4)}
	f.upstream = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.seen <- r.Header.Clone()
		f.seenPath <- r.URL.Path
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(f.upstream.Close)

	getenv := map[string]string{
		"DEVGATEWAY_ISSUER": testIssuer, "DEVGATEWAY_JWKS_URL": jwks.URL, "DEVGATEWAY_TENANT_ID": testTenant, "DEVGATEWAY_OPA_URL": opa.URL,
	}
	handler, err := buildHandler(func(k string) string { return getenv[k] }, []string{
		"PASSAGE_SVC_NUMBERING_URL=" + f.upstream.URL, "PASSAGE_SVC_PARTY_URL=" + f.upstream.URL, "UNRELATED=1",
	})
	if err != nil {
		t.Fatal(err)
	}
	f.gateway = httptest.NewServer(handler)
	t.Cleanup(f.gateway.Close)
	return f
}

func (f *fixture) token(t *testing.T, sub string, mutate func(map[string]any)) string {
	t.Helper()
	now := time.Now()
	claims := map[string]any{
		"iss": testIssuer, "sub": sub, "tid": testTenant, "aud": []string{"passage-api"},
		"iat": now.Unix(), "exp": now.Add(5 * time.Minute).Unix(),
		"le": "00000000-0000-0000-0000-0000000000e1", "duties": "numbering.write,party.party.read", "tier": "standard",
	}
	if mutate != nil {
		mutate(claims)
	}
	header, _ := json.Marshal(map[string]string{"alg": "RS256", "kid": "key-1"})
	body, _ := json.Marshal(claims)
	signed := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(body)
	digest := sha256.Sum256([]byte(signed))
	sig, err := rsa.SignPKCS1v15(rand.Reader, f.key, crypto.SHA256, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	return signed + "." + base64.RawURLEncoding.EncodeToString(sig)
}

func (f *fixture) get(t *testing.T, path, token string, headers map[string]string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, f.gateway.URL+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

func TestGatewayRejectsRequestsWithoutAValidToken(t *testing.T) {
	f := newFixture(t)
	if got := f.get(t, "/api/v1/numbering/sequences", "", nil).StatusCode; got != http.StatusUnauthorized {
		t.Fatalf("no token: status %d, want 401", got)
	}
	forged := f.get(t, "/api/v1/numbering/sequences", "", map[string]string{
		"X-Passage-Tenant": testTenant, "X-Passage-User": adminUser, "X-Passage-Duties": "numbering.write",
	})
	if forged.StatusCode != http.StatusUnauthorized {
		t.Fatalf("forged identity headers without a token: status %d, want 401", forged.StatusCode)
	}
	wrongTenant := f.token(t, adminUser, func(c map[string]any) { c["tid"] = "00000000-0000-0000-0000-000000000002" })
	if got := f.get(t, "/api/v1/numbering/sequences", wrongTenant, nil).StatusCode; got != http.StatusUnauthorized {
		t.Fatalf("other tenant token: status %d, want 401", got)
	}
	expired := f.token(t, adminUser, func(c map[string]any) {
		c["iat"] = time.Now().Add(-10 * time.Minute).Unix()
		c["exp"] = time.Now().Add(-5 * time.Minute).Unix()
	})
	if got := f.get(t, "/api/v1/numbering/sequences", expired, nil).StatusCode; got != http.StatusUnauthorized {
		t.Fatalf("expired token: status %d, want 401", got)
	}
}

func TestGatewayForwardsVerifiedIdentityAndDropsForgedHeaders(t *testing.T) {
	f := newFixture(t)
	resp := f.get(t, "/api/v1/numbering/sequences", f.token(t, adminUser, nil), map[string]string{
		"X-Passage-Duties": "audit.read,identity.user.maintain", "X-Passage-User": clerkUser, "X-Passage-Tenant": "forged", "X-Passage-Extra": "x",
	})
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("admin status %d, want 204", resp.StatusCode)
	}
	seen, path := <-f.seen, <-f.seenPath
	want := map[string]string{
		"X-Passage-Tenant": testTenant, "X-Passage-User": adminUser, "X-Passage-Legal-Entity": "00000000-0000-0000-0000-0000000000e1",
		"X-Passage-Duties": "numbering.write,party.party.read", "X-Passage-Tier": "standard",
	}
	for k, v := range want {
		if got := seen.Get(k); got != v {
			t.Errorf("%s = %q, want %q", k, got, v)
		}
	}
	if seen.Get("X-Passage-Extra") != "" {
		t.Error("unknown X-Passage header reached the upstream")
	}
	if seen.Get("Authorization") != "" {
		t.Error("bearer token reached the upstream")
	}
	if path != "/v1/numbering/sequences" {
		t.Errorf("upstream path = %q, want /v1/numbering/sequences", path)
	}
}

func TestGatewayDeniesNonAdminOnAdminRoutes(t *testing.T) {
	f := newFixture(t)
	resp := f.get(t, "/api/v1/numbering/sequences", f.token(t, clerkUser, nil), nil)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("clerk status %d, want 403", resp.StatusCode)
	}
	var problem struct {
		Errors map[string]string `json:"errors"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&problem)
	if problem.Errors["duty_missing"] != "numbering.write" {
		t.Errorf("duty_missing = %q, want numbering.write", problem.Errors["duty_missing"])
	}
	select {
	case <-f.seen:
		t.Fatal("denied request reached the upstream")
	default:
	}
	if got := f.get(t, "/api/v1/party/parties", f.token(t, clerkUser, nil), nil).StatusCode; got != http.StatusNoContent {
		t.Fatalf("clerk on a route it may use: status %d, want 204", got)
	}
}

func TestGatewayFailsClosedWhenPolicyIsDown(t *testing.T) {
	f := newFixture(t)
	*f.opaUp = false
	if got := f.get(t, "/api/v1/numbering/sequences", f.token(t, adminUser, nil), nil).StatusCode; got != http.StatusServiceUnavailable {
		t.Fatalf("status %d, want 503", got)
	}
}

func TestGatewayRoutesOnlyConfiguredServices(t *testing.T) {
	f := newFixture(t)
	if got := f.get(t, "/api/v1/unknown/things", f.token(t, adminUser, nil), nil).StatusCode; got != http.StatusNotFound {
		t.Fatalf("unrouted service: status %d, want 404", got)
	}
}

func TestGatewayAnswers404ForBareAPIAndLookalikePrefixes(t *testing.T) {
	f := newFixture(t)
	for _, p := range []string{"/api", "/apifoo", "/v1/numbering/sequences"} {
		if got := f.get(t, p, f.token(t, adminUser, nil), nil).StatusCode; got != http.StatusNotFound {
			t.Errorf("%s: status %d, want 404", p, got)
		}
	}
}

func TestUpstreamsFromReadsPassageSvcVariables(t *testing.T) {
	got, err := upstreamsFrom([]string{"PASSAGE_SVC_LEDGER_URL=http://127.0.0.1:1", "PASSAGE_SVC_URL=http://x", "OTHER=1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got["ledger"] == nil || got["ledger"].String() != (&url.URL{Scheme: "http", Host: "127.0.0.1:1"}).String() {
		t.Fatalf("upstreams = %v", got)
	}
	if _, err := upstreamsFrom([]string{"PASSAGE_SVC_LEDGER_URL=not a url"}); err == nil {
		t.Fatal("malformed upstream accepted")
	}
	if _, err := upstreamsFrom(nil); err == nil {
		t.Fatal("empty upstream set accepted")
	}
}

func TestGatewayKeepsIdentityHeadersNamedInTheConnectionHeader(t *testing.T) {
	f := newFixture(t)
	named := "close, X-Passage-Legal-Entity, X-Passage-Tenant, X-Passage-User, X-Passage-Duties, X-Passage-Tier"
	resp := f.get(t, "/api/v1/numbering/sequences", f.token(t, adminUser, nil), map[string]string{"Connection": named})
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("status %d, want 204", resp.StatusCode)
	}
	seen := <-f.seen
	for _, h := range []string{"X-Passage-Legal-Entity", "X-Passage-Tenant", "X-Passage-User", "X-Passage-Duties", "X-Passage-Tier"} {
		if seen.Get(h) == "" {
			t.Errorf("%s was dropped by a Connection header naming it", h)
		}
	}
}

func TestGatewayRefusesPathsThatAreNotCanonical(t *testing.T) {
	f := newFixture(t)
	token := f.token(t, adminUser, nil)
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	for _, path := range []string{
		"/api/v1/party/../numbering/sequences", "/api/v1/party/./x", "/api/v1/party//x", "/api/v1/party/x/",
		"/api/v1/party%2Fx", "/api/v1/party/%2e%2e/numbering", "/api/v1/party/%2E/x",
	} {
		req, err := http.NewRequest(http.MethodGet, f.gateway.URL+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400", path, resp.StatusCode)
		}
	}
	select {
	case <-f.seen:
		t.Fatal("a non-canonical path reached the upstream")
	default:
	}
}

func TestGatewayForwardsLegalEntitySelectionOnlyWhenTheTokenHoldsIt(t *testing.T) {
	f := newFixture(t)
	own := "00000000-0000-0000-0000-0000000000e1"
	ok := f.get(t, "/api/v1/party/parties", f.token(t, adminUser, nil), map[string]string{"X-Passage-LegalEntity": own, "X-Passage-LegalEntity-Scope": "all"})
	if ok.StatusCode != http.StatusNoContent {
		t.Fatalf("own legal entity: status %d, want 204", ok.StatusCode)
	}
	seen := <-f.seen
	if seen.Get("X-Passage-LegalEntity") != own {
		t.Errorf("selection = %q, want %q", seen.Get("X-Passage-LegalEntity"), own)
	}
	if seen.Get("X-Passage-LegalEntity-Scope") != "all" {
		t.Errorf("scope = %q: like the Envoy gateway, the scope request passes through for the service to gate on org.switcher.all-scope", seen.Get("X-Passage-LegalEntity-Scope"))
	}
	other := f.get(t, "/api/v1/party/parties", f.token(t, adminUser, nil), map[string]string{"X-Passage-LegalEntity": "00000000-0000-0000-0000-0000000000e2"})
	if other.StatusCode != http.StatusForbidden {
		t.Fatalf("foreign legal entity: status %d, want 403", other.StatusCode)
	}
}

func TestDevGuardsRefuseNonLoopbackUnlessOverridden(t *testing.T) {
	base := map[string]string{
		"DEVGATEWAY_ISSUER": testIssuer, "DEVGATEWAY_JWKS_URL": "http://127.0.0.1:1/jwks", "DEVGATEWAY_TENANT_ID": testTenant, "DEVGATEWAY_OPA_URL": "http://127.0.0.1:2",
	}
	build := func(extra map[string]string, upstream string) error {
		env := map[string]string{}
		for k, v := range base {
			env[k] = v
		}
		for k, v := range extra {
			env[k] = v
		}
		_, err := buildHandler(func(k string) string { return env[k] }, []string{"PASSAGE_SVC_PARTY_URL=" + upstream})
		return err
	}
	if err := build(nil, "http://10.0.0.5:8080"); err == nil {
		t.Fatal("a non-loopback upstream was accepted")
	}
	if err := build(map[string]string{"DEVGATEWAY_ALLOW_NONLOCAL": "1"}, "http://10.0.0.5:8080"); err != nil {
		t.Fatalf("override refused: %v", err)
	}
	for addr, want := range map[string]bool{"127.0.0.1:8088": true, "localhost:8088": true, "[::1]:8088": true, ":8088": false, "0.0.0.0:8088": false, "10.0.0.5:8088": false} {
		if loopback(addr) != want {
			t.Errorf("loopback(%q) = %v, want %v", addr, !want, want)
		}
	}
}
