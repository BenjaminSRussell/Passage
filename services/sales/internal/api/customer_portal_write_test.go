package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/sales/internal/domain"
	"github.com/BenjaminSRussell/passage/services/sales/internal/store"
)

const (
	invA    = "0195f0a0-0000-7000-8000-0000000000e1"
	siteA   = "0195f0a0-0000-7000-8000-0000000000f1"
	whA     = "0195f0a0-0000-7000-8000-0000000000f2"
	acctA   = custA
	nsA     = "aaaaaaaa00004000800000000000000a"
	futureD = "2099-01-02"
)

// portalWriteStore holds one purchase history per customer and records what the portal asked it to store.
type portalWriteStore struct {
	*customerStore
	history      map[string][]store.PortalProduct
	historyAsked []store.PortalProductFilter
	createdOrder []store.NewOrder
	confirmed    []string
	replay       *store.Order
	createdRet   []store.NewReturnOrder
	returnErr    error
}

func newPortalWriteStore() *portalWriteStore {
	row := func(product, currency, account string) store.PortalProduct {
		return store.PortalProduct{ProductID: product, Unit: "EA", UnitPrice: decimal.RequireFromString("120"), DiscPct: decimal.RequireFromString("10"), SiteID: siteA, WarehouseID: whA,
			Currency: currency, InvoiceAccountID: account, LastOrderedAt: time.Now()}
	}
	return &portalWriteStore{customerStore: newCustomerStore(false), history: map[string][]store.PortalProduct{
		custA: {row(prodPump, "USD", acctA)},
		custB: {row(prodPump, "EUR", custB), row(prodOnlyB, "EUR", custB)},
	}}
}

func (s *portalWriteStore) PortalProducts(_ context.Context, f store.PortalProductFilter) ([]store.PortalProduct, error) {
	s.historyAsked = append(s.historyAsked, f)
	out := []store.PortalProduct{}
	for _, h := range s.history[f.CustomerID] {
		if len(f.ProductIDs) == 0 {
			out = append(out, h)
		}
		for _, id := range f.ProductIDs {
			if strings.EqualFold(id, h.ProductID) {
				out = append(out, h)
			}
		}
	}
	return out, nil
}

func (s *portalWriteStore) CreateOrder(_ context.Context, in store.NewOrder) (store.Order, bool, error) {
	s.createdOrder = append(s.createdOrder, in)
	if s.replay != nil {
		return *s.replay, false, nil
	}
	o := store.Order{ID: "0195f0a0-0000-7000-8000-0000000000aa", CustomerID: in.CustomerID, InvoiceAccountID: in.InvoiceAccountID, OrderNumber: "SO-NEW", Status: "DRAFT",
		Currency: in.Currency, Channel: in.Channel, Version: 1, RequestedShipDate: in.RequestedShipDate, Lines: in.Lines}
	for _, l := range in.Lines {
		o.NetTotal = o.NetTotal.Add(l.LineNet)
	}
	return o, true, nil
}

func (s *portalWriteStore) ConfirmOrder(_ context.Context, id string, _ int, _ string, _ time.Time) (store.ConfirmResult, error) {
	s.confirmed = append(s.confirmed, id)
	o := store.Order{ID: id, CustomerID: s.createdOrder[len(s.createdOrder)-1].CustomerID, OrderNumber: "SO-NEW", Status: "OPEN", Currency: s.createdOrder[0].Currency,
		RequestedShipDate: s.createdOrder[0].RequestedShipDate, Lines: s.createdOrder[0].Lines, Version: 2}
	for _, l := range o.Lines {
		o.NetTotal = o.NetTotal.Add(l.LineNet)
	}
	return store.ConfirmResult{Order: o, Confirmed: true}, nil
}

func (s *portalWriteStore) CreateReturnOrder(_ context.Context, in store.NewReturnOrder) (store.ReturnOrder, bool, error) {
	s.createdRet = append(s.createdRet, in)
	if s.returnErr != nil {
		return store.ReturnOrder{}, false, s.returnErr
	}
	return store.ReturnOrder{ID: retA, CustomerID: in.CustomerID, RMANumber: "RMA-NEW", Status: domain.RMARequested, Currency: "USD", OriginalInvoiceNo: "INV-1",
		Lines: []store.ReturnLine{{LineNo: 1, ProductID: in.Lines[0].ProductID, Unit: "EA", QuantityExpected: in.Lines[0].Quantity}}}, true, nil
}

type fixedGroups struct{}

