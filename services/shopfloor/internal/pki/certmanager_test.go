package pki

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

var cmNow = time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC)

// fakeKube serves the three calls the issuer makes against the Kubernetes API and plays cert-manager: the
// request is Pending for the first reads, then Ready with a certificate the test CA signed (keeping the
// request's common name, as cert-manager's CA issuer does) or a failure the test chose.
type fakeKube struct {
	t         *testing.T
	ca        *LocalCA
	caPEM     string
	mu        sync.Mutex
	csr       *x509.CertificateRequest
	reads     int
	readyAt   int    // reads before it is ready; -1 never
	deny      string // when set, the request is Denied with this message
	forceCN   string // when set, the issued certificate carries this common name
	deleted   bool
	status    int // when set, every call answers it
	sawToken  string
	sawIssuer map[string]any
}

func newFakeKube(t *testing.T) *fakeKube {
	t.Helper()
	certPEM, keyPEM, err := NewTestCA("edge-ca", cmNow)
	if err != nil {
		t.Fatal(err)
	}
	ca, err := NewLocalCA(certPEM, keyPEM)
	if err != nil {
		t.Fatal(err)
	}
	return &fakeKube{t: t, ca: ca, caPEM: string(certPEM), readyAt: 1}
}

func (k *fakeKube) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.sawToken = r.Header.Get("Authorization")
	if k.status != 0 {
		w.WriteHeader(k.status)
		return
	}
	base := "/apis/cert-manager.io/v1/namespaces/shopfloor/certificaterequests"
	switch {
	case r.Method == http.MethodPost && r.URL.Path == base:
		var in struct {
			Metadata map[string]any `json:"metadata"`
			Spec     struct {
				Request   string         `json:"request"`
				IssuerRef map[string]any `json:"issuerRef"`
				Usages    []string       `json:"usages"`
				Duration  string         `json:"duration"`
			} `json:"spec"`
		}
		b, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(b, &in); err != nil {
			k.t.Errorf("request body: %v", err)
		}
		k.sawIssuer = in.Spec.IssuerRef
		if in.Spec.Duration != "2160h" || len(in.Spec.Usages) != 2 {
			k.t.Errorf("spec: duration %q usages %v", in.Spec.Duration, in.Spec.Usages)
		}
		raw, _ := base64.StdEncoding.DecodeString(in.Spec.Request)
		blk, _ := pem.Decode(raw)
		csr, err := x509.ParseCertificateRequest(blk.Bytes)
		if err != nil {
			k.t.Errorf("csr: %v", err)
		}
		k.csr = csr
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{"metadata": map[string]any{"name": "edge-abc12"}})
	case r.Method == http.MethodGet && r.URL.Path == base+"/edge-abc12":
		k.reads++
		out := map[string]any{"metadata": map[string]any{"name": "edge-abc12"}}
		switch {
		case k.deny != "":
			out["status"] = map[string]any{"conditions": []map[string]any{{"type": "Denied", "status": "True", "message": k.deny}}}
		case k.readyAt >= 0 && k.reads > k.readyAt:
			cn := k.csr.Subject.CommonName
			if k.forceCN != "" {
				cn = k.forceCN
			}
			iss, err := k.ca.Issue(k.csr, cn, cmNow)
			if err != nil {
				k.t.Errorf("issue: %v", err)
			}
			out["status"] = map[string]any{"certificate": base64.StdEncoding.EncodeToString([]byte(iss.CertPEM)), "ca": base64.StdEncoding.EncodeToString([]byte(k.caPEM)),
				"conditions": []map[string]any{{"type": "Ready", "status": "True"}}}
		default:
			out["status"] = map[string]any{"conditions": []map[string]any{{"type": "Approved", "status": "True"}}}
		}
		_ = json.NewEncoder(w).Encode(out)
	case r.Method == http.MethodDelete && r.URL.Path == base+"/edge-abc12":
		k.deleted = true
		w.WriteHeader(http.StatusOK)
	default:
		k.t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusNotFound)
	}
}

func cmCSR(t *testing.T, cn string) *x509.CertificateRequest {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{Subject: pkix.Name{CommonName: cn}}, key)
	if err != nil {
		t.Fatal(err)
	}
	csr, err := ParseCSR(string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: der})))
	if err != nil {
		t.Fatal(err)
	}
	return csr
}

func (k *fakeKube) issuer(srv *httptest.Server) *CertManager {
	return &CertManager{APIServer: srv.URL, Namespace: "shopfloor", IssuerName: "edge-ca", PollEvery: time.Millisecond, Timeout: 2 * time.Second,
		Token: func() (string, error) { return "sa-token", nil }}
}

const edgeSubject = "edge:0195f0a0-0000-7000-8000-0000000000a1"

func TestCertManagerIssuesAndCleansUp(t *testing.T) {
	k := newFakeKube(t)
	srv := httptest.NewServer(k)
	defer srv.Close()
	got, err := k.issuer(srv).Issue(cmCSR(t, edgeSubject), edgeSubject, cmNow)
	if err != nil {
		t.Fatal(err)
	}
	blk, _ := pem.Decode([]byte(got.CertPEM))
	leaf, err := x509.ParseCertificate(blk.Bytes)
	if err != nil || leaf.Subject.CommonName != edgeSubject || got.Serial == "" || !got.NotAfter.After(got.NotBefore) {
		t.Fatalf("issued %+v leaf %v err %v", got, leaf, err)
	}
	if !strings.Contains(got.ChainPEM, "BEGIN CERTIFICATE") {
		t.Fatalf("chain %q", got.ChainPEM)
	}
	if !k.deleted || k.sawToken != "Bearer sa-token" || k.sawIssuer["name"] != "edge-ca" || k.sawIssuer["kind"] != "Issuer" || k.sawIssuer["group"] != "cert-manager.io" {
		t.Fatalf("deleted=%v token=%q issuer=%v", k.deleted, k.sawToken, k.sawIssuer)
	}
}

func TestCertManagerRefusesWhatItCannotTrust(t *testing.T) {
	for name, tc := range map[string]struct {
		setup func(*fakeKube)
		cn    string
		want  string
	}{
		"request names another identity": {func(*fakeKube) {}, "someone-else", "common name must be"},
		"cert-manager denies it":         {func(k *fakeKube) { k.deny = "issuer is not allowed" }, edgeSubject, "issuer is not allowed"},
		"cert-manager never answers":     {func(k *fakeKube) { k.readyAt = -1 }, edgeSubject, "did not issue"},
		"certificate for another name":   {func(k *fakeKube) { k.forceCN = "edge:other" }, edgeSubject, "not \"edge:0195"},
		"the API refuses":                {func(k *fakeKube) { k.status = http.StatusForbidden }, edgeSubject, "answered 403"},
	} {
		t.Run(name, func(t *testing.T) {
			k := newFakeKube(t)
			tc.setup(k)
			srv := httptest.NewServer(k)
			defer srv.Close()
			cm := k.issuer(srv)
			cm.Timeout = 150 * time.Millisecond
			_, err := cm.Issue(cmCSR(t, tc.cn), edgeSubject, cmNow)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v, want it to mention %q", err, tc.want)
			}
			if name == "cert-manager denies it" && !k.deleted {
				t.Fatal("a denied request was left behind")
			}
		})
	}
}
