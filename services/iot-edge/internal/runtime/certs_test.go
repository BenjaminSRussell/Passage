package runtime

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/services/iot-edge/internal/domain"
)

// testCA signs client certificates like the shopfloor issuer: the subject is the server's choice and the
// request's own names are ignored. life sets the certificate lifetime.
type testCA struct {
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
	pem  string
	life time.Duration
}

func newTestCA(t *testing.T) *testCA {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test edge CA"}, NotBefore: time.Now().Add(-time.Hour),
		NotAfter: time.Now().Add(24 * time.Hour * 365), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, _ := x509.ParseCertificate(der)
	return &testCA{cert: cert, key: key, pem: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})), life: 90 * 24 * time.Hour}
}

func (ca *testCA) sign(t *testing.T, csrPEM, subject string) CertResponse {
	t.Helper()
	block, _ := pem.Decode([]byte(csrPEM))
	csr, err := x509.ParseCertificateRequest(block.Bytes)
	if err != nil || csr.CheckSignature() != nil {
		t.Fatalf("bad csr: %v", err)
	}
	serial, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 100))
	now := time.Now()
	tpl := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: subject}, NotBefore: now.Add(-time.Minute), NotAfter: now.Add(ca.life),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}
	der, err := x509.CreateCertificate(rand.Reader, tpl, ca.cert, csr.PublicKey, ca.key)
	if err != nil {
		t.Fatal(err)
	}
	return CertResponse{Subject: subject, Certificate: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})), CAChain: ca.pem,
		Serial: hex.EncodeToString(serial.Bytes()), NotAfter: tpl.NotAfter}
}

// plantServer is a TLS server that asks for a client certificate but lets the enroll call through
// without one, as the gateway does, and answers /protected only to a verified certificate.
func plantServer(t *testing.T, ca *testCA) (*httptest.Server, *[]string) {
	t.Helper()
	pool := x509.NewCertPool()
	pool.AddCert(ca.cert)
	var mu sync.Mutex
	var seen []string
	mux := http.NewServeMux()
	respond := func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			CSR string `json:"csr"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		_ = json.NewEncoder(w).Encode(ca.sign(t, in.CSR, "edge:"+r.PathValue("id")[:4]))
	}
	mux.HandleFunc("POST /v1/shopfloor/edge-nodes/{id}", func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, ":enroll"):
			respond(w, r)
		case strings.HasSuffix(r.URL.Path, ":renew-certificate"):
			if len(r.TLS.PeerCertificates) == 0 {
				http.Error(w, "no certificate", http.StatusUnauthorized)
				return
			}
			mu.Lock()
			seen = append(seen, r.TLS.PeerCertificates[0].Subject.CommonName)
			mu.Unlock()
			respond(w, r)
		default:
			http.NotFound(w, r)
		}
	})
	srv := httptest.NewUnstartedServer(mux)
	srv.TLS = &tls.Config{ClientAuth: tls.VerifyClientCertIfGiven, ClientCAs: pool, MinVersion: tls.VersionTLS12}
	srv.StartTLS()
	t.Cleanup(srv.Close)
	return srv, &seen
}

// Enrollment makes the key on the connector, installs the issued certificate, and from then on the
// connector presents it; a renewal over mTLS replaces it without a restart; a fresh store loads it back.
func TestEnroll_InstallsCertificateAndMTLSClientUsesIt(t *testing.T) {
	ca := newTestCA(t)
	srv, seen := plantServer(t, ca)
	dir := t.TempDir()
	store := &CertStore{Dir: dir}
	if err := store.Load(); err != nil || store.Has() {
		t.Fatalf("empty dir: has %v, err %v", store.Has(), err)
	}
	edge := "0195f0a0-0000-7000-8000-0000000000a1"
	got, err := Enroll(context.Background(), srv.Client(), srv.URL, edge, "token", store)
	if err != nil || !store.Has() || got.Subject != "edge:0195" || store.Serial() != got.Serial {
		t.Fatalf("enroll = %+v, %v (store serial %q)", got, err, store.Serial())
	}
	if info, err := os.Stat(filepath.Join(dir, keyFile)); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("key file: %v %v", info, err)
	}
	if time.Until(store.NotAfter()) < 89*24*time.Hour {
		t.Fatalf("expires %v", store.NotAfter())
	}

	roots := x509.NewCertPool()
	roots.AddCert(srv.Certificate())
	c := &Client{BaseURL: srv.URL, EdgeID: edge, HTTP: store.HTTPClient(roots)}
	if err := c.Renew(context.Background(), store); err != nil {
		t.Fatalf("renew: %v", err)
	}
	if len(*seen) != 1 || (*seen)[0] != "edge:0195" || store.Serial() == got.Serial {
		t.Fatalf("server saw %v, serial %q (was %q)", *seen, store.Serial(), got.Serial)
	}
	fresh := &CertStore{Dir: dir}
	if err := fresh.Load(); err != nil || fresh.Serial() != store.Serial() {
		t.Fatalf("reload: %v serial %q want %q", err, fresh.Serial(), store.Serial())
	}
	// Without a certificate the protected call is refused by the server, and the client says why.
	bare := &Client{BaseURL: srv.URL, EdgeID: edge, HTTP: (&CertStore{Dir: t.TempDir()}).HTTPClient(roots)}
	if err := bare.Renew(context.Background(), &CertStore{Dir: t.TempDir()}); err == nil {
		t.Fatal("renewal without a certificate succeeded")
	}
}

func TestEnroll_RefusedTokenInstallsNothing(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"code":"SF-0171","detail":"the enrollment token is not valid"}`))
	}))
	defer srv.Close()
	store := &CertStore{Dir: t.TempDir()}
	_, err := Enroll(context.Background(), srv.Client(), srv.URL, "edge", "bad", store)
	if err == nil || !strings.Contains(err.Error(), "SF-0171") || store.Has() {
		t.Fatalf("err = %v, has = %v", err, store.Has())
	}
}

