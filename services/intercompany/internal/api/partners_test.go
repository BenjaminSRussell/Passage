package api_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/intercompany/internal/api"
	"github.com/BenjaminSRussell/passage/services/intercompany/internal/domain"
	"github.com/BenjaminSRussell/passage/services/intercompany/internal/store"
)

// fakePartners is an in-memory PartnerStore with the store's rules: one
// active pairing per direction, Idempotency-Key replay, version check. The
// Postgres store is tested against these rules in internal/store.
type fakePartners struct {
	mu   sync.Mutex
	recs map[string]domain.PartnerRecord // tenant/id
	keys map[string]string               // tenant/key -> id
}

func newFake() *fakePartners {
	return &fakePartners{recs: map[string]domain.PartnerRecord{}, keys: map[string]string{}}
}

func tenantOf(ctx context.Context) string { p, _ := tenant.FromContext(ctx); return p.TenantID }

func (f *fakePartners) activeExists(t string, p domain.Partner, except string) bool {
	for k, r := range f.recs {
		if strings.HasPrefix(k, t+"/") && r.ID != except && r.Active && r.SellerEntity == p.SellerEntity && r.BuyerEntity == p.BuyerEntity {
			return true
		}
	}
	return false
}

func (f *fakePartners) CreatePartner(ctx context.Context, p domain.Partner, key string) (domain.PartnerRecord, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	t := tenantOf(ctx)
	if id, ok := f.keys[t+"/"+key]; ok {
		return f.recs[t+"/"+id], true, nil
	}
	if err := domain.ValidatePairing(p); err != nil {
		return domain.PartnerRecord{}, false, err
	}
	if p.Active && f.activeExists(t, p, "") {
		return domain.PartnerRecord{}, false, &domain.Error{Code: domain.CodePairingExists, Detail: "exists"}
	}
	now := time.Date(2026, 10, 27, 9, 0, 0, 0, time.UTC)
	rec := domain.PartnerRecord{ID: ids.New(), Version: 1, CreatedAt: now, UpdatedAt: now, Partner: p}
	f.recs[t+"/"+rec.ID] = rec
	f.keys[t+"/"+key] = rec.ID
	return rec, false, nil
}

func (f *fakePartners) ListPartners(ctx context.Context, filter store.PartnerFilter, after string, limit int) (store.PartnerPage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	page := store.PartnerPage{Items: []domain.PartnerRecord{}}
	for k, r := range f.recs {
		if strings.HasPrefix(k, tenantOf(ctx)+"/") && (filter.Entity == "" || r.SellerEntity == filter.Entity || r.BuyerEntity == filter.Entity) {
			page.Items = append(page.Items, r)
		}
	}
	return page, nil
}

func (f *fakePartners) GetPartner(ctx context.Context, id string) (domain.PartnerRecord, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	rec, ok := f.recs[tenantOf(ctx)+"/"+id]
	if !ok {
		return domain.PartnerRecord{}, &domain.Error{Code: domain.CodePartnerNotFound, Detail: "no intercompany partner with id " + id}
	}
	return rec, nil
}

func (f *fakePartners) UpdatePartner(ctx context.Context, id string, version int, patch domain.PartnerPatch) (domain.PartnerRecord, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	t := tenantOf(ctx)
	rec, ok := f.recs[t+"/"+id]
	if !ok {
		return domain.PartnerRecord{}, &domain.Error{Code: domain.CodePartnerNotFound, Detail: "missing"}
	}
	if rec.Version != version {
		return domain.PartnerRecord{}, &domain.Error{Code: domain.CodeVersionConflict, Detail: "stale"}
	}
	next, changed := patch.Apply(rec.Partner)
	if len(changed) == 0 {
		return rec, nil
	}
	if err := domain.ValidatePairing(next); err != nil {
		return domain.PartnerRecord{}, err
	}
	if next.Active && f.activeExists(t, next, id) {
		return domain.PartnerRecord{}, &domain.Error{Code: domain.CodePairingExists, Detail: "exists"}
	}
	rec.Partner, rec.Version = next, rec.Version+1
	f.recs[t+"/"+id] = rec
	return rec, nil
}

const (
	partnersPath = "/v1/interco/partners"
	allDuties    = api.DutyView + "," + api.DutyConfigure
)