func (fixedGroups) CustomerGroup(context.Context, string, string) (string, error) {
	return "0195f0a0-0000-7000-8000-0000000000a9", nil
}

type fakeNames struct {
	names map[string]string
	err   error
	asked [][]string
}

func (f *fakeNames) ProductNames(_ context.Context, _ string, ids []string) (map[string]string, error) {
	f.asked = append(f.asked, ids)
	return f.names, f.err
}

type writeHarness struct {
	h      http.Handler
	signer *tenant.ServiceSigner
	s      *portalWriteStore
}

func newWriteHarness(t *testing.T, s *portalWriteStore, names ProductNamer) *writeHarness {
	t.Helper()
	c := newCustomerHarness(t, s)
	r := chi.NewRouter()
	r.Use(tenant.Middleware)
	api := New(s).WithCustomerGroups(fixedGroups{})
	if names != nil {
		api = api.WithProductNames(names)
	}
	api.Register(r)
	return &writeHarness{h: r, signer: c.signer, s: s}
}

// post sends one write signed as the portal for customer, holding only the given duties (all the portal's when none).
func (w *writeHarness) post(t *testing.T, customer, path, key, body string, duties ...string) *httptest.ResponseRecorder {
	t.Helper()
	p := tenant.Principal{TenantID: testkit.TenantID(1), LegalEntityID: portLE, UserID: "contact", CustomerID: customer, Duties: map[string]struct{}{}}
	var only []string
	if len(duties) > 0 {
		only = duties
	}
	token, err := w.signer.MintDuties("sales", p, "", only)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("X-Passage-Service-Token", token)
	req.Header.Set("Content-Type", "application/json")
	if key != "" {
		req.Header.Set(idempotencyKey, key)
	}
	rec := httptest.NewRecorder()
	w.h.ServeHTTP(rec, req)
	return rec
}

// get sends one read signed as the portal for customer.
func (w *writeHarness) get(t *testing.T, customer, path string) *httptest.ResponseRecorder {
	t.Helper()
	token, err := w.signer.Mint("sales", tenant.Principal{TenantID: testkit.TenantID(1), LegalEntityID: portLE, UserID: "contact", CustomerID: customer, Duties: map[string]struct{}{}}, "")
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set("X-Passage-Service-Token", token)
	rec := httptest.NewRecorder()
	w.h.ServeHTTP(rec, req)
	return rec
}

func portalOrderBody(product, qty string) string {
	return `{"requestedShipDate":"` + futureD + `","externalRef":"PO-77","lines":[{"productId":"` + product + `","quantity":"` + qty + `"}]}`
}

func TestCustomerPortal_PlaceOrderIsForTheTokensCustomerOnly(t *testing.T) {
	s := newPortalWriteStore()
	w := newWriteHarness(t, s, &fakeNames{names: map[string]string{prodPump: "Centrifugal pump"}})
	rec := w.post(t, custA, customerPortal+"/orders", "submit-0001", portalOrderBody(prodPump, "3"))
	if rec.Code != http.StatusCreated {
		t.Fatalf("place order: %d %s", rec.Code, rec.Body)
	}
	if len(s.createdOrder) != 1 {
		t.Fatalf("created %d orders", len(s.createdOrder))
	}
	in := s.createdOrder[0]
	if in.CustomerID != custA || in.LegalEntityID != portLE || in.InvoiceAccountID != acctA || in.Currency != "USD" {
		t.Fatalf("the order is for %+v, want customer A in the token's entity", in)
	}
	if in.Channel != "PORTAL-"+nsA || in.IdempotencyKey == nil || *in.IdempotencyKey != nsA+":submit-0001" {
		t.Errorf("channel %q key %v are not namespaced by the customer", in.Channel, in.IdempotencyKey)
	}
	l := in.Lines[0]
	if !l.UnitPrice.Equal(decimal.RequireFromString("120")) || !l.DiscPct.Equal(decimal.RequireFromString("10")) || l.SiteID != siteA || l.WarehouseID != whA || !l.LineNet.Equal(decimal.RequireFromString("324")) {
		t.Errorf("the line took %+v, want the price, site and warehouse of the customer's last order (net 324)", l)
	}
	if in.CanOverrideListing {
		t.Error("a portal order may never override a listing")
	}
	if len(s.confirmed) != 1 {
		t.Errorf("the order was confirmed %d times, want once (credit check)", len(s.confirmed))
	}
	for _, f := range s.historyAsked {
		if f.CustomerID != custA || f.LegalEntityID != portLE {
			t.Errorf("history read for %+v, want customer A only", f)
		}
	}
	var out portalOrder
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if out.Status != "OPEN" || len(out.Lines) != 1 || out.Lines[0].ProductName != "Centrifugal pump" || out.Lines[0].ProductID != prodPump {
		t.Errorf("answer %+v", out)
	}
	for _, internal := range []string{"warehouseId", "siteId", "creditCheck", "holds", "invoiceAccountId", "SO-B"} {
		if strings.Contains(rec.Body.String(), internal) {
			t.Errorf("the placed order exposes %q: %s", internal, rec.Body)
		}
	}
}

