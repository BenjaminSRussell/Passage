package api

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/sales/internal/store"
)

const (
	custA  = "aaaaaaaa-0000-4000-8000-00000000000a"
	custB  = "bbbbbbbb-0000-4000-8000-00000000000b"
	ordA   = "0195f0a0-0000-7000-8000-0000000000a1"
	ordB   = "0195f0a0-0000-7000-8000-0000000000b1"
	ordA0  = "0195f0a0-0000-7000-8000-0000000000a2"
	retA   = "0195f0a0-0000-7000-8000-0000000000c1"
	retB   = "0195f0a0-0000-7000-8000-0000000000c2"
	portLE = "00000000-0000-4000-8000-0000000000b1"
)

// customerStore holds the rows of two customers and answers like the SQL does: the lists honour the customer filter (and
// ExcludeDraft) unless leaky, which ignores them so the tests can prove the handler never sends another customer's row even if a
// store did. GetOrder and GetReturnOrder read any row of the tenant, as the staff reads do.
type customerStore struct {
	store.Store
	leaky   bool
	orders  []store.Order
	returns []store.ReturnOrder
	filters []store.OrderFilter
	rfilter []store.ReturnOrderFilter
}

func newCustomerStore(leaky bool) *customerStore {
	mkOrder := func(id, cust, status, number string) store.Order {
		return store.Order{ID: id, CustomerID: cust, InvoiceAccountID: cust, OrderNumber: number, Status: status, Currency: "USD", Channel: "EDI-SECRET",
			NetTotal: decimal.RequireFromString("10"), Version: 4, Holds: []store.Hold{{HoldType: "CREDIT"}}, CreditCheck: json.RawMessage(`{"exposure":"secret"}`),
			Lines: []store.Line{{ID: "l1", LineNo: 1, ProductID: "prod-secret", Quantity: decimal.NewFromInt(2), Unit: "EA", WarehouseID: "wh-secret", LineNet: decimal.RequireFromString("10"), LineStatus: "OPEN"}}}
	}
	mkReturn := func(id, cust, number string) store.ReturnOrder {
		cost := decimal.RequireFromString("7")
		return store.ReturnOrder{ID: id, CustomerID: cust, RMANumber: number, Status: "AUTHORIZED", Currency: "USD", CreatedBy: "clerk-secret", JournalPostingID: strPtr("journal-secret"),
			Lines: []store.ReturnLine{{LineNo: 1, ProductID: "prod-secret", WarehouseID: "wh-secret", Unit: "EA", UnitCost: &cost}}}
	}
	return &customerStore{leaky: leaky,
		orders:  []store.Order{mkOrder(ordA, custA, "CONFIRMED", "SO-A-1"), mkOrder(ordB, custB, "CONFIRMED", "SO-B-1"), mkOrder(ordA0, custA, "DRAFT", "SO-A-DRAFT")},
		returns: []store.ReturnOrder{mkReturn(retA, custA, "RMA-A-1"), mkReturn(retB, custB, "RMA-B-1")}}
}

func (s *customerStore) GetOrder(_ context.Context, id string) (store.Order, error) {
	for _, o := range s.orders {
		if o.ID == id {
			return o, nil
		}
	}
	return store.Order{}, httpx.NewProblem(http.StatusNotFound, store.CodeOrderNotFound, "order not found")
}

func (s *customerStore) ListOrders(_ context.Context, f store.OrderFilter) (store.OrderPage, error) {
	s.filters = append(s.filters, f)
	page := store.OrderPage{Items: []store.OrderSummary{}}
	for _, o := range s.orders {
		if s.leaky || ((f.CustomerID == "" || o.CustomerID == f.CustomerID) && (!f.ExcludeDraft || o.Status != "DRAFT")) {
			page.Items = append(page.Items, store.OrderSummary{ID: o.ID, OrderNumber: o.OrderNumber, CustomerID: o.CustomerID, Status: o.Status, Currency: o.Currency,
				NetTotal: o.NetTotal, ActiveHolds: []string{"CREDIT"}, Version: o.Version, Channel: o.Channel})
		}
	}
	return page, nil
}

func (s *customerStore) GetReturnOrder(_ context.Context, id string) (store.ReturnOrder, error) {
	for _, o := range s.returns {
		if o.ID == id {
			return o, nil
		}
	}
	return store.ReturnOrder{}, httpx.NewProblem(http.StatusNotFound, store.CodeRMANotFound, "return order not found")
}

