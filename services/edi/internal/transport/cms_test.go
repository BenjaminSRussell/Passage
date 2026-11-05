package transport

import (
	"bytes"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"mime"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

var testAt = time.Date(2026, 11, 5, 9, 0, 0, 0, time.UTC)

type keyPair struct {
	cert *x509.Certificate
	key  crypto.Signer
}

func newKeyPair(t *testing.T, name string, ec bool) keyPair {
	t.Helper()
	var key crypto.Signer
	var err error
	if ec {
		key, err = ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	} else {
		key, err = rsa.GenerateKey(rand.Reader, 2048)
	}
	if err != nil {
		t.Fatal(err)
	}
	serial, _ := rand.Int(rand.Reader, big.NewInt(1<<62))
	tmpl := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: name}, NotBefore: testAt.Add(-time.Hour), NotAfter: testAt.Add(24 * 365 * time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageEmailProtection}}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, key.Public(), key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return keyPair{cert: cert, key: key}
}

func TestCMS_SignVerifyRSAAndECDSA(t *testing.T) {
	content := []byte("Content-Type: application/edi-x12\r\n\r\nISA*00*...~")
	for _, ec := range []bool{false, true} {
		kp := newKeyPair(t, "us", ec)
		sig, err := SignDetached(content, kp.cert, kp.key, testAt)
		if err != nil {
			t.Fatal(err)
		}
		h, err := VerifyDetached(sig, content, kp.cert)
		if err != nil || h != crypto.SHA256 {
			t.Fatalf("ec=%v: %v %v", ec, h, err)
		}
		if _, err := VerifyDetached(sig, append(content, '!'), kp.cert); !errors.Is(err, ErrSignature) {
			t.Fatalf("tampered content verified: %v", err)
		}
		other := newKeyPair(t, "us", ec)
		if _, err := VerifyDetached(sig, content, other.cert); !errors.Is(err, ErrSignature) {
			t.Fatalf("a certificate that did not sign must not verify: %v", err)
		}
		bad := bytes.Clone(sig)
		bad[len(bad)-3] ^= 0xff
		if _, err := VerifyDetached(bad, content, kp.cert); !errors.Is(err, ErrSignature) {
			t.Fatalf("a corrupted signature verified: %v", err)
		}
	}
	if _, err := VerifyDetached([]byte("not der"), content, newKeyPair(t, "x", true).cert); !errors.Is(err, ErrSignature) {
		t.Fatal(err)
	}
}

func writePEM(t *testing.T, dir, name, typ string, der []byte) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, pem.EncodeToMemory(&pem.Block{Type: typ, Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// TestCMS_InteropWithOpenSSL checks our signature with `openssl cms -verify`
// and an OpenSSL S/MIME signature with VerifyDetached, so the format is not
// only self-consistent. Skipped where openssl is not installed.
func TestCMS_InteropWithOpenSSL(t *testing.T) {
	bin, err := exec.LookPath("openssl")
	if err != nil {
		t.Skip("openssl not installed")
	}
	dir := t.TempDir()
	kp := newKeyPair(t, "partner", false)
	certPath := writePEM(t, dir, "cert.pem", "CERTIFICATE", kp.cert.Raw)
	keyDER, _ := x509.MarshalPKCS8PrivateKey(kp.key)
	keyPath := writePEM(t, dir, "key.pem", "PRIVATE KEY", keyDER)
	content := []byte("Content-Type: application/edifact\r\nContent-Transfer-Encoding: binary\r\n\r\nUNB+UNOC:3+A+B+261105:0900+1'UNZ+0+1'")
	contentPath := filepath.Join(dir, "content")
	if err := os.WriteFile(contentPath, content, 0o600); err != nil {
		t.Fatal(err)
	}

	sig, err := SignDetached(content, kp.cert, kp.key, testAt)
	if err != nil {
		t.Fatal(err)
	}
	sigPath := filepath.Join(dir, "sig.der")
	if err := os.WriteFile(sigPath, sig, 0o600); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(bin, "cms", "-verify", "-binary", "-noverify", "-inform", "DER", "-in", sigPath, "-content", contentPath, "-out", os.DevNull).CombinedOutput(); err != nil {
		t.Fatalf("openssl rejected our signature: %v\n%s", err, out)
	}

	osig := filepath.Join(dir, "openssl.der")
	if out, err := exec.Command(bin, "cms", "-sign", "-binary", "-md", "sha256", "-signer", certPath, "-inkey", keyPath, "-outform", "DER", "-in", contentPath, "-out", osig).CombinedOutput(); err != nil {
		t.Fatalf("openssl sign: %v\n%s", err, out)
	}
	theirs, err := os.ReadFile(osig)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyDetached(theirs, content, kp.cert); err != nil {
		t.Fatalf("OpenSSL signature did not verify: %v", err)
	}

	// A whole S/MIME message signed by openssl splits and verifies as AS2 does.
	smime := filepath.Join(dir, "smime.txt")
	if out, err := exec.Command(bin, "smime", "-sign", "-binary", "-crlfeol", "-md", "sha256", "-signer", certPath, "-inkey", keyPath, "-in", contentPath, "-out", smime).CombinedOutput(); err != nil {
		t.Fatalf("openssl smime: %v\n%s", err, out)
	}
	msg, err := os.ReadFile(smime)
	if err != nil {
		t.Fatal(err)
	}
	hdr, body, err := parseEntity(msg)
	if err != nil {
		t.Fatal(err)
	}
	ct := hdr.Get("Content-Type")
	_, params, _ := mime.ParseMediaType(ct)
	signed, s, err := splitSigned(params["boundary"], body)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(signed, content) {
		t.Fatalf("signed part differs:\n%q\n%q", signed, content)
	}
	if _, err := VerifyDetached(s, signed, kp.cert); err != nil {
		t.Fatal(err)
	}
}
