// Package secrets resolves the secret references of partner transports. The
// database stores only a reference (a file name); the material lives in a
// directory the deployment mounts read-only from a Kubernetes Secret that an
// ExternalSecret fills from Vault (EDI_SECRETS_DIR). Files are read at use
// time, so a rotated secret is picked up without a restart, and their content
// is never logged or returned.
package secrets

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/crypto/ssh"

	"github.com/BenjaminSRussell/passage/services/edi/internal/domain"
)

// maxSecretBytes caps one secret file.
const maxSecretBytes = 64 << 10

// ErrNotFound means the reference names no readable secret.
var ErrNotFound = errors.New("secrets: not found")

// ErrMalformed means the secret exists but is not what the reference is used for.
var ErrMalformed = errors.New("secrets: malformed secret")

// Store reads a secret by reference.
type Store interface {
	Read(ref string) ([]byte, error)
}

// Dir reads secrets from files in a directory. Kubernetes mounts each key of
// a Secret as a symlink into a ..data directory; the resolved file must stay
// inside the directory.
type Dir string

func (d Dir) Read(ref string) ([]byte, error) {
	if !domain.ValidSecretRef(ref) || d == "" {
		return nil, ErrNotFound
	}
	root, err := filepath.EvalSymlinks(string(d))
	if err != nil {
		return nil, ErrNotFound
	}
	p, err := filepath.EvalSymlinks(filepath.Join(root, ref))
	if err != nil {
		return nil, ErrNotFound
	}
	if rel, err := filepath.Rel(root, p); err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return nil, ErrNotFound
	}
	f, err := os.Open(p)
	if err != nil {
		return nil, ErrNotFound
	}
	defer func() { _ = f.Close() }()
	if fi, err := f.Stat(); err != nil || !fi.Mode().IsRegular() {
		return nil, ErrNotFound
	}
	b, err := io.ReadAll(io.LimitReader(f, maxSecretBytes+1))
	if err != nil {
		return nil, ErrNotFound
	}
	if len(b) > maxSecretBytes {
		return nil, ErrMalformed
	}
	return b, nil
}

// Map is an in-memory Store for tests.
type Map map[string][]byte

func (m Map) Read(ref string) ([]byte, error) {
	b, ok := m[ref]
	if !ok {
		return nil, ErrNotFound
	}
	return b, nil
}

// AS2Identity is the PEM secret behind an AS2 signing key reference: a
// private key (PKCS #8, PKCS #1 or SEC 1) and the certificate it belongs to.
func AS2Identity(s Store, ref string) (*x509.Certificate, crypto.Signer, error) {
	b, err := s.Read(ref)
	if err != nil {
		return nil, nil, err
	}
	var (
		cert *x509.Certificate
		key  crypto.Signer
	)
	for {
		var block *pem.Block
		block, b = pem.Decode(b)
		if block == nil {
			break
		}
		switch block.Type {
		case "CERTIFICATE":
			if cert == nil {
				if cert, err = x509.ParseCertificate(block.Bytes); err != nil {
					return nil, nil, fmt.Errorf("%w: %s: certificate does not parse", ErrMalformed, ref)
				}
			}
		case "PRIVATE KEY":
			k, err := x509.ParsePKCS8PrivateKey(block.Bytes)
			if err != nil {
				return nil, nil, fmt.Errorf("%w: %s: private key does not parse", ErrMalformed, ref)
			}
			signer, ok := k.(crypto.Signer)
			if !ok {
				return nil, nil, fmt.Errorf("%w: %s: unsupported key type", ErrMalformed, ref)
			}
			key = signer
		case "RSA PRIVATE KEY":
			k, err := x509.ParsePKCS1PrivateKey(block.Bytes)
			if err != nil {
				return nil, nil, fmt.Errorf("%w: %s: private key does not parse", ErrMalformed, ref)
			}
			key = k
		case "EC PRIVATE KEY":
			k, err := x509.ParseECPrivateKey(block.Bytes)
			if err != nil {
				return nil, nil, fmt.Errorf("%w: %s: private key does not parse", ErrMalformed, ref)
			}
			key = k
		}
	}
	if cert == nil || key == nil {
		return nil, nil, fmt.Errorf("%w: %s must hold a PEM private key and its certificate", ErrMalformed, ref)
	}
	if !samePublicKey(cert.PublicKey, key.Public()) {
		return nil, nil, fmt.Errorf("%w: %s: the certificate is not for the private key", ErrMalformed, ref)
	}
	return cert, key, nil
}

func samePublicKey(a, b crypto.PublicKey) bool {
	switch k := a.(type) {
	case *rsa.PublicKey:
		return k.Equal(b)
	case *ecdsa.PublicKey:
		return k.Equal(b)
	}
	return false
}

// SSHSigner is the private key (OpenSSH or PEM, unencrypted) behind an SFTP
// key reference.
func SSHSigner(s Store, ref string) (ssh.Signer, error) {
	b, err := s.Read(ref)
	if err != nil {
		return nil, err
	}
	signer, err := ssh.ParsePrivateKey(b)
	if err != nil {
		return nil, fmt.Errorf("%w: %s is not an unencrypted SSH private key", ErrMalformed, ref)
	}
	return signer, nil
}

// Password is the password behind an SFTP password reference, without a
// trailing line end.
func Password(s Store, ref string) (string, error) {
	b, err := s.Read(ref)
	if err != nil {
		return "", err
	}
	pw := strings.TrimRight(string(b), "\r\n")
	if pw == "" || strings.ContainsAny(pw, "\r\n") {
		return "", fmt.Errorf("%w: %s must hold one line", ErrMalformed, ref)
	}
	return pw, nil
}
