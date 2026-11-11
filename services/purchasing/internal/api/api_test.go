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

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/purchasing/internal/domain"
	"github.com/BenjaminSRussell/passage/services/purchasing/internal/store"
)

// fakeStore is an in-memory Store recording what the handlers pass down.
type fakeStore struct {
	requisitions map[string]store.Requisition
	agreements   []domain.TradeAgreement
	orders       []store.PurchaseOrder

	lastPriceFilter store.PriceFilter
	lastSubmit      struct {
		version int
		chain   []string
		idem    store.Idem
	}
	lastReqFilter   store.RequisitionFilter
	lastOrderFilter store.PurchaseOrderFilter
	lastAfter       string
	lastLimit       int
	pingErr         error

	purchaseAgreements map[string]store.AgreementDetail
	sourcing           []store.SourcingLine
	openLines          []store.OpenOrderLine
	vendorOpen         []store.VendorOpenOrders
	backordered        []store.BackorderedLine
	expiring           []store.ExpiringAgreement
	nearLimit          []store.NearLimitLine
	changeOrders       []store.ChangeOrder
	overdue            []store.OverdueLine
	priceHist          []store.PriceHistoryEntry
	lastPrice          *store.PriceHistoryEntry
	lastPriceErr       error
	lastPriceCalls     int
	lastInquiry        store.InquiryFilter
	lastOpenLines      store.OpenLineFilter
	lastAgrFilter      store.AgreementFilter
	lastSourcing       store.SourcingFilter
	sourcingCalls      []store.SourcingFilter
	lastConsolidated   []string
	lastSplit          splitCall
	approval           approvalFake
	editing            editFake
	release            releaseLog
	lastIdem           store.Idem

	// PO lifecycle: stored orders by id, and what edits and actions received.
	poByID   map[string]store.PurchaseOrder
	versions map[string][]store.PurchaseOrderVersion
	lastEdit struct {
		id      string
		ifMatch int
		next    store.PurchaseOrder
	}
	lastAction struct {
		id, action string
		ifMatch    int
		idem       store.Idem
	}

	// RFQ cycle: the one stored RFQ and what the RFQ methods received.
	rfq       *domain.RFQ
	rfqLog    rfqLog
	rfqTypes  map[string]domain.RFQType
	typeSaved *domain.RFQType
	amended   *domain.AmendmentInput
	edited    *domain.RFQEdit
	acked     string

	// Invoicing plans: what the plan methods received and what they answer.
	planLog  planLog
	schedLog schedLog

	// Subsequent price change: what the reprice methods received.
	reprice repriceLog

	// Scheduling agreements: what the scheduling methods received.
	sa saLog

	// Purchasing policy: what the policy methods received.
	pol policyLog

	// Category vendor assignments and managers (FR-17.20).
	categories categoryLog

	// Charge codes and auto charges (FR-17.9).
	charges chargeLog

	// Service lines and model service specifications (FR-17.23).
	svc svcLog

	// Signing limits and their requests (FR-17.17).
	signing signingLog

	// Changes other services asked of purchase orders (slice 18).
	inbound inboundLog

	// Vendor acknowledgements of purchase orders (511-1).
	ack ackLog

	// Procurement catalogs: what the catalog methods received and answer.
	cat catalogLog
	imp importLog
}

func newFake() *fakeStore {
	return &fakeStore{requisitions: map[string]store.Requisition{}, purchaseAgreements: map[string]store.AgreementDetail{},
		poByID: map[string]store.PurchaseOrder{}, versions: map[string][]store.PurchaseOrderVersion{}}
}

func (f *fakeStore) CreateTradeAgreement(_ context.Context, row domain.TradeAgreement, _ store.Idem) (domain.TradeAgreement, error) {
	row.ID = ids.New()
	f.agreements = append(f.agreements, row)
	return row, nil
}

func (f *fakeStore) GetTradeAgreement(_ context.Context, id string) (domain.TradeAgreement, error) {
	for _, a := range f.agreements {
		if a.ID == id {
			return a, nil
		}
	}
	return domain.TradeAgreement{}, httpx.NewProblem(http.StatusNotFound, store.CodeTradeAgreementNotFound, "trade agreement not found")
}

func (f *fakeStore) ListTradeAgreements(_ context.Context, _ store.TradeAgreementFilter, after string, limit int) (store.Page[domain.TradeAgreement], error) {
	f.lastAfter, f.lastLimit = after, limit
	return store.Page[domain.TradeAgreement]{Items: f.agreements, Next: "next-id"}, nil
}

func (f *fakeStore) PriceCandidates(_ context.Context, pf store.PriceFilter) ([]domain.TradeAgreement, error) {
	f.lastPriceFilter = pf
	return f.agreements, nil
}

func (f *fakeStore) CreateRequisition(_ context.Context, in store.Requisition, _ store.Idem) (store.Requisition, error) {
	in.ID, in.Number, in.Status, in.Version = ids.New(), "PR-00000001", store.RequisitionDraft, 1
	f.requisitions[in.ID] = in
	return in, nil
}

