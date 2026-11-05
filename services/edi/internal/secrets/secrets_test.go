package secrets

import (
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/crypto/ssh"

	"github.com/BenjaminSRussell/passage/services/edi/internal/transport/transporttest"
)

func TestDir_ReadsOnlyNamedFilesInsideTheDirectory(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "secrets")
	data := filepath.Join(dir, "..data")
	if err := os.MkdirAll(data, 0o700); err != nil {
		t.Fatal(err)
	}
	// Kubernetes layout: key -> ..data/key.
	if err := os.WriteFile(filepath.Join(data, "sftp-acme"), []byte("pw\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join("..data", "sftp-acme"), filepath.Join(dir, "sftp-acme")); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(root, "outside")
	if err := os.WriteFile(outside, []byte("nope"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "escape")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "subdir"), 0o700); err != nil {
		t.Fatal(err)
	}
	d := Dir(dir)
	if pw, err := Password(d, "sftp-acme"); err != nil || pw != "pw" {
		t.Fatalf("%q %v", pw, err)
	}
	for _, ref := range []string{"escape", "../outside", "..data", "subdir", "missing", "Upper", ""} {
		if _, err := d.Read(ref); !errors.Is(err, ErrNotFound) {
			t.Fatalf("%q: %v", ref, err)
		}
	}
}

func TestAS2IdentityAndSSHSigner(t *testing.T) {
	kp := transporttest.NewKeyPair(t, "PASSAGE", false)
	other := transporttest.NewKeyPair(t, "OTHER", false)
	keyDER, _ := x509.MarshalPKCS8PrivateKey(other.Key)
	mismatched := append(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}), kp.CertPEM()...)
	ec := transporttest.NewKeyPair(t, "EC", true)
	ecDER, _ := x509.MarshalECPrivateKey(ec.Key.(*ecdsa.PrivateKey))
	sec1 := append(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: ecDER}), ec.CertPEM()...)
	_, edKey, _ := ed25519.GenerateKey(rand.Reader)
	sshBlock, err := ssh.MarshalPrivateKey(edKey, "")
	if err != nil {
		t.Fatal(err)
	}
	m := Map{"as2": kp.PEM(t), "as2-ec": sec1, "cert-only": []byte(kp.CertPEM()), "mismatch": mismatched, "garbage": []byte("x"),
		"ssh": pem.EncodeToMemory(sshBlock), "empty": []byte("\n"), "two-lines": []byte("a\nb\n")}
	for _, ref := range []string{"as2", "as2-ec"} {
		if cert, key, err := AS2Identity(m, ref); err != nil || cert == nil || key == nil {
			t.Fatalf("%s: %v", ref, err)
		}
	}
	for _, ref := range []string{"cert-only", "mismatch", "garbage"} {
		if _, _, err := AS2Identity(m, ref); !errors.Is(err, ErrMalformed) {
			t.Fatalf("%s: %v", ref, err)
		}
	}
	if _, _, err := AS2Identity(m, "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	if s, err := SSHSigner(m, "ssh"); err != nil || s.PublicKey().Type() != ssh.KeyAlgoED25519 {
		t.Fatalf("%v", err)
	}
	if _, err := SSHSigner(m, "garbage"); !errors.Is(err, ErrMalformed) {
		t.Fatal(err)
	}
	for _, ref := range []string{"empty", "two-lines"} {
		if _, err := Password(m, ref); !errors.Is(err, ErrMalformed) {
			t.Fatalf("%s: %v", ref, err)
		}
	}
}
