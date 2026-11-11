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
	"github.com/BenjaminSRussell/passage/services/receivables/internal/domain"
	"github.com/BenjaminSRussell/passage/services/receivables/internal/store"
)

const (
	custA     = "aaaaaaaa-0000-4000-8000-00000000000a"
	custB     = "bbbbbbbb-0000-4000-8000-00000000000b"
	itemA     = "11111111-0000-4000-8000-0000000000a1"
	itemB     = "11111111-0000-4000-8000-0000000000b1"
	portalTen = "00000000-0000-4000-8000-000000000001"
	portalLE  = "00000000-0000-4000-8000-000000000002"
)

// twoCustomerStore holds the rows of two customers and answers like the SQL does: ListOpenItems and ListPayments honour the
// customer filter; GetOpenItem reads any row of the tenant (the staff read), so the handler must refuse the other customer's.
// leaky ignores the filter, to prove the handler never sends a row of another customer even if a store did.
type twoCustomerStore struct {
	*fakeStore
	items    []store.OpenItem
	payments []store.Payment
	leaky    bool
	filters  []store.OpenItemFilter
	pfilters []store.PaymentFilter
	stmt     []string
}

func newTwoCustomerStore() *twoCustomerStore {
	cb := custB
	ca := custA
	return &twoCustomerStore{
		fakeStore: &fakeStore{},
		items: []store.OpenItem{
			{ID: itemA, CustomerID: custA, DocumentNumber: "INV-A-1", Currency: "USD", Amount: decimal.RequireFromString("100"), OpenAmount: decimal.RequireFromString("100"), Side: "DEBIT", Status: "OPEN", DunningLevel: 2, DunningBlock: true, Version: 5, ImportSource: strPtr("LEGACY")},
			{ID: itemB, CustomerID: custB, DocumentNumber: "INV-B-1", Currency: "USD", Amount: decimal.RequireFromString("900"), OpenAmount: decimal.RequireFromString("900"), Side: "DEBIT", Status: "OPEN"},
		},
		payments: []store.Payment{
			{ID: "pay-a", CustomerID: &ca, Amount: decimal.RequireFromString("50"), Currency: "USD", PaymentMode: "ACH", Status: "APPLIED", PayerAccountHash: strPtr("deadbeef"), SourceRef: strPtr("secret-ref")},
			{ID: "pay-b", CustomerID: &cb, Amount: decimal.RequireFromString("70"), Currency: "USD", PaymentMode: "WIRE", Status: "APPLIED"},
			{ID: "pay-none", Amount: decimal.RequireFromString("5"), Currency: "USD", PaymentMode: "CHECK", Status: "RECEIVED"},
		},
	}
}

func strPtr(s string) *string { return &s }

func (s *twoCustomerStore) ListOpenItems(_ context.Context, f store.OpenItemFilter, _ int) ([]store.OpenItem, error) {
	s.filters = append(s.filters, f)
	out := []store.OpenItem{}
	for _, o := range s.items {
		if s.leaky || f.CustomerID == "" || o.CustomerID == f.CustomerID {
			out = append(out, o)
		}
	}
	return out, nil
}

func (s *twoCustomerStore) GetOpenItem(_ context.Context, id string) (store.OpenItem, error) {
	for _, o := range s.items {
		if o.ID == id {
			return o, nil
		}
	}
	return store.OpenItem{}, httpx.NewProblem(http.StatusNotFound, store.CodeOpenItemNotFound, "open item not found")
}

func (s *twoCustomerStore) ListPayments(_ context.Context, f store.PaymentFilter, _ int) ([]store.Payment, error) {
	s.pfilters = append(s.pfilters, f)
	out := []store.Payment{}
	for _, p := range s.payments {
		if s.leaky || f.CustomerID == "" || (p.CustomerID != nil && *p.CustomerID == f.CustomerID) {
			out = append(out, p)
		}
	}
	return out, nil
}

func (s *twoCustomerStore) Statement(_ context.Context, le, customer string, _ time.Time) ([]domain.StatementSection, error) {
	s.stmt = append(s.stmt, le+"|"+customer)
	return []domain.StatementSection{}, nil
}

