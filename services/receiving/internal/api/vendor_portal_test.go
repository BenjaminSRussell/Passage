package api_test

import (
	"crypto/ed25519"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/domain"
)

const (
	otherVendorID = "33333333-3333-4333-8333-333333333333"
	otherVendorPO = "66666666-6666-4666-8666-666666666666"
)

// portal signs requests the way the supplier portal does: a service token whose verified principal carries the vendor.
type portal struct {
	rg     *rig
	signer *tenant.ServiceSigner
}

func newPortal(t *testing.T, rg *rig) *portal {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := tenant.NewServiceSigner(tenant.VendorPortalIssuer, priv)
	if err != nil {
		t.Fatal(err)
	}
	tenant.SetServiceVerifier(tenant.NewServiceVerifier("receiving", map[string]ed25519.PublicKey{tenant.VendorPortalIssuer: pub}))
	t.Cleanup(func() { tenant.SetServiceVerifier(nil) })
	return &portal{rg: rg, signer: signer}
}

func (p *portal) call(t *testing.T, vendor, method, path, body string) (int, string) {
	t.Helper()
	token, err := p.signer.Mint("receiving", tenant.Principal{TenantID: testkit.TenantID(1), LegalEntityID: legalEntityID, UserID: "vendor-user", VendorID: vendor, Duties: map[string]struct{}{}}, "")
	if err != nil {
		t.Fatal(err)
	}
	rec := p.rg.call(method, path, body, headers{"X-Passage-Service-Token": token, "Idempotency-Key": p.rg.key()})
	return rec.Code, rec.Body.String()
}

func twoVendorRig() *rig {
	other := asnPO()
	other.ID, other.VendorID = otherVendorPO, otherVendorID
	rg := newSheetRig(asnPO())
	rg.orders[otherVendorPO] = other
	return rg
}

func TestVendorPortal_AsnIsScopedToTheVendor(t *testing.T) {
	rg := twoVendorRig()
	p := newPortal(t, rg)

	code, body := p.call(t, vendorID, http.MethodPost, "/v1/receiving/vendor/asns", asnBodyFor("TRK-A", asnLine(1, "10", "", "")))
	if code != http.StatusCreated {
		t.Fatalf("own PO: %d %s", code, body)
	}
	var mine asnJSON
	_ = json.Unmarshal([]byte(body), &mine)
	if mine.VendorID != vendorID {
		t.Fatalf("ASN vendor %s, want the caller's %s", mine.VendorID, vendorID)
	}

	// The other vendor's order reads as missing, whatever the body says about it.
	bOnA, _ := json.Marshal(map[string]any{"legalEntityId": legalEntityID, "purchaseOrderId": poID, "trackingNumber": "TRK-B", "lines": []map[string]any{asnLine(1, "1", "", "")}})
	if code, body := p.call(t, otherVendorID, http.MethodPost, "/v1/receiving/vendor/asns", string(bOnA)); code != http.StatusNotFound {
		t.Fatalf("another vendor's PO: %d %s, want 404", code, body)
	}

	if code, body := p.call(t, otherVendorID, http.MethodGet, "/v1/receiving/vendor/asns/"+mine.ID, ""); code != http.StatusNotFound {
		t.Fatalf("another vendor reads the ASN: %d %s, want 404", code, body)
	}
	code, body = p.call(t, otherVendorID, http.MethodGet, "/v1/receiving/vendor/asns", "")
	var page struct {
		Items []asnJSON `json:"items"`
	}
	_ = json.Unmarshal([]byte(body), &page)
	if code != http.StatusOK || len(page.Items) != 0 {
		t.Fatalf("another vendor lists: %d %s, want none", code, body)
	}
	// A vendorId in the query never widens the list.
	code, body = p.call(t, otherVendorID, http.MethodGet, "/v1/receiving/vendor/asns?vendorId="+vendorID, "")
	if code != http.StatusOK || strings.Contains(body, mine.ID) {
		t.Fatalf("vendorId query widened the list: %d %s", code, body)
	}
	if code, body := p.call(t, vendorID, http.MethodGet, "/v1/receiving/vendor/asns/"+mine.ID, ""); code != http.StatusOK || !strings.Contains(body, mine.ID) {
		t.Fatalf("own ASN: %d %s", code, body)
	}
}

