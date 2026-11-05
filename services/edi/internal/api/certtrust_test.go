package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/services/edi/internal/certtrust"
	"github.com/BenjaminSRussell/passage/services/edi/internal/domain"
	"github.com/BenjaminSRussell/passage/services/edi/internal/transport"
	"github.com/BenjaminSRussell/passage/services/edi/internal/transport/transporttest"
)

// With trust roots and a CRL directory configured, a partner AS2
// certificate is saved only when it chains to a root (through the
// intermediates stored after it) and has a current CRL that does not list
// it, and it is checked again at every inbound message and outbound
// delivery: no current CRL is refused until one comes (503, retried), a
// revoked certificate is refused for good (403 inbound, FAILED outbound).
// The certificate stays pinned: a message signed by another certificate of
// the same CA is refused.
func TestTransportAS2_PartnerCertificateChainAndRevocation(t *testing.T) {
	e := newTransportEnv(t)
	e.putProfile()
	root := transporttest.NewCA(t, "Distributor Root")
	issuing := root.Intermediate(t, "Distributor Issuing CA")
	dir := t.TempDir()
	roots, crls := filepath.Join(dir, "roots.pem"), filepath.Join(dir, "crl")
	if err := os.WriteFile(roots, []byte(transporttest.KeyPair{Cert: root.Cert}.CertPEM()), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(crls, 0o700); err != nil {
		t.Fatal(err)
	}
	trust, err := certtrust.New(roots, crls)
	if err != nil {
		t.Fatal(err)
	}
	e.a.AS2Trust, e.disp.Trust = trust, trust
	now := time.Now()
	writeCRL := func(name string, der []byte) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(crls, name), der, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	certField := func(k transporttest.KeyPair) func(*domain.AS2Settings) {
		return func(a *domain.AS2Settings) { a.PartnerCertificate = k.CertPEM() + issuing.CertPEM() }
	}

	// The self-signed certificate of the other tests does not chain.
	if rec := e.putTransport(e.as2Config(nil), srcScope); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "as2.partnerCertificate") {
		t.Fatalf("self-signed: %d %s", rec.Code, rec.Body.String())
	}
	// The partner now uses a certificate from its CA.
	e.them = issuing.Issue(t, "DISTRIBUTOR")
	e.peer = e.startPartner()
	if rec := e.putTransport(e.as2Config(certField(e.them)), srcScope); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "no current CRL") {
		t.Fatalf("no CRL yet: %d %s", rec.Code, rec.Body.String())
	}
	if rec := e.putTransport(e.as2Config(func(a *domain.AS2Settings) { a.PartnerCertificate = e.them.CertPEM() }), srcScope); rec.Code != http.StatusBadRequest {
		t.Fatalf("without the intermediate: %d %s", rec.Code, rec.Body.String())
	}
	writeCRL("root.crl", root.CRL(t, 1, now.Add(-time.Hour), now.Add(48*time.Hour)))
	writeCRL("issuing.crl", issuing.CRL(t, 1, now.Add(-time.Hour), now.Add(48*time.Hour)))
	v := e.mustPutTransport(e.as2Config(certField(e.them)))
	if v.AS2Inbound.PartnerCertificateTrust != "trusted" || v.AS2Inbound.PartnerCertificateSHA256 != domain.CertificateFingerprint(e.them.Cert) {
		t.Fatalf("view %+v", v.AS2Inbound)
	}

	c := e.partnerClient(v.AS2Inbound.URL, e.them)
	send := func(id, control string) (transport.Receipt, error) {
		return c.Send(context.Background(), transport.Message{ID: id, ContentType: transport.ContentX12, Payload: []byte(x12Control(t, control))})
	}
	if r, err := send("po-1@distributor", "000000401"); err != nil || !strings.HasSuffix(r.Disposition, "; processed") {
		t.Fatalf("trusted partner: %+v %v", r, err)
	}
	e.dispatch(1) // the 997, with the partner's MDN checked against the pinned certificate
	// Pinned: another certificate of the same CA cannot sign for the partner.
	sibling := e.partnerClient(v.AS2Inbound.URL, issuing.Issue(t, "DISTRIBUTOR"))
	if _, err := sibling.Send(context.Background(), transport.Message{ID: "po-x@distributor", ContentType: transport.ContentX12,
		Payload: []byte(x12Control(t, "000000402"))}); !errors.Is(err, transport.ErrRejected) || !strings.Contains(err.Error(), "authentication-failed") {
		t.Fatalf("sibling certificate: %v", err)
	}

	// The issuing CA's CRL runs out: fail closed, but only until it is refreshed.
	writeCRL("issuing.crl", issuing.CRL(t, 2, now.Add(-48*time.Hour), now.Add(-time.Minute)))
	if _, err := send("po-2@distributor", "000000403"); err == nil || !strings.Contains(err.Error(), "HTTP 503") {
		t.Fatalf("stale CRL inbound: %v", err)
	}
	out := e.outbound("asn-trust")
	e.dispatch(1)
	if d := e.detail(out.InterchangeID); d.Deliveries[0].Status != "PENDING" || !strings.Contains(d.Deliveries[0].LastError, "revocation status unknown") {
		t.Fatalf("stale CRL outbound: %+v", d.Deliveries)
	}
	if v := e.getTransportView(); v.AS2Inbound.PartnerCertificateTrust != "revocation-unknown" {
		t.Fatalf("view %+v", v.AS2Inbound)
	}
	writeCRL("issuing.crl", issuing.CRL(t, 3, now.Add(-time.Minute), now.Add(48*time.Hour)))
	if r, err := send("po-2@distributor", "000000403"); err != nil || !strings.HasSuffix(r.Disposition, "; processed") {
		t.Fatalf("refreshed CRL: %+v %v", r, err)
	}

	// Revoked: refused inbound (403), outbound fails at once, not saved again.
	writeCRL("issuing.crl", issuing.CRL(t, 4, now, now.Add(48*time.Hour), e.them.Cert))
	if _, err := send("po-3@distributor", "000000404"); err == nil || !strings.Contains(err.Error(), "HTTP 403") {
		t.Fatalf("revoked inbound: %v", err)
	}
	if _, err := e.admin.Exec(context.Background(), `update transport_delivery set next_attempt_at = now() where status = 'PENDING'`); err != nil {
		t.Fatal(err)
	}
	e.dispatch(2) // the 997 for po-2 and the waiting 856
	if d := e.detail(out.InterchangeID); d.Deliveries[0].Status != "FAILED" || !strings.Contains(d.Deliveries[0].LastError, "revoked") {
		t.Fatalf("revoked outbound: %+v", d.Deliveries)
	}
	if rec := e.putTransport(e.as2Config(certField(e.them)), srcScope); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "revoked") {
		t.Fatalf("revoked PUT: %d %s", rec.Code, rec.Body.String())
	}
	if v := e.getTransportView(); v.AS2Inbound.PartnerCertificateTrust != "untrusted" {
		t.Fatalf("view %+v", v.AS2Inbound)
	}
}

func (e *transportEnv) getTransportView() transportView {
	e.t.Helper()
	rec := do(e.t, e.h, http.MethodGet, "/edi/v1/partners/"+e.partner+"/transport", "", "edi.admin", srcScope)
	var v transportView
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil || rec.Code != http.StatusOK {
		e.t.Fatalf("get transport: %d %s", rec.Code, rec.Body.String())
	}
	return v
}