type call struct {
	method, path, duties, body string
	headers                    map[string]string
	tenant                     string
}

func (c call) on(h http.Handler) *httptest.ResponseRecorder {
	req := httptest.NewRequest(c.method, c.path, strings.NewReader(c.body))
	req.Header.Set("Content-Type", "application/json")
	t := c.tenant
	if t == "" {
		t = tenantID
	}
	req.Header.Set("X-Passage-Tenant", t)
	req.Header.Set("X-Passage-Duties", c.duties)
	for k, v := range c.headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

type partnerBody struct {
	ID              string `json:"id"`
	Version         int    `json:"version"`
	SellerEntity    string `json:"sellerEntity"`
	BuyerEntity     string `json:"buyerEntity"`
	CustomerAccount string `json:"customerAccount"`
	VendorAccount   string `json:"vendorAccount"`
	Active          bool   `json:"active"`
	Actions         struct {
		AutoCreate, AutoConfirm, AutoPostPackingSlip, AutoPostInvoice bool
	} `json:"actions"`
	Pricing   string `json:"pricing"`
	MarkupPct string `json:"markupPct"`
	Tolerance struct {
		PricePct string `json:"pricePct"`
		DateDays int    `json:"dateDays"`
	} `json:"tolerance"`
}

type partnerProblem struct {
	Code   string            `json:"code"`
	Detail string            `json:"detail"`
	Errors map[string]string `json:"errors"`
}

func decode[T any](t *testing.T, rec *httptest.ResponseRecorder) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatalf("decode %s: %v", rec.Body.String(), err)
	}
	return v
}

const minimalPartner = `{"sellerEntity":"P","buyerEntity":"S","customerAccount":"IC-S","vendorAccount":"IC-P"}`

// create stores a pairing through the API and returns it.
func create(t *testing.T, h http.Handler, body string) partnerBody {
	t.Helper()
	rec := call{method: http.MethodPost, path: partnersPath, duties: allDuties, body: body, headers: map[string]string{"Idempotency-Key": ids.New()}}.on(h)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create = %d %s", rec.Code, rec.Body.String())
	}
	return decode[partnerBody](t, rec)
}

func TestCreatePartner_DefaultsETagAndReplay(t *testing.T) {
	h := router(api.New(newFake()))
	c := call{method: http.MethodPost, path: partnersPath, duties: api.DutyConfigure, body: minimalPartner, headers: map[string]string{"Idempotency-Key": "k-1"}}
	rec := c.on(h)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d %s", rec.Code, rec.Body.String())
	}
	got := decode[partnerBody](t, rec)
	// ic.partnerPolicy defaults: active, auto-create on, auto-post off, PriceList, price 0.5 %, date 2 days.
	if !got.Active || !got.Actions.AutoCreate || got.Actions.AutoConfirm || got.Actions.AutoPostPackingSlip || got.Actions.AutoPostInvoice ||
		got.Pricing != "PriceList" || got.MarkupPct != "0" || got.Tolerance.PricePct != "0.5" || got.Tolerance.DateDays != 2 || got.Version != 1 {
		t.Fatalf("defaults = %+v", got)
	}
	if rec.Header().Get("ETag") != `"1"` || rec.Header().Get("Location") != partnersPath+"/"+got.ID || rec.Header().Get("Idempotent-Replayed") != "" {
		t.Fatalf("headers = %v", rec.Header())
	}
	again := c.on(h)
	if again.Code != http.StatusCreated || again.Header().Get("Idempotent-Replayed") != "true" || decode[partnerBody](t, again).ID != got.ID {
		t.Fatalf("replay = %d %v %s", again.Code, again.Header(), again.Body.String())
	}
}