func TestCustomerPortal_PlaceOrderRefusals(t *testing.T) {
	cases := []struct {
		name, customer, key, body string
		duties                    []string
		want                      int
		code                      string
	}{
		{"a body naming a customer", custA, "submit-0001", `{"customerId":"` + custB + `","requestedShipDate":"` + futureD + `","lines":[{"productId":"` + prodPump + `","quantity":"1"}]}`, nil, 400, "COM-0001"},
		{"a body naming a price", custA, "submit-0001", `{"requestedShipDate":"` + futureD + `","lines":[{"productId":"` + prodPump + `","quantity":"1","unitPrice":"0.01"}]}`, nil, 400, "COM-0001"},
		{"a product only another customer bought", custA, "submit-0001", portalOrderBody(prodOnlyB, "1"), nil, 422, codePortalProduct},
		{"no idempotency key", custA, "", portalOrderBody(prodPump, "1"), nil, 400, "COM-0001"},
		{"a past date", custA, "submit-0001", `{"requestedShipDate":"2001-01-01","lines":[{"productId":"` + prodPump + `","quantity":"1"}]}`, nil, 400, "COM-0001"},
		{"a zero quantity", custA, "submit-0001", portalOrderBody(prodPump, "0"), nil, 400, "COM-0001"},
		{"no lines", custA, "submit-0001", `{"requestedShipDate":"` + futureD + `","lines":[]}`, nil, 400, "COM-0001"},
		{"a token holding only the read duty", custA, "submit-0001", portalOrderBody(prodPump, "1"), []string{dutyCustomerPortal}, 403, ""},
		{"the return duty", custA, "submit-0001", portalOrderBody(prodPump, "1"), []string{dutyCustomerPortalReturn}, 403, ""},
	}
	for _, tc := range cases {
		s := newPortalWriteStore()
		w := newWriteHarness(t, s, nil)
		rec := w.post(t, tc.customer, customerPortal+"/orders", tc.key, tc.body, tc.duties...)
		var p struct{ Code string }
		_ = json.Unmarshal(rec.Body.Bytes(), &p)
		if rec.Code != tc.want || (tc.code != "" && p.Code != tc.code) {
			t.Errorf("%s: %d %s, want %d %s", tc.name, rec.Code, rec.Body, tc.want, tc.code)
		}
		if len(s.createdOrder) != 0 {
			t.Errorf("%s: an order was stored", tc.name)
		}
	}
}

// Customer B has bought the pump in EUR and customer A in USD: each is ordered at its own terms and never at the other's.
func TestCustomerPortal_PlaceOrderUsesOnlyTheCallersHistory(t *testing.T) {
	s := newPortalWriteStore()
	w := newWriteHarness(t, s, nil)
	if rec := w.post(t, custB, customerPortal+"/orders", "submit-0002", portalOrderBody(prodPump, "1")); rec.Code != http.StatusCreated {
		t.Fatalf("B: %d %s", rec.Code, rec.Body)
	}
	if rec := w.post(t, custA, customerPortal+"/orders", "submit-0002", portalOrderBody(prodOnlyB, "1")); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("A ordering B's product: %d %s", rec.Code, rec.Body)
	}
	if got := s.createdOrder[0]; got.CustomerID != custB || got.Currency != "EUR" || got.InvoiceAccountID != custB {
		t.Fatalf("B's order %+v", got)
	}
	if len(s.createdOrder) != 1 {
		t.Fatalf("%d orders stored, want only B's", len(s.createdOrder))
	}
}