// BR-25.14 on the installed certificate: ok, renew inside 30 days, alert after 7 days of failures, expired.
func TestCertStore_ActionFollowsBR2514(t *testing.T) {
	ca := newTestCA(t)
	ca.life = 40 * 24 * time.Hour
	keyPEM, csr, _ := NewKeyAndCSR("")
	issued := ca.sign(t, csr, "edge:x")
	store := &CertStore{Dir: t.TempDir()}
	if err := store.Install(issued.Certificate, issued.CAChain, keyPEM); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	for _, tc := range []struct {
		at   time.Time
		fail bool
		want domain.CertAction
	}{
		{now, false, domain.CertOK},
		{now.Add(11 * 24 * time.Hour), false, domain.CertRenew},
		{now.Add(20 * 24 * time.Hour), true, domain.CertRenew},
		{now.Add(41 * 24 * time.Hour), false, domain.CertExpired},
	} {
		if tc.fail {
			store.RenewFailed(tc.at.Add(-8 * 24 * time.Hour))
			tc.want = domain.CertAlert
		}
		if got := store.Action(tc.at); got != tc.want {
			t.Errorf("at +%v (failing %v): %s, want %s", tc.at.Sub(now).Round(time.Hour), tc.fail, got, tc.want)
		}
	}
	if err := store.Install(issued.Certificate, issued.CAChain, []byte("not a key")); err == nil {
		t.Error("a certificate that does not match the key was installed")
	}
}

// Beat renews when due, reports the serial and expiry, and a failed renewal is counted and retried; an
// expired certificate stops publishing but buffering goes on.
func TestBeat_RenewsDueCertificateAndExpiryStopsPublishing(t *testing.T) {
	ca := newTestCA(t)
	ca.life = 20 * 24 * time.Hour // inside the renewal window from the start
	keyPEM, csr, _ := NewKeyAndCSR("")
	first := ca.sign(t, csr, "edge:x")
	store := &CertStore{Dir: t.TempDir()}
	if err := store.Install(first.Certificate, first.CAChain, keyPEM); err != nil {
		t.Fatal(err)
	}
	ca.life = 90 * 24 * time.Hour
	var renewErr error
	renewals := 0
	srv := newServer()
	ctl := &fakeControl{}
	r, _ := simRuntime(t, srv, 1, Options{Control: ctl, Certs: store, Now: time.Now, Renew: func(context.Context) error {
		renewals++
		if renewErr != nil {
			return renewErr
		}
		k, c, _ := NewKeyAndCSR("")
		n := ca.sign(t, c, "edge:x")
		return store.Install(n.Certificate, n.CAChain, k)
	}})
	renewErr = errors.New("server unreachable")
	if err := r.Beat(context.Background()); err != nil || renewals != 1 || r.Stats().CertRenewFailures != 1 {
		t.Fatalf("failed renewal: %v renewals %d stats %+v", err, renewals, r.Stats())
	}
	if ctl.beats[0].CertSerial != first.Serial || ctl.beats[0].CertExpiresAt.IsZero() {
		t.Fatalf("heartbeat certificate = %q %v", ctl.beats[0].CertSerial, ctl.beats[0].CertExpiresAt)
	}
	renewErr = nil
	if err := r.Beat(context.Background()); err != nil || renewals != 2 || store.Serial() == first.Serial || r.Stats().CertRenewals != 1 {
		t.Fatalf("renewal: %v renewals %d serial %q", err, renewals, store.Serial())
	}
	if err := r.Beat(context.Background()); err != nil || renewals != 2 {
		t.Fatalf("renewed again inside the window it just left: renewals %d", renewals)
	}

	// An expired certificate: events keep buffering, nothing is sent.
	expired := &CertStore{Dir: t.TempDir()}
	ca.life = -time.Hour
	k, c, _ := NewKeyAndCSR("")
	old := ca.sign(t, c, "edge:x")
	if err := expired.Install(old.Certificate, old.CAChain, k); err != nil {
		t.Fatal(err)
	}
	srv2 := newServer()
	r2, _ := simRuntime(t, srv2, 1, Options{Certs: expired, Now: time.Now})
	r2.Ingest(Reading{Address: "sim/1/state", TS: time.Now(), Text: "RUNNING"})
	if _, err := r2.Flush(context.Background()); err == nil || srv2.batches != 0 || r2.Stats().BufferLen != 1 {
		t.Fatalf("flush with an expired certificate: %v batches %d stats %+v", err, srv2.batches, r2.Stats())
	}
}

func TestNewKeyAndCSRNamesTheBoundIdentity(t *testing.T) {
	_, csrPEM, err := NewKeyAndCSR("0195F0A0-0000-7000-8000-0000000000A1")
	if err != nil {
		t.Fatal(err)
	}
	blk, _ := pem.Decode([]byte(csrPEM))
	csr, err := x509.ParseCertificateRequest(blk.Bytes)
	if err != nil || csr.Subject.CommonName != "edge:0195f0a0-0000-7000-8000-0000000000a1" {
		t.Fatalf("subject %q, %v", csr.Subject.CommonName, err)
	}
}
