package pki

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// CertManager is the platform Issuer (BR-25.14): it has cert-manager sign the connector's request. It makes a
// cert-manager CertificateRequest for the request in the shop floor service's namespace, waits for cert-manager
// to issue it from the configured Issuer or ClusterIssuer, reads the certificate and deletes the request. It
// talks to the Kubernetes API with the pod's service account, which needs only create, get and delete on
// certificaterequests in that namespace (the chart's Role).
//
// cert-manager signs the subject the request carries, and the service decides the identity (never the
// requester). So Issue refuses a request whose common name is not the subject the service chose, and checks the
// certificate that came back: its subject, its key and its client-auth usage. A connector writes
// edge:<node id> as the common name of its request, which is the subject the service binds.
type CertManager struct {
	APIServer string // https://kubernetes.default.svc
	Namespace string
	// IssuerName, IssuerKind (Issuer or ClusterIssuer, default Issuer) and IssuerGroup (default cert-manager.io)
	// say which issuer signs.
	IssuerName, IssuerKind, IssuerGroup string
	// Token returns the bearer token of the service account; Client carries the cluster's CA.
	Token  func() (string, error)
	Client *http.Client
	// PollEvery and Timeout bound the wait for cert-manager (defaults 500 ms and 30 s).
	PollEvery, Timeout time.Duration
}

const (
	crAPI      = "/apis/cert-manager.io/v1/namespaces/%s/certificaterequests"
	saTokenKey = "/var/run/secrets/kubernetes.io/serviceaccount/token"
)

// ServiceAccountToken reads the pod's token each time (Kubernetes rotates it).
func ServiceAccountToken() (string, error) {
	b, err := os.ReadFile(saTokenKey)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}

type crCondition struct {
	Type    string `json:"type"`
	Status  string `json:"status"`
	Reason  string `json:"reason"`
	Message string `json:"message"`
}

type crObject struct {
	Metadata struct {
		Name string `json:"name"`
	} `json:"metadata"`
	Status struct {
		Certificate string        `json:"certificate"`
		CA          string        `json:"ca"`
		Conditions  []crCondition `json:"conditions"`
	} `json:"status"`
}

func (c *CertManager) call(ctx context.Context, method, path string, body any) (int, []byte, error) {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return 0, nil, err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(c.APIServer, "/")+path, rd)
	if err != nil {
		return 0, nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")
	if c.Token != nil {
		tok, err := c.Token()
		if err != nil {
			return 0, nil, fmt.Errorf("service account token: %w", err)
		}
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	cl := c.Client
	if cl == nil {
		cl = &http.Client{Timeout: 15 * time.Second}
	}
	res, err := cl.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer res.Body.Close()
	b, err := io.ReadAll(io.LimitReader(res.Body, 4<<20))
	return res.StatusCode, b, err
}

// Issue has cert-manager sign the request for subject.
func (c *CertManager) Issue(csr *x509.CertificateRequest, subject string, now time.Time) (Issued, error) {
	if subject == "" {
		return Issued{}, errors.New("subject is required")
	}
	if csr.Subject.CommonName != subject {
		return Issued{}, fmt.Errorf("%w: the common name must be %q, the identity bound to this node", ErrBadCSR, subject)
	}
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	every := c.PollEvery
	if every <= 0 {
		every = 500 * time.Millisecond
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	kind, group := c.IssuerKind, c.IssuerGroup
	if kind == "" {
		kind = "Issuer"
	}
	if group == "" {
		group = "cert-manager.io"
	}
	csrPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: csr.Raw})
	path := fmt.Sprintf(crAPI, url.PathEscape(c.Namespace))
	status, b, err := c.call(ctx, http.MethodPost, path, map[string]any{
		"apiVersion": "cert-manager.io/v1", "kind": "CertificateRequest",
		"metadata": map[string]any{"generateName": "edge-", "namespace": c.Namespace},
		"spec": map[string]any{
			"request":   base64.StdEncoding.EncodeToString(csrPEM),
			"issuerRef": map[string]any{"name": c.IssuerName, "kind": kind, "group": group},
			"usages":    []string{"digital signature", "client auth"},
			"duration":  fmt.Sprintf("%dh", int(CertLifetime/time.Hour)),
		}})
	if err != nil {
		return Issued{}, fmt.Errorf("cert-manager: create the certificate request: %w", err)
	}
	if status != http.StatusCreated && status != http.StatusOK {
		return Issued{}, fmt.Errorf("cert-manager: the Kubernetes API answered %d to the certificate request", status)
	}
	var cr crObject
	if err := json.Unmarshal(b, &cr); err != nil || cr.Metadata.Name == "" {
		return Issued{}, errors.New("cert-manager: the Kubernetes API answered without a request name")
	}
	name := cr.Metadata.Name
	defer func() { // the request has done its work, whatever came of it
		dctx, dcancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer dcancel()
		_, _, _ = c.call(dctx, http.MethodDelete, path+"/"+url.PathEscape(name), nil)
	}()
	for {
		if cr.Status.Certificate != "" && ready(cr) {
			return c.issued(cr, csr, subject)
		}
		for _, cond := range cr.Status.Conditions {
			if cond.Status == "True" && (cond.Type == "Denied" || cond.Type == "InvalidRequest") ||
				cond.Type == "Ready" && cond.Status == "False" && cond.Reason == "Failed" {
				return Issued{}, fmt.Errorf("cert-manager refused the certificate request (%s): %s", cond.Type, cond.Message)
			}
		}
		select {
		case <-ctx.Done():
			return Issued{}, errors.New("cert-manager did not issue the certificate in time; is the issuer ready?")
		case <-time.After(every):
		}
		status, b, err = c.call(ctx, http.MethodGet, path+"/"+url.PathEscape(name), nil)
		if err != nil {
			return Issued{}, fmt.Errorf("cert-manager: read the certificate request: %w", err)
		}
		if status != http.StatusOK {
			return Issued{}, fmt.Errorf("cert-manager: the Kubernetes API answered %d reading the certificate request", status)
		}
		cr = crObject{}
		if err := json.Unmarshal(b, &cr); err != nil {
			return Issued{}, fmt.Errorf("cert-manager: unreadable certificate request: %w", err)
		}
	}
}

