package api

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/shopfloor/internal/pki"
	"github.com/BenjaminSRussell/passage/services/shopfloor/internal/store"
	"github.com/BenjaminSRussell/passage/services/shopfloor/migrations"
)

func (f *fakeStore) IssueEnrollmentToken(_ context.Context, id, by string, now time.Time) (store.EnrollmentToken, error) {
	return store.EnrollmentToken{EdgeID: id, Token: "tok-for-" + by, ExpiresAt: now.Add(store.EnrollmentTTL)}, nil
}

func (f *fakeStore) Enroll(context.Context, string, string, pki.Issuer, time.Time) (store.EdgeCertificate, error) {
	return store.EdgeCertificate{}, httpx.NewProblem(http.StatusUnauthorized, store.CodeEnrollmentInvalid, "not valid")
}

func (f *fakeStore) RenewCertificate(_ context.Context, id, subject, _ string, _ pki.Issuer, _ time.Time) (store.EdgeCertificate, error) {
	f.edge().subject = subject
	return store.EdgeCertificate{EdgeID: id, Subject: subject}, nil
}

func csrFor(t *testing.T, cn string) (string, *ecdsa.PrivateKey) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{Subject: pkix.Name{CommonName: cn}}, key)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: der})), key
}

// fullRouter mounts the service as cmd/shopfloor does: the public redeem route on a root without the
// tenant middleware and everything else behind it.
func fullRouter(a *API) http.Handler {
	root := httpx.NewRouterWithoutTenant()
	a.RegisterPublic(root)
	inner := chi.NewRouter()
	inner.Use(tenant.Middleware)
	a.Register(inner)
	root.Mount("/", inner)
	return root
}

// Minting needs the configure duty and answers a token that is not cached; renewing needs the connect
// duty and runs as the caller's identity; a bad request is refused before the store.
func TestEnrollment_RoutesDutiesAndValidation(t *testing.T) {
	fs := &fakeStore{}
	h := newRouter(fs)
	mint := "/v1/shopfloor/edge-nodes/" + edgeID + ":enrollment-token"
	if rec := call(t, h, http.MethodPost, mint, "", DutyEdgeConnect); rec.Code != http.StatusForbidden {
		t.Fatalf("mint with the connect duty: %d", rec.Code)
	}
	rec := call(t, h, http.MethodPost, mint, "", DutyEdgeConfigure)
	if rec.Code != http.StatusCreated || rec.Header().Get("Cache-Control") != "no-store" || !strings.Contains(rec.Body.String(), `"token":"tok-for-supervisor-1"`) {
		t.Fatalf("mint: %d %s %v", rec.Code, rec.Body, rec.Header())
	}
	renew := "/v1/shopfloor/edge-nodes/" + edgeID + ":renew-certificate"
	if rec := call(t, h, http.MethodPost, renew, `{"csr":"x"}`, DutyEdgeConfigure); rec.Code != http.StatusForbidden {
		t.Fatalf("renew with the configure duty: %d", rec.Code)
	}
	if rec := call(t, h, http.MethodPost, renew, `{"csr":""}`, DutyEdgeConnect); rec.Code != http.StatusBadRequest {
		t.Fatalf("renew without a csr: %d", rec.Code)
	}
	if rec := call(t, h, http.MethodPost, renew, `{"csr":"x"}`, DutyEdgeConnect); rec.Code != http.StatusOK || fs.edge().subject != "supervisor-1" {
		t.Fatalf("renew: %d %s subject %q", rec.Code, rec.Body, fs.edge().subject)
	}
}

