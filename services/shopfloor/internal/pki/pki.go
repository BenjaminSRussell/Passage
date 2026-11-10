// Package pki issues the mTLS client certificates of iot-edge connectors (FR-25.19, BR-25.14). The
// shopfloor service decides who may have one (a one-time enrollment token, or the connector's current
// identity for a renewal); this package turns a certificate signing request into a certificate for the
// identity the server chooses, never the one the request asks for.
//
// LocalCA signs with a CA key held in files. It serves dev and the demo plant. Production issuing is the
// platform certificate issuer (cert-manager) behind the same Issuer interface, which is not built.
package pki

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"os"
	"time"
)

// CertLifetime is how long an edge client certificate lasts (BR-25.14).
const CertLifetime = 90 * 24 * time.Hour

// ErrBadCSR wraps every reason a signing request is refused.
var ErrBadCSR = errors.New("invalid certificate signing request")

// Issued is a certificate the issuer signed.
type Issued struct {
	CertPEM   string // the leaf
	ChainPEM  string // the issuing CA certificate(s), for the connector's trust store
	Serial    string // lower-case hex
	NotBefore time.Time
	NotAfter  time.Time
}

// Issuer signs a client certificate for subject from a parsed, verified request.
type Issuer interface {
	Issue(csr *x509.CertificateRequest, subject string, now time.Time) (Issued, error)
}

// ParseCSR reads a PEM certificate request, checks its signature (proof the requester holds the
// key) and refuses weak keys: ECDSA P-256 or stronger, RSA 2048 or more.
func ParseCSR(pemText string) (*x509.CertificateRequest, error) {
	block, _ := pem.Decode([]byte(pemText))
	if block == nil || block.Type != "CERTIFICATE REQUEST" {
		return nil, fmt.Errorf("%w: expected a PEM CERTIFICATE REQUEST", ErrBadCSR)
	}
	csr, err := x509.ParseCertificateRequest(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrBadCSR, err)
	}
	if err := csr.CheckSignature(); err != nil {
		return nil, fmt.Errorf("%w: signature does not match the key", ErrBadCSR)
	}
	switch k := csr.PublicKey.(type) {
	case *ecdsa.PublicKey:
		if k.Curve.Params().BitSize < 256 {
			return nil, fmt.Errorf("%w: ECDSA key must be P-256 or stronger", ErrBadCSR)
		}
	case *rsa.PublicKey:
		if k.N.BitLen() < 2048 {
			return nil, fmt.Errorf("%w: RSA key must be at least 2048 bits", ErrBadCSR)
		}
	default:
		return nil, fmt.Errorf("%w: key type is not supported", ErrBadCSR)
	}
	return csr, nil
}

// LocalCA is an Issuer with its CA certificate and key in memory.
type LocalCA struct {
	cert *x509.Certificate
	key  crypto.Signer
	pem  string
}

// LoadLocalCA reads a PEM CA certificate and its PEM private key (PKCS#8 or EC).
func LoadLocalCA(certFile, keyFile string) (*LocalCA, error) {
	certPEM, err := os.ReadFile(certFile)
	if err != nil {
		return nil, err
	}
	keyPEM, err := os.ReadFile(keyFile)
	if err != nil {
		return nil, err
	}
	return NewLocalCA(certPEM, keyPEM)
}

// NewLocalCA parses a CA certificate and key.
func NewLocalCA(certPEM, keyPEM []byte) (*LocalCA, error) {
	cb, _ := pem.Decode(certPEM)
	if cb == nil {
		return nil, errors.New("CA certificate is not PEM")
	}
	cert, err := x509.ParseCertificate(cb.Bytes)
	if err != nil {
		return nil, fmt.Errorf("CA certificate: %w", err)
	}
	if !cert.IsCA {
		return nil, errors.New("CA certificate is not a CA")
	}
	kb, _ := pem.Decode(keyPEM)
	if kb == nil {
		return nil, errors.New("CA key is not PEM")
	}
	var key any
	if key, err = x509.ParsePKCS8PrivateKey(kb.Bytes); err != nil {
		if key, err = x509.ParseECPrivateKey(kb.Bytes); err != nil {
			return nil, errors.New("CA key is neither PKCS#8 nor EC")
		}
	}
	signer, ok := key.(crypto.Signer)
	if !ok {
		return nil, errors.New("CA key cannot sign")
	}
	return &LocalCA{cert: cert, key: signer, pem: string(certPEM)}, nil
}

// NewTestCA makes a fresh CA, for tests and the demo plant: it returns the PEM certificate and key.
func NewTestCA(name string, now time.Time) (certPEM, keyPEM []byte, err error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	tpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: name},
		NotBefore: now.Add(-time.Hour), NotAfter: now.Add(10 * 365 * 24 * time.Hour), IsCA: true, BasicConstraintsValid: true,
		KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageCRLSign}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		return nil, nil, err
	}
	kd, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: kd}), nil
}

// Issue signs a client-auth certificate whose common name is subject and which lasts CertLifetime. The
// request's own subject and names are ignored: the identity is what the server bound to the node.
func (ca *LocalCA) Issue(csr *x509.CertificateRequest, subject string, now time.Time) (Issued, error) {
	if subject == "" {
		return Issued{}, errors.New("subject is required")
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
	if err != nil {
		return Issued{}, err
	}
	notBefore, notAfter := now.Add(-5*time.Minute).UTC(), now.Add(CertLifetime).UTC()
	if notAfter.After(ca.cert.NotAfter) {
		notAfter = ca.cert.NotAfter
	}
	tpl := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: subject}, NotBefore: notBefore, NotAfter: notAfter,
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}, BasicConstraintsValid: true}
	der, err := x509.CreateCertificate(rand.Reader, tpl, ca.cert, csr.PublicKey, ca.key)
	if err != nil {
		return Issued{}, err
	}
	return Issued{CertPEM: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})), ChainPEM: ca.pem,
		Serial: hex.EncodeToString(serial.Bytes()), NotBefore: notBefore, NotAfter: notAfter}, nil
}