func (s *customerStore) ListReturnOrders(_ context.Context, f store.ReturnOrderFilter) ([]store.ReturnOrder, error) {
	s.rfilter = append(s.rfilter, f)
	out := []store.ReturnOrder{}
	for _, o := range s.returns {
		if s.leaky || f.CustomerID == "" || o.CustomerID == f.CustomerID {
			out = append(out, o)
		}
	}
	return out, nil
}

type customerHarness struct {
	h      http.Handler
	signer *tenant.ServiceSigner
}

func newCustomerHarness(t *testing.T, s store.Store) *customerHarness {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := tenant.NewServiceSigner(tenant.CustomerPortalIssuer, priv)
	if err != nil {
		t.Fatal(err)
	}
	tenant.SetServiceVerifier(tenant.NewServiceVerifier("sales", map[string]ed25519.PublicKey{tenant.CustomerPortalIssuer: pub}))
	t.Cleanup(func() { tenant.SetServiceVerifier(nil) })
	r := chi.NewRouter()
	r.Use(tenant.Middleware)
	New(s).Register(r)
	return &customerHarness{h: r, signer: signer}
}

func (c *customerHarness) get(t *testing.T, customer, path string, extra map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	token, err := c.signer.Mint("sales", tenant.Principal{TenantID: testkit.TenantID(1), LegalEntityID: portLE, UserID: "contact", CustomerID: customer, Duties: map[string]struct{}{}}, "")
	if err != nil {
		t.Fatal(err)
	}
	return c.getWith(path, token, extra)
}