func (f *fakeStore) GetRequisition(_ context.Context, id string) (store.Requisition, error) {
	r, ok := f.requisitions[id]
	if !ok {
		return store.Requisition{}, httpx.NewProblem(http.StatusNotFound, store.CodeRequisitionNotFound, "requisition not found")
	}
	return r, nil
}

func (f *fakeStore) ListRequisitions(_ context.Context, rf store.RequisitionFilter, after string, limit int) (store.Page[store.RequisitionSummary], error) {
	f.lastReqFilter, f.lastAfter, f.lastLimit = rf, after, limit
	return store.Page[store.RequisitionSummary]{Items: []store.RequisitionSummary{}}, nil
}

func (f *fakeStore) SubmitRequisition(_ context.Context, id string, version int, chain []string, _ string, idem store.Idem) (store.Requisition, error) {
	f.lastSubmit.version, f.lastSubmit.chain, f.lastSubmit.idem = version, chain, idem
	r := f.requisitions[id]
	r.ApproverChain, r.Version, r.Status = chain, r.Version+1, store.RequisitionInReview
	if len(chain) == 0 {
		r.Status = store.RequisitionApproved
	}
	return r, nil
}

func (f *fakeStore) CreatePurchaseOrder(_ context.Context, in store.PurchaseOrder, _ store.Idem) (store.PurchaseOrder, error) {
	in.ID, in.RowVersion = ids.New(), 1
	f.orders = append(f.orders, in)
	return in, nil
}

func (f *fakeStore) GetPurchaseOrder(_ context.Context, id string) (store.PurchaseOrder, error) {
	po, ok := f.poByID[id]
	if !ok {
		return store.PurchaseOrder{}, httpx.NewProblem(http.StatusNotFound, store.CodePurchaseOrderNotFound, "purchase order not found")
	}
	return po, nil
}

func (f *fakeStore) ListPurchaseOrders(_ context.Context, of store.PurchaseOrderFilter, after string, limit int) (store.Page[store.PurchaseOrderSummary], error) {
	f.lastOrderFilter, f.lastAfter, f.lastLimit = of, after, limit
	return store.Page[store.PurchaseOrderSummary]{Items: []store.PurchaseOrderSummary{}}, nil
}

func (f *fakeStore) Ping(context.Context) error { return f.pingErr }

func (f *fakeStore) CreateAgreement(_ context.Context, in domain.Agreement, idem store.Idem) (store.AgreementDetail, error) {
	f.lastIdem = idem
	in.ID, in.Number, in.Version = ids.New(), "PA-00000001", 1
	for i := range in.Lines {
		in.Lines[i].ID, in.Lines[i].LineNo = ids.New(), i+1
	}
	d := store.AgreementDetail{Agreement: in}
	f.purchaseAgreements[in.ID] = d
	return d, nil
}

func (f *fakeStore) GetAgreement(_ context.Context, id string) (store.AgreementDetail, error) {
	d, ok := f.purchaseAgreements[id]
	if !ok {
		return store.AgreementDetail{}, httpx.NewProblem(http.StatusNotFound, store.CodeAgreementNotFound, "purchase agreement not found")
	}
	return d, nil
}

func (f *fakeStore) GetAgreementLine(_ context.Context, lineID string) (domain.Agreement, error) {
	for _, d := range f.purchaseAgreements {
		for _, l := range d.Agreement.Lines {
			if l.ID == lineID {
				a := d.Agreement
				a.Lines = []domain.AgreementLine{l}
				return a, nil
			}
		}
	}
	return domain.Agreement{}, httpx.NewProblem(http.StatusNotFound, store.CodeAgreementNotFound, "agreement line not found")
}

func (f *fakeStore) ListAgreements(_ context.Context, af store.AgreementFilter, after string, limit int) (store.Page[store.AgreementSummary], error) {
	f.lastAgrFilter, f.lastAfter, f.lastLimit = af, after, limit
	return store.Page[store.AgreementSummary]{Items: []store.AgreementSummary{}}, nil
}

func (f *fakeStore) ChangeAgreementStatus(_ context.Context, id string, version int, action string, idem store.Idem) (store.AgreementDetail, error) {
	f.lastIdem = idem
	d, ok := f.purchaseAgreements[id]
	if !ok {
		return store.AgreementDetail{}, httpx.NewProblem(http.StatusNotFound, store.CodeAgreementNotFound, "purchase agreement not found")
	}
	if d.Agreement.Version != version {
		return store.AgreementDetail{}, httpx.NewProblem(http.StatusConflict, store.CodeVersionConflict, "stale")
	}
	next, err := domain.NextStatus(d.Agreement.Status, action)
	if err != nil {
		return store.AgreementDetail{}, httpx.NewProblem(http.StatusConflict, domain.CodeAgreementState, err.Error())
	}
	d.Agreement.Status, d.Agreement.Version = next, version+1
	f.purchaseAgreements[id] = d
	return d, nil
}