func TestCustomerPortal_PlaceOrderReplayIsNotConfirmedAgain(t *testing.T) {
	s := newPortalWriteStore()
	w := newWriteHarness(t, s, nil)
	first := w.post(t, custA, customerPortal+"/orders", "submit-0003", portalOrderBody(prodPump, "1"))
	if first.Code != http.StatusCreated {
		t.Fatal(first.Body)
	}
	var out portalOrder
	_ = json.Unmarshal(first.Body.Bytes(), &out)
	s.replay = &store.Order{ID: out.ID, CustomerID: custA, Status: "OPEN", OrderNumber: "SO-NEW", Currency: "USD", Version: 2}
	rec := w.post(t, custA, customerPortal+"/orders", "submit-0003", portalOrderBody(prodPump, "1"))
	if rec.Code != http.StatusOK || len(s.confirmed) != 1 {
		t.Fatalf("replay %d, confirms %d: %s", rec.Code, len(s.confirmed), rec.Body)
	}
	// An order another customer holds under the same key is never answered.
	s.replay = &store.Order{ID: out.ID, CustomerID: custB, Status: "OPEN", OrderNumber: "SO-B-1", Currency: "USD"}
	if rec := w.post(t, custA, customerPortal+"/orders", "submit-0003", portalOrderBody(prodPump, "1")); rec.Code != http.StatusConflict || strings.Contains(rec.Body.String(), "SO-B-1") {
		t.Fatalf("foreign replay: %d %s", rec.Code, rec.Body)
	}
}

func TestCustomerPortal_ProductListIsTheCallersHistoryWithNames(t *testing.T) {
	s := newPortalWriteStore()
	w := newWriteHarness(t, s, &fakeNames{names: map[string]string{prodPump: "Centrifugal pump", prodOnlyB: "Valve"}})
	got := w.get(t, custA, customerPortal+"/products")
	var page struct {
		Items []portalProduct `json:"items"`
	}
	_ = json.Unmarshal(got.Body.Bytes(), &page)
	if got.Code != http.StatusOK || len(page.Items) != 1 || page.Items[0].ProductID != prodPump || page.Items[0].ProductName != "Centrifugal pump" || !page.Items[0].UnitPrice.Equal(decimal.RequireFromString("108")) {
		t.Fatalf("products: %d %s", got.Code, got.Body)
	}
	if strings.Contains(got.Body.String(), "Valve") || strings.Contains(got.Body.String(), prodOnlyB) {
		t.Error("the list shows another customer's product")
	}
}

func TestCustomerPortal_NamesAreBestEffort(t *testing.T) {
	s := newPortalWriteStore()
	names := &fakeNames{err: errors.New("product is down")}
	w := newWriteHarness(t, s, names)
	rec := w.post(t, custA, customerPortal+"/orders", "submit-0004", portalOrderBody(prodPump, "1"))
	if rec.Code != http.StatusCreated || strings.Contains(rec.Body.String(), "productName") {
		t.Fatalf("an unreachable product service must not fail the order: %d %s", rec.Code, rec.Body)
	}
	w = newWriteHarness(t, newPortalWriteStore(), &fakeNames{names: map[string]string{prodPump: "Centrifugal pump"}})
	if got := w.get(t, custA, customerPortal+"/orders/"+ordA); !strings.Contains(got.Body.String(), `"productName":"Centrifugal pump"`) || !strings.Contains(got.Body.String(), `"productId":"`+prodPump+`"`) {
		t.Errorf("an order read carries product names: %s", got.Body)
	}
	if got := w.get(t, custA, customerPortal+"/returns/"+retA); !strings.Contains(got.Body.String(), `"productName":"Centrifugal pump"`) {
		t.Errorf("a return read carries product names: %s", got.Body)
	}
}

func portalReturnBody(invoice, product, qty string) string {
	return `{"originalInvoiceId":"` + invoice + `","returnReasonCode":"DAMAGED","lines":[{"productId":"` + product + `","quantity":"` + qty + `"}]}`
}

