package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/services/edi/internal/domain"
	"github.com/BenjaminSRussell/passage/services/edi/internal/store"
)

type fakeAccessPoint struct {
	submitted []string
	err       error
}

func (f *fakeAccessPoint) Submit(_ context.Context, mandate, number, doc string) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	f.submitted = append(f.submitted, mandate+":"+number)
	if !strings.Contains(doc, number) {
		return "", errors.New("document does not carry its number")
	}
	return "AP-" + number, nil
}

const (
	sellerProfile = `{"legalEntityId":"` + srcLE + `","party":{"name":"Demo Manufacturer","legalName":"Demo Manufacturer Inc","endpointScheme":"0088","endpointId":"0614141000012",
"countryCode":"US","vatId":"US123456789","legalId":"US-123","streetName":"1 Mill Rd","cityName":"Cedar Ridge","postalZone":"12345","contactName":"AR","contactEmail":"ar@example.test"},"iban":"DE89370400440532013000","paymentMeansCode":"58"}`
	buyerProfile = `{"legalEntityId":"` + srcLE + `","party":{"name":"Demo Distributor","endpointScheme":"0088","endpointId":"0614141000029",
"countryCode":"US","legalId":"US-987","streetName":"9 Dock St","cityName":"Harbor","postalZone":"54321"},"buyerReference":"BUYER-REF-1"}`
)

func eiSetup(t *testing.T, f *fakeSources) (*API, http.Handler) {
	t.Helper()
	a, h, _, _, _ := autoSetup(t, f)
	f.invoice.Currency, f.invoice.DueDate = "USD", "2026-11-20"
	f.invoice.Lines[0].Description = "Pump seal kit"
	for role, body := range map[string]string{"SELLER/" + srcLE: sellerProfile, "BUYER/" + srcCustomer: buyerProfile} {
		if rec := do(t, h, http.MethodPut, "/edi/v1/einvoice/parties/"+role, body, "edi.einvoice.operate", srcScope); rec.Code != http.StatusOK {
			t.Fatalf("party %s: %d %s", role, rec.Code, rec.Body.String())
		}
	}
	return a, h
}

func createEI(t *testing.T, h http.Handler, mandate string) (int, store.EInvoice) {
	t.Helper()
	body := `{"legalEntityId":"` + srcLE + `","invoiceId":"` + srcInvoice + `","mandate":"` + mandate + `"}`
	rec := do(t, h, http.MethodPost, "/edi/v1/einvoice/invoices", body, "edi.einvoice.operate", srcScope)
	var e store.EInvoice
	_ = json.Unmarshal(rec.Body.Bytes(), &e)
	return rec.Code, e
}