func TestCreatePartner_Validation(t *testing.T) {
	h := router(api.New(newFake()))
	cases := []struct {
		name, body string
		headers    map[string]string
		status     int
		code       string
		field      string
	}{
		{"missing Idempotency-Key", minimalPartner, nil, http.StatusBadRequest, domain.CodeInvalid, "Idempotency-Key"},
		{"missing customer account", `{"sellerEntity":"P","buyerEntity":"S","vendorAccount":"IC-P"}`, nil, http.StatusBadRequest, domain.CodeInvalid, "customerAccount"},
		{"same entity", `{"sellerEntity":"P","buyerEntity":"P","customerAccount":"C","vendorAccount":"V"}`, nil, http.StatusBadRequest, domain.CodeInvalid, "buyerEntity"},
		{"entity too long", `{"sellerEntity":"` + strings.Repeat("P", 65) + `","buyerEntity":"S","customerAccount":"C","vendorAccount":"V"}`, nil, http.StatusBadRequest, domain.CodeInvalid, "sellerEntity"},
		{"unknown pricing", `{"sellerEntity":"P","buyerEntity":"S","customerAccount":"C","vendorAccount":"V","pricing":"Guess"}`, nil, http.StatusBadRequest, domain.CodeInvalid, "pricing"},
		{"float-like markup", `{"sellerEntity":"P","buyerEntity":"S","customerAccount":"C","vendorAccount":"V","markupPct":"1e2"}`, nil, http.StatusBadRequest, domain.CodeInvalid, "markupPct"},
		{"tolerance over 30 days", `{"sellerEntity":"P","buyerEntity":"S","customerAccount":"C","vendorAccount":"V","tolerance":{"dateDays":31}}`, nil, http.StatusBadRequest, domain.CodeInvalid, ""},
		{"unknown field", `{"sellerEntity":"P","buyerEntity":"S","customerAccount":"C","vendorAccount":"V","valueMaps":[]}`, nil, http.StatusBadRequest, domain.CodeInvalid, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			headers := c.headers
			if headers == nil && c.field != "Idempotency-Key" {
				headers = map[string]string{"Idempotency-Key": ids.New()}
			}
			rec := call{method: http.MethodPost, path: partnersPath, duties: api.DutyConfigure, body: c.body, headers: headers}.on(h)
			p := decode[partnerProblem](t, rec)
			if rec.Code != c.status || p.Code != c.code {
				t.Fatalf("got %d %s; want %d %s (%s)", rec.Code, p.Code, c.status, c.code, rec.Body.String())
			}
			if c.field != "" && p.Errors[c.field] == "" {
				t.Fatalf("no error on %s: %v", c.field, p.Errors)
			}
		})
	}
}

func TestCreatePartner_SecondActivePairingConflicts(t *testing.T) {
	h := router(api.New(newFake()))
	create(t, h, minimalPartner)
	rec := call{method: http.MethodPost, path: partnersPath, duties: api.DutyConfigure, body: minimalPartner, headers: map[string]string{"Idempotency-Key": "other"}}.on(h)
	if p := decode[partnerProblem](t, rec); rec.Code != http.StatusConflict || p.Code != domain.CodePairingExists {
		t.Fatalf("got %d %+v", rec.Code, p)
	}
}

func TestPartners_DutiesEnforced(t *testing.T) {
	h := router(api.New(newFake()))
	p := create(t, h, minimalPartner)
	cases := []call{
		{method: http.MethodPost, path: partnersPath, duties: api.DutyView, body: minimalPartner, headers: map[string]string{"Idempotency-Key": "x"}},
		{method: http.MethodPatch, path: partnersPath + "/" + p.ID, duties: api.DutyView, body: `{"active":false}`, headers: map[string]string{"If-Match": `"1"`}},
		{method: http.MethodGet, path: partnersPath, duties: api.DutyConfigure},
		{method: http.MethodGet, path: partnersPath + "/" + p.ID, duties: api.DutyPost},
		{method: http.MethodPost, path: partnersPath + "/" + p.ID + "/lines:price", duties: api.DutyConfigure, body: `{}`},
		{method: http.MethodPost, path: partnersPath + "/" + p.ID + "/changes:evaluate", duties: api.DutyReconcile, body: `{}`},
	}
	for _, c := range cases {
		if rec := c.on(h); rec.Code != http.StatusForbidden {
			t.Errorf("%s %s with %s = %d; want 403", c.method, c.path, c.duties, rec.Code)
		}
	}
}

