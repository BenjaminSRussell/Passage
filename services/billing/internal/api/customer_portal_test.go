package api

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/billing/internal/domain"
	"github.com/BenjaminSRussell/passage/services/billing/internal/store"
)

const (
	custA = "aaaaaaaa-0000-4000-8000-00000000000a"
	custB = "bbbbbbbb-0000-4000-8000-00000000000b"
	invA  = "0195f0a0-0000-7000-8000-0000000000a1"
	invB  = "0195f0a0-0000-7000-8000-0000000000b1"
	propA = "0195f0a0-0000-7000-8000-0000000000a2"
)

// customerStore answers ListInvoices like the SQL does (customer and posted-only filters) unless leaky, which ignores both so
// the tests can prove the handler never sends another customer's row or a proposal even if a store did.
type customerStore struct {
	*fakeStore
	leaky   bool
	filters []store.ListFilter
}

func (s *customerStore) ListInvoices(_ context.Context, f store.ListFilter) (store.InvoicePage, error) {
	s.filters = append(s.filters, f)
	page := store.InvoicePage{}
	for _, inv := range s.invoices {
		if s.leaky || ((f.CustomerID == "" || inv.CustomerID == f.CustomerID) && (!f.PostedOnly || inv.Status != domain.StatusProposal)) {
			page.Items = append(page.Items, inv)
		}
	}
	return page, nil
}

func newCustomerStore(leaky bool) *customerStore {
	s := &customerStore{fakeStore: newFakeStore(), leaky: leaky}
	mk := func(id, cust, status, number string) store.Invoice {
		return store.Invoice{ID: id, LegalEntityID: leID, CustomerID: cust, InvoiceAccountID: cust, Type: domain.TypeShipment, Status: status, Number: number,
			Currency: "USD", InvoiceDate: time.Date(2027, 3, 1, 0, 0, 0, 0, time.UTC), DueDate: time.Date(2027, 3, 31, 0, 0, 0, 0, time.UTC),
			Totals: domain.Totals{Net: decimal.RequireFromString("100"), Gross: decimal.RequireFromString("119")}, Version: 3,
			Lines: []store.Line{{Line: domain.Line{No: 1, Description: "Pump", Qty: decimal.NewFromInt(1), UnitPrice: decimal.RequireFromString("100"),
				RevenueAccount: "4000-SECRET", TaxCode: "VAT19"}, Net: decimal.RequireFromString("100"), Source: domain.Source{OrderID: "order-secret"}}}}
	}
	s.invoices[invA] = mk(invA, custA, domain.StatusPosted, "INV-A-1")
	s.invoices[invB] = mk(invB, custB, domain.StatusPosted, "INV-B-1")
	s.invoices[propA] = mk(propA, custA, domain.StatusProposal, "")
	return s
}

type customerHarness struct {
	h      http.Handler
	signer *tenant.ServiceSigner
}

func newCustomerHarness(t *testing.T, s *customerStore) *customerHarness {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := tenant.NewServiceSigner(tenant.CustomerPortalIssuer, priv)
	if err != nil {
		t.Fatal(err)
	}
	tenant.SetServiceVerifier(tenant.NewServiceVerifier("billing", map[string]ed25519.PublicKey{tenant.CustomerPortalIssuer: pub}))
	t.Cleanup(func() { tenant.SetServiceVerifier(nil) })
	r := httpx.NewRouter()
	New(s, &fakeTax{perLine: "0"}, 2000).Register(r)
	return &customerHarness{h: r, signer: signer}
}

func (c *customerHarness) get(t *testing.T, customer, path string, extra map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	token, err := c.signer.Mint("billing", tenant.Principal{TenantID: testkit.TenantID(1), LegalEntityID: leID, UserID: "contact", CustomerID: customer, Duties: map[string]struct{}{}}, "")
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set("X-Passage-Service-Token", token)
	for k, v := range extra {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	c.h.ServeHTTP(rec, req)
	return rec
}

func TestCustomerPortal_InvoicesAreOnlyTheCallersOwnAndPosted(t *testing.T) {
	for _, leaky := range []bool{false, true} {
		s := newCustomerStore(leaky)
		c := newCustomerHarness(t, s)
		for _, tc := range []struct{ customer, other, want string }{{custA, custB, "INV-A-1"}, {custB, custA, "INV-B-1"}} {
			rec := c.get(t, tc.customer, "/v1/billing/customer-portal/invoices?customerId="+tc.other+"&status=PROPOSAL", nil)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("a proposal status is refused: %d %s", rec.Code, rec.Body)
			}
			rec = c.get(t, tc.customer, "/v1/billing/customer-portal/invoices?customerId="+tc.other, nil)
			var page struct {
				Items []map[string]any `json:"items"`
			}
			_ = json.Unmarshal(rec.Body.Bytes(), &page)
			if rec.Code != http.StatusOK || len(page.Items) != 1 || page.Items[0]["number"] != tc.want {
				t.Fatalf("leaky=%v customer %s sees %d %s, want only %s", leaky, tc.customer, rec.Code, rec.Body, tc.want)
			}
		}
		if !leaky {
			for _, f := range s.filters {
				if (f.CustomerID != custA && f.CustomerID != custB) || !f.PostedOnly {
					t.Fatalf("the store was asked %+v, want the token's customer and posted only", f)
				}
			}
		}
	}
}