func TestVendorPortal_RefusesACallerThatIsNotASupplier(t *testing.T) {
	rg := twoVendorRig()
	// A buyer holding the portal duty by header is not a supplier portal call.
	rec := rg.call(http.MethodGet, "/v1/receiving/vendor/asns", "", duty("receiving.vendor-portal.asn", rg.key()))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("buyer on a vendor route: %d %s, want 403", rec.Code, rec.Body.String())
	}
	// The buyer's own list keeps working for the buyer and is not narrowed.
	rg.createAsn(t, "TRK-1", asnLine(1, "5", "", ""))
	rec = rg.call(http.MethodGet, asnsPath, "", duty("receiving.asn.receive", rg.key()))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "TRK-1") {
		t.Fatalf("buyer list: %d %s", rec.Code, rec.Body.String())
	}
}

func TestVendorPortal_InvoiceDocumentsAreScopedAndDeduplicatedAcrossVendors(t *testing.T) {
	rg := newCaptureRig(nil)
	p := newPortal(t, rg)

	code, body := p.call(t, vendorID, http.MethodPost, "/v1/receiving/vendor/invoice-captures", captureRequest("UBL", "", ublDocument))
	if code != http.StatusCreated {
		t.Fatalf("submit: %d %s", code, body)
	}
	var mine captureJSON
	_ = json.Unmarshal([]byte(body), &mine)

	if code, body := p.call(t, otherVendorID, http.MethodGet, "/v1/receiving/vendor/invoice-captures/"+mine.ID, ""); code != http.StatusNotFound {
		t.Fatalf("another vendor reads the document: %d %s, want 404", code, body)
	}
	if _, body := p.call(t, otherVendorID, http.MethodGet, "/v1/receiving/vendor/invoice-captures", ""); strings.Contains(body, mine.ID) {
		t.Fatalf("another vendor lists the document: %s", body)
	}
	// The same document from another vendor is refused without saying whose it is.
	if code, body := p.call(t, otherVendorID, http.MethodPost, "/v1/receiving/vendor/invoice-captures", captureRequest("UBL", "", ublDocument)); code != http.StatusConflict || strings.Contains(body, mine.ID) {
		t.Fatalf("duplicate from another vendor: %d %s, want 409 without the id", code, body)
	}
	if code, body := p.call(t, vendorID, http.MethodGet, "/v1/receiving/vendor/invoice-captures", ""); code != http.StatusOK || !strings.Contains(body, mine.ID) {
		t.Fatalf("own list: %d %s", code, body)
	}
	// The buyer sees the vendor's submission in the AP capture queue.
	rec := rg.call(http.MethodGet, capturesPath, "", duty("receiving.ap.inquire", rg.key()))
	if !strings.Contains(rec.Body.String(), mine.ID) {
		t.Fatalf("buyer queue: %s", rec.Body.String())
	}
}

func TestVendorPortal_InvoicesShowTheVendorOnlyItsOwnWithoutInternalFields(t *testing.T) {
	rg := newRig(domain.PeriodOpen)
	p := newPortal(t, rg)
	_, inv := rg.invoiced(t, "PS-1", "INV-A", 40, "2.5", "")

	code, body := p.call(t, vendorID, http.MethodGet, "/v1/receiving/vendor/invoices", "")
	if code != http.StatusOK || !strings.Contains(body, "INV-A") || !strings.Contains(body, inv.ID) {
		t.Fatalf("vendor invoices: %d %s", code, body)
	}
	for _, internal := range []string{"ledgerEntryId", "auditReason", "postedBy", "ledgerId", "lines"} {
		if strings.Contains(body, internal) {
			t.Errorf("vendor view leaks %s: %s", internal, body)
		}
	}
	if !strings.Contains(body, `"status":"ACCEPTED"`) {
		t.Errorf("a posted invoice is ACCEPTED to the vendor: %s", body)
	}
	if code, body := p.call(t, vendorID, http.MethodGet, "/v1/receiving/vendor/invoices/"+inv.ID, ""); code != http.StatusOK || !strings.Contains(body, "INV-A") {
		t.Fatalf("own invoice by id: %d %s", code, body)
	}
	if code, body := p.call(t, otherVendorID, http.MethodGet, "/v1/receiving/vendor/invoices", ""); code != http.StatusOK || strings.Contains(body, "INV-A") {
		t.Fatalf("another vendor lists: %d %s, want none", code, body)
	}
	if code, body := p.call(t, otherVendorID, http.MethodGet, "/v1/receiving/vendor/invoices/"+inv.ID, ""); code != http.StatusNotFound {
		t.Fatalf("another vendor's invoice by id: %d %s, want 404", code, body)
	}
}
