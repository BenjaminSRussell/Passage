package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/assets/internal/api"
	"github.com/BenjaminSRussell/passage/services/assets/internal/domain"
	"github.com/BenjaminSRussell/passage/services/assets/internal/store"
)

// fakeStore is an in-memory store.Store for handler tests. It records the last
// disposal input so tests can check what the handler passed on.
type fakeStore struct {
	assets       map[string]store.Asset
	books        map[string]store.Book
	leases       map[string]store.Lease
	lastDisposal store.DisposalInput
	lastFilter   store.AssetFilter

	// Slice 2 (ops_test.go, insurance_capex_test.go).
	runs         map[string]store.DepreciationRun
	capex        map[string]store.CapexRequest
	series       map[string]store.IndexSeries
	impairment   map[string]decimal.Decimal
	lastAdjust   store.AdjustmentInput
	lastAddition store.AdditionInput
	lastRun      store.RunInput
	lastPolicy   store.InsurancePolicy
	lastSubmit   store.SubmitInput

	// Slice 4 (capex_decision_test.go).
	lastDecision   store.CapexDecisionInput
	lastMidQuarter store.MidQuarterFilter

	// Slice 5 (transfer_group_test.go).
	groups          map[string]store.GroupAsset
	lastTransfer    store.TransferInput
	lastGroup       store.GroupAssetInput
	lastGroupFilter store.GroupFilter
}

func newFake() *fakeStore {
	return &fakeStore{assets: map[string]store.Asset{}, books: map[string]store.Book{}, leases: map[string]store.Lease{},
		runs: map[string]store.DepreciationRun{}, capex: map[string]store.CapexRequest{}, series: map[string]store.IndexSeries{}, impairment: map[string]decimal.Decimal{},
		groups: map[string]store.GroupAsset{}}
}

func (f *fakeStore) CreateAsset(_ context.Context, in store.Asset, _ string) (store.Asset, error) {
	in.ID, in.Status, in.Version = ids.New(), string(domain.StatusNotYetAcquired), 1
	f.assets[in.ID] = in
	return in, nil
}

func (f *fakeStore) ListAssets(_ context.Context, filter store.AssetFilter) (store.AssetPage, error) {
	f.lastFilter = filter
	page := store.AssetPage{Items: []store.Asset{}}
	for _, a := range f.assets {
		page.Items = append(page.Items, a)
	}
	return page, nil
}

func (f *fakeStore) GetAsset(_ context.Context, id string) (store.AssetDetail, error) {
	a, ok := f.assets[id]
	if !ok {
		return store.AssetDetail{}, httpx.NewProblem(http.StatusNotFound, store.CodeAssetNotFound, "no asset")
	}
	detail := store.AssetDetail{Asset: a, Books: []store.Book{}}
	for _, b := range f.books {
		if b.AssetID == id {
			detail.Books = append(detail.Books, b)
		}
	}
	return detail, nil
}

func (f *fakeStore) AddBook(_ context.Context, assetID string, in store.Book, _ string) (store.Book, error) {
	in.ID, in.AssetID, in.AccumulatedDepreciation, in.NBV, in.Status = ids.New(), assetID, decimal.Zero, in.Cost, "Open"
	f.books[in.ID] = in
	return in, nil
}

func (f *fakeStore) GetBook(_ context.Context, _, bookID string) (store.Book, error) {
	b, ok := f.books[bookID]
	if !ok {
		return store.Book{}, httpx.NewProblem(http.StatusNotFound, store.CodeBookNotFound, "no book")
	}
	return b, nil
}

func (f *fakeStore) ListTransactions(_ context.Context, assetID string) ([]store.Transaction, error) {
	if _, ok := f.assets[assetID]; !ok {
		return nil, httpx.NewProblem(http.StatusNotFound, store.CodeAssetNotFound, "no asset")
	}
	return []store.Transaction{}, nil
}

func (f *fakeStore) Dispose(_ context.Context, assetID string, in store.DisposalInput, _ string) (store.Disposal, error) {
	f.lastDisposal = in
	a, ok := f.assets[assetID]
	if !ok {
		return store.Disposal{}, httpx.NewProblem(http.StatusNotFound, store.CodeAssetNotFound, "no asset")
	}
	if in.Version != a.Version {
		return store.Disposal{}, httpx.NewProblem(http.StatusPreconditionFailed, store.CodeVersionConflict, "stale")
	}
	a.Version++
	f.assets[assetID] = a
	return store.Disposal{Asset: a, Books: []store.Book{}, Transactions: []store.Transaction{}}, nil
}

