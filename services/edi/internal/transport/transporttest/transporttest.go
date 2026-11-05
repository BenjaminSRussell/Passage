// Package transporttest holds in-process partner endpoints for tests: an
// SSH server with the sftp subsystem on the real file system, and
// self-signed AS2 certificates. Only tests import it.
package transporttest

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/subtle"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"testing"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

// NewSigner returns a fresh Ed25519 SSH key.
func NewSigner(t testing.TB) ssh.Signer {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	s, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// SFTPLogin is who may log in to the test server: a client public key, a
// password, or both. User, when set, must match too.
type SFTPLogin struct {
	User     string
	Key      ssh.PublicKey
	Password string
}

// StartSFTPServer runs an in-process SSH server with the sftp subsystem on the
// real file system until the test ends. It returns the address and the host
// key fingerprint (SHA256:...).
func StartSFTPServer(t testing.TB, login SFTPLogin) (string, string) {
	t.Helper()
	host := NewSigner(t)
	conf := &ssh.ServerConfig{}
	if login.Key != nil {
		conf.PublicKeyCallback = func(m ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			if (login.User == "" || m.User() == login.User) && string(key.Marshal()) == string(login.Key.Marshal()) {
				return nil, nil
			}
			return nil, os.ErrPermission
		}
	}
	if login.Password != "" {
		conf.PasswordCallback = func(m ssh.ConnMetadata, pw []byte) (*ssh.Permissions, error) {
			if (login.User == "" || m.User() == login.User) && subtle.ConstantTimeCompare(pw, []byte(login.Password)) == 1 {
				return nil, nil
			}
			return nil, os.ErrPermission
		}
	}
	conf.AddHostKey(host)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go serveSSH(conn, conf)
		}
	}()
	return ln.Addr().String(), ssh.FingerprintSHA256(host.PublicKey())
}

func serveSSH(conn net.Conn, conf *ssh.ServerConfig) {
	_, chans, reqs, err := ssh.NewServerConn(conn, conf)
	if err != nil {
		return
	}
	go ssh.DiscardRequests(reqs)
	for nc := range chans {
		ch, creqs, err := nc.Accept()
		if err != nil {
			continue
		}
		go func() {
			for r := range creqs {
				ok := r.Type == "subsystem" && len(r.Payload) > 4 && string(r.Payload[4:]) == "sftp"
				_ = r.Reply(ok, nil)
				if ok {
					if srv, err := sftp.NewServer(ch); err == nil {
						_ = srv.Serve()
						_ = srv.Close()
					}
				}
			}
		}()
	}
}

// KeyPair is a self-signed certificate and its key.
type KeyPair struct {
	Cert *x509.Certificate
	Key  crypto.Signer
}

// CertPEM is the certificate as PEM.
func (k KeyPair) CertPEM() string {
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: k.Cert.Raw}))
}

// PEM is the PKCS #8 key followed by the certificate, the layout of an AS2
// signing secret.
func (k KeyPair) PEM(t testing.TB) []byte {
	t.Helper()
	der, err := x509.MarshalPKCS8PrivateKey(k.Key)
	if err != nil {
		t.Fatal(err)
	}
	return append(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), k.CertPEM()...)
}

// NewKeyPair returns a self-signed RSA (or P-256 when ec) certificate for name
// valid from an hour before now for a year.
func NewKeyPair(t testing.TB, name string, ec bool) KeyPair {
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
	now := time.Now()
	tmpl := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: name}, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(24 * 365 * time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageEmailProtection}}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, key.Public(), key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return KeyPair{Cert: cert, Key: key}
}
