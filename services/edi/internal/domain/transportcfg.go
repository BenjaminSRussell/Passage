package domain

import (
	"crypto/ecdsa"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"net"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// Transport kinds a partner profile may select (FR-35.5). API is the default:
// documents are returned by the outbound routes and received on
// POST /inbound/x12, and nothing is transmitted.
const (
	TransportAPI  = "API"
	TransportAS2  = "AS2"
	TransportSFTP = "SFTP"
)

// Defaults of the optional transport settings.
const (
	DefaultSigningAlg      = "sha-256"
	DefaultEncryptionAlg   = "none"
	DefaultMDNMode         = "sync"
	DefaultSFTPPort        = 22
	DefaultSFTPArchiveDir  = "processed"
	DefaultSFTPFailedDir   = "failed"
	DefaultSFTPPollSeconds = 300
)

// PartnerTransport is how documents reach a partner and come back. Secrets
// are never part of it: SigningKeyRef and CredentialRef name files in the
// service's mounted secret directory, read when a message is sent.
type PartnerTransport struct {
	Kind string        `json:"kind"`
	AS2  *AS2Settings  `json:"as2,omitempty"`
	SFTP *SFTPSettings `json:"sftp,omitempty"`
}

// AS2Settings is one partner's AS2 profile. Our messages are always signed
// and a signed MDN is always required; the partner's messages must be signed
// with PartnerCertificate, and encrypted when EncryptionAlgorithm is not none.
type AS2Settings struct {
	PartnerAS2ID        string `json:"partnerAs2Id"`
	OurAS2ID            string `json:"ourAs2Id"`
	URL                 string `json:"url"`
	PartnerCertificate  string `json:"partnerCertificate"`
	SigningKeyRef       string `json:"signingKeyRef"`
	SigningAlgorithm    string `json:"signingAlgorithm,omitempty"`
	EncryptionAlgorithm string `json:"encryptionAlgorithm,omitempty"`
	MDNMode             string `json:"mdnMode,omitempty"`
}

// SFTPSettings is one partner's SFTP drop. InboundDir empty means nothing is
// polled; ArchiveDir and FailedDir are subdirectories of InboundDir.
type SFTPSettings struct {
	Host                string `json:"host"`
	Port                int    `json:"port,omitempty"`
	User                string `json:"user"`
	HostKeyFingerprint  string `json:"hostKeyFingerprint"`
	AuthMethod          string `json:"authMethod"`
	CredentialRef       string `json:"credentialRef"`
	OutboundDir         string `json:"outboundDir"`
	InboundDir          string `json:"inboundDir,omitempty"`
	ArchiveDir          string `json:"archiveDir,omitempty"`
	FailedDir           string `json:"failedDir,omitempty"`
	PollIntervalSeconds int    `json:"pollIntervalSeconds,omitempty"`
}

var (
	as2IDRe       = regexp.MustCompile(`^[A-Za-z0-9._@:-]{1,128}$`)
	secretRefRe   = regexp.MustCompile(`^[a-z0-9]([a-z0-9._-]{0,61}[a-z0-9])?$`)
	sftpHostRe    = regexp.MustCompile(`^[A-Za-z0-9.-]{1,253}$`)
	sftpUserRe    = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)
	fingerprintRe = regexp.MustCompile(`^SHA256:[A-Za-z0-9+/]{43}$`)
	pathSegmentRe = regexp.MustCompile(`^[A-Za-z0-9_-][A-Za-z0-9._-]{0,63}$`)
)

// ValidSecretRef reports whether ref is a secret file name: lower-case letters,
// digits, '.', '_' and '-', at most 63 characters, no "..".
func ValidSecretRef(ref string) bool {
	return secretRefRe.MatchString(ref) && !strings.Contains(ref, "..")
}

// WithDefaults fills the optional settings.
func (t PartnerTransport) WithDefaults() PartnerTransport {
	if t.AS2 != nil {
		a := *t.AS2
		if a.SigningAlgorithm == "" {
			a.SigningAlgorithm = DefaultSigningAlg
		}
		if a.EncryptionAlgorithm == "" {
			a.EncryptionAlgorithm = DefaultEncryptionAlg
		}
		if a.MDNMode == "" {
			a.MDNMode = DefaultMDNMode
		}
		a.PartnerCertificate = strings.TrimSpace(a.PartnerCertificate) + "\n"
		t.AS2 = &a
	}
	if t.SFTP != nil {
		s := *t.SFTP
		if s.Port == 0 {
			s.Port = DefaultSFTPPort
		}
		if s.ArchiveDir == "" {
			s.ArchiveDir = DefaultSFTPArchiveDir
		}
		if s.FailedDir == "" {
			s.FailedDir = DefaultSFTPFailedDir
		}
		if s.PollIntervalSeconds == 0 {
			s.PollIntervalSeconds = DefaultSFTPPollSeconds
		}
		t.SFTP = &s
	}
	return t
}

