// Package certtrust checks partner AS2 certificates beyond the pin: the
// certificate must chain to a configured trust pool (EDI_AS2_TRUST_ROOTS, a
// PEM bundle of CA certificates) and, with a CRL directory configured
// (EDI_AS2_CRL_DIR), no certificate of the chain below its root may be
// revoked, judged by a current CRL its issuer signed. Both checks fail
// closed: no chain, a revoked certificate, or no current CRL from an issuer
// refuses the certificate. The files are read when they change, so a CRL
// refreshed in the mounted directory is used without a restart. Nothing here
// fetches from the network: CRL distribution points and OCSP responders are
// not contacted, the directory is filled by whoever operates the trust.
package certtrust

import (
	"bytes"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

var (
	// ErrUntrusted means the certificate does not chain to the trust pool or
	// is revoked: it stays refused until the partner certificate or the trust
	// changes.
	ErrUntrusted = errors.New("certtrust: certificate not trusted")
	// ErrRevocationUnknown means a certificate of the chain has no current
	// CRL from its issuer (missing, expired, or not signed by the issuer), or
	// the trust files cannot be read: refused until the CRL is refreshed.
	ErrRevocationUnknown = errors.New("certtrust: revocation status unknown")
)

// maxFileBytes caps one trust file (a CRL of a large CA stays well below).
const maxFileBytes = 16 << 20

// Checker verifies certificates against the configured roots and CRLs. A nil
// *Checker accepts every certificate (pinning only).
type Checker struct {
	rootsFile string
	crlDir    string

	mu    sync.Mutex
	stamp string
	roots *x509.CertPool
	crls  []*x509.RevocationList
}

// New returns a checker over the PEM bundle rootsFile and, when crlDir is
// set, the CRLs in that directory (DER or PEM "X509 CRL" files; others and
// dot files are skipped). It reads them once so a broken configuration
// fails at start. A CRL directory needs roots.
func New(rootsFile, crlDir string) (*Checker, error) {
	if rootsFile == "" {
		if crlDir != "" {
			return nil, errors.New("certtrust: a CRL directory needs trust roots")
		}
		return nil, nil
	}
	c := &Checker{rootsFile: rootsFile, crlDir: crlDir}
	if _, _, err := c.load(); err != nil {
		return nil, err
	}
	return c, nil
}

// Revocation reports whether CRLs are checked.
func (c *Checker) Revocation() bool { return c != nil && c.crlDir != "" }

// Verify checks leaf, with the intermediates the partner supplied, at time
// at: some chain to a trust root must verify, and with revocation checking
// every certificate of that chain below the root must be covered by a
// current CRL of its issuer that does not list it. Errors wrap ErrUntrusted
// or ErrRevocationUnknown.
func (c *Checker) Verify(leaf *x509.Certificate, intermediates []*x509.Certificate, at time.Time) error {
	if c == nil {
		return nil
	}
	roots, crls, err := c.load()
	if err != nil {
		return fmt.Errorf("%w: %v", ErrRevocationUnknown, err)
	}
	inter := x509.NewCertPool()
	for _, ic := range intermediates {
		inter.AddCert(ic)
	}
	chains, err := leaf.Verify(x509.VerifyOptions{Roots: roots, Intermediates: inter, CurrentTime: at, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageAny}})
	if err != nil {
		return fmt.Errorf("%w: %v", ErrUntrusted, err)
	}
	if c.crlDir == "" {
		return nil
	}
	var firstErr error
	for _, chain := range chains {
		err := checkChain(chain, crls, at)
		if err == nil {
			return nil
		}
		// A revoked certificate outweighs an unknown status on another chain.
		if firstErr == nil || errors.Is(err, ErrUntrusted) {
			firstErr = err
		}
	}
	return firstErr
}

func checkChain(chain []*x509.Certificate, crls []*x509.RevocationList, at time.Time) error {
	for i := 0; i+1 < len(chain); i++ {
		cert, issuer := chain[i], chain[i+1]
		crl := currentCRL(crls, issuer, at)
		if crl == nil {
			return fmt.Errorf("%w: no current CRL from %s for %s", ErrRevocationUnknown, name(issuer), name(cert))
		}
		for _, r := range crl.RevokedCertificateEntries {
			if r.SerialNumber != nil && r.SerialNumber.Cmp(cert.SerialNumber) == 0 && !r.RevocationTime.After(at) {
				return fmt.Errorf("%w: %s (serial %s) was revoked by %s on %s", ErrUntrusted, name(cert), cert.SerialNumber, name(issuer),
					r.RevocationTime.UTC().Format(time.RFC3339))
			}
		}
	}
	return nil
}

