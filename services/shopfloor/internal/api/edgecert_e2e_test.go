package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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

// gatewayRouter is the service as cmd/shopfloor mounts it: the public enrollment route, then the certificate identity
// middleware in front of the tenant middleware, with the real store as the lookup.
func gatewayRouter(a *API, st *store.Postgres) http.Handler {
	root := httpx.NewRouterWithoutTenant()
	a.RegisterPublic(root)
	inner := chi.NewRouter()
	inner.Use(EdgeCertIdentity(st), tenant.Middleware)
	a.Register(inner)
	root.Mount("/", inner)
	return root
}

func fingerprintOf(t *testing.T, certPEM string) string {
	t.Helper()
	blk, _ := pem.Decode([]byte(certPEM))
	if blk == nil {
		t.Fatal("certificate is not PEM")
	}
	sum := sha256.Sum256(blk.Bytes)
	return hex.EncodeToString(sum[:])
}

// The connector reaches the service only with a certificate the node was issued and has not revoked, as the gateway
// forwards it (subject and hash in X-Forwarded-Client-Cert): no hash, an unknown hash and a revoked certificate get no
// identity, and a revoked certificate cannot renew. A certificate from before fingerprints were kept stops working
// once the node has a fingerprinted one, and revoking one revokes them all.
func TestHTTP_EdgeCertificateAllowlistThroughTheGateway(t *testing.T) {
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
	st := store.New(app)
	a := New(st).WithIssuer(ca)
	h := gatewayRouter(a, st)
	supervisor := func(method, path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("X-Passage-Tenant", testkit.TenantID(1))
		req.Header.Set("X-Passage-User", "supervisor-1")
		req.Header.Set("X-Passage-Duties", DutyEdgeConfigure+","+DutyView)
		req.Header.Set("Idempotency-Key", "k-"+method+path+body)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	newNode := func(name string) (store.EdgeNode, store.EdgeCertificate) {
		t.Helper()
		var node store.EdgeNode
		rec := supervisor(http.MethodPost, "/v1/shopfloor/edge-nodes", `{"name":"`+name+`","platform":"linux"}`)
		_ = json.Unmarshal(rec.Body.Bytes(), &node)
		var tok store.EnrollmentToken
		rec = supervisor(http.MethodPost, "/v1/shopfloor/edge-nodes/"+node.ID+":enrollment-token", "")
		_ = json.Unmarshal(rec.Body.Bytes(), &tok)
		csr, _ := csrFor(t, "connector")
		b, _ := json.Marshal(map[string]string{"token": tok.Token, "csr": csr})
		rec = httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/shopfloor/edge-nodes/"+node.ID+":enroll", strings.NewReader(string(b))))
		var cert store.EdgeCertificate
		if err := json.Unmarshal(rec.Body.Bytes(), &cert); err != nil || rec.Code != http.StatusOK {
			t.Fatalf("enroll %s: %d %s", name, rec.Code, rec.Body)
		}
		return node, cert
	}
	// connector calls as the gateway forwards a client certificate: xfcc "" sends no header.
	connector := func(method, path, body, xfcc string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		if xfcc != "" {
			req.Header.Set("X-Forwarded-Client-Cert", xfcc)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	xfcc := func(nodeID, hash string) string {
		v := `By=spiffe://cluster/envoy;`
		if hash != "" {
			v += `Hash=` + hash + `;`
		}
		return v + `Subject="CN=edge:` + nodeID + `"`
	}
	beat := `{"bufferFillPct":"1","machines":[]}`
	heartbeat := func(nodeID, hash string) int {
		return connector(http.MethodPost, "/v1/shopfloor/edge-nodes/"+nodeID+":heartbeat", beat, xfcc(nodeID, hash)).Code
	}

	node, first := newNode("Press line gateway")
	fp1 := fingerprintOf(t, first.Certificate)
	if code := heartbeat(node.ID, fp1); code != http.StatusOK {
		t.Fatalf("heartbeat with the enrolled certificate: %d", code)
	}
	for name, hash := range map[string]string{"no hash": "", "an unknown hash": strings.Repeat("ab", 32)} {
		if code := heartbeat(node.ID, hash); code == http.StatusOK {
			t.Errorf("%s got through: %d", name, code)
		}
	}

	// Renew with the first certificate; both work until the first is revoked, then only the new one does, and the
	// revoked one can no longer renew.
	csr, _ := csrFor(t, "connector")
	body, _ := json.Marshal(map[string]string{"csr": csr})
	renewPath := "/v1/shopfloor/edge-nodes/" + node.ID + ":renew-certificate"
	rec := connector(http.MethodPost, renewPath, string(body), xfcc(node.ID, fp1))
	var second store.EdgeCertificate
	if err := json.Unmarshal(rec.Body.Bytes(), &second); err != nil || rec.Code != http.StatusOK {
		t.Fatalf("renew: %d %s", rec.Code, rec.Body)
	}
	fp2 := fingerprintOf(t, second.Certificate)
	if heartbeat(node.ID, fp1) != http.StatusOK || heartbeat(node.ID, fp2) != http.StatusOK {
		t.Fatal("both certificates before the revocation")
	}
	if rec := supervisor(http.MethodPost, "/v1/shopfloor/edge-nodes/"+node.ID+"/certificates/"+first.Serial+":revoke", `{"reason":"laptop stolen"}`); rec.Code != http.StatusOK {
		t.Fatalf("revoke: %d %s", rec.Code, rec.Body)
	}
	if heartbeat(node.ID, fp1) == http.StatusOK || heartbeat(node.ID, fp2) != http.StatusOK {
		t.Fatal("after revoking the first certificate")
	}
	if rec := connector(http.MethodPost, renewPath, string(body), xfcc(node.ID, fp1)); rec.Code == http.StatusOK {
		t.Fatalf("a revoked certificate renewed: %d", rec.Code)
	}

	// A node enrolled before fingerprints were kept (simulated by clearing them): any certificate the gateway accepted
	// passes, revoking an old one revokes every unfingerprinted one and unbinds the node.
	legacy, old := newNode("Mixer gateway")
	if _, err := admin.Exec(context.Background(), `update edge_certificate set fingerprint = null where edge_id = $1`, legacy.ID); err != nil {
		t.Fatal(err)
	}
	stolen := strings.Repeat("cd", 32)
	if code := heartbeat(legacy.ID, stolen); code != http.StatusOK {
		t.Fatalf("a legacy node's certificate: %d", code)
	}
	rec = supervisor(http.MethodPost, "/v1/shopfloor/edge-nodes/"+legacy.ID+"/certificates/"+old.Serial+":revoke", `{"reason":"stolen"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("revoke a legacy certificate: %d %s", rec.Code, rec.Body)
	}
	if code := heartbeat(legacy.ID, stolen); code == http.StatusOK {
		t.Fatal("a revoked legacy certificate still works")
	}

	// Another legacy node renews: from then on only its fingerprinted certificate works.
	legacy2, _ := newNode("Filler gateway")
	if _, err := admin.Exec(context.Background(), `update edge_certificate set fingerprint = null where edge_id = $1`, legacy2.ID); err != nil {
		t.Fatal(err)
	}
	rec = connector(http.MethodPost, "/v1/shopfloor/edge-nodes/"+legacy2.ID+":renew-certificate", string(body), xfcc(legacy2.ID, stolen))
	var fresh store.EdgeCertificate
	if err := json.Unmarshal(rec.Body.Bytes(), &fresh); err != nil || rec.Code != http.StatusOK {
		t.Fatalf("legacy renew: %d %s", rec.Code, rec.Body)
	}
	if heartbeat(legacy2.ID, stolen) == http.StatusOK || heartbeat(legacy2.ID, fingerprintOf(t, fresh.Certificate)) != http.StatusOK {
		t.Fatal("after a legacy node got a fingerprinted certificate")
	}
}