// The redeem route needs no identity, refuses a body without token or csr, and answers a token that
// names another node exactly as it answers a bad one.
func TestEnrollment_RedeemIsPublicAndRefusesMismatches(t *testing.T) {
	h := fullRouter(New(&fakeStore{}))
	path := "/v1/shopfloor/edge-nodes/" + edgeID + ":enroll"
	post := func(body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		h.ServeHTTP(rec, req)
		return rec
	}
	if rec := post(`{"token":"","csr":""}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("empty: %d %s", rec.Code, rec.Body)
	}
	other := testkit.TenantID(1) + "." + "0195f0a0-0000-7000-8000-00000000ffff" + "." + strings.Repeat("a", 43)
	for name, tok := range map[string]string{"garbage": "nope", "other node": other} {
		rec := post(`{"token":"` + tok + `","csr":"x"}`)
		if rec.Code != http.StatusUnauthorized || !strings.Contains(rec.Body.String(), store.CodeEnrollmentInvalid) {
			t.Fatalf("%s: %d %s", name, rec.Code, rec.Body)
		}
	}
	// Everything else still needs the tenant headers.
	rec := httptest.NewRecorder()
	fullRouter(New(&fakeStore{})).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/shopfloor/edge-nodes", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("tenant routes behind the public root: %d", rec.Code)
	}
}

// End to end over Postgres: a supervisor registers a node and mints a token, the connector redeems it
// once for a certificate whose subject the server chose and bound to the node, only that identity then
// pulls configuration and reports, a new token replaces the old, a renewal keeps the subject, and an
// expired, reused or switched-off enrollment is refused alike.
func TestHTTP_EdgeEnrollmentBindsTheCertificateIdentity(t *testing.T) {
	admin, app := testkit.DBWithApp(t)
	if err := dbx.Migrate(context.Background(), admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	caCert, caKey, err := pki.NewTestCA("Passage test edge CA", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	ca, err := pki.NewLocalCA(caCert, caKey)
	if err != nil {
		t.Fatal(err)
	}
	a := New(store.New(app)).WithIssuer(ca)
	now := time.Now().UTC()
	a.now = func() time.Time { return now }
	h := fullRouter(a)
	send := func(method, path, body, duties, user string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("X-Passage-Tenant", testkit.TenantID(1))
		req.Header.Set("X-Passage-User", user)
		req.Header.Set("X-Passage-Duties", duties)
		req.Header.Set("Idempotency-Key", "k-"+path+body)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	redeem := func(id, token, csr string) *httptest.ResponseRecorder {
		b, _ := json.Marshal(map[string]string{"token": token, "csr": csr})
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/shopfloor/edge-nodes/"+id+":enroll", strings.NewReader(string(b))))
		return rec
	}
	create := send(http.MethodPost, "/v1/shopfloor/edge-nodes", `{"name":"Press line gateway","platform":"linux"}`, DutyEdgeConfigure, "supervisor-1")
	var node store.EdgeNode
	_ = json.Unmarshal(create.Body.Bytes(), &node)
	if create.Code != http.StatusCreated || node.ConnectorSubject != nil {
		t.Fatalf("create: %d %s", create.Code, create.Body)
	}
	mint := func() store.EnrollmentToken {
		rec := send(http.MethodPost, "/v1/shopfloor/edge-nodes/"+node.ID+":enrollment-token", "", DutyEdgeConfigure, "supervisor-1")
		var tok store.EnrollmentToken
		_ = json.Unmarshal(rec.Body.Bytes(), &tok)
		if rec.Code != http.StatusCreated || tok.Token == "" || !tok.ExpiresAt.Equal(now.Add(store.EnrollmentTTL)) {
			t.Fatalf("mint: %d %s", rec.Code, rec.Body)
		}
		return tok
	}
	first, second := mint(), mint()
	csr, _ := csrFor(t, "evil-admin")

	// The first token was replaced by the second; the second works once.
	if rec := redeem(node.ID, first.Token, csr); rec.Code != http.StatusUnauthorized || !strings.Contains(rec.Body.String(), store.CodeEnrollmentInvalid) {
		t.Fatalf("replaced token: %d %s", rec.Code, rec.Body)
	}
	wrong := second.Token[:len(second.Token)-3] + "AAA"
	if rec := redeem(node.ID, wrong, csr); rec.Code != http.StatusUnauthorized {
		t.Fatalf("wrong secret: %d", rec.Code)
	}
	if rec := redeem(node.ID, second.Token, "not a csr"); rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), store.CodeCSRInvalid) {
		t.Fatalf("bad csr: %d %s", rec.Code, rec.Body)
	}
	rec := redeem(node.ID, second.Token, csr)
	var cert store.EdgeCertificate
	_ = json.Unmarshal(rec.Body.Bytes(), &cert)
	subject := "edge:" + node.ID
	if rec.Code != http.StatusOK || cert.Subject != subject || cert.Certificate == "" || cert.Serial == "" || !cert.NotAfter.After(now.Add(89*24*time.Hour)) {
		t.Fatalf("enroll: %d %s", rec.Code, rec.Body)
	}
	block, _ := pem.Decode([]byte(cert.Certificate))
	leaf, err := x509.ParseCertificate(block.Bytes)
	if err != nil || leaf.Subject.CommonName != subject {
		t.Fatalf("certificate subject = %v, %v (the request asked for evil-admin)", leaf, err)
	}
	if rec := redeem(node.ID, second.Token, csr); rec.Code != http.StatusUnauthorized {
		t.Fatalf("token used twice: %d", rec.Code)
	}

	// The node is bound to the certificate identity and shows its serial and expiry.
	got := send(http.MethodGet, "/v1/shopfloor/edge-nodes/"+node.ID, "", DutyView, "supervisor-1")
	var bound store.EdgeNode
	_ = json.Unmarshal(got.Body.Bytes(), &bound)
	if bound.ConnectorSubject == nil || *bound.ConnectorSubject != subject || bound.CertSerial == nil || *bound.CertSerial != cert.Serial || bound.CertExpiresAt == nil {
		t.Fatalf("node after enrollment = %s", got.Body)
	}
	beat := `{"bufferFillPct":"1.5","machines":[]}`
	if rec := send(http.MethodPost, "/v1/shopfloor/edge-nodes/"+node.ID+":heartbeat", beat, DutyEdgeConnect, "someone-else"); rec.Code != http.StatusForbidden {
		t.Fatalf("heartbeat as another identity: %d", rec.Code)
	}
	if rec := send(http.MethodPost, "/v1/shopfloor/edge-nodes/"+node.ID+":heartbeat", beat, DutyEdgeConnect, subject); rec.Code != http.StatusOK {
		t.Fatalf("heartbeat as the certificate identity: %d %s", rec.Code, rec.Body)
	}

	// Renewal: only the bound identity, same subject, new serial, an audit row each.
	csr2, _ := csrFor(t, "still-evil")
	renewPath := "/v1/shopfloor/edge-nodes/" + node.ID + ":renew-certificate"
	body, _ := json.Marshal(map[string]string{"csr": csr2})
	if rec := send(http.MethodPost, renewPath, string(body), DutyEdgeConnect, "someone-else"); rec.Code != http.StatusForbidden {
		t.Fatalf("renew as another identity: %d", rec.Code)
	}
	rec = send(http.MethodPost, renewPath, string(body), DutyEdgeConnect, subject)
	var renewed store.EdgeCertificate
	_ = json.Unmarshal(rec.Body.Bytes(), &renewed)
	if rec.Code != http.StatusOK || renewed.Subject != subject || renewed.Serial == cert.Serial {
		t.Fatalf("renew: %d %s", rec.Code, rec.Body)
	}
	var issued int
	if err := admin.QueryRow(context.Background(), `select count(*) from edge_certificate where edge_id = $1`, node.ID).Scan(&issued); err != nil || issued != 2 {
		t.Fatalf("certificate rows = %d, %v", issued, err)
	}

	// A token expires, and a switched-off node cannot be enrolled or given a token.
	third := mint()
	now = now.Add(store.EnrollmentTTL + time.Second)
	if rec := redeem(node.ID, third.Token, csr); rec.Code != http.StatusUnauthorized {
		t.Fatalf("expired token: %d", rec.Code)
	}
	now = now.Add(-store.EnrollmentTTL)
	fourth := mint()
	cur := send(http.MethodGet, "/v1/shopfloor/edge-nodes/"+node.ID, "", DutyView, "supervisor-1")
	_ = json.Unmarshal(cur.Body.Bytes(), &bound)
	reqUpd := httptest.NewRequest(http.MethodPut, "/v1/shopfloor/edge-nodes/"+node.ID, strings.NewReader(`{"name":"Press line gateway","platform":"linux","ring":"all","active":false,"connectorSubject":"`+subject+`"}`))
	reqUpd.Header.Set("X-Passage-Tenant", testkit.TenantID(1))
	reqUpd.Header.Set("X-Passage-User", "supervisor-1")
	reqUpd.Header.Set("X-Passage-Duties", DutyEdgeConfigure)
	reqUpd.Header.Set("If-Match", etag(bound.Version))
	recUpd := httptest.NewRecorder()
	h.ServeHTTP(recUpd, reqUpd)
	if recUpd.Code != http.StatusOK {
		t.Fatalf("switch off: %d %s", recUpd.Code, recUpd.Body)
	}
	if rec := redeem(node.ID, fourth.Token, csr); rec.Code != http.StatusUnauthorized {
		t.Fatalf("token for a switched-off node: %d", rec.Code)
	}
	if rec := send(http.MethodPost, "/v1/shopfloor/edge-nodes/"+node.ID+":enrollment-token", "", DutyEdgeConfigure, "supervisor-1"); rec.Code != http.StatusConflict {
		t.Fatalf("mint for a switched-off node: %d", rec.Code)
	}
}

// Without an issuer both routes answer SF-0173 and nothing else is affected.
func TestHTTP_EnrollmentWithoutAnIssuerIsRefused(t *testing.T) {
	admin, app := testkit.DBWithApp(t)
	if err := dbx.Migrate(context.Background(), admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	h := fullRouter(New(store.New(app)))
	csr, _ := csrFor(t, "x")
	b, _ := json.Marshal(map[string]string{"token": testkit.TenantID(1) + "." + edgeID + "." + strings.Repeat("a", 43), "csr": csr})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/shopfloor/edge-nodes/"+edgeID+":enroll", strings.NewReader(string(b))))
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), store.CodeIssuerMissing) {
		t.Fatalf("redeem without an issuer: %d %s", rec.Code, rec.Body)
	}
}