// portalHarness signs requests like the customer portal: a service token whose verified principal carries the customer.
type portalHarness struct {
	h      http.Handler
	signer *tenant.ServiceSigner
	store  *twoCustomerStore
}

func newPortalHarness(t *testing.T, st *twoCustomerStore) *portalHarness {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := tenant.NewServiceSigner(tenant.CustomerPortalIssuer, priv)
	if err != nil {
		t.Fatal(err)
	}
	tenant.SetServiceVerifier(tenant.NewServiceVerifier("receivables", map[string]ed25519.PublicKey{tenant.CustomerPortalIssuer: pub}))
	t.Cleanup(func() { tenant.SetServiceVerifier(nil) })
	return &portalHarness{h: router(st), signer: signer, store: st}
}

func (p *portalHarness) get(t *testing.T, customer, path string, extra map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	token, err := p.signer.Mint("receivables", tenant.Principal{TenantID: portalTen, LegalEntityID: portalLE, UserID: "contact", CustomerID: customer, Duties: map[string]struct{}{}}, "")
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set("X-Passage-Service-Token", token)
	for k, v := range extra {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	p.h.ServeHTTP(rec, req)
	return rec
}

type portalItems struct {
	Items []map[string]any `json:"items"`
}

func TestCustomerPortal_OpenItemsAreOnlyTheCallersOwn(t *testing.T) {
	for _, leaky := range []bool{false, true} {
		st := newTwoCustomerStore()
		st.leaky = leaky
		p := newPortalHarness(t, st)
		for _, tc := range []struct{ customer, other, want string }{{custA, custB, "INV-A-1"}, {custB, custA, "INV-B-1"}} {
			// The query names the other customer, as an attacker would; it must change nothing.
			rec := p.get(t, tc.customer, "/v1/receivables/customer-portal/open-items?customer="+tc.other+"&customerId="+tc.other+"&status=all", nil)
			if rec.Code != http.StatusOK {
				t.Fatalf("leaky=%v list: %d %s", leaky, rec.Code, rec.Body)
			}
			var page portalItems
			_ = json.Unmarshal(rec.Body.Bytes(), &page)
			if len(page.Items) != 1 || page.Items[0]["documentNumber"] != tc.want {
				t.Fatalf("leaky=%v customer %s sees %s, want only %s", leaky, tc.customer, rec.Body, tc.want)
			}
			if strings.Contains(rec.Body.String(), tc.other) {
				t.Errorf("the answer names the other customer: %s", rec.Body)
			}
		}
		if !leaky {
			for _, f := range st.filters {
				if f.CustomerID != custA && f.CustomerID != custB {
					t.Fatalf("the store was asked for customer %q, want the token's", f.CustomerID)
				}
			}
		}
	}
}

func TestCustomerPortal_AnotherCustomersItemIsNotFound(t *testing.T) {
	st := newTwoCustomerStore()
	p := newPortalHarness(t, st)
	if rec := p.get(t, custA, "/v1/receivables/customer-portal/open-items/"+itemA, nil); rec.Code != http.StatusOK {
		t.Fatalf("own item: %d %s", rec.Code, rec.Body)
	}
	rec := p.get(t, custA, "/v1/receivables/customer-portal/open-items/"+itemB, nil)
	if rec.Code != http.StatusNotFound || strings.Contains(rec.Body.String(), "INV-B-1") {
		t.Fatalf("another customer's item: %d %s, want 404 without its content", rec.Code, rec.Body)
	}
	missing := p.get(t, custA, "/v1/receivables/customer-portal/open-items/11111111-0000-4000-8000-0000000000ff", nil)
	// Only the echoed request path differs; the code and the words are the same.
	if missing.Code != rec.Code || strings.ReplaceAll(missing.Body.String(), "0000000000ff", "0000000000b1") != rec.Body.String() {
		t.Errorf("an item of another customer answers differently from a missing one: %d %s vs %d %s", rec.Code, rec.Body, missing.Code, missing.Body)
	}
}

func TestCustomerPortal_PaymentsAreOnlyTheCallersOwnAndNeverExposeInternals(t *testing.T) {
	for _, leaky := range []bool{false, true} {
		st := newTwoCustomerStore()
		st.leaky = leaky
		p := newPortalHarness(t, st)
		rec := p.get(t, custA, "/v1/receivables/customer-portal/payments?customer="+custB, nil)
		var page portalItems
		_ = json.Unmarshal(rec.Body.Bytes(), &page)
		if rec.Code != http.StatusOK || len(page.Items) != 1 || page.Items[0]["id"] != "pay-a" {
			t.Fatalf("leaky=%v payments: %d %s", leaky, rec.Code, rec.Body)
		}
		for _, internal := range []string{"deadbeef", "secret-ref", "payerAccountHash", "sourceRef", "pay-b", "pay-none"} {
			if strings.Contains(rec.Body.String(), internal) {
				t.Errorf("payments expose %q: %s", internal, rec.Body)
			}
		}
	}
}

func TestCustomerPortal_OpenItemsHideInternalState(t *testing.T) {
	p := newPortalHarness(t, newTwoCustomerStore())
	rec := p.get(t, custA, "/v1/receivables/customer-portal/open-items/"+itemA, nil)
	for _, internal := range []string{"dunning", "LEGACY", "importSource", "legacyKey", "version", "customerId", "legalEntityId"} {
		if strings.Contains(rec.Body.String(), internal) {
			t.Errorf("an open item exposes %q: %s", internal, rec.Body)
		}
	}
}

func TestCustomerPortal_StatementIsForTheTokensCustomerAndEntity(t *testing.T) {
	st := newTwoCustomerStore()
	p := newPortalHarness(t, st)
	other := "00000000-0000-4000-8000-0000000000ee"
	rec := p.get(t, custA, "/v1/receivables/customer-portal/statement?customer="+custB+"&legalEntityId="+other, nil)
	if rec.Code != http.StatusOK || len(st.stmt) != 1 || st.stmt[0] != portalLE+"|"+custA {
		t.Fatalf("statement: %d %s asked %v, want the token's entity and customer", rec.Code, rec.Body, st.stmt)
	}
	if rec := p.get(t, custA, "/v1/receivables/customer-portal/statement?asOf=yesterday", nil); rec.Code != http.StatusBadRequest {
		t.Errorf("bad asOf: %d", rec.Code)
	}
}

func TestCustomerPortal_OnlyTheCustomerPortalTokenReachesTheRoutes(t *testing.T) {
	st := newTwoCustomerStore()
	h := router(st)
	paths := []string{"/v1/receivables/customer-portal/open-items", "/v1/receivables/customer-portal/open-items/" + itemA,
		"/v1/receivables/customer-portal/payments", "/v1/receivables/customer-portal/statement"}
	for _, path := range paths {
		// A staff user with every receivables duty is not a customer: no customer comes from a header or a query.
		rec := do(h, http.MethodGet, path+"?customer="+custA, "", "receivables.view,receivables.customer-portal.read", map[string]string{"X-Passage-Customer": custA})
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s as a user with the duty: %d %s, want 403", path, rec.Code, rec.Body)
		}
		if rec := do(h, http.MethodGet, path, "", "receivables.view", nil); rec.Code != http.StatusForbidden {
			t.Errorf("%s as staff: %d, want 403", path, rec.Code)
		}
	}
	if len(st.filters)+len(st.pfilters)+len(st.stmt) != 0 {
		t.Error("the store was read for a caller without a customer")
	}
}

func TestCustomerPortal_TokenCannotReachStaffRoutes(t *testing.T) {
	p := newPortalHarness(t, newTwoCustomerStore())
	for _, path := range []string{"/v1/receivables/open-items", "/v1/receivables/payments", "/v1/receivables/statements?legalEntityId=" + portalLE + "&customer=" + custB, "/v1/receivables/aging", "/v1/receivables/open-items/" + itemB} {
		if rec := p.get(t, custA, path, map[string]string{"X-Passage-Duties": "receivables.view"}); rec.Code != http.StatusForbidden {
			t.Errorf("%s with a customer portal token: %d %s, want 403", path, rec.Code, rec.Body)
		}
	}
}