func ready(cr crObject) bool {
	for _, c := range cr.Status.Conditions {
		if c.Type == "Ready" && c.Status == "True" {
			return true
		}
	}
	return false
}

// issued checks what cert-manager returned against what was asked for.
func (c *CertManager) issued(cr crObject, csr *x509.CertificateRequest, subject string) (Issued, error) {
	chainPEM, err := base64.StdEncoding.DecodeString(cr.Status.Certificate)
	if err != nil {
		return Issued{}, errors.New("cert-manager: the certificate is not base64")
	}
	block, rest := pem.Decode(chainPEM)
	if block == nil || block.Type != "CERTIFICATE" {
		return Issued{}, errors.New("cert-manager: the answer holds no certificate")
	}
	leaf, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return Issued{}, fmt.Errorf("cert-manager: the certificate: %w", err)
	}
	if leaf.Subject.CommonName != subject {
		return Issued{}, fmt.Errorf("cert-manager issued a certificate for %q, not %q", leaf.Subject.CommonName, subject)
	}
	if !sameKey(leaf.PublicKey, csr.PublicKey) {
		return Issued{}, errors.New("cert-manager issued a certificate for a different key than the request's")
	}
	clientAuth := false
	for _, u := range leaf.ExtKeyUsage {
		clientAuth = clientAuth || u == x509.ExtKeyUsageClientAuth
	}
	if !clientAuth {
		return Issued{}, errors.New("cert-manager issued a certificate that is not valid for client authentication")
	}
	ca := string(rest)
	if cr.Status.CA != "" {
		if b, err := base64.StdEncoding.DecodeString(cr.Status.CA); err == nil {
			ca = string(b)
		}
	}
	return Issued{CertPEM: string(pem.EncodeToMemory(block)), ChainPEM: strings.TrimSpace(ca) + "\n", Serial: hex.EncodeToString(leaf.SerialNumber.Bytes()),
		NotBefore: leaf.NotBefore.UTC(), NotAfter: leaf.NotAfter.UTC()}, nil
}

func sameKey(a, b any) bool {
	ab, err1 := x509.MarshalPKIXPublicKey(a)
	bb, err2 := x509.MarshalPKIXPublicKey(b)
	return err1 == nil && err2 == nil && bytes.Equal(ab, bb)
}