func TestCustomerPortal_AnotherCustomersInvoiceAndAProposalAreNotFound(t *testing.T) {
	c := newCustomerHarness(t, newCustomerStore(false))
	if rec := c.get(t, custA, "/v1/billing/customer-portal/invoices/"+invA, nil); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "INV-A-1") {
		t.Fatalf("own invoice: %d %s", rec.Code, rec.Body)
	}
	other := c.get(t, custA, "/v1/billing/customer-portal/invoices/"+invB, nil)
	proposal := c.get(t, custB, "/v1/billing/customer-portal/invoices/"+propA, nil) // a proposal of A, asked by B
	ownProposal := c.get(t, custA, "/v1/billing/customer-portal/invoices/"+propA, nil)
	missing := c.get(t, custA, "/v1/billing/customer-portal/invoices/0195f0a0-0000-7000-8000-0000000000ff", nil)
	for name, rec := range map[string]*httptest.ResponseRecorder{"another customer's": other, "another customer's proposal": proposal, "own proposal": ownProposal, "unknown": missing} {
		if rec.Code != http.StatusNotFound || strings.Contains(rec.Body.String(), "INV-B-1") {
			t.Errorf("%s invoice: %d %s, want 404", name, rec.Code, rec.Body)
		}
		var p struct{ Code string }
		_ = json.Unmarshal(rec.Body.Bytes(), &p)
		if p.Code != store.CodeNotFound {
			t.Errorf("%s invoice code %q, want the unknown-invoice code", name, p.Code)
		}
	}
}

func TestCustomerPortal_InvoicesHideInternals(t *testing.T) {
	c := newCustomerHarness(t, newCustomerStore(false))
	body := c.get(t, custA, "/v1/billing/customer-portal/invoices/"+invA, nil).Body.String()
	for _, internal := range []string{"4000-SECRET", "VAT19", "order-secret", "revenueAccount", "taxCode", "legalEntityId", "customerId", "invoiceAccountId", "version", "orderId"} {
		if strings.Contains(body, internal) {
			t.Errorf("an invoice exposes %q: %s", internal, body)
		}
	}
	if !strings.Contains(body, `"description":"Pump"`) {
		t.Errorf("the invoice lacks its lines: %s", body)
	}
}

func TestCustomerPortal_OnlyTheCustomerPortalTokenReachesTheRoutes(t *testing.T) {
	s := newCustomerStore(false)
	c := newCustomerHarness(t, s)
	for _, path := range []string{"/v1/billing/customer-portal/invoices", "/v1/billing/customer-portal/invoices/" + invA} {
		// A staff user holding every duty, with a customer named in a header and a query, is not a customer.
		rec := doAs(c.h, http.MethodGet, path+"?customerId="+custA, "", "", allDuties+",billing.customer-portal.read", map[string]string{"X-Passage-Customer": custA})
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s as a user: %d %s, want 403", path, rec.Code, rec.Body)
		}
	}
	if len(s.filters) != 0 {
		t.Error("the store was read for a caller without a customer")
	}
}

func TestCustomerPortal_TokenCannotReachStaffRoutes(t *testing.T) {
	c := newCustomerHarness(t, newCustomerStore(false))
	for _, path := range []string{"/v1/billing/invoices", "/v1/billing/invoices/" + invB, "/v1/billing/invoices/" + invB + "/trace", "/v1/billing/invoices?customerId=" + custB} {
		if rec := c.get(t, custA, path, map[string]string{"X-Passage-Duties": allDuties}); rec.Code != http.StatusForbidden {
			t.Errorf("%s with a customer portal token: %d %s, want 403", path, rec.Code, rec.Body)
		}
	}
}
