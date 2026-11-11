package api_test

import (
	"context"
	"crypto/ed25519"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/payables/internal/api"
	"github.com/BenjaminSRussell/passage/services/payables/internal/store"
)

const (
	portalVendorA = "0b1c2d3e-0000-4000-8000-0000000000aa"
	portalVendorB = "0b1c2d3e-0000-4000-8000-0000000000bb"
	portalLE      = "00000000-0000-0000-0000-0000000000a1"
	itemX         = "11111111-1111-4111-8111-1111111111e1"
)

type fakeVendorPayments struct {
	vendors []string
	filters []store.VendorPaymentFilter
}

func (f *fakeVendorPayments) VendorPayments(_ context.Context, vendorID string, fl store.VendorPaymentFilter) ([]store.VendorPayment, error) {
	f.vendors, f.filters = append(f.vendors, vendorID), append(f.filters, fl)
	mk := func(n, status string) store.VendorPayment {
		return store.VendorPayment{InvoiceID: itemX, InvoiceNumber: n, InvoiceDate: "2027-03-01", DueDate: "2027-03-31", Currency: "USD", Amount: decimal.RequireFromString("100"), OpenAmount: decimal.RequireFromString("100"), Status: status}
	}
	return []store.VendorPayment{mk("INV-1", store.PaymentOpen), mk("INV-2", store.PaymentPaid)}, nil
}

type portalRig struct {
	h      http.Handler
	f      *fakeVendorPayments
	signer *tenant.ServiceSigner
}

func newPortalRig(t *testing.T) *portalRig {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := tenant.NewServiceSigner(tenant.VendorPortalIssuer, priv)
	if err != nil {
		t.Fatal(err)
	}
	tenant.SetServiceVerifier(tenant.NewServiceVerifier("payables", map[string]ed25519.PublicKey{tenant.VendorPortalIssuer: pub}))
	t.Cleanup(func() { tenant.SetServiceVerifier(nil) })
	f := &fakeVendorPayments{}
	a := api.New(fakeDB{}, fakeValidator{}, fixedNow).WithVendorPayments(f)
	r := httpx.NewRouter()
	a.Register(r)
	return &portalRig{h: r, f: f, signer: signer}
}

func (p *portalRig) get(t *testing.T, vendor, path string) *httptest.ResponseRecorder {
	t.Helper()
	token, err := p.signer.Mint("payables", tenant.Principal{TenantID: testkit.TenantID(1), LegalEntityID: portalLE, UserID: "vendor-user", VendorID: vendor, Duties: map[string]struct{}{}}, "")
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set("X-Passage-Service-Token", token)
	rec := httptest.NewRecorder()
	p.h.ServeHTTP(rec, req)
	return rec
}

func TestVendorPayments_TheVendorIsTheTokensNeverTheQueryAndAnswersCarryNoInternals(t *testing.T) {
	p := newPortalRig(t)
	rec := p.get(t, portalVendorA, "/v1/payables/vendor/payments?vendorId="+portalVendorB+"&legalEntityId=x")
	if rec.Code != http.StatusOK || len(p.f.vendors) != 1 || p.f.vendors[0] != portalVendorA {
		t.Fatalf("list: %d %s, store saw %v", rec.Code, rec.Body, p.f.vendors)
	}
	body := rec.Body.String()
	for _, banned := range []string{"vendorId", "hold", "proposal", "legalEntityId"} {
		if strings.Contains(body, banned) {
			t.Errorf("the vendor's answer mentions %q: %s", banned, body)
		}
	}
	if !strings.Contains(body, `"invoiceNumber":"INV-1"`) || !strings.Contains(body, `"status":"PAID"`) {
		t.Errorf("answer %s", body)
	}
}

func TestVendorPayments_PagesAndFiltersAreValidated(t *testing.T) {
	p := newPortalRig(t)
	if rec := p.get(t, portalVendorA, "/v1/payables/vendor/payments?limit=1"); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"nextCursor"`) {
		t.Fatalf("a page of one over two rows has a next cursor: %d %s", rec.Code, rec.Body)
	}
	if got := p.f.filters[0]; got.Limit != 1 {
		t.Errorf("limit %d", got.Limit)
	}
	for _, q := range []string{"status=BOGUS", "limit=0", "limit=500", "cursor=!!", "cursor=" + "MjAyNy0wMy0wMXxub3Q"} {
		if rec := p.get(t, portalVendorA, "/v1/payables/vendor/payments?"+q); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d %s, want 400", q, rec.Code, rec.Body)
		}
	}
	if len(p.f.vendors) != 1 {
		t.Errorf("a refused query reached the store: %v", p.f.vendors)
	}
}

func TestVendorPayments_OnlyTheSupplierPortalWithAVendorMayRead(t *testing.T) {
	p := newPortalRig(t)
	// A buyer with every duty it can hold is not a supplier, and the open-item duty does not open the route.
	if rec := do(p.h, http.MethodGet, "/v1/payables/vendor/payments", "", true, "payables.vendor-portal.read"); rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), api.CodeVendorScope) {
		t.Errorf("a header caller holding the portal duty: %d %s", rec.Code, rec.Body)
	}
	if rec := do(p.h, http.MethodGet, "/v1/payables/vendor/payments", "", true, "open-item.read"); rec.Code != http.StatusForbidden {
		t.Errorf("a buyer with open-item.read: %d %s", rec.Code, rec.Body)
	}
	if rec := do(p.h, http.MethodGet, "/v1/payables/vendor/payments", "", false, ""); rec.Code == http.StatusOK {
		t.Errorf("an anonymous caller: %d", rec.Code)
	}
	if len(p.f.vendors) != 0 {
		t.Errorf("a refused caller reached the store: %v", p.f.vendors)
	}
}