// TransportRules are the deployment choices a transport is checked against.
type TransportRules struct {
	Now time.Time
	// AllowHTTP admits http:// AS2 URLs (development and tests only).
	AllowHTTP bool
	// AsyncMDN is whether this deployment can receive asynchronous MDNs (it
	// has a public AS2 URL).
	AsyncMDN bool
	// TrustCertificate, when set, checks the partner certificate with the
	// intermediates after it against the deployment's trust roots and CRLs
	// and returns why it is refused ("" when trusted).
	TrustCertificate func(leaf *x509.Certificate, intermediates []*x509.Certificate) string
}

// Validate returns field errors keyed by JSON path; an empty map means valid.
// Call it on the result of WithDefaults.
func (t PartnerTransport) Validate(r TransportRules) map[string]string {
	fields := map[string]string{}
	switch t.Kind {
	case TransportAPI:
		if t.AS2 != nil || t.SFTP != nil {
			fields["kind"] = "API takes no as2 or sftp settings"
		}
	case TransportAS2:
		if t.SFTP != nil {
			fields["sftp"] = "must be absent when kind is AS2"
		}
		if t.AS2 == nil {
			fields["as2"] = "is required when kind is AS2"
		} else {
			t.AS2.validate(r, fields)
		}
	case TransportSFTP:
		if t.AS2 != nil {
			fields["as2"] = "must be absent when kind is SFTP"
		}
		if t.SFTP == nil {
			fields["sftp"] = "is required when kind is SFTP"
		} else {
			t.SFTP.validate(fields)
		}
	default:
		fields["kind"] = "must be API, AS2 or SFTP"
	}
	return fields
}

func (a *AS2Settings) validate(r TransportRules, fields map[string]string) {
	if !as2IDRe.MatchString(a.PartnerAS2ID) {
		fields["as2.partnerAs2Id"] = "must be 1 to 128 letters, digits or . _ @ : -"
	}
	if !as2IDRe.MatchString(a.OurAS2ID) {
		fields["as2.ourAs2Id"] = "must be 1 to 128 letters, digits or . _ @ : -"
	}
	if msg := checkAS2URL(a.URL, r.AllowHTTP); msg != "" {
		fields["as2.url"] = msg
	}
	if !ValidSecretRef(a.SigningKeyRef) {
		fields["as2.signingKeyRef"] = "must name a secret: 1 to 63 lower-case letters, digits or . _ -"
	}
	switch a.SigningAlgorithm {
	case "sha-256", "sha-384", "sha-512":
	default:
		fields["as2.signingAlgorithm"] = "must be sha-256, sha-384 or sha-512"
	}
	switch a.EncryptionAlgorithm {
	case "none", "aes128-cbc", "aes192-cbc", "aes256-cbc":
	default:
		fields["as2.encryptionAlgorithm"] = "must be none, aes128-cbc, aes192-cbc or aes256-cbc"
	}
	switch a.MDNMode {
	case "sync":
	case "async":
		if !r.AsyncMDN {
			fields["as2.mdnMode"] = "async needs EDI_AS2_PUBLIC_URL, where partners post MDNs"
		}
	default:
		fields["as2.mdnMode"] = "must be sync or async"
	}
	cert, intermediates, msg := ParsePartnerChain(a.PartnerCertificate, r.Now)
	switch {
	case msg != "":
		fields["as2.partnerCertificate"] = msg
	case a.EncryptionAlgorithm != "none" && !isRSAKey(cert):
		fields["as2.partnerCertificate"] = "must hold an RSA key to encrypt to the partner"
	case r.TrustCertificate != nil:
		if why := r.TrustCertificate(cert, intermediates); why != "" {
			fields["as2.partnerCertificate"] = why
		}
	}
}

func isRSAKey(c *x509.Certificate) bool {
	_, ok := c.PublicKey.(*rsa.PublicKey)
	return ok
}

func checkAS2URL(raw string, allowHTTP bool) string {
	u, err := url.Parse(raw)
	if err != nil || len(raw) > 2048 || u.Host == "" || u.User != nil || u.Fragment != "" || strings.ContainsAny(raw, " \r\n\t") {
		return "must be an absolute https URL without credentials"
	}
	switch u.Scheme {
	case "https":
	case "http":
		if !allowHTTP {
			return "must use https"
		}
	default:
		return "must be an absolute https URL without credentials"
	}
	return ""
}

// ParsePartnerCertificate reads the partner's pinned PEM certificate (the
// first block, see ParsePartnerChain) holding an RSA (2048 bits or more) or
// ECDSA key that is valid at now. The message is empty when the certificate
// is acceptable.
func ParsePartnerCertificate(pemText string, now time.Time) (*x509.Certificate, string) {
	cert, _, msg := ParsePartnerChain(pemText, now)
	return cert, msg
}