func TestCustomerPortal_RequestReturnIsForTheTokensCustomerOnly(t *testing.T) {
	s := newPortalWriteStore()
	w := newWriteHarness(t, s, nil)
	rec := w.post(t, custA, customerPortal+"/returns", "return-0001", portalReturnBody(invA, prodPump, "2"))
	if rec.Code != http.StatusCreated {
		t.Fatalf("request return: %d %s", rec.Code, rec.Body)
	}
	in := s.createdRet[0]
	if in.CustomerID != custA || in.LegalEntityID != portLE || in.OriginalInvoiceID != invA || in.IdempotencyKey != nsA+":return-0001" {
		t.Fatalf("return stored as %+v", in)
	}
	l := in.Lines[0]
	if !l.FromOrderLine || l.Disposition != domain.DispositionReturnToStock || l.ReturnCostMethod != domain.CostCurrent {
		t.Errorf("line %+v: unit, site and warehouse must come from the invoice line's order line, not the customer's latest order", l)
	}
	if strings.Contains(rec.Body.String(), "unitCost") || strings.Contains(rec.Body.String(), "warehouseId") {
		t.Errorf("the return exposes internals: %s", rec.Body)
	}

	for name, tc := range map[string]struct {
		customer, key, body string
		duties              []string
		want                int
	}{
		"a body naming a customer": {custA, "return-0002", `{"customerId":"` + custB + `","originalInvoiceId":"` + invA + `","returnReasonCode":"X","lines":[]}`, nil, 400},
		"no key":                   {custA, "", portalReturnBody(invA, prodPump, "1"), nil, 400},
		"the order duty":           {custA, "return-0002", portalReturnBody(invA, prodPump, "1"), []string{dutyCustomerPortalOrder}, 403},
		"the read duty":            {custA, "return-0002", portalReturnBody(invA, prodPump, "1"), []string{dutyCustomerPortal}, 403},
	} {
		s2 := newPortalWriteStore()
		w2 := newWriteHarness(t, s2, nil)
		if rec := w2.post(t, tc.customer, customerPortal+"/returns", tc.key, tc.body, tc.duties...); rec.Code != tc.want || len(s2.createdRet) != 0 {
			t.Errorf("%s: %d %s (stored %d)", name, rec.Code, rec.Body, len(s2.createdRet))
		}
	}
}

// The return-order flow refuses an invoice of another customer; the portal answers that exactly as it answers an unknown invoice.
func TestCustomerPortal_ReturnOfAnotherCustomersInvoiceIsOneAnswer(t *testing.T) {
	for _, detail := range []string{"the original invoice does not exist", "the original invoice is not a posted invoice of this legal entity and customer"} {
		s := newPortalWriteStore()
		s.returnErr = httpx.NewProblem(http.StatusUnprocessableEntity, store.CodeRMAInvoice, detail)
		w := newWriteHarness(t, s, nil)
		rec := w.post(t, custA, customerPortal+"/returns", "return-0003", portalReturnBody(invA, prodPump, "1"))
		var p struct{ Code, Detail string }
		_ = json.Unmarshal(rec.Body.Bytes(), &p)
		if rec.Code != http.StatusUnprocessableEntity || p.Code != codePortalInvoice || strings.Contains(p.Detail, "legal entity") || strings.Contains(p.Detail, "does not exist") {
			t.Errorf("%q: %d %s", detail, rec.Code, rec.Body)
		}
	}
}

// The unit the customer names must be the unit of the newest line; a price outside the window is not offered (the store drops it,
// so the product is refused as not recently ordered).
func TestCustomerPortal_PlaceOrderRefusesAnotherUnitAndStalePrices(t *testing.T) {
	s := newPortalWriteStore()
	w := newWriteHarness(t, s, nil)
	body := `{"requestedShipDate":"` + futureD + `","lines":[{"productId":"` + prodPump + `","quantity":"1","unit":"BOX"}]}`
	rec := w.post(t, custA, customerPortal+"/orders", "submit-0009", body)
	var p struct {
		Code   string
		Errors map[string]string
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &p)
	if rec.Code != http.StatusUnprocessableEntity || p.Code != codePortalProduct || !strings.Contains(p.Errors["lines[0].unit"], "EA") || len(s.createdOrder) != 0 {
		t.Fatalf("another unit: %d %s", rec.Code, rec.Body)
	}
	for _, f := range s.historyAsked {
		if f.Since.IsZero() || time.Since(f.Since) < 360*24*time.Hour || time.Since(f.Since) > 370*24*time.Hour {
			t.Errorf("history window %v, want about 365 days", f.Since)
		}
	}
	s2 := newPortalWriteStore()
	s2.history[custA] = nil // the store found nothing inside the window
	w2 := newWriteHarness(t, s2, nil)
	if rec := w2.post(t, custA, customerPortal+"/orders", "submit-0010", portalOrderBody(prodPump, "1")); rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "recently") {
		t.Fatalf("stale: %d %s", rec.Code, rec.Body)
	}
	api := New(s).WithPortalPriceWindow(30)
	if api.priceWindowDays() != 30 || New(s).priceWindowDays() != 365 {
		t.Fatal("price window")
	}
}