func TestEInvoice_PeppolBuildStoreSubmitAndAnswer(t *testing.T) {
	f := newFake()
	a, h := eiSetup(t, f)
	ap := &fakeAccessPoint{}

	code, e := createEI(t, h, "PEPPOL")
	if code != http.StatusCreated || e.Status != "VALID" || e.Format != "UBL21" {
		t.Fatalf("create: %d %+v", code, e)
	}
	for _, want := range []string{"<cbc:ID>INV-1</cbc:ID>", "urn:fdc:peppol.eu:2017:poacc:billing:3.0", "PO-1001", "Pump seal kit", "<cbc:DocumentCurrencyCode>USD</cbc:DocumentCurrencyCode>"} {
		if !strings.Contains(e.XML, want) {
			t.Errorf("UBL lacks %q", want)
		}
	}
	// The payable equals billing's gross.
	if !strings.Contains(e.XML, `<cbc:PayableAmount currencyID="USD">86.80</cbc:PayableAmount>`) {
		t.Errorf("payable is not billing's gross: %s", e.XML)
	}
	// A repeat returns the stored one.
	code2, again := createEI(t, h, "PEPPOL")
	if code2 != http.StatusOK || again.ID != e.ID {
		t.Fatalf("repeat: %d %s vs %s", code2, again.ID, e.ID)
	}

	// No access point: 503 and nothing changes.
	sub := "/edi/v1/einvoice/invoices/" + e.ID + ":submit"
	if rec := do(t, h, http.MethodPost, sub, "", "edi.einvoice.operate", srcScope); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("no access point: %d %s", rec.Code, rec.Body.String())
	}
	a.AccessPoint = ap
	rec := do(t, h, http.MethodPost, sub, "", "edi.einvoice.operate", srcScope)
	var sent store.EInvoice
	_ = json.Unmarshal(rec.Body.Bytes(), &sent)
	if rec.Code != http.StatusOK || sent.Status != "SUBMITTED" || sent.Reference != "AP-INV-1" || strings.Join(ap.submitted, ",") != "PEPPOL:INV-1" {
		t.Fatalf("submit: %d %s %v", rec.Code, rec.Body.String(), ap.submitted)
	}
	if rec := do(t, h, http.MethodPost, sub, "", "edi.einvoice.operate", srcScope); rec.Code != http.StatusConflict {
		t.Fatalf("a submitted e-invoice is not submitted again: %d", rec.Code)
	}

	// The network rejects, the invoice is submitted again and accepted.
	resp := "/edi/v1/einvoice/invoices/" + e.ID + ":response"
	if rec := do(t, h, http.MethodPost, resp, `{"outcome":"REJECTED"}`, "edi.einvoice.operate", srcScope); rec.Code != http.StatusBadRequest {
		t.Fatalf("a rejection needs its reason: %d", rec.Code)
	}
	if rec := do(t, h, http.MethodPost, resp, `{"outcome":"REJECTED","reason":"PEPPOL-EN16931-R001 missing"}`, "edi.einvoice.operate", srcScope); rec.Code != http.StatusOK {
		t.Fatalf("reject: %d %s", rec.Code, rec.Body.String())
	}
	if rec := do(t, h, http.MethodPost, sub, "", "edi.einvoice.operate", srcScope); rec.Code != http.StatusOK {
		t.Fatalf("resubmit: %d %s", rec.Code, rec.Body.String())
	}
	if rec := do(t, h, http.MethodPost, resp, `{"outcome":"ACCEPTED","reference":"MLR-1"}`, "edi.einvoice.operate", srcScope); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"ACCEPTED"`) {
		t.Fatalf("accept: %d %s", rec.Code, rec.Body.String())
	}
	if rec := do(t, h, http.MethodPost, resp, `{"outcome":"ACCEPTED"}`, "edi.einvoice.operate", srcScope); rec.Code != http.StatusConflict {
		t.Fatalf("answered twice: %d", rec.Code)
	}
	list := do(t, h, http.MethodGet, "/edi/v1/einvoice/invoices?status=ACCEPTED", "", "edi.einvoice.operate", srcScope)
	if !strings.Contains(list.Body.String(), e.ID) || strings.Contains(list.Body.String(), "<Invoice") {
		t.Fatalf("list: %s", list.Body.String())
	}
}

func TestEInvoice_MissingProfileBlockingIssuesAndHooks(t *testing.T) {
	f := newFake()
	a, h := eiSetup(t, f)

	// A buyer with no profile is a conflict, not a half-built document.
	f.invoice.CustomerID = srcCustomer
	other := *f
	other.invoice.CustomerID = "00000000-0000-0000-0000-0000000000c9"
	a.Sources = &other
	if code, _ := createEI(t, h, "PEPPOL"); code != http.StatusConflict {
		t.Fatalf("no buyer profile: %d", code)
	}
	a.Sources = f

	// An unknown mandate is a validation error.
	if code, _ := createEI(t, h, "ZATCA"); code != http.StatusBadRequest {
		t.Fatalf("unknown mandate: %d", code)
	}

	// The Italian mandate is a hook without a generator: stored INVALID with EINV-0150.
	code, e := createEI(t, h, "IT_SDI")
	if code != http.StatusCreated || e.Status != "INVALID" || len(e.Issues) != 1 || e.Issues[0].Code != domain.CodeMandateHook {
		t.Fatalf("hook: %d %+v", code, e)
	}
	a.AccessPoint = &fakeAccessPoint{}
	if rec := do(t, h, http.MethodPost, "/edi/v1/einvoice/invoices/"+e.ID+":submit", "", "edi.einvoice.operate", srcScope); rec.Code != http.StatusConflict {
		t.Fatalf("an invalid e-invoice is not submitted: %d", rec.Code)
	}
	// The country pack installs its generator; building again replaces the INVALID document.
	a.Mandates = domain.DefaultMandates(domain.HookMandate{MandateCode: domain.MandateITSDI, MandateFormat: "FATTURAPA",
		Generate: func(inv domain.UBLInvoice) (string, []domain.Issue) {
			return "<p:FatturaElettronica>" + inv.ID + "</p:FatturaElettronica>", nil
		}})
	code, fixed := createEI(t, h, "IT_SDI")
	if code != http.StatusOK || fixed.ID != e.ID || fixed.Status != "VALID" || fixed.Format != "FATTURAPA" || fixed.Version != 2 {
		t.Fatalf("rebuilt: %d %+v", code, fixed)
	}

	// A mixed-rate invoice cannot be mapped: INVALID with EINV-0113.
	f.invoice.ID = "00000000-0000-0000-0000-0000000000b9"
	f.invoice.Lines = append(f.invoice.Lines, domain.SourceInvoiceLine{LineNo: 2, Qty: decimal.NewFromInt(1), UnitPrice: decimal.NewFromInt(10), Net: decimal.NewFromInt(10),
		Tax: decimal.Zero, OrderID: srcOrder, OrderLineID: "ol-1", ProductID: "prod-a"})
	f.invoice.Totals = domain.SourceInvoiceTotals{Net: decimal.NewFromInt(90), Tax: decimal.RequireFromString("6.80"), Gross: decimal.RequireFromString("96.80")}
	body := `{"legalEntityId":"` + srcLE + `","invoiceId":"` + f.invoice.ID + `","mandate":"PEPPOL"}`
	rec := do(t, h, http.MethodPost, "/edi/v1/einvoice/invoices", body, "edi.einvoice.operate", srcScope)
	if rec.Code != http.StatusCreated || !strings.Contains(rec.Body.String(), domain.CodeMixedRates) {
		t.Fatalf("mixed rates: %d %s", rec.Code, rec.Body.String())
	}
}

func TestEInvoice_PartyRoutesValidateAndScope(t *testing.T) {
	_, h := eiSetup(t, newFake())
	bad := `{"legalEntityId":"` + srcLE + `","party":{"name":"","countryCode":"usa"}}`
	if rec := do(t, h, http.MethodPut, "/edi/v1/einvoice/parties/BUYER/"+srcCustomer, bad, "edi.einvoice.operate", srcScope); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad party: %d", rec.Code)
	}
	// The seller profile of an entity is stored under that entity.
	if rec := do(t, h, http.MethodPut, "/edi/v1/einvoice/parties/SELLER/"+srcCustomer, sellerProfile, "edi.einvoice.operate", srcScope); rec.Code != http.StatusBadRequest {
		t.Fatalf("seller under another subject: %d", rec.Code)
	}
	if rec := do(t, h, http.MethodGet, "/edi/v1/einvoice/parties/BUYER/"+srcCustomer+"?legalEntityId="+srcLE, "", "edi.einvoice.operate", srcScope); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "BUYER-REF-1") {
		t.Fatalf("get: %d %s", rec.Code, rec.Body.String())
	}
	// A caller of another legal entity cannot write this entity's profile.
	other := map[string]string{"X-Passage-Legal-Entity": "00000000-0000-0000-0000-0000000000e2"}
	if rec := do(t, h, http.MethodPut, "/edi/v1/einvoice/parties/BUYER/"+srcCustomer, buyerProfile, "edi.einvoice.operate", other); rec.Code != http.StatusBadRequest {
		t.Fatalf("outside scope: %d", rec.Code)
	}
}