func TestGetAndListPartners(t *testing.T) {
	h := router(api.New(newFake()))
	p := create(t, h, minimalPartner)
	create(t, h, `{"sellerEntity":"S","buyerEntity":"X","customerAccount":"IC-X","vendorAccount":"IC-S"}`)
	rec := call{method: http.MethodGet, path: partnersPath + "/" + p.ID, duties: api.DutyView}.on(h)
	if rec.Code != http.StatusOK || rec.Header().Get("ETag") != `"1"` || decode[partnerBody](t, rec).CustomerAccount != "IC-S" {
		t.Fatalf("get = %d %v %s", rec.Code, rec.Header(), rec.Body.String())
	}
	for _, missing := range []string{ids.New(), "not-a-uuid"} {
		rec := call{method: http.MethodGet, path: partnersPath + "/" + missing, duties: api.DutyView}.on(h)
		if pb := decode[partnerProblem](t, rec); rec.Code != http.StatusNotFound || pb.Code != domain.CodePartnerNotFound {
			t.Fatalf("get %s = %d %+v", missing, rec.Code, pb)
		}
	}
	other := call{method: http.MethodGet, path: partnersPath + "/" + p.ID, duties: api.DutyView, tenant: ids.New()}.on(h)
	if other.Code != http.StatusNotFound {
		t.Fatalf("another tenant reads the pairing: %d", other.Code)
	}
	list := call{method: http.MethodGet, path: partnersPath + "?entity=X", duties: api.DutyView}.on(h)
	items := decode[struct{ Items []partnerBody }](t, list).Items
	if list.Code != http.StatusOK || len(items) != 1 || items[0].BuyerEntity != "X" {
		t.Fatalf("list entity X = %d %s", list.Code, list.Body.String())
	}
	for _, q := range []string{"?limit=0", "?limit=201", "?active=yes", "?after=page-2"} {
		rec := call{method: http.MethodGet, path: partnersPath + q, duties: api.DutyView}.on(h)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("list %s = %d; want 400", q, rec.Code)
		}
	}
}

func TestUpdatePartner_IfMatchAndPolicyChange(t *testing.T) {
	h := router(api.New(newFake()))
	p := create(t, h, minimalPartner)
	path := partnersPath + "/" + p.ID
	patch := func(ifMatch, body string) *httptest.ResponseRecorder {
		headers := map[string]string{}
		if ifMatch != "" {
			headers["If-Match"] = ifMatch
		}
		return call{method: http.MethodPatch, path: path, duties: api.DutyConfigure, body: body, headers: headers}.on(h)
	}
	if rec := patch("", `{"active":false}`); rec.Code != http.StatusPreconditionRequired || decode[partnerProblem](t, rec).Code != domain.CodeVersionConflict {
		t.Fatalf("missing If-Match = %d %s", rec.Code, rec.Body.String())
	}
	if rec := patch("abc", `{"active":false}`); rec.Code != http.StatusBadRequest || decode[partnerProblem](t, rec).Errors["If-Match"] == "" {
		t.Fatalf("malformed If-Match = %d %s", rec.Code, rec.Body.String())
	}
	rec := patch(`"1"`, `{"pricing":"CostPlus","markupPct":"10","tolerance":{"pricePct":"1"}}`)
	got := decode[partnerBody](t, rec)
	if rec.Code != http.StatusOK || got.Version != 2 || got.Pricing != "CostPlus" || got.MarkupPct != "10" || got.Tolerance.PricePct != "1" ||
		got.Tolerance.DateDays != 2 || rec.Header().Get("ETag") != `"2"` {
		t.Fatalf("patch = %d %s", rec.Code, rec.Body.String())
	}
	if rec := patch(`"1"`, `{"active":false}`); rec.Code != http.StatusPreconditionFailed || decode[partnerProblem](t, rec).Code != domain.CodeVersionConflict {
		t.Fatalf("stale If-Match = %d %s", rec.Code, rec.Body.String())
	}
	if rec := patch(`W/"2"`, `{"sellerEntity":"Q"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("changing the direction = %d %s", rec.Code, rec.Body.String())
	}
	if rec := patch(`"2"`, `{"tolerance":{"dateDays":31}}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("31 days = %d %s", rec.Code, rec.Body.String())
	}
}

type pricedBody struct {
	PartnerVersion int    `json:"partnerVersion"`
	UnitPrice      string `json:"unitPrice"`
	Extended       string `json:"extended"`
	Basis          struct {
		Policy, Method, MarkupPct, Source, EffectiveFrom string
	} `json:"basis"`
}