func (f *fakeStore) ListOpenRequisitionLines(_ context.Context, sf store.SourcingFilter, after string, limit int) (store.Page[store.SourcingLine], error) {
	f.lastSourcing, f.lastAfter, f.lastLimit = sf, after, limit
	f.sourcingCalls = append(f.sourcingCalls, sf)
	items := []store.SourcingLine{}
	for _, l := range f.sourcing {
		if sf.Sourcing == "" || (sf.Sourcing == store.SourcingNeedsSourcing) == (l.PreferredVendorID == "") {
			items = append(items, l)
		}
	}
	return store.Page[store.SourcingLine]{Items: items}, nil
}

func (f *fakeStore) ListOpenOrderLines(_ context.Context, of store.OpenLineFilter, after string, limit int) (store.Page[store.OpenOrderLine], error) {
	f.lastOpenLines, f.lastAfter, f.lastLimit = of, after, limit
	return store.Page[store.OpenOrderLine]{Items: f.openLines}, nil
}

func (f *fakeStore) ListOpenOrdersByVendor(_ context.Context, qf store.InquiryFilter, after string, limit int) (store.Page[store.VendorOpenOrders], error) {
	f.lastInquiry, f.lastAfter, f.lastLimit = qf, after, limit
	return store.Page[store.VendorOpenOrders]{Items: f.vendorOpen}, nil
}

func (f *fakeStore) ListOverdueLines(_ context.Context, qf store.InquiryFilter, after string, limit int) (store.Page[store.OverdueLine], error) {
	f.lastInquiry, f.lastAfter, f.lastLimit = qf, after, limit
	return store.Page[store.OverdueLine]{Items: f.overdue}, nil
}

func (f *fakeStore) ListBackorderedLines(_ context.Context, qf store.InquiryFilter, after string, limit int) (store.Page[store.BackorderedLine], error) {
	f.lastInquiry, f.lastAfter, f.lastLimit = qf, after, limit
	return store.Page[store.BackorderedLine]{Items: f.backordered}, nil
}

func (f *fakeStore) ListExpiringAgreements(_ context.Context, qf store.InquiryFilter, after string, limit int) (store.Page[store.ExpiringAgreement], error) {
	f.lastInquiry, f.lastAfter, f.lastLimit = qf, after, limit
	return store.Page[store.ExpiringAgreement]{Items: f.expiring}, nil
}

func (f *fakeStore) ListNearLimitAgreementLines(_ context.Context, qf store.InquiryFilter, after string, limit int) (store.Page[store.NearLimitLine], error) {
	f.lastInquiry, f.lastAfter, f.lastLimit = qf, after, limit
	return store.Page[store.NearLimitLine]{Items: f.nearLimit}, nil
}

func (f *fakeStore) ListChangeOrders(_ context.Context, qf store.InquiryFilter, after string, limit int) (store.Page[store.ChangeOrder], error) {
	f.lastInquiry, f.lastAfter, f.lastLimit = qf, after, limit
	return store.Page[store.ChangeOrder]{Items: f.changeOrders}, nil
}

func (f *fakeStore) ListPriceHistory(_ context.Context, qf store.InquiryFilter, after string, limit int) (store.Page[store.PriceHistoryEntry], error) {
	f.lastInquiry, f.lastAfter, f.lastLimit = qf, after, limit
	return store.Page[store.PriceHistoryEntry]{Items: f.priceHist}, nil
}

func (f *fakeStore) LastOrderPrice(_ context.Context, legalEntityID, productID, currency, unit string) (*store.PriceHistoryEntry, error) {
	f.lastPriceCalls++
	if f.lastPriceErr != nil {
		return nil, f.lastPriceErr
	}
	return f.lastPrice, nil
}

func (f *fakeStore) RequisitionLinesByID(_ context.Context, lineIDs []string) ([]store.SourcingLine, error) {
	want := map[string]bool{}
	for _, id := range lineIDs {
		want[id] = true
	}
	var out []store.SourcingLine
	for _, l := range f.sourcing {
		if want[l.ID] {
			out = append(out, l)
		}
	}
	return out, nil
}

// splitCall records what SplitRequisitionLine received.
type splitCall struct {
	lineID     string
	version    int
	quantities []decimal.Decimal
	minor      int32
	by         string
	idem       store.Idem
}

func (f *fakeStore) SplitRequisitionLine(_ context.Context, lineID string, version int, quantities []decimal.Decimal, minor int32, by string, idem store.Idem) (store.Requisition, error) {
	f.lastSplit = splitCall{lineID, version, quantities, minor, by, idem}
	return store.Requisition{ID: ids.New(), Version: version + 1, Lines: []store.RequisitionLine{}}, nil
}

func (f *fakeStore) ConsolidateRequisitionLines(_ context.Context, orders []store.PurchaseOrder, lineIDs []string, idem store.Idem) ([]store.PurchaseOrder, error) {
	f.lastConsolidated, f.lastIdem = lineIDs, idem
	for i := range orders {
		orders[i].ID, orders[i].RowVersion = ids.New(), 1
	}
	f.orders = append(f.orders, orders...)
	return orders, nil
}

