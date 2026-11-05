package certtrust

import (
	"crypto/x509"
	"encoding/pem"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/services/edi/internal/transport/transporttest"
)

func writeFile(t *testing.T, path string, b []byte) {
	t.Helper()
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
}

func pemCert(c *x509.Certificate) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: c.Raw})
}

func TestVerify_ChainAndCRLsFailClosed(t *testing.T) {
	root := transporttest.NewCA(t, "Partner Root")
	inter := root.Intermediate(t, "Partner Issuing CA")
	leaf := inter.Issue(t, "DISTRIBUTOR")
	other := transporttest.NewCA(t, "Someone Else")
	dir := t.TempDir()
	roots := filepath.Join(dir, "roots.pem")
	writeFile(t, roots, pemCert(root.Cert))
	now := time.Now()

	// No roots: pinning only, everything is accepted.
	none, err := New("", "")
	if err != nil || none != nil || none.Verify(leaf.Cert, nil, now) != nil || none.Revocation() {
		t.Fatalf("no trust: %v %v", none, err)
	}
	if _, err := New("", dir); err == nil {
		t.Fatal("a CRL directory without roots")
	}
	if _, err := New(filepath.Join(dir, "missing.pem"), ""); err == nil {
		t.Fatal("missing roots file")
	}

	chain, err := New(roots, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := chain.Verify(leaf.Cert, []*x509.Certificate{inter.Cert}, now); err != nil {
		t.Fatalf("chained: %v", err)
	}
	if err := chain.Verify(leaf.Cert, nil, now); !errors.Is(err, ErrUntrusted) {
		t.Fatalf("without the intermediate: %v", err)
	}
	if err := chain.Verify(other.Issue(t, "X").Cert, nil, now); !errors.Is(err, ErrUntrusted) {
		t.Fatalf("another CA: %v", err)
	}
	if err := chain.Verify(transporttest.NewKeyPair(t, "SELF", false).Cert, nil, now); !errors.Is(err, ErrUntrusted) {
		t.Fatalf("self-signed: %v", err)
	}

	// With CRLs every certificate below the root needs a current CRL.
	crls := filepath.Join(dir, "crl")
	if err := os.Mkdir(crls, 0o700); err != nil {
		t.Fatal(err)
	}
	checker, err := New(roots, crls)
	if err != nil || !checker.Revocation() {
		t.Fatal(err)
	}
	ints := []*x509.Certificate{inter.Cert}
	if err := checker.Verify(leaf.Cert, ints, now); !errors.Is(err, ErrRevocationUnknown) {
		t.Fatalf("no CRL at all: %v", err)
	}
	writeFile(t, filepath.Join(crls, "root.crl"), root.CRL(t, 1, now.Add(-time.Hour), now.Add(time.Hour)))
	if err := checker.Verify(leaf.Cert, ints, now); !errors.Is(err, ErrRevocationUnknown) {
		t.Fatalf("no CRL from the intermediate: %v", err)
	}
	// An expired CRL, and one signed by another key under the same name, do not count.
	writeFile(t, filepath.Join(crls, "issuing-old.crl"), inter.CRL(t, 1, now.Add(-48*time.Hour), now.Add(-24*time.Hour)))
	forged := transporttest.CA{KeyPair: transporttest.KeyPair{Cert: inter.Cert, Key: other.Key}}
	writeFile(t, filepath.Join(crls, "issuing-forged.crl"), forged.CRL(t, 9, now.Add(-time.Hour), now.Add(time.Hour)))
	if err := checker.Verify(leaf.Cert, ints, now); !errors.Is(err, ErrRevocationUnknown) {
		t.Fatalf("expired or forged CRL: %v", err)
	}
	// A current CRL (PEM this time) is picked up without a new checker.
	writeFile(t, filepath.Join(crls, "issuing.pem"), pem.EncodeToMemory(&pem.Block{Type: "X509 CRL", Bytes: inter.CRL(t, 2, now.Add(-time.Hour), now.Add(time.Hour))}))
	if err := checker.Verify(leaf.Cert, ints, now); err != nil {
		t.Fatalf("current CRLs: %v", err)
	}
	// The newest CRL revokes the leaf.
	writeFile(t, filepath.Join(crls, "issuing-new.crl"), inter.CRL(t, 3, now.Add(-time.Minute), now.Add(time.Hour), leaf.Cert))
	if err := checker.Verify(leaf.Cert, ints, now); !errors.Is(err, ErrUntrusted) {
		t.Fatalf("revoked leaf: %v", err)
	}
	// A revoked intermediate refuses everything under it.
	if err := os.Remove(filepath.Join(crls, "issuing-new.crl")); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(crls, "root.crl"), root.CRL(t, 2, now.Add(-time.Minute), now.Add(time.Hour), inter.Cert))
	if err := checker.Verify(leaf.Cert, ints, now); !errors.Is(err, ErrUntrusted) {
		t.Fatalf("revoked intermediate: %v", err)
	}
	// A file that is not a CRL makes the status unknown (fail closed), as do
	// unreadable roots.
	writeFile(t, filepath.Join(crls, "junk.crl"), []byte("not a CRL"))
	if err := checker.Verify(leaf.Cert, ints, now); !errors.Is(err, ErrRevocationUnknown) {
		t.Fatalf("junk CRL: %v", err)
	}
	if err := os.Remove(filepath.Join(crls, "junk.crl")); err != nil {
		t.Fatal(err)
	}
	writeFile(t, roots, []byte("-----BEGIN PRIVATE KEY-----\nAA==\n-----END PRIVATE KEY-----\n"))
	if err := checker.Verify(leaf.Cert, ints, now); !errors.Is(err, ErrRevocationUnknown) {
		t.Fatalf("broken roots: %v", err)
	}
}