// currentCRL is the newest CRL issued and signed by issuer that is valid at
// at (thisUpdate not after it, nextUpdate set and after it).
func currentCRL(crls []*x509.RevocationList, issuer *x509.Certificate, at time.Time) *x509.RevocationList {
	var best *x509.RevocationList
	for _, crl := range crls {
		if !bytes.Equal(crl.RawIssuer, issuer.RawSubject) || crl.CheckSignatureFrom(issuer) != nil {
			continue
		}
		if crl.ThisUpdate.After(at) || crl.NextUpdate.IsZero() || !crl.NextUpdate.After(at) {
			continue
		}
		if best == nil || crl.ThisUpdate.After(best.ThisUpdate) {
			best = crl
		}
	}
	return best
}

func name(c *x509.Certificate) string {
	if c.Subject.CommonName != "" {
		return "CN=" + c.Subject.CommonName
	}
	return c.Subject.String()
}

// load returns the roots and CRLs, reading the files again when one changed
// (size or modification time).
func (c *Checker) load() (*x509.CertPool, []*x509.RevocationList, error) {
	files := []string{c.rootsFile}
	if c.crlDir != "" {
		entries, err := os.ReadDir(c.crlDir)
		if err != nil {
			return nil, nil, fmt.Errorf("CRL directory: %w", err)
		}
		for _, e := range entries {
			if e.IsDir() || strings.HasPrefix(e.Name(), ".") {
				continue
			}
			files = append(files, filepath.Join(c.crlDir, e.Name()))
		}
		sort.Strings(files[1:])
	}
	var stamp strings.Builder
	for _, f := range files {
		fi, err := os.Stat(f)
		if err != nil {
			return nil, nil, fmt.Errorf("trust file: %w", err)
		}
		fmt.Fprintf(&stamp, "%s|%d|%d;", f, fi.Size(), fi.ModTime().UnixNano())
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.roots != nil && c.stamp == stamp.String() {
		return c.roots, c.crls, nil
	}
	roots, err := readRoots(c.rootsFile)
	if err != nil {
		return nil, nil, err
	}
	var crls []*x509.RevocationList
	for _, f := range files[1:] {
		list, err := readCRLs(f)
		if err != nil {
			return nil, nil, err
		}
		crls = append(crls, list...)
	}
	c.stamp, c.roots, c.crls = stamp.String(), roots, crls
	return roots, crls, nil
}

func readFile(path string) ([]byte, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if len(b) > maxFileBytes {
		return nil, fmt.Errorf("%s is larger than %d bytes", filepath.Base(path), maxFileBytes)
	}
	return b, nil
}

func readRoots(path string) (*x509.CertPool, error) {
	b, err := readFile(path)
	if err != nil {
		return nil, fmt.Errorf("trust roots: %w", err)
	}
	pool := x509.NewCertPool()
	n := 0
	for rest := b; ; {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			break
		}
		if block.Type != "CERTIFICATE" {
			return nil, fmt.Errorf("trust roots: %s holds a %s block, not only certificates", filepath.Base(path), block.Type)
		}
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("trust roots: %w", err)
		}
		pool.AddCert(cert)
		n++
	}
	if n == 0 {
		return nil, fmt.Errorf("trust roots: %s holds no PEM certificate", filepath.Base(path))
	}
	return pool, nil
}

// readCRLs reads one CRL file: PEM with one or more X509 CRL blocks, or DER.
func readCRLs(path string) ([]*x509.RevocationList, error) {
	b, err := readFile(path)
	if err != nil {
		return nil, fmt.Errorf("CRL: %w", err)
	}
	if block, _ := pem.Decode(b); block == nil {
		crl, err := x509.ParseRevocationList(b)
		if err != nil {
			return nil, fmt.Errorf("CRL %s: %w", filepath.Base(path), err)
		}
		return []*x509.RevocationList{crl}, nil
	}
	var out []*x509.RevocationList
	for rest := b; ; {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			break
		}
		if block.Type != "X509 CRL" {
			return nil, fmt.Errorf("CRL %s holds a %s block", filepath.Base(path), block.Type)
		}
		crl, err := x509.ParseRevocationList(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("CRL %s: %w", filepath.Base(path), err)
		}
		out = append(out, crl)
	}
	return out, nil
}