type groups struct{ vendor, product []string }

func (g groups) VendorGroups(context.Context, string) ([]string, error)  { return g.vendor, nil }
func (g groups) ProductGroups(context.Context, string) ([]string, error) { return g.product, nil }

const (
	tenantA = "00000000-0000-0000-0000-000000000001"
	userA   = "00000000-0000-7000-8000-0000000000a1"
	userB   = "00000000-0000-7000-8000-0000000000b1"
	leA     = "00000000-0000-7000-8000-0000000001e1"
)

// harness serves the API with a fixed clock (2026-03-01) over a fake store.
type harness struct {
	t      *testing.T
	fake   *fakeStore
	api    *API
	router http.Handler
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	f := newFake()
	a := New(f)
	a.now = func() time.Time { return time.Date(2026, 3, 1, 15, 0, 0, 0, time.UTC) }
	r := httpx.NewRouter()
	a.Register(r)
	return &harness{t: t, fake: f, api: a, router: r}
}

type call struct {
	method, path, body string
	user, scope        string
	duties             []string
	headers            map[string]string
	noTenant           bool
	unscoped           bool
}

func (h *harness) do(c call) *httptest.ResponseRecorder {
	h.t.Helper()
	req := httptest.NewRequest(c.method, c.path, strings.NewReader(c.body))
	if !c.noTenant {
		req.Header.Set(tenant.HeaderTenant, tenantA)
	}
	if c.user != "" {
		req.Header.Set(tenant.HeaderUser, c.user)
	}
	scope := c.scope
	if scope == "" && !c.unscoped {
		scope = leA
	}
	if scope != "" {
		req.Header.Set(tenant.HeaderLegalEntity, scope)
	}
	req.Header.Set(tenant.HeaderDuties, strings.Join(c.duties, ","))
	for k, v := range c.headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.router.ServeHTTP(rec, req)
	return rec
}

func problemCode(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var p httpx.Problem
	if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
		t.Fatalf("body %q is not a problem: %v", rec.Body.String(), err)
	}
	return p.Code
}

func idem(k string) map[string]string { return map[string]string{"Idempotency-Key": k} }

const reqBody = `{"legalEntityId":"` + "00000000-0000-7000-8000-0000000001e1" + `","currency":"USD","lines":[
 {"description":"Toner","category":"OFFICE","qty":"4","unit":"EA","estimatedUnitPrice":"300","requestedDate":"2026-03-10"}]}`

func TestRoutes_RefuseWithoutDutyTenantOrKey(t *testing.T) {
	h := newHarness(t)
	tests := []struct {
		name   string
		c      call
		status int
		code   string
	}{
		{"no tenant", call{method: "GET", path: "/v1/procurement/requisitions", noTenant: true}, 401, "AUTH-0001"},
		{"no duty", call{method: "POST", path: "/v1/procurement/requisitions", user: userA, body: reqBody, headers: idem("k")}, 403, "AUTH-0003"},
		{"wrong duty for PO inquire", call{method: "GET", path: "/v1/procurement/purchase-orders", duties: []string{dutyPurchaseOrderMaintain}}, 403, "AUTH-0003"},
		{"no user on create", call{method: "POST", path: "/v1/procurement/requisitions", duties: []string{dutyRequisitionCreate}, body: reqBody, headers: idem("k")}, 401, codeAuth},
		{"no idempotency key", call{method: "POST", path: "/v1/procurement/requisitions", user: userA, duties: []string{dutyRequisitionCreate}, body: reqBody}, 400, codeIdemRequired},
		{"unknown field", call{method: "POST", path: "/v1/procurement/requisitions", user: userA, duties: []string{dutyRequisitionCreate}, body: `{"bogus":1}`, headers: idem("k")}, 400, codeInvalid},
		{"legal entity outside scope", call{method: "POST", path: "/v1/procurement/requisitions", user: userA, scope: "00000000-0000-7000-8000-0000000001e2",
			duties: []string{dutyRequisitionCreate}, body: reqBody, headers: idem("k")}, 403, codeScope},
		{"bad id", call{method: "GET", path: "/v1/procurement/purchase-orders/nope", duties: []string{dutyPurchaseOrderInquire}}, 422, codeInvalid},
		{"missing PO", call{method: "GET", path: "/v1/procurement/purchase-orders/" + ids.New(), duties: []string{dutyPurchaseOrderInquire}}, 404, store.CodePurchaseOrderNotFound},
		{"bad cursor", call{method: "GET", path: "/v1/procurement/purchase-orders?cursor=x", duties: []string{dutyPurchaseOrderInquire}}, 422, codeInvalid},
		{"long search", call{method: "GET", path: "/v1/procurement/purchase-orders?q=" + strings.Repeat("x", 101), duties: []string{dutyPurchaseOrderInquire}}, 422, codeInvalid},
		{"limit over max", call{method: "GET", path: "/v1/procurement/trade-agreements?limit=500", duties: []string{dutyTradeAgreementMaintain}}, 422, codeInvalid},
		{"bad status filter", call{method: "GET", path: "/v1/procurement/requisitions?status=OPEN", duties: []string{dutyRequisitionCreate}}, 422, codeInvalid},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := h.do(tc.c)
			if rec.Code != tc.status || problemCode(t, rec) != tc.code {
				t.Fatalf("got %d %s, want %d %s: %s", rec.Code, problemCode(t, rec), tc.status, tc.code, rec.Body)
			}
		})
	}
}

