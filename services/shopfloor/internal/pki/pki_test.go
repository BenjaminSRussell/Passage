package pki

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"testing"
	"time"
)

var now = time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC)

func csrPEM(t *testing.T, key any, cn string) string {
	t.Helper()
	der, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{Subject: pkix.Name{CommonName: cn}, DNSNames: []string{"evil.example"}}, key)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: der}))
}

// The certificate carries the subject the server chose and 90 days of life, never the request's names,
// verifies against the CA for client auth, and holds the requester's key.
func TestLocalCA_IssuesForTheServersSubject(t *testing.T) {
	cp, kp, err := NewTestCA("Passage edge CA", now)
	if err != nil {
		t.Fatal(err)
	}
	ca, err := NewLocalCA(cp, kp)
	if err != nil {
		t.Fatal(err)
	}
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	csr, err := ParseCSR(csrPEM(t, key, "i-am-the-admin"))
	if err != nil {
		t.Fatal(err)
	}
	out, err := ca.Issue(csr, "edge:0195f0a0-0000-7000-8000-0000000000a1", now)
	if err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode([]byte(out.CertPEM))
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	if cert.Subject.CommonName != "edge:0195f0a0-0000-7000-8000-0000000000a1" || len(cert.DNSNames) != 0 {
		t.Fatalf("subject = %v names = %v", cert.Subject, cert.DNSNames)
	}
	if got := cert.NotAfter.Sub(now); got != CertLifetime || !cert.NotAfter.Equal(out.NotAfter) {
		t.Fatalf("lifetime = %v", got)
	}
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM([]byte(out.ChainPEM))
	if _, err := cert.Verify(x509.VerifyOptions{Roots: pool, CurrentTime: now.Add(24 * time.Hour), KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}); err != nil {
		t.Fatalf("does not verify: %v", err)
	}
	if !cert.PublicKey.(*ecdsa.PublicKey).Equal(&key.PublicKey) || out.Serial == "" {
		t.Fatal("certificate does not carry the requester's key")
	}
	if _, err := cert.Verify(x509.VerifyOptions{Roots: pool, CurrentTime: now.Add(91 * 24 * time.Hour), KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}); err == nil {
		t.Fatal("certificate still valid after 90 days")
	}
}

func TestParseCSR_RefusesWeakAndBrokenRequests(t *testing.T) {
	weak, _ := rsa.GenerateKey(rand.Reader, 1024)
	p224, _ := ecdsa.GenerateKey(elliptic.P224(), rand.Reader)
	for name, in := range map[string]string{"not pem": "hello", "rsa 1024": csrPEM(t, weak, "x"), "p224": csrPEM(t, p224, "x")} {
		if _, err := ParseCSR(in); !errors.Is(err, ErrBadCSR) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	good, _ := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	if _, err := ParseCSR(csrPEM(t, good, "x")); err != nil {
		t.Errorf("P-384 refused: %v", err)
	}
	// A request whose signature was changed is refused.
	block, _ := pem.Decode([]byte(csrPEM(t, good, "x")))
	block.Bytes[len(block.Bytes)-3] ^= 0xff
	if _, err := ParseCSR(string(pem.EncodeToMemory(block))); !errors.Is(err, ErrBadCSR) {
		t.Errorf("tampered request: %v", err)
	}
}

func TestNewLocalCA_RefusesBadInput(t *testing.T) {
	if _, err := NewLocalCA([]byte("x"), []byte("y")); err == nil {
		t.Error("garbage accepted")
	}
	cp, kp, _ := NewTestCA("ca", now)
	if _, err := NewLocalCA(cp, []byte("y")); err == nil {
		t.Error("bad key accepted")
	}
	if _, err := NewLocalCA(cp, kp); err != nil {
		t.Error(err)
	}
}