func (f *fakeStore) CreateLease(_ context.Context, in store.Lease, _ string) (store.Lease, error) {
	in.ID, in.Status, in.Version = ids.New(), "Active", 1
	f.leases[in.ID] = in
	return in, nil
}

func (f *fakeStore) ListLeases(_ context.Context, _ store.LeaseFilter) (store.LeasePage, error) {
	page := store.LeasePage{Items: []store.Lease{}}
	for _, l := range f.leases {
		l.Schedule = nil
		page.Items = append(page.Items, l)
	}
	return page, nil
}

func (f *fakeStore) GetLease(_ context.Context, id string) (store.Lease, error) {
	l, ok := f.leases[id]
	if !ok {
		return store.Lease{}, httpx.NewProblem(http.StatusNotFound, store.CodeLeaseNotFound, "no lease")
	}
	return l, nil
}

func (f *fakeStore) Ping(context.Context) error { return nil }

const allDuties = "AssetView,AssetMaintain,AssetDispose,LeaseMaintain"

type req struct {
	method, path, body, duties string
	key                        bool
	headers                    map[string]string
}

func do(h http.Handler, q req) *httptest.ResponseRecorder {
	r := httptest.NewRequest(q.method, q.path, strings.NewReader(q.body))
	r.Header.Set("X-Passage-Tenant", testkit.TenantID(1))
	r.Header.Set("X-Passage-Duties", q.duties)
	if q.key {
		r.Header.Set("Idempotency-Key", ids.New())
	}
	for k, v := range q.headers {
		r.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	return rec
}

func call(h http.Handler, method, path, body, duties string, withKey bool) *httptest.ResponseRecorder {
	return do(h, req{method: method, path: path, body: body, duties: duties, key: withKey})
}

func problem(t *testing.T, rec *httptest.ResponseRecorder) httpx.Problem {
	t.Helper()
	var p httpx.Problem
	if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
		t.Fatalf("not a problem body: %s", rec.Body.String())
	}
	return p
}

func server(f store.Store) http.Handler {
	r := httpx.NewRouter()
	api.New(f).Register(r)
	return r
}

func createAsset(t *testing.T, h http.Handler) store.Asset {
	t.Helper()
	rec := call(h, http.MethodPost, "/v1/assets", `{"legalEntityId":"`+ids.New()+`","assetNumber":"FA-1","name":"Van","classCode":"VEH"}`, allDuties, true)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create asset status = %d body %s", rec.Code, rec.Body.String())
	}
	var a store.Asset
	if err := json.Unmarshal(rec.Body.Bytes(), &a); err != nil {
		t.Fatal(err)
	}
	return a
}

// addAsset creates an asset and attaches one book, returning both IDs.
func addAsset(t *testing.T, h http.Handler, book string) (string, string) {
	t.Helper()
	a := createAsset(t, h)
	rec := call(h, http.MethodPost, "/v1/assets/"+a.ID+"/books", book, allDuties, true)
	if rec.Code != http.StatusCreated {
		t.Fatalf("add book status = %d body %s", rec.Code, rec.Body.String())
	}
	var b store.Book
	if err := json.Unmarshal(rec.Body.Bytes(), &b); err != nil {
		t.Fatal(err)
	}
	return a.ID, b.ID
}

const midMonthBook = `{"bookCode":"IFRS","currency":"USD","method":"StraightLine","serviceLifePeriods":60,"convention":"MidMonth15",
	"inServiceDate":"2026-03-10","cost":"12000"}`