func TestCreateRequisition_ScopeAllowsListedEntityAndWarnsOnZeroEstimate(t *testing.T) {
	h := newHarness(t)
	body := `{"legalEntityId":"` + leA + `","currency":"USD","lines":[
	 {"description":"Toner","category":"OFFICE","qty":"4","unit":"EA","estimatedUnitPrice":"300","requestedDate":"2026-03-10"},
	 {"description":"Chairs","category":"OFFICE","qty":"2","unit":"EA","requestedDate":"2026-03-10"}]}`
	rec := h.do(call{method: "POST", path: "/v1/procurement/requisitions", user: userA, scope: "00000000-0000-7000-8000-0000000001e3," + leA, duties: []string{dutyRequisitionCreate},
		body: body, headers: idem("k1")})
	if rec.Code != http.StatusCreated {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	var out store.Requisition
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.RequesterID != userA || !out.Total.Equal(decimal.NewFromInt(1200)) || rec.Header().Get("ETag") != `"1"` {
		t.Fatalf("requisition = %+v etag %s", out, rec.Header().Get("ETag"))
	}
	if len(out.Warnings) != 1 || out.Warnings[0].Code != codeNoEstimate || out.Warnings[0].LineNo != 2 {
		t.Fatalf("warnings = %+v, want one PRC-REQ-003 on line 2", out.Warnings)
	}
}

func TestCreateRequisition_DefaultsEstimateFromPreferredVendorAgreement(t *testing.T) {
	h := newHarness(t)
	vendor, product := ids.New(), ids.New()
	h.fake.agreements = []domain.TradeAgreement{{ID: "ta-1", Relation: domain.RelationPrice, AccountCode: domain.AccountVendor, AccountID: vendor,
		ItemCode: domain.ItemProduct, ItemID: product, Currency: "USD", Unit: "EA", FromQty: decimal.Zero, Price: decimal.RequireFromString("1250"),
		PriceUnit: decimal.NewFromInt(100), ValidFrom: mustDay(t, "2026-01-01"), Status: domain.StatusActive}}
	body := `{"legalEntityId":"` + leA + `","currency":"USD","lines":[{"productId":"` + product + `","preferredVendorId":"` + vendor +
		`","qty":"3","unit":"EA","requestedDate":"2026-03-10"}]}`
	rec := h.do(call{method: "POST", path: "/v1/procurement/requisitions", user: userA, duties: []string{dutyRequisitionCreate}, body: body, headers: idem("k2")})
	if rec.Code != http.StatusCreated {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	var out store.Requisition
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if !out.Lines[0].EstimatedUnitPrice.Equal(decimal.RequireFromString("12.5")) || !out.Total.Equal(decimal.RequireFromString("37.5")) || len(out.Warnings) != 0 {
		t.Fatalf("line %+v total %s warnings %+v; want 12.5 each, 37.50 total, no warning", out.Lines[0], out.Total, out.Warnings)
	}
	if h.fake.lastPriceFilter.VendorID != vendor || h.fake.lastPriceFilter.Date.String() != "2026-03-10" {
		t.Fatalf("lookup used %+v; want the preferred vendor on the requested date", h.fake.lastPriceFilter)
	}
}

func TestSubmitRequisition_RoutesRequiresIfMatchAndRequester(t *testing.T) {
	h := newHarness(t)
	id := ids.New()
	h.fake.requisitions[id] = store.Requisition{ID: id, RequesterID: userA, Status: store.RequisitionDraft, Version: 3, Currency: "USD",
		Total: decimal.NewFromInt(50000), Lines: []store.RequisitionLine{{Category: "MRO"}}}
	path := "/v1/procurement/requisitions/" + id + ":submit"
	duties := []string{dutyRequisitionCreate}

	if rec := h.do(call{method: "POST", path: path, user: userA, duties: duties, headers: idem("s1")}); rec.Code != http.StatusPreconditionRequired ||
		problemCode(t, rec) != codeIfMatchMissing {
		t.Fatalf("no If-Match: %d %s", rec.Code, rec.Body)
	}
	if rec := h.do(call{method: "POST", path: path, user: userB, duties: duties, headers: map[string]string{"Idempotency-Key": "s1", "If-Match": `"3"`}}); rec.Code != http.StatusForbidden ||
		problemCode(t, rec) != codeNotRequester {
		t.Fatalf("other user: %d %s", rec.Code, rec.Body)
	}
	if rec := h.do(call{method: "POST", path: "/v1/procurement/requisitions/" + id + ":close", user: userA, duties: duties,
		headers: map[string]string{"Idempotency-Key": "s1", "If-Match": `"3"`}}); rec.Code != http.StatusNotFound || problemCode(t, rec) != codeUnknownAction {
		t.Fatalf("unknown action: %d %s", rec.Code, rec.Body)
	}
	rec := h.do(call{method: "POST", path: path, user: userA, duties: duties, headers: map[string]string{"Idempotency-Key": "s1", "If-Match": `"3"`}})
	if rec.Code != http.StatusOK || rec.Header().Get("ETag") != `"4"` {
		t.Fatalf("submit: %d %s etag %s", rec.Code, rec.Body, rec.Header().Get("ETag"))
	}
	if got := h.fake.lastSubmit; got.version != 3 || strings.Join(got.chain, "|") != "MANAGER|CATEGORY_OWNER" || got.idem.Key != "s1" || got.idem.Hash == "" {
		t.Fatalf("store got %+v; want version 3, manager + category owner, the key and a hash", got)
	}
}

func TestHashBody_ScopesKeysToTheOperation(t *testing.T) {
	body := map[string]string{"a": "b"}
	if hashBody("requisition.create", body) == hashBody("purchase-order.create", body) {
		t.Fatal("the same key and body on two endpoints must hash differently")
	}
	if hashBody("x", body) != hashBody("x", body) {
		t.Fatal("hash must be deterministic")
	}
}

func TestCreateOrder_PricesFromTheMostSpecificAgreementAndBalancesTotals(t *testing.T) {
	h := newHarness(t)
	h.api.WithMemberships(groups{vendor: []string{"00000000-0000-7000-8000-0000000000c1"}})
	vendor, product := ids.New(), ids.New()
	row := func(id, code, acct, price, from string) domain.TradeAgreement {
		return domain.TradeAgreement{ID: id, Relation: domain.RelationPrice, AccountCode: code, AccountID: acct, ItemCode: domain.ItemAll,
			Currency: "USD", Unit: "EA", FromQty: decimal.Zero, Price: decimal.RequireFromString(price), PriceUnit: decimal.NewFromInt(1),
			ValidFrom: mustDay(t, from), Status: domain.StatusActive}
	}
	h.fake.agreements = []domain.TradeAgreement{
		row("all", domain.AccountAll, "", "8", "2026-01-01"),
		row("group", domain.AccountGroup, "00000000-0000-7000-8000-0000000000c1", "9", "2026-01-01"),
		row("vendor", domain.AccountVendor, vendor, "10", "2026-01-01"),
	}
	body := `{"legalEntityId":"` + leA + `","vendorId":"` + vendor + `","currency":"USD",
	 "lines":[{"productId":"` + product + `","qty":"100","unit":"EA","discountPct":"10","taxAmount":"72","deliveryDate":"2026-04-01"},
	          {"description":"Setup","qty":"1","unit":"EA","unitPrice":"50","deliveryDate":"2026-04-01"}],
	 "charges":[{"calc":"FIXED","value":"25"},{"calc":"PERCENT","value":"1"}]}`
	rec := h.do(call{method: "POST", path: "/v1/procurement/purchase-orders", duties: []string{dutyPurchaseOrderMaintain}, body: body, headers: idem("po1")})
	if rec.Code != http.StatusCreated {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	var po store.PurchaseOrder
	if err := json.Unmarshal(rec.Body.Bytes(), &po); err != nil {
		t.Fatal(err)
	}
	if po.Lines[0].PriceSource != "TRADE_AGREEMENT:vendor" || !po.Lines[0].UnitPrice.Equal(decimal.NewFromInt(10)) || po.Lines[1].PriceSource != "MANUAL" {
		t.Fatalf("lines = %+v; want the vendor row (10) and a manual line", po.Lines)
	}
	// 1000 gross − 100 discount + 50 = 950 net; charges 25 + 9.50; tax 72.
	want := map[string]string{"subtotal": "1050", "discount": "100", "charges": "34.5", "tax": "72", "total": "1056.5"}
	got := map[string]decimal.Decimal{"subtotal": po.Subtotal, "discount": po.DiscountTotal, "charges": po.ChargesTotal, "tax": po.TaxTotal, "total": po.Total}
	for k, v := range want {
		if !got[k].Equal(decimal.RequireFromString(v)) {
			t.Errorf("%s = %s, want %s", k, got[k], v)
		}
	}
	if pf := h.fake.lastPriceFilter; pf.Date.String() != "2026-03-01" || len(pf.VendorGroupIDs) != 1 {
		t.Fatalf("price filter %+v; want today's date and the vendor's group", pf)
	}
}

func TestCreateOrder_Rejections(t *testing.T) {
	vendor := ids.New()
	line := func(extra string) string {
		return `{"legalEntityId":"` + leA + `","vendorId":"` + vendor + `","currency":"USD","lines":[{"productId":"` + ids.New() +
			`","qty":"1","unit":"EA","deliveryDate":"2026-04-01"` + extra + `}]}`
	}
	tests := []struct {
		name, body, code string
		status           int
	}{
		{"no agreement price", line(""), codeNoPrice, 422},
		{"negative discount", line(`,"unitPrice":"1","discountPct":"-1"`), codeInvalid, 422},
		{"zero qty", strings.Replace(line(`,"unitPrice":"1"`), `"qty":"1"`, `"qty":"0"`, 1), codeInvalid, 422},
		{"bad charge calc", strings.Replace(line(`,"unitPrice":"1"`), `]}`, `],"charges":[{"calc":"WEIGHT","value":"1"}]}`, 1), codeInvalid, 422},
		{"negative charge", strings.Replace(line(`,"unitPrice":"1"`), `]}`, `],"charges":[{"calc":"FIXED","value":"-5"}]}`, 1), codeInvalid, 422},
		{"missing vendor", `{"legalEntityId":"` + leA + `","currency":"USD","lines":[]}`, codeInvalid, 422},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			rec := h.do(call{method: "POST", path: "/v1/procurement/purchase-orders", duties: []string{dutyPurchaseOrderMaintain}, body: tc.body, headers: idem("k")})
			if rec.Code != tc.status || problemCode(t, rec) != tc.code {
				t.Fatalf("got %d %s, want %d %s: %s", rec.Code, problemCode(t, rec), tc.status, tc.code, rec.Body)
			}
			if len(h.fake.orders) != 0 {
				t.Fatal("a rejected order reached the store")
			}
		})
	}
}

func TestLookupPrice_ExplainsAndPassesGroupsAndTie(t *testing.T) {
	h := newHarness(t)
	vendor, product, group := ids.New(), ids.New(), ids.New()
	h.fake.agreements = []domain.TradeAgreement{
		{ID: "old-cheap", Relation: domain.RelationPrice, AccountCode: domain.AccountVendor, AccountID: vendor, ItemCode: domain.ItemAll, Currency: "USD",
			Unit: "EA", FromQty: decimal.Zero, Price: decimal.NewFromInt(9), PriceUnit: decimal.NewFromInt(1), ValidFrom: mustDay(t, "2026-01-01"), Status: domain.StatusActive},
		{ID: "new-dear", Relation: domain.RelationPrice, AccountCode: domain.AccountVendor, AccountID: vendor, ItemCode: domain.ItemAll, Currency: "USD",
			Unit: "EA", FromQty: decimal.Zero, Price: decimal.NewFromInt(10), PriceUnit: decimal.NewFromInt(1), ValidFrom: mustDay(t, "2026-02-01"), Status: domain.StatusActive},
	}
	base := "/v1/procurement/pricing/lookup?vendor=" + vendor + "&product=" + product + "&qty=5&date=2026-03-01&currency=USD&unit=EA&vendorGroup=" + group
	for _, tc := range []struct{ tie, winner string }{{"", "new-dear"}, {"LOWEST_PRICE", "old-cheap"}} {
		path := base
		if tc.tie != "" {
			path += "&tie=" + tc.tie
		}
		rec := h.do(call{method: "GET", path: path, duties: []string{dutyPurchaseOrderMaintain}})
		var out struct {
			Winner     *agreementDTO      `json:"winner"`
			Candidates []domain.Candidate `json:"candidates"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || rec.Code != http.StatusOK {
			t.Fatalf("tie %q: %d %s", tc.tie, rec.Code, rec.Body)
		}
		if out.Winner == nil || out.Winner.ID != tc.winner || len(out.Candidates) != 2 {
			t.Fatalf("tie %q: winner %+v candidates %d; want %s of 2", tc.tie, out.Winner, len(out.Candidates), tc.winner)
		}
	}
	if pf := h.fake.lastPriceFilter; len(pf.VendorGroupIDs) != 1 || pf.VendorGroupIDs[0] != group {
		t.Fatalf("vendor groups passed = %v", pf.VendorGroupIDs)
	}
	for _, bad := range []struct{ query, code string }{{"&tie=RANDOM", codeBadTie}, {"&vendorGroup=nope", codeInvalid}} {
		if rec := h.do(call{method: "GET", path: base + bad.query, duties: []string{dutyPurchaseOrderMaintain}}); rec.Code != 422 || problemCode(t, rec) != bad.code {
			t.Fatalf("%s: %d %s", bad.query, rec.Code, rec.Body)
		}
	}
}

func TestLists_PassFiltersCursorAndLimit(t *testing.T) {
	h := newHarness(t)
	cursor := ids.New()
	vendor := ids.New()
	rec := h.do(call{method: "GET", path: "/v1/procurement/purchase-orders?approvalStatus=DRAFT&vendorId=" + vendor + "&limit=10&q=+po-0012+&cursor=" + cursor,
		duties: []string{dutyPurchaseOrderInquire}})
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"items":[]`) {
		t.Fatalf("orders: %d %s", rec.Code, rec.Body)
	}
	if h.fake.lastOrderFilter.ApprovalStatus != "DRAFT" || h.fake.lastOrderFilter.VendorID != vendor || h.fake.lastOrderFilter.Q != "po-0012" || h.fake.lastAfter != cursor || h.fake.lastLimit != 10 {
		t.Fatalf("filter %+v after %s limit %d", h.fake.lastOrderFilter, h.fake.lastAfter, h.fake.lastLimit)
	}
	rec = h.do(call{method: "GET", path: "/v1/procurement/requisitions?mine=true&status=DRAFT", user: userA, duties: []string{dutyRequisitionCreate}})
	if rec.Code != http.StatusOK || h.fake.lastReqFilter.RequesterID != userA || h.fake.lastReqFilter.Status != "DRAFT" {
		t.Fatalf("mine: %d %+v", rec.Code, h.fake.lastReqFilter)
	}
	h.fake.agreements = []domain.TradeAgreement{{ID: "a1", FromQty: decimal.Zero, Price: decimal.NewFromInt(1), PriceUnit: decimal.NewFromInt(1)}}
	rec = h.do(call{method: "GET", path: "/v1/procurement/trade-agreements", duties: []string{dutyTradeAgreementMaintain}})
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"nextCursor":"next-id"`) || !strings.Contains(rec.Body.String(), `"id":"a1"`) {
		t.Fatalf("agreements: %d %s", rec.Code, rec.Body)
	}
}

func TestReadyAndMetrics(t *testing.T) {
	h := newHarness(t)
	if rec := h.do(call{method: "GET", path: "/readyz", noTenant: true}); rec.Code != http.StatusOK {
		t.Fatalf("ready = %d", rec.Code)
	}
	h.fake.pingErr = errors.New("down")
	if rec := h.do(call{method: "GET", path: "/readyz", noTenant: true}); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("ready while the database is down = %d", rec.Code)
	}
	h.do(call{method: "POST", path: "/v1/procurement/requisitions", user: userA, duties: []string{dutyRequisitionCreate}, body: reqBody, headers: idem("m")})
	rec := httptest.NewRecorder()
	h.api.Metrics().ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	if !strings.Contains(rec.Body.String(), "purchasing_requisitions_created_total 1\n") {
		t.Fatalf("metrics:\n%s", rec.Body)
	}
}

func TestWriteWithoutLegalEntityScopeIsForbidden(t *testing.T) {
	h := newHarness(t)
	rec := h.do(call{method: "POST", path: "/v1/procurement/requisitions", user: userA, unscoped: true, duties: []string{dutyRequisitionCreate}, body: reqBody, headers: idem("k-unscoped")})
	if rec.Code != http.StatusForbidden || problemCode(t, rec) != codeScope {
		t.Fatalf("got %d %s", rec.Code, rec.Body)
	}
}

// releaseLog records what the release run methods received and answer.
type releaseLog struct {
	in      store.ReleaseInput
	idem    store.Idem
	runs    map[string]store.ReleaseRun
	lastRun store.ReleaseRunFilter
}

func (f *fakeStore) ReleaseRequisitions(_ context.Context, in store.ReleaseInput, idem store.Idem) (store.ReleaseRun, error) {
	f.release.in, f.release.idem = in, idem
	run := store.ReleaseRun{ID: ids.New(), LegalEntityID: in.LegalEntityID, WindowDays: in.WindowDays, MaxLines: in.MaxLines, LinesConsidered: in.Considered,
		Status: store.ReleaseCompleted, PurchaseOrders: []store.ReleasedOrder{}, Skipped: in.Skipped, Warnings: []store.Warning{}}
	for _, g := range in.Groups {
		run.Warnings = append(run.Warnings, g.Warnings...)
		for _, o := range g.Orders {
			run.PurchaseOrders = append(run.PurchaseOrders, store.ReleasedOrder{PurchaseOrderID: ids.New(), VendorID: o.VendorID, Currency: o.Currency, Total: o.Total, LineCount: len(o.Lines)})
		}
		run.LinesReleased += len(g.Lines)
	}
	run.LinesSkipped = len(run.Skipped)
	if f.release.runs == nil {
		f.release.runs = map[string]store.ReleaseRun{}
	}
	f.release.runs[run.ID] = run
	return run, nil
}

func (f *fakeStore) GetReleaseRun(_ context.Context, id string) (store.ReleaseRun, error) {
	r, ok := f.release.runs[id]
	if !ok {
		return store.ReleaseRun{}, httpx.NewProblem(http.StatusNotFound, store.CodeReleaseRunNotFound, "release run not found")
	}
	return r, nil
}

func (f *fakeStore) ListReleaseRuns(_ context.Context, rf store.ReleaseRunFilter, after string, limit int) (store.Page[store.ReleaseRun], error) {
	f.release.lastRun, f.lastAfter, f.lastLimit = rf, after, limit
	items := []store.ReleaseRun{}
	for _, r := range f.release.runs {
		items = append(items, r)
	}
	return store.Page[store.ReleaseRun]{Items: items}, nil
}
