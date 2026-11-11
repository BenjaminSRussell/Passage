package runtime

import (
	"bytes"
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
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/BenjaminSRussell/passage/services/iot-edge/internal/domain"
)

// File names inside a CertStore directory.
const (
	certFile = "client.crt"
	keyFile  = "client.key"
	caFile   = "ca.crt"
)

// CertStore holds the connector's mTLS client certificate (FR-25.19, BR-25.14). The private key is made
// on the connector and never leaves it: enrollment and renewal send only a certificate request. Files
// are replaced atomically (key first), so a crash leaves a usable pair on disk.
type CertStore struct {
	Dir string

	mu       sync.RWMutex
	cert     *tls.Certificate
	leaf     *x509.Certificate
	failing  *time.Time // when renewal began failing, nil while it is not
	serverCA *x509.CertPool
}

// NewKeyAndCSR makes an ECDSA P-256 key and a certificate request for it. The server decides the identity and
// writes it itself when it signs with its own CA; the platform issuer (cert-manager) signs the subject the
// request carries, so the request names the identity the server binds to this node, edge:<node id>. A request
// whose name is not that one is refused by that issuer. edgeID empty names the generic "iot-edge".
func NewKeyAndCSR(edgeID string) (keyPEM []byte, csrPEM string, err error) {
	cn := "iot-edge"
	if edgeID != "" {
		cn = "edge:" + strings.ToLower(edgeID)
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, "", err
	}
	der, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{Subject: pkix.Name{CommonName: cn}}, key)
	if err != nil {
		return nil, "", err
	}
	kd, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, "", err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: kd}),
		string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: der})), nil
}

// Load reads the pair in Dir. A missing pair is not an error: Has reports false and the connector enrolls.
func (s *CertStore) Load() error {
	cert, err := tls.LoadX509KeyPair(filepath.Join(s.Dir, certFile), filepath.Join(s.Dir, keyFile))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("client certificate in %s: %w", s.Dir, err)
	}
	return s.set(&cert)
}

func (s *CertStore) set(cert *tls.Certificate) error {
	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.cert, s.leaf = cert, leaf
	s.mu.Unlock()
	return nil
}

// Has reports whether a certificate is installed.
func (s *CertStore) Has() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cert != nil
}

// Install writes a new certificate (with the key it was requested for) and starts using it.
func (s *CertStore) Install(certPEM, chainPEM string, keyPEM []byte) error {
	cert, err := tls.X509KeyPair([]byte(certPEM), keyPEM)
	if err != nil {
		return fmt.Errorf("issued certificate does not match the key: %w", err)
	}
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return err
	}
	for name, data := range map[string][]byte{keyFile: keyPEM, certFile: []byte(certPEM), caFile: []byte(chainPEM)} {
		if err := writeAtomic(filepath.Join(s.Dir, name), data, 0o600); err != nil {
			return err
		}
	}
	if err := s.set(&cert); err != nil {
		return err
	}
	s.mu.Lock()
	s.failing = nil
	s.mu.Unlock()
	return nil
}

func writeAtomic(path string, data []byte, mode os.FileMode) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, mode); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// GetClientCertificate serves the current certificate to every new TLS handshake, so a renewal takes
// effect without restarting the connector.
func (s *CertStore) GetClientCertificate(*tls.CertificateRequestInfo) (*tls.Certificate, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.cert == nil {
		return nil, errors.New("no client certificate: the connector is not enrolled")
	}
	return s.cert, nil
}

// NotAfter and Serial describe the installed certificate (zero values when none).
func (s *CertStore) NotAfter() time.Time {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.leaf == nil {
		return time.Time{}
	}
	return s.leaf.NotAfter
}

// Serial is the lower-case hex serial of the installed certificate, as the server records it.
func (s *CertStore) Serial() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.leaf == nil {
		return ""
	}
	return hexSerial(s.leaf)
}

// hexSerial matches how the server writes a serial: hex of the big-endian bytes.
func hexSerial(c *x509.Certificate) string { return hex.EncodeToString(c.SerialNumber.Bytes()) }

// Action applies BR-25.14 to the installed certificate.
func (s *CertStore) Action(now time.Time) domain.CertAction {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.leaf == nil {
		return domain.CertExpired
	}
	return domain.DecideCert(now, s.leaf.NotAfter, s.failing)
}

// RenewFailed records a failed renewal; the first failure starts the 7-day clock (SF-0018).
func (s *CertStore) RenewFailed(now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failing == nil {
		s.failing = &now
	}
}

// HTTPClient returns a client that presents the certificate on every handshake (TLS 1.2 or newer) and
// trusts serverCAs for the server, or the system roots when serverCAs is nil.
func (s *CertStore) HTTPClient(serverCAs *x509.CertPool) *http.Client {
	return &http.Client{Timeout: 15 * time.Second, Transport: &http.Transport{
		TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: serverCAs, GetClientCertificate: s.GetClientCertificate},
	}}
}

// CertResponse is the server's answer to an enrollment or a renewal.
type CertResponse struct {
	Subject     string    `json:"subject"`
	Certificate string    `json:"certificate"`
	CAChain     string    `json:"caChain"`
	Serial      string    `json:"serial"`
	NotAfter    time.Time `json:"notAfter"`
}

// Enroll redeems an enrollment token (POST edge-nodes/{id}:enroll, no session) and installs the
// certificate it receives. hc is a plain client: the connector has no certificate yet.
func Enroll(ctx context.Context, hc *http.Client, baseURL, edgeID, token string, store *CertStore) (CertResponse, error) {
	keyPEM, csr, err := NewKeyAndCSR(edgeID)
	if err != nil {
		return CertResponse{}, err
	}
	body, _ := json.Marshal(map[string]string{"token": token, "csr": csr})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/v1/shopfloor/edge-nodes/"+url.PathEscape(edgeID)+":enroll", bytes.NewReader(body))
	if err != nil {
		return CertResponse{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := hc.Do(req)
	if err != nil {
		return CertResponse{}, err
	}
	defer res.Body.Close()
	var buf bytes.Buffer
	_, _ = buf.ReadFrom(res.Body)
	if res.StatusCode != http.StatusOK {
		return CertResponse{}, fmt.Errorf("enrollment: %s", problemText(res, buf.Bytes()))
	}
	var out CertResponse
	if err := json.Unmarshal(buf.Bytes(), &out); err != nil {
		return CertResponse{}, fmt.Errorf("enrollment answer: %w", err)
	}
	return out, store.Install(out.Certificate, out.CAChain, keyPEM)
}

// Renew asks the server for a fresh certificate over the mTLS client and installs it. It makes a new
// key each time, so a leaked old key stops mattering when the old certificate expires.
func (c *Client) Renew(ctx context.Context, store *CertStore) error {
	keyPEM, csr, err := NewKeyAndCSR(c.EdgeID)
	if err != nil {
		return err
	}
	res, body, err := c.do(ctx, http.MethodPost, "/v1/shopfloor/edge-nodes/"+url.PathEscape(c.EdgeID)+":renew-certificate", map[string]string{"csr": csr})
	if err != nil {
		return err
	}
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("renewal: %s", problemText(res, body))
	}
	var out CertResponse
	if err := json.Unmarshal(body, &out); err != nil {
		return fmt.Errorf("renewal answer: %w", err)
	}
	return store.Install(out.Certificate, out.CAChain, keyPEM)
}