func (c *customerHarness) getWith(path, token string, extra map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if token != "" {
		req.Header.Set("X-Passage-Service-Token", token)
	}
	for k, v := range extra {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	c.h.ServeHTTP(rec, req)
	return rec
}

type itemsOf struct {
	Items []map[string]any `json:"items"`
}

func TestCustomerPortal_OrdersAreOnlyTheCallersOwnAndNotDraft(t *testing.T) {
	for _, leaky := range []bool{false, true} {
		s := newCustomerStore(leaky)
		c := newCustomerHarness(t, s)
		for _, tc := range []struct{ customer, other, want string }{{custA, custB, "SO-A-1"}, {custB, custA, "SO-B-1"}} {
			rec := c.get(t, tc.customer, customerPortal+"/orders?customerId="+tc.other, nil)
			var page itemsOf
			_ = json.Unmarshal(rec.Body.Bytes(), &page)
			if rec.Code != http.StatusOK || len(page.Items) != 1 || page.Items[0]["orderNumber"] != tc.want {
				t.Fatalf("leaky=%v customer %s sees %d %s, want only %s", leaky, tc.customer, rec.Code, rec.Body, tc.want)
			}
			for _, internal := range []string{"CREDIT", "EDI-SECRET", "activeHolds", "version", "customerId", "legalEntityId"} {
				if strings.Contains(rec.Body.String(), internal) {
					t.Errorf("the order list exposes %q: %s", internal, rec.Body)
				}
			}
		}
		if rec := c.get(t, custA, customerPortal+"/orders?status=DRAFT", nil); rec.Code != http.StatusBadRequest {
			t.Errorf("a draft status is refused: %d", rec.Code)
		}
		if !leaky {
			for _, f := range s.filters {
				if (f.CustomerID != custA && f.CustomerID != custB) || !f.ExcludeDraft {
					t.Fatalf("the store was asked %+v, want the token's customer and no drafts", f)
				}
			}
		}
	}
}

func TestCustomerPortal_AnotherCustomersOrderAndADraftAreNotFound(t *testing.T) {
	c := newCustomerHarness(t, newCustomerStore(false))
	rec := c.get(t, custA, customerPortal+"/orders/"+ordA, nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "SO-A-1") {
		t.Fatalf("own order: %d %s", rec.Code, rec.Body)
	}
	for _, internal := range []string{"prod-secret", "wh-secret", "warehouseId", "productId", "holds", "creditCheck", "exposure", "EDI-SECRET", "invoiceAccountId"} {
		if strings.Contains(rec.Body.String(), internal) {
			t.Errorf("the order exposes %q: %s", internal, rec.Body)
		}
	}
	cases := map[string]*httptest.ResponseRecorder{
		"another customer's": c.get(t, custA, customerPortal+"/orders/"+ordB, nil),
		"a draft":            c.get(t, custA, customerPortal+"/orders/"+ordA0, nil),
		"unknown":            c.get(t, custA, customerPortal+"/orders/0195f0a0-0000-7000-8000-0000000000ff", nil),
		"not an id":          c.get(t, custA, customerPortal+"/orders/x", nil),
	}
	for name, rec := range cases {
		var p struct{ Code string }
		_ = json.Unmarshal(rec.Body.Bytes(), &p)
		if rec.Code != http.StatusNotFound || p.Code != store.CodeOrderNotFound || strings.Contains(rec.Body.String(), "SO-B-1") {
			t.Errorf("%s order: %d %s, want 404 %s", name, rec.Code, rec.Body, store.CodeOrderNotFound)
		}
	}
}

func TestCustomerPortal_ReturnsAreOnlyTheCallersOwnAndHideCosts(t *testing.T) {
	for _, leaky := range []bool{false, true} {
		s := newCustomerStore(leaky)
		c := newCustomerHarness(t, s)
		rec := c.get(t, custA, customerPortal+"/returns?customerId="+custB, nil)
		var page itemsOf
		_ = json.Unmarshal(rec.Body.Bytes(), &page)
		if rec.Code != http.StatusOK || len(page.Items) != 1 || page.Items[0]["rmaNumber"] != "RMA-A-1" {
			t.Fatalf("leaky=%v returns: %d %s", leaky, rec.Code, rec.Body)
		}
		for _, internal := range []string{"prod-secret", "wh-secret", "unitCost", "clerk-secret", "journal-secret", "RMA-B-1", "createdBy"} {
			if strings.Contains(rec.Body.String(), internal) {
				t.Errorf("returns expose %q: %s", internal, rec.Body)
			}
		}
		if !leaky && (len(s.rfilter) != 1 || s.rfilter[0].CustomerID != custA || s.rfilter[0].LegalEntityID != portLE) {
			t.Fatalf("the store was asked %+v, want the token's customer and entity", s.rfilter)
		}
	}
	c := newCustomerHarness(t, newCustomerStore(false))
	if rec := c.get(t, custA, customerPortal+"/returns/"+retA, nil); rec.Code != http.StatusOK {
		t.Fatalf("own return: %d %s", rec.Code, rec.Body)
	}
	rec := c.get(t, custA, customerPortal+"/returns/"+retB, nil)
	var p struct{ Code string }
	_ = json.Unmarshal(rec.Body.Bytes(), &p)
	if rec.Code != http.StatusNotFound || p.Code != store.CodeRMANotFound || strings.Contains(rec.Body.String(), "RMA-B-1") {
		t.Fatalf("another customer's return: %d %s", rec.Code, rec.Body)
	}
}

func TestCustomerPortal_OnlyTheCustomerPortalTokenReachesTheRoutes(t *testing.T) {
	s := newCustomerStore(false)
	paths := []string{"/orders", "/orders/" + ordA, "/returns", "/returns/" + retA}
	for _, path := range paths {
		// A staff user with every duty, naming a customer in a header and a query, is not a customer.
		rec := serve(t, s, http.MethodGet, customerPortal+path+"?customerId="+custA, principal(dutyCustomerPortal, dutyRead, dutyWrite), map[string]string{"X-Passage-Customer": custA})
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s as a user: %d %s, want 403", path, rec.Code, rec.Body)
		}
		// A principal that carries a customer id without the portal's signature is not trusted either.
		p := principal(dutyCustomerPortal)
		p.CustomerID = custA
		if rec := serve(t, s, http.MethodGet, customerPortal+path, p, nil); rec.Code != http.StatusForbidden {
			t.Errorf("%s as an unsigned customer principal: %d %s, want 403", path, rec.Code, rec.Body)
		}
	}
	if len(s.filters)+len(s.rfilter) != 0 {
		t.Error("the store was read for a caller without a customer")
	}
}

func TestCustomerPortal_TokenCannotReachStaffRoutes(t *testing.T) {
	c := newCustomerHarness(t, newCustomerStore(false))
	for _, path := range []string{"/v1/sales/orders", "/v1/sales/orders/" + ordB, "/v1/sales/return-orders", "/v1/sales/return-orders/" + retB,
		"/v1/sales/credit-profiles/" + custB, "/v1/sales/orders?customerId=" + custB} {
		if rec := c.get(t, custA, path, map[string]string{"X-Passage-Duties": dutyRead}); rec.Code != http.StatusForbidden {
			t.Errorf("%s with a customer portal token: %d %s, want 403", path, rec.Code, rec.Body)
		}
	}
}