func TestPriceLine_FromStoredPolicy(t *testing.T) {
	h := router(api.New(newFake()))
	costPlus := create(t, h, `{"sellerEntity":"P","buyerEntity":"S","customerAccount":"IC-S","vendorAccount":"IC-P","pricing":"CostPlus","markupPct":"10"}`)
	table := create(t, h, `{"sellerEntity":"S","buyerEntity":"P","customerAccount":"IC-P","vendorAccount":"IC-S","pricing":"TransferPriceTable"}`)
	list := create(t, h, `{"sellerEntity":"P","buyerEntity":"X","customerAccount":"IC-X","vendorAccount":"IC-P"}`)
	price := func(id, body string) *httptest.ResponseRecorder {
		return call{method: http.MethodPost, path: fmt.Sprintf("%s/%s/lines:price", partnersPath, id), duties: api.DutyView, body: body}.on(h)
	}

	// 8 units at cost 60.00 with the pairing's 10 % markup: 66 a unit, 528.00 in all.
	rec := price(costPlus.ID, `{"item":"PUMP-100","date":"2026-11-02","currency":"EUR","quantity":"8","unitCost":"60.00"}`)
	got := decode[pricedBody](t, rec)
	if rec.Code != http.StatusOK || got.UnitPrice != "66" || got.Extended != "528.00" || got.Basis.Method != "CostPlus" ||
		got.Basis.MarkupPct != "10" || got.Basis.Source != domain.SourcePartnerPolicy || got.PartnerVersion != 1 {
		t.Fatalf("cost plus = %d %s", rec.Code, rec.Body.String())
	}
	if rec := price(costPlus.ID, `{"item":"PUMP-100","date":"2026-11-02","currency":"EUR","quantity":"8"}`); rec.Code != http.StatusBadRequest ||
		decode[partnerProblem](t, rec).Errors["unitCost"] == "" {
		t.Fatalf("cost plus without a cost = %d %s", rec.Code, rec.Body.String())
	}

	tableBody := `{"item":"PUMP-100","date":"2026-11-02","currency":"EUR","quantity":"3","unitCost":"18","transferPrices":[
		{"item":"PUMP-100","method":"Table","unitPrice":"24.00","effectiveFrom":"2026-01-01"},
		{"item":"PUMP-100","method":"Table","unitPrice":"25.00","effectiveFrom":"2026-12-01"}]}`
	rec = price(table.ID, tableBody)
	got = decode[pricedBody](t, rec)
	if rec.Code != http.StatusOK || got.UnitPrice != "24" || got.Extended != "72.00" || got.Basis.Source != domain.SourceTransferTable || got.Basis.EffectiveFrom != "2026-01-01" {
		t.Fatalf("table = %d %s", rec.Code, rec.Body.String())
	}
	rec = price(table.ID, strings.Replace(tableBody, `"item":"PUMP-100","date"`, `"item":"VALVE-7","date"`, 1))
	if p := decode[partnerProblem](t, rec); rec.Code != http.StatusUnprocessableEntity || p.Code != domain.CodeTransferPrice {
		t.Fatalf("no row for the item = %d %+v", rec.Code, p)
	}
	if rec := price(table.ID, `{"item":"PUMP-100","date":"2026-11-02","currency":"EUR","quantity":"3","unitCost":"18"}`); rec.Code != http.StatusBadRequest ||
		decode[partnerProblem](t, rec).Errors["transferPrices"] == "" {
		t.Fatalf("table without rows = %d %s", rec.Code, rec.Body.String())
	}

	rec = price(list.ID, `{"item":"PUMP-100","date":"2026-11-02","currency":"JPY","quantity":"3","listPrice":"333.5"}`)
	if got := decode[pricedBody](t, rec); rec.Code != http.StatusOK || got.Extended != "1001" || got.Basis.Method != "PriceList" {
		t.Fatalf("price list in JPY = %d %s", rec.Code, rec.Body.String())
	}

	off := call{method: http.MethodPatch, path: partnersPath + "/" + list.ID, duties: api.DutyConfigure, body: `{"active":false}`, headers: map[string]string{"If-Match": `"1"`}}.on(h)
	if off.Code != http.StatusOK {
		t.Fatalf("deactivate = %d %s", off.Code, off.Body.String())
	}
	rec = price(list.ID, `{"item":"PUMP-100","date":"2026-11-02","currency":"EUR","quantity":"3","listPrice":"25"}`)
	if p := decode[partnerProblem](t, rec); rec.Code != http.StatusUnprocessableEntity || p.Code != domain.CodeNoPolicy {
		t.Fatalf("inactive pairing = %d %+v", rec.Code, p)
	}
	if rec := price(ids.New(), `{"item":"X","date":"2026-11-02","currency":"EUR","quantity":"1","listPrice":"1"}`); rec.Code != http.StatusNotFound {
		t.Fatalf("missing pairing = %d", rec.Code)
	}
	if rec := price(costPlus.ID, `{"item":"","date":"11/02/2026","currency":"eur","quantity":"0"}`); rec.Code != http.StatusBadRequest ||
		len(decode[partnerProblem](t, rec).Errors) != 4 {
		t.Fatalf("bad line = %d %s", rec.Code, rec.Body.String())
	}
}