func TestCreateAsset_DutyKeyTenantAndScope(t *testing.T) {
	h := server(newFake())
	le := ids.New()
	body := `{"legalEntityId":"` + le + `","assetNumber":"FA-000001","name":"Press","classCode":"MACHINE","barcode":" BC-1 "}`
	if rec := call(h, http.MethodPost, "/v1/assets", body, "AssetView", true); rec.Code != http.StatusForbidden {
		t.Errorf("without AssetMaintain status = %d, want 403", rec.Code)
	}
	if rec := call(h, http.MethodPost, "/v1/assets", body, allDuties, false); rec.Code != http.StatusBadRequest {
		t.Errorf("without Idempotency-Key status = %d, want 400", rec.Code)
	}
	bad := do(h, req{method: http.MethodPost, path: "/v1/assets", body: body, duties: allDuties, key: true, headers: map[string]string{"X-Passage-Tenant": "acme"}})
	if bad.Code != http.StatusUnauthorized {
		t.Errorf("malformed tenant status = %d, want 401", bad.Code)
	}
	other := do(h, req{method: http.MethodPost, path: "/v1/assets", body: body, duties: allDuties, key: true, headers: map[string]string{"X-Passage-Legal-Entity": ids.New()}})
	if other.Code != http.StatusForbidden {
		t.Errorf("legal entity outside scope status = %d, want 403", other.Code)
	}
	rec := call(h, http.MethodPost, "/v1/assets", body, allDuties, true)
	if rec.Code != http.StatusCreated || rec.Header().Get("ETag") != `"1"` {
		t.Fatalf("status = %d etag %q body %s", rec.Code, rec.Header().Get("ETag"), rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"barcode":"BC-1"`) {
		t.Errorf("barcode not trimmed: %s", rec.Body.String())
	}
}

func TestCreateAsset_ValidationListsFields(t *testing.T) {
	h := server(newFake())
	rec := call(h, http.MethodPost, "/v1/assets", `{"legalEntityId":"nope","assetNumber":"","name":"x","classCode":"lower","barcode":"","quantity":"0"}`, allDuties, true)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d", rec.Code)
	}
	p := problem(t, rec)
	for _, field := range []string{"legalEntityId", "assetNumber", "classCode", "barcode", "quantity"} {
		if p.Errors[field] == "" {
			t.Errorf("field %s missing from %v", field, p.Errors)
		}
	}
	if p.Errors["name"] != "" {
		t.Errorf("valid name flagged: %v", p.Errors)
	}
}

func TestListAssets_FiltersAndValidation(t *testing.T) {
	f := newFake()
	h := server(f)
	createAsset(t, h)
	rec := call(h, http.MethodGet, "/v1/assets?status=Open&limit=10", "", "AssetView", false)
	if rec.Code != http.StatusOK || f.lastFilter.Status != "Open" || f.lastFilter.Limit != 10 {
		t.Fatalf("status %d filter %+v", rec.Code, f.lastFilter)
	}
	if rec := call(h, http.MethodGet, "/v1/assets", "", "AssetView", false); rec.Code != http.StatusOK || f.lastFilter.Limit != 50 {
		t.Fatalf("default limit = %d", f.lastFilter.Limit)
	}
	bad := call(h, http.MethodGet, "/v1/assets?status=Gone&limit=500&after=x", "", "AssetView", false)
	if bad.Code != http.StatusBadRequest {
		t.Fatalf("bad filters status = %d", bad.Code)
	}
	if p := problem(t, bad); p.Errors["status"] == "" || p.Errors["limit"] == "" || p.Errors["after"] == "" {
		t.Errorf("errors = %v", p.Errors)
	}
	if rec := call(h, http.MethodGet, "/v1/assets", "", "LeaseMaintain", false); rec.Code != http.StatusForbidden {
		t.Errorf("without AssetView status = %d", rec.Code)
	}
}

func TestGetAsset_IncludesBooksAndETag(t *testing.T) {
	h := server(newFake())
	assetID, bookID := addAsset(t, h, midMonthBook)
	rec := call(h, http.MethodGet, "/v1/assets/"+assetID, "", "AssetView", false)
	if rec.Code != http.StatusOK || rec.Header().Get("ETag") != `"1"` {
		t.Fatalf("status %d etag %q", rec.Code, rec.Header().Get("ETag"))
	}
	var detail store.AssetDetail
	if err := json.Unmarshal(rec.Body.Bytes(), &detail); err != nil {
		t.Fatal(err)
	}
	if len(detail.Books) != 1 || detail.Books[0].ID != bookID || detail.Books[0].InServiceDate.String() != "2026-03-10" {
		t.Fatalf("books = %+v", detail.Books)
	}
	if !strings.Contains(rec.Body.String(), `"inServiceDate":"2026-03-10"`) {
		t.Errorf("in-service date is not a calendar date on the wire: %s", rec.Body.String())
	}
	if rec := call(h, http.MethodGet, "/v1/assets/not-a-uuid", "", "AssetView", false); rec.Code != http.StatusNotFound || problem(t, rec).Code != store.CodeAssetNotFound {
		t.Errorf("malformed id status = %d", rec.Code)
	}
}

func TestSchedule_MidMonthExampleAC1(t *testing.T) {
	// AC-37.1 through the HTTP surface: March 2026 is a half month and the life totals 12,000.00.
	h := server(newFake())
	assetID, bookID := addAsset(t, h, midMonthBook)
	rec := call(h, http.MethodGet, "/v1/assets/"+assetID+"/schedule?book="+bookID, "", allDuties, false)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Total string `json:"total"`
		Lines []struct {
			Period string `json:"period"`
			Amount string `json:"amount"`
		} `json:"lines"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Lines) != 61 || out.Lines[0].Period != "2026-03" || out.Lines[0].Amount != "100.00" || out.Lines[60].Amount != "100.00" || out.Total != "12000.00" {
		t.Fatalf("lines = %d, first = %+v, total %s", len(out.Lines), out.Lines[0], out.Total)
	}
	if rec := call(h, http.MethodGet, "/v1/assets/"+assetID+"/schedule?book=", "", allDuties, false); rec.Code != http.StatusBadRequest {
		t.Errorf("missing book status = %d, want 400", rec.Code)
	}
}

func TestSchedule_UsesCurrencyMinorUnits(t *testing.T) {
	h := server(newFake())
	assetID, bookID := addAsset(t, h, `{"bookCode":"JGAAP","currency":"JPY","method":"StraightLine","serviceLifePeriods":3,"convention":"None",
		"inServiceDate":"2026-01-01","cost":"1000"}`)
	rec := call(h, http.MethodGet, "/v1/assets/"+assetID+"/schedule?book="+bookID, "", allDuties, false)
	if !strings.Contains(rec.Body.String(), `"amount":"333"`) || !strings.Contains(rec.Body.String(), `"amount":"334"`) || !strings.Contains(rec.Body.String(), `"total":"1000"`) {
		t.Fatalf("JPY schedule = %s", rec.Body.String())
	}
}

func TestAddBook_RejectsConventionForMethodAndMidMonth1(t *testing.T) {
	h := server(newFake())
	a := createAsset(t, h)
	rec := call(h, http.MethodPost, "/v1/assets/"+a.ID+"/books", `{"bookCode":"TAX","currency":"USD","method":"DecliningBalance","factor":"2",
		"serviceLifePeriods":5,"convention":"MidMonth15","inServiceDate":"2026-03-10","cost":"25000"}`, allDuties, true)
	if rec.Code != http.StatusUnprocessableEntity || problem(t, rec).Code != domain.CodeInvalidDepreciation {
		t.Fatalf("status = %d body %s", rec.Code, rec.Body.String())
	}
	rec = call(h, http.MethodPost, "/v1/assets/"+a.ID+"/books", strings.Replace(midMonthBook, "MidMonth15", "MidMonth1", 1), allDuties, true)
	if rec.Code != http.StatusBadRequest || problem(t, rec).Errors["convention"] == "" {
		t.Fatalf("MidMonth1 status = %d body %s", rec.Code, rec.Body.String())
	}
	if rec := call(h, http.MethodPost, "/v1/assets/"+ids.New()+"/books", midMonthBook, allDuties, true); rec.Code != http.StatusNotFound {
		t.Errorf("unknown asset status = %d", rec.Code)
	}
}

func TestDispose_PreconditionsAndHappyPath(t *testing.T) {
	f := newFake()
	h := server(f)
	assetID, _ := addAsset(t, h, `{"bookCode":"IFRS","currency":"USD","method":"StraightLine","serviceLifePeriods":60,"convention":"None",
		"inServiceDate":"2026-03-01","cost":"10000"}`)
	path := "/v1/assets/" + assetID + "/dispose"
	body := `{"kind":"sale","percentage":"50","proceeds":"7000","transactionDate":"2026-10-01"}`
	noMatch := call(h, http.MethodPost, path, body, allDuties, true)
	if noMatch.Code != http.StatusPreconditionRequired || problem(t, noMatch).Code != store.CodeVersionConflict {
		t.Fatalf("without If-Match: status %d body %s", noMatch.Code, noMatch.Body.String())
	}
	withMatch := func(b, etag string) *httptest.ResponseRecorder {
		return do(h, req{method: http.MethodPost, path: path, body: b, duties: allDuties, key: true, headers: map[string]string{"If-Match": etag}})
	}
	if rec := withMatch(strings.Replace(body, `"50"`, `"0"`, 1), `"1"`); rec.Code != http.StatusUnprocessableEntity || problem(t, rec).Code != domain.CodeInvalidDisposal {
		t.Fatalf("percentage 0: status %d body %s", rec.Code, rec.Body.String())
	}
	if rec := withMatch(`{"kind":"scrap","percentage":"100","proceeds":"5"}`, `"1"`); rec.Code != http.StatusBadRequest || problem(t, rec).Errors["proceeds"] == "" {
		t.Fatalf("scrap with proceeds: status %d body %s", rec.Code, rec.Body.String())
	}
	if rec := withMatch(body, `"9"`); rec.Code != http.StatusPreconditionFailed {
		t.Fatalf("stale If-Match: status %d", rec.Code)
	}
	ok := withMatch(body, `W/"1"`)
	if ok.Code != http.StatusCreated || ok.Header().Get("ETag") != `"2"` {
		t.Fatalf("status = %d etag %q body %s", ok.Code, ok.Header().Get("ETag"), ok.Body.String())
	}
	if got := f.lastDisposal; got.Version != 1 || !got.Percentage.Equal(decimal.NewFromInt(50)) || got.Date.String() != "2026-10-01" || got.Kind != domain.DisposalSale {
		t.Errorf("store received %+v", got)
	}
	if rec := withMatch(body, `"2"`); rec.Code != http.StatusCreated {
		t.Fatalf("second disposal: status %d", rec.Code)
	}
	if rec := call(h, http.MethodPost, path, body, "AssetMaintain", true); rec.Code != http.StatusForbidden {
		t.Errorf("without AssetDispose status = %d", rec.Code)
	}
}

func TestLease_WorkedExampleListAndIBRRange(t *testing.T) {
	// AC-37.6 through the HTTP surface, and AST-0515 for an IBR above 50%.
	h := server(newFake())
	body := `{"legalEntityId":"` + ids.New() + `","leaseNumber":"L-1","commencementDate":"2026-01-01","termMonths":60,
		"paymentsPerYear":1,"paymentAmount":"10000","paymentTiming":"arrears","ibrRate":"0.05","currency":"USD"}`
	rec := call(h, http.MethodPost, "/v1/leases", body, allDuties, true)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d body %s", rec.Code, rec.Body.String())
	}
	var lease store.Lease
	if err := json.Unmarshal(rec.Body.Bytes(), &lease); err != nil {
		t.Fatal(err)
	}
	if lease.Liability.StringFixed(2) != "43294.77" || len(lease.Schedule) != 5 || !lease.Schedule[4].Closing.IsZero() || lease.Schedule[4].Interest.StringFixed(2) != "476.18" {
		t.Fatalf("lease = liability %s, %d lines", lease.Liability, len(lease.Schedule))
	}
	if got := call(h, http.MethodGet, "/v1/leases/"+lease.ID, "", allDuties, false); got.Code != http.StatusOK {
		t.Errorf("get lease status = %d", got.Code)
	}
	list := call(h, http.MethodGet, "/v1/leases", "", "LeaseMaintain", false)
	if list.Code != http.StatusOK || !strings.Contains(list.Body.String(), `"leaseNumber":"L-1"`) || strings.Contains(list.Body.String(), `"schedule"`) {
		t.Errorf("list = %d %s", list.Code, list.Body.String())
	}
	bad := call(h, http.MethodPost, "/v1/leases", strings.Replace(body, `"0.05"`, `"0.6"`, 1), allDuties, true)
	if bad.Code != http.StatusUnprocessableEntity || problem(t, bad).Code != domain.CodeIBRRange {
		t.Fatalf("IBR 0.6: status %d body %s", bad.Code, bad.Body.String())
	}
	odd := call(h, http.MethodPost, "/v1/leases", strings.Replace(body, `"termMonths":60`, `"termMonths":61`, 1), allDuties, true)
	if odd.Code != http.StatusUnprocessableEntity || problem(t, odd).Code != domain.CodeInvalidLease {
		t.Fatalf("term 61 months annual: status %d body %s", odd.Code, odd.Body.String())
	}
	if rec := call(h, http.MethodGet, "/v1/leases/"+ids.New(), "", "LeaseMaintain", false); rec.Code != http.StatusNotFound {
		t.Errorf("unknown lease status = %d", rec.Code)
	}
	if rec := call(h, http.MethodPost, "/v1/leases", body, "AssetMaintain", true); rec.Code != http.StatusForbidden {
		t.Errorf("without LeaseMaintain status = %d", rec.Code)
	}
}

func TestTransactions_UnknownAsset(t *testing.T) {
	h := server(newFake())
	if rec := call(h, http.MethodGet, "/v1/assets/"+ids.New()+"/transactions", "", "AssetView", false); rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d", rec.Code)
	}
	a := createAsset(t, h)
	rec := call(h, http.MethodGet, "/v1/assets/"+a.ID+"/transactions", "", "AssetView", false)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"items":[]`) {
		t.Fatalf("status = %d body %s", rec.Code, rec.Body.String())
	}
}
