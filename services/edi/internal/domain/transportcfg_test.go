package domain

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"strings"
	"testing"
	"time"
)

func certPEM(t *testing.T, ec bool, notAfter time.Time) string {
	t.Helper()
	var pub, priv any
	if ec {
		k, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		pub, priv = k.Public(), k
	} else {
		k, _ := rsa.GenerateKey(rand.Reader, 2048)
		pub, priv = k.Public(), k
	}
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(7), Subject: pkix.Name{CommonName: "p"}, NotBefore: notAfter.Add(-48 * time.Hour), NotAfter: notAfter}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, pub, priv)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
}

func TestPartnerTransport_Validate(t *testing.T) {
	now := time.Date(2026, 11, 5, 9, 0, 0, 0, time.UTC)
	rules := TransportRules{Now: now}
	rsaCert, ecCert := certPEM(t, false, now.Add(24*time.Hour)), certPEM(t, true, now.Add(24*time.Hour))
	as2 := func(mut func(*AS2Settings)) PartnerTransport {
		a := AS2Settings{PartnerAS2ID: "ACME", OurAS2ID: "PASSAGE", URL: "https://as2.acme.example/as2", PartnerCertificate: rsaCert, SigningKeyRef: "as2-passage"}
		if mut != nil {
			mut(&a)
		}
		return PartnerTransport{Kind: TransportAS2, AS2: &a}.WithDefaults()
	}
	if f := as2(nil).Validate(rules); len(f) != 0 {
		t.Fatalf("valid AS2: %v", f)
	}
	sftp := func(mut func(*SFTPSettings)) PartnerTransport {
		s := SFTPSettings{Host: "sftp.acme.example", User: "edi", HostKeyFingerprint: "SHA256:" + strings.Repeat("A", 43), AuthMethod: "key",
			CredentialRef: "sftp-acme", OutboundDir: "/to-acme", InboundDir: "from-acme/inbox"}
		if mut != nil {
			mut(&s)
		}
		return PartnerTransport{Kind: TransportSFTP, SFTP: &s}.WithDefaults()
	}
	ok := sftp(nil)
	if f := ok.Validate(rules); len(f) != 0 || ok.SFTP.Port != 22 || ok.SFTP.ArchiveDir != "processed" || ok.SFTP.FailedDir != "failed" || ok.SFTP.PollIntervalSeconds != 300 {
		t.Fatalf("valid SFTP: %v %+v", f, ok.SFTP)
	}
	if f := (PartnerTransport{Kind: TransportAPI}).Validate(rules); len(f) != 0 {
		t.Fatal(f)
	}
	for name, c := range map[string]struct {
		cfg   PartnerTransport
		rules TransportRules
		field string
	}{
		"http URL":               {as2(func(a *AS2Settings) { a.URL = "http://as2.acme.example/as2" }), rules, "as2.url"},
		"relative URL":           {as2(func(a *AS2Settings) { a.URL = "/as2" }), rules, "as2.url"},
		"expired certificate":    {as2(func(a *AS2Settings) { a.PartnerCertificate = certPEM(t, false, now.Add(-time.Hour)) }), rules, "as2.partnerCertificate"},
		"two certificates":       {as2(func(a *AS2Settings) { a.PartnerCertificate = rsaCert + rsaCert }), rules, "as2.partnerCertificate"},
		"EC cert and encryption": {as2(func(a *AS2Settings) { a.PartnerCertificate, a.EncryptionAlgorithm = ecCert, "aes128-cbc" }), rules, "as2.partnerCertificate"},
		"async without URL":      {as2(func(a *AS2Settings) { a.MDNMode = "async" }), rules, "as2.mdnMode"},
		"sha-1":                  {as2(func(a *AS2Settings) { a.SigningAlgorithm = "sha-1" }), rules, "as2.signingAlgorithm"},
		"ref with ..":            {as2(func(a *AS2Settings) { a.SigningKeyRef = "a..b" }), rules, "as2.signingKeyRef"},
		"API with settings":      {PartnerTransport{Kind: TransportAPI, SFTP: &SFTPSettings{}}, rules, "kind"},
		"bad fingerprint":        {sftp(func(s *SFTPSettings) { s.HostKeyFingerprint = "MD5:aa" }), rules, "sftp.hostKeyFingerprint"},
		"host with a flag":       {sftp(func(s *SFTPSettings) { s.Host = "-oProxyCommand" }), rules, "sftp.host"},
		"port":                   {sftp(func(s *SFTPSettings) { s.Port = 70000 }), rules, "sftp.port"},
		"same in and out":        {sftp(func(s *SFTPSettings) { s.InboundDir = s.OutboundDir }), rules, "sftp.inboundDir"},
		"archive is a path":      {sftp(func(s *SFTPSettings) { s.ArchiveDir = "a/b" }), rules, "sftp.archiveDir"},
		"failed equals archive":  {sftp(func(s *SFTPSettings) { s.FailedDir = "processed" }), rules, "sftp.failedDir"},
		"poll too often":         {sftp(func(s *SFTPSettings) { s.PollIntervalSeconds = 5 }), rules, "sftp.pollIntervalSeconds"},
		"auth method":            {sftp(func(s *SFTPSettings) { s.AuthMethod = "none" }), rules, "sftp.authMethod"},
	} {
		if f := c.cfg.Validate(c.rules); f[c.field] == "" {
			t.Errorf("%s: want %s in %v", name, c.field, f)
		}
	}
	if f := as2(func(a *AS2Settings) { a.URL = "http://localhost:8080/as2" }).Validate(TransportRules{Now: now, AllowHTTP: true}); len(f) != 0 {
		t.Fatalf("http allowed in development: %v", f)
	}
	if f := as2(func(a *AS2Settings) { a.MDNMode = "async" }).Validate(TransportRules{Now: now, AsyncMDN: true}); len(f) != 0 {
		t.Fatal(f)
	}
	if f := sftp(func(s *SFTPSettings) { s.Host = "2001:db8::1" }).Validate(rules); len(f) != 0 {
		t.Fatalf("IPv6 host: %v", f)
	}
	for _, ref := range []string{"a", "as2-passage", "k.pem", "x_1"} {
		if !ValidSecretRef(ref) {
			t.Errorf("%q refused", ref)
		}
	}
	for _, ref := range []string{"", "-a", "A", "a/b", "a..b", "a-", strings.Repeat("a", 64), "-----BEGIN"} {
		if ValidSecretRef(ref) {
			t.Errorf("%q accepted", ref)
		}
	}
}