// ParsePartnerChain reads the partner certificate field: the partner's own
// certificate (pinned: its signatures are verified against it), optionally
// followed by the intermediate CA certificates that chain it to a trust
// root (used only when trust roots are configured, EDI_AS2_TRUST_ROOTS). At
// most five intermediates, 16 KiB in all, no private key.
func ParsePartnerChain(pemText string, now time.Time) (*x509.Certificate, []*x509.Certificate, string) {
	if len(pemText) > 16384 || strings.Contains(pemText, "PRIVATE KEY") {
		return nil, nil, "must be PEM certificates (no private key), at most 16 KiB"
	}
	var certs []*x509.Certificate
	for rest := []byte(pemText); ; {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			if strings.TrimSpace(string(rest)) != "" {
				return nil, nil, "must hold only PEM CERTIFICATE blocks"
			}
			break
		}
		if block.Type != "CERTIFICATE" {
			return nil, nil, "must hold only PEM CERTIFICATE blocks"
		}
		c, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return nil, nil, "is not a valid X.509 certificate"
		}
		certs = append(certs, c)
	}
	if len(certs) == 0 {
		return nil, nil, "must be a PEM CERTIFICATE block"
	}
	if len(certs) > 6 {
		return nil, nil, "may carry at most five intermediate certificates after the partner certificate"
	}
	for _, ic := range certs[1:] {
		if !ic.IsCA {
			return nil, nil, "certificates after the partner certificate must be CA certificates (intermediates)"
		}
	}
	cert, msg := checkPartnerKey(certs[0], now)
	return cert, certs[1:], msg
}

func checkPartnerKey(cert *x509.Certificate, now time.Time) (*x509.Certificate, string) {
	switch k := cert.PublicKey.(type) {
	case *rsa.PublicKey:
		if k.N.BitLen() < 2048 {
			return nil, "RSA key must be at least 2048 bits"
		}
	case *ecdsa.PublicKey:
	default:
		return nil, "must hold an RSA or ECDSA key"
	}
	if now.After(cert.NotAfter) || now.Before(cert.NotBefore) {
		return nil, "is not valid now (check notBefore and notAfter)"
	}
	return cert, ""
}

// CertificateFingerprint is the hex SHA-256 of the certificate's DER.
func CertificateFingerprint(cert *x509.Certificate) string {
	sum := sha256.Sum256(cert.Raw)
	return hex.EncodeToString(sum[:])
}

func validRemoteDir(p string) bool {
	if p == "" || len(p) > 255 {
		return false
	}
	trimmed := strings.Trim(p, "/")
	if trimmed == "" {
		return p == "/"
	}
	for _, seg := range strings.Split(trimmed, "/") {
		if seg == "." || seg == ".." || !pathSegmentRe.MatchString(seg) {
			return false
		}
	}
	return true
}

func (s *SFTPSettings) validate(fields map[string]string) {
	if ip := net.ParseIP(s.Host); ip == nil && (!sftpHostRe.MatchString(s.Host) || strings.HasPrefix(s.Host, "-") || strings.Contains(s.Host, "..")) {
		fields["sftp.host"] = "must be a host name or IP address"
	}
	if s.Port < 1 || s.Port > 65535 {
		fields["sftp.port"] = "must be 1 to 65535"
	}
	if !sftpUserRe.MatchString(s.User) {
		fields["sftp.user"] = "must be 1 to 64 letters, digits or . _ -"
	}
	if !fingerprintRe.MatchString(s.HostKeyFingerprint) {
		fields["sftp.hostKeyFingerprint"] = "must be the SHA256: fingerprint ssh-keygen -lf prints"
	}
	if s.AuthMethod != "key" && s.AuthMethod != "password" {
		fields["sftp.authMethod"] = "must be key or password"
	}
	if !ValidSecretRef(s.CredentialRef) {
		fields["sftp.credentialRef"] = "must name a secret: 1 to 63 lower-case letters, digits or . _ -"
	}
	if !validRemoteDir(s.OutboundDir) {
		fields["sftp.outboundDir"] = "must be a path of letters, digits and . _ - without . or .. segments"
	}
	if s.InboundDir != "" && !validRemoteDir(s.InboundDir) {
		fields["sftp.inboundDir"] = "must be a path of letters, digits and . _ - without . or .. segments"
	}
	if s.InboundDir != "" && s.InboundDir == s.OutboundDir {
		fields["sftp.inboundDir"] = "must differ from outboundDir"
	}
	if !pathSegmentRe.MatchString(s.ArchiveDir) {
		fields["sftp.archiveDir"] = "must be one directory name inside inboundDir"
	}
	if !pathSegmentRe.MatchString(s.FailedDir) || s.FailedDir == s.ArchiveDir {
		fields["sftp.failedDir"] = "must be one directory name inside inboundDir, other than archiveDir"
	}
	if s.PollIntervalSeconds < 60 || s.PollIntervalSeconds > 86400 {
		fields["sftp.pollIntervalSeconds"] = "must be 60 to 86400"
	}
}