func TestEvaluateChange_UsesStoredTolerance(t *testing.T) {
	h := router(api.New(newFake()))
	p := create(t, h, `{"sellerEntity":"P","buyerEntity":"S","customerAccount":"IC-S","vendorAccount":"IC-P","tolerance":{"pricePct":"1","dateDays":3}}`)
	evaluate := func(body string) *httptest.ResponseRecorder {
		return call{method: http.MethodPost, path: partnersPath + "/" + p.ID + "/changes:evaluate", duties: api.DutyView, body: body}.on(h)
	}
	type decision struct {
		Action        string   `json:"action"`
		Code          string   `json:"code"`
		Reasons       []string `json:"reasons"`
		DateShiftDays int      `json:"dateShiftDays"`
	}
	// 1 % and 3 days are inside this pairing's tolerance, outside the default.
	rec := evaluate(`{"oldQuantity":"100","newQuantity":"100","oldPrice":"25.00","newPrice":"25.25","oldDate":"2026-12-10","newDate":"2026-12-13"}`)
	if got := decode[decision](t, rec); rec.Code != http.StatusOK || got.Action != domain.ChangeUpdateSilently || got.Code != "" || len(got.Reasons) != 0 || got.DateShiftDays != 3 {
		t.Fatalf("within tolerance = %d %s", rec.Code, rec.Body.String())
	}
	rec = evaluate(`{"oldQuantity":"100","newQuantity":"90","oldPrice":"25.00","newPrice":"25.00","oldDate":"2026-12-10","newDate":"2026-12-06"}`)
	if got := decode[decision](t, rec); rec.Code != http.StatusOK || got.Action != domain.ChangeReApprove || got.Code != domain.CodeChangeTolerance ||
		len(got.Reasons) != 2 || got.DateShiftDays != -4 {
		t.Fatalf("quantity and date breach = %d %s", rec.Code, rec.Body.String())
	}
	if rec := evaluate(`{"oldQuantity":"0","newQuantity":"x","oldPrice":"-1","newPrice":"1","oldDate":"2026-12-10","newDate":"soon"}`); rec.Code != http.StatusBadRequest ||
		len(decode[partnerProblem](t, rec).Errors) != 4 {
		t.Fatalf("bad change = %d %s", rec.Code, rec.Body.String())
	}
}

func TestPartnerMetrics(t *testing.T) {
	a := api.New(newFake())
	h := router(a)
	p := create(t, h, minimalPartner)
	call{method: http.MethodPost, path: partnersPath + "/" + p.ID + "/lines:price", duties: api.DutyView,
		body: `{"item":"X","date":"2026-11-02","currency":"EUR","quantity":"1","listPrice":"1"}`}.on(h)
	m := httptest.NewRecorder()
	a.Metrics().ServeHTTP(m, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if !strings.Contains(m.Body.String(), "intercompany_partner_changes_total 1") || !strings.Contains(m.Body.String(), "intercompany_lines_priced_total 1") {
		t.Fatalf("metrics = %s", m.Body.String())
	}
}
