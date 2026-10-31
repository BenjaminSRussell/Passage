package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/production/internal/domain"
	"github.com/BenjaminSRussell/passage/services/production/internal/store"
)

// fakeStore records what the handlers pass and answers with canned results.
type fakeStore struct {
	pingErr        error
	created        []store.NewOrder
	replayHash     map[string]string // idempotency key → request hash already stored
	order          store.Order
	getErr         error
	listFilter     store.ListFilter
	transitions    []store.Transition
	transErr       error
	updates        []store.OrderUpdate
	log            []store.StatusLogEntry
	costs          store.CostSummary
	journals       []store.NewJournal
	posts          []store.PostRequest
	journal        store.Journal
	journalErr     error
	deleted        []string
	profiles       []store.PostingProfile
	saved          []store.ProfileSave
	assigned       []store.NewAsBuilt
	asBuiltF       []store.AsBuiltFilter
	detached       []string
	asBuiltErr     error
	splits         []store.Split
	merges         []store.Merge
	bulks          []store.Bulk
	batchErr       error
	edits          []store.OrderEdit
	editErr        error
	editLog        []store.OrderEditEntry
	wipFilters     []store.WIPFilter
	wip            store.WIPReport
	varFilters     []store.VarianceFilter
	variances      store.VarianceReport
	triggers       []store.NewTriggerPoint
	triggerList    []store.TriggerPoint
	triggerDel     []string
	triggerErr     error
	triggerOld     bool // CreateTriggerPoint answers as a replay
	subFilters     []store.SubcontractFilter
	opFilters      []store.OperationFilter
	opOverview     store.OperationsOverview
	journalFilters []store.JournalFilter
	journalPage    store.JournalInquiry
	subcontract    store.SubcontractReport
	// operation commands (FR-21.6)
	operCommands []store.OperationCommand
	operOutcome  store.OperationOutcome
	operErr      error
	// order change management (FR-21.16)
	ocm ocmFake
}

func (f *fakeStore) Ping(context.Context) error { return f.pingErr }

func (f *fakeStore) CreateOrder(_ context.Context, in store.NewOrder) (store.Order, bool, error) {
	if h, ok := f.replayHash[in.IdempotencyKey]; ok {
		if h != in.RequestHash {
			return store.Order{}, false, httpx.NewProblem(http.StatusUnprocessableEntity, store.CodeIdempotencyReuse, "reused")
		}
		return f.order, false, nil
	}
	f.created = append(f.created, in)
	if f.replayHash == nil {
		f.replayHash = map[string]string{}
	}
	f.replayHash[in.IdempotencyKey] = in.RequestHash
	return store.Order{ID: ids.New(), Version: 1, Status: domain.Created, Lines: in.Lines, Operations: in.Operations}, true, nil
}

func (f *fakeStore) GetOrder(context.Context, string) (store.Order, error) { return f.order, f.getErr }

func (f *fakeStore) ListOrders(_ context.Context, lf store.ListFilter) (store.OrderPage, error) {
	f.listFilter = lf
	return store.OrderPage{Items: []store.OrderSummary{{ID: f.order.ID, OrderNumber: "PO-000001", Status: domain.Created}}}, nil
}

func (f *fakeStore) StatusLog(context.Context, string) ([]store.StatusLogEntry, error) {
	return f.log, f.getErr
}

func (f *fakeStore) UpdateOrder(_ context.Context, u store.OrderUpdate) (store.Order, error) {
	f.updates = append(f.updates, u)
	return store.Order{ID: u.OrderID, Version: u.IfMatch + 1}, nil
}

func (f *fakeStore) Transition(_ context.Context, t store.Transition) (store.Order, bool, error) {
	f.transitions = append(f.transitions, t)
	if f.transErr != nil {
		return store.Order{}, false, f.transErr
	}
	return store.Order{ID: t.OrderID, Version: t.IfMatch + 1}, false, nil
}

func (f *fakeStore) Costs(context.Context, string) (store.CostSummary, error) {
	return f.costs, f.getErr
}

func (f *fakeStore) CreateJournal(_ context.Context, in store.NewJournal) (store.Journal, bool, error) {
	f.journals = append(f.journals, in)
	return store.Journal{ID: ids.New(), OrderID: in.OrderID, Type: in.Type}, true, f.journalErr
}

func (f *fakeStore) PostJournal(_ context.Context, r store.PostRequest) (store.Journal, bool, error) {
	f.posts = append(f.posts, r)
	if f.journalErr != nil {
		return store.Journal{}, false, f.journalErr
	}
	return store.Journal{ID: r.JournalID, Posted: true}, len(f.posts) > 1, nil
}

func (f *fakeStore) GetJournal(context.Context, string) (store.Journal, error) {
	return f.journal, f.journalErr
}

func (f *fakeStore) ListJournals(context.Context, string) ([]store.Journal, error) {
	return []store.Journal{f.journal}, f.journalErr
}

func (f *fakeStore) DeleteJournal(_ context.Context, id string) error {
	f.deleted = append(f.deleted, id)
	return f.journalErr
}

func (f *fakeStore) ListPostingProfiles(context.Context, string) ([]store.PostingProfile, error) {
	return f.profiles, nil
}

func (f *fakeStore) SavePostingProfile(_ context.Context, in store.ProfileSave) (store.PostingProfile, bool, error) {
	f.saved = append(f.saved, in)
	return store.PostingProfile{LegalEntityID: in.LegalEntityID, Profile: in.Profile, Version: in.IfMatch + 1}, in.IfMatch == 0, nil
}

func (f *fakeStore) AssignAsBuilt(_ context.Context, in store.NewAsBuilt) (store.AsBuiltRecord, bool, error) {
	if f.asBuiltErr != nil {
		return store.AsBuiltRecord{}, false, f.asBuiltErr
	}
	for _, prev := range f.assigned {
		if prev.IdempotencyKey == in.IdempotencyKey {
			if prev.RequestHash != in.RequestHash {
				return store.AsBuiltRecord{}, false, httpx.NewProblem(http.StatusUnprocessableEntity, store.CodeIdempotencyReuse, "reused")
			}
			return store.AsBuiltRecord{OrderID: in.OrderID}, false, nil
		}
	}
	f.assigned = append(f.assigned, in)
	return store.AsBuiltRecord{ID: ids.New(), OrderID: in.OrderID, FinishedSerialOrBatch: in.AsBuilt.FinishedSerial,
		ComponentSerialOrBatch: in.AsBuilt.ComponentSerial, Tracking: in.AsBuilt.Tracking, Qty: in.AsBuilt.Qty}, true, nil
}

func (f *fakeStore) ListAsBuilt(_ context.Context, af store.AsBuiltFilter) ([]store.AsBuiltRecord, error) {
	f.asBuiltF = append(f.asBuiltF, af)
	return []store.AsBuiltRecord{}, f.asBuiltErr
}

func (f *fakeStore) DetachAsBuilt(_ context.Context, id, _ string) (store.AsBuiltRecord, error) {
	f.detached = append(f.detached, id)
	return store.AsBuiltRecord{ID: id}, f.asBuiltErr
}

var (
	tenantID = "00000000-0000-0000-0000-000000000021"
	entityID = "00000000-0000-0000-0000-0000000000e1"
)

func newServer(t *testing.T, f *fakeStore) (*API, http.Handler) {
	t.Helper()
	a := New(f, Config{RAFTolerancePct: decimal.NewFromInt(5), OverConsumptionPct: decimal.NewFromInt(10), AutoStartOnFeedback: true})
	r := httpx.NewRouter()
	a.Register(r)
	return a, r
}

type call struct {
	method, path, body string
	duties             []string
	headers            map[string]string
}

func do(h http.Handler, c call) *httptest.ResponseRecorder {
	req := httptest.NewRequest(c.method, c.path, strings.NewReader(c.body))
	req.Header.Set(tenant.HeaderTenant, tenantID)
	req.Header.Set(tenant.HeaderLegalEntity, entityID)
	req.Header.Set(tenant.HeaderUser, "planner-1")
	req.Header.Set(tenant.HeaderDuties, strings.Join(c.duties, ","))
	for k, v := range c.headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func problemOf(t *testing.T, rec *httptest.ResponseRecorder) httpx.Problem {
	t.Helper()
	var p httpx.Problem
	if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
		t.Fatalf("not a problem body (%d): %s", rec.Code, rec.Body.String())
	}
	return p
}

func expectProblem(t *testing.T, rec *httptest.ResponseRecorder, status int, code string) httpx.Problem {
	t.Helper()
	if rec.Code != status {
		t.Fatalf("want %d %s, got %d: %s", status, code, rec.Code, rec.Body.String())
	}
	p := problemOf(t, rec)
	if p.Code != code {
		t.Fatalf("want code %s, got %s (%s)", code, p.Code, p.Detail)
	}
	return p
}

func createBody(mut func(m map[string]any)) string {
	m := map[string]any{
		"productId": ids.New(), "siteId": ids.New(), "warehouseId": ids.New(), "currency": "USD", "quantity": "100",
		"deliveryDate": "2026-11-30",
		"bom": []any{
			map[string]any{"lineNo": "10", "itemId": ids.New(), "qtyPer": "3", "scrapPct": "5", "unitCost": "10"},
			map[string]any{"lineNo": "30", "phantom": true, "qtyPer": "2", "children": []any{
				map[string]any{"itemId": ids.New(), "qtyPer": "1", "unitCost": "4"},
			}},
		},
		"operations": []any{map[string]any{"operNumber": 10, "resourceId": ids.New(), "runTimeHPer": "0.5", "scrapPct": "10", "laborRate": "30"}},
	}
	if mut != nil {
		mut(m)
	}
	raw, _ := json.Marshal(m)
	return string(raw)
}

func TestCreate_ExplodesPlansAndDefaultsTheLegalEntity(t *testing.T) {
	f := &fakeStore{}
	_, h := newServer(t, f)
	rec := do(h, call{method: http.MethodPost, path: "/v1/production/orders", body: createBody(nil), duties: []string{DutyWrite},
		headers: map[string]string{idempotencyName: "create-1"}})
	if rec.Code != http.StatusCreated {
		t.Fatalf("want 201, got %d: %s", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("ETag") != `"1"` || !strings.HasPrefix(rec.Header().Get("Location"), "/v1/production/orders/") {
		t.Fatalf("headers: ETag %q Location %q", rec.Header().Get("ETag"), rec.Header().Get("Location"))
	}
	if len(f.created) != 1 {
		t.Fatalf("store called %d times", len(f.created))
	}
	in := f.created[0]
	if in.LegalEntityID != entityID || in.OriginType != "Manual" || in.IdempotencyKey != "create-1" || in.RequestHash == "" {
		t.Fatalf("create input: %+v", in)
	}
	if len(in.Lines) != 2 || in.Lines[0].LineNo != "10" || !in.Lines[0].QtyRequired.Equal(decimal.NewFromInt(315)) ||
		in.Lines[1].LineNo != "30.1" || !in.Lines[1].QtyRequired.Equal(decimal.NewFromInt(200)) {
		t.Fatalf("lines not exploded per BR-21.2: %+v", in.Lines)
	}
	// BR-21.8: 100 / (1 − 0.10) rounded up at 6 places.
	if !in.Operations[0].ProcessQty.Equal(decimal.RequireFromString("111.111112")) {
		t.Fatalf("operation not planned: %s", in.Operations[0].ProcessQty)
	}
}

func TestCreate_IdempotentReplayAndReuse(t *testing.T) {
	f := &fakeStore{}
	_, h := newServer(t, f)
	body := createBody(nil)
	first := call{method: http.MethodPost, path: "/v1/production/orders", body: body, duties: []string{DutyWrite},
		headers: map[string]string{idempotencyName: "create-2"}}
	if rec := do(h, first); rec.Code != http.StatusCreated {
		t.Fatalf("first: %d %s", rec.Code, rec.Body.String())
	}
	rec := do(h, first)
	if rec.Code != http.StatusOK || rec.Header().Get("Idempotent-Replayed") != "true" {
		t.Fatalf("replay: %d replayed=%q", rec.Code, rec.Header().Get("Idempotent-Replayed"))
	}
	other := first
	other.body = createBody(func(m map[string]any) { m["quantity"] = "101" })
	expectProblem(t, do(h, other), http.StatusUnprocessableEntity, store.CodeIdempotencyReuse)
	if len(f.created) != 1 {
		t.Fatalf("replays reached the store as creates: %d", len(f.created))
	}
}

func TestCreate_Validation(t *testing.T) {
	tests := []struct {
		name   string
		mut    func(m map[string]any)
		key    string
		field  string
		status int
		code   string
	}{
		{"missing idempotency key", nil, "", idempotencyName, http.StatusBadRequest, validationCode},
		{"bad product id", func(m map[string]any) { m["productId"] = "FG-100" }, "k", "productId", http.StatusBadRequest, validationCode},
		{"lower-case currency", func(m map[string]any) { m["currency"] = "usd" }, "k", "currency", http.StatusBadRequest, validationCode},
		{"zero quantity", func(m map[string]any) { m["quantity"] = "0" }, "k", "quantity", http.StatusBadRequest, validationCode},
		{"seven decimals", func(m map[string]any) { m["quantity"] = "1.0000001" }, "k", "quantity", http.StatusBadRequest, validationCode},
		{"quantity beyond numeric(18,6)", func(m map[string]any) { m["quantity"] = "1000000000000" }, "k", "quantity", http.StatusBadRequest, validationCode},
		{"unknown origin", func(m map[string]any) { m["originType"] = "Forecast" }, "k", "originType", http.StatusBadRequest, validationCode},
		{"bad delivery date", func(m map[string]any) { m["deliveryDate"] = "30/11/2026" }, "k", "deliveryDate", http.StatusBadRequest, validationCode},
		{"no operations", func(m map[string]any) { m["operations"] = []any{} }, "k", "operations", http.StatusBadRequest, validationCode},
		{"other legal entity", func(m map[string]any) { m["legalEntityId"] = ids.New() }, "k", "legalEntityId", http.StatusBadRequest, validationCode},
		{"duplicate line number", func(m map[string]any) {
			m["bom"] = []any{map[string]any{"lineNo": "10", "itemId": ids.New(), "qtyPer": "1"}, map[string]any{"lineNo": "10", "itemId": ids.New(), "qtyPer": "1"}}
		}, "k", "bom[1].lineNo", http.StatusBadRequest, validationCode},
		{"negative unit cost", func(m map[string]any) {
			m["bom"] = []any{map[string]any{"lineNo": "10", "itemId": ids.New(), "qtyPer": "1", "unitCost": "-1"}}
		}, "k", "bom[0].unitCost", http.StatusBadRequest, validationCode},
		{"bad item under a phantom", func(m map[string]any) {
			m["bom"] = []any{map[string]any{"lineNo": "30", "phantom": true, "qtyPer": "1", "children": []any{map[string]any{"itemId": "x", "qtyPer": "1"}}}}
		}, "k", "bom[0].children[0].itemId", http.StatusBadRequest, validationCode},
		{"children on a stocked line", func(m map[string]any) {
			m["bom"] = []any{map[string]any{"lineNo": "10", "itemId": ids.New(), "qtyPer": "1", "children": []any{map[string]any{"itemId": ids.New()}}}}
		}, "k", "bom[0].children", http.StatusBadRequest, validationCode},
		{"client-set process quantity", func(m map[string]any) {
			m["operations"] = []any{map[string]any{"operNumber": 10, "resourceId": ids.New(), "processQty": "5"}}
		}, "k", "operations[0].processQty", http.StatusBadRequest, validationCode},
		{"scrap of 100% on a line", func(m map[string]any) {
			m["bom"] = []any{map[string]any{"lineNo": "10", "itemId": ids.New(), "qtyPer": "1", "scrapPct": "100"}}
		}, "k", "", http.StatusUnprocessableEntity, domain.CodeInvalidInput},
		{"operations out of order", func(m map[string]any) {
			m["operations"] = []any{map[string]any{"operNumber": 20, "resourceId": ids.New()}, map[string]any{"operNumber": 10, "resourceId": ids.New()}}
		}, "k", "", http.StatusUnprocessableEntity, domain.CodeInvalidInput},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := &fakeStore{}
			_, h := newServer(t, f)
			headers := map[string]string{}
			if tt.key != "" {
				headers[idempotencyName] = tt.key
			}
			rec := do(h, call{method: http.MethodPost, path: "/v1/production/orders", body: createBody(tt.mut), duties: []string{DutyWrite}, headers: headers})
			p := expectProblem(t, rec, tt.status, tt.code)
			if tt.field != "" {
				if _, ok := p.Errors[tt.field]; !ok {
					t.Fatalf("no error for %s: %v", tt.field, p.Errors)
				}
			}
			if len(f.created) != 0 {
				t.Fatal("an invalid request reached the store")
			}
		})
	}
}

func TestCreate_RejectsUnknownFields(t *testing.T) {
	_, h := newServer(t, &fakeStore{})
	body := createBody(func(m map[string]any) { m["orderNumber"] = "PO-1" })
	expectProblem(t, do(h, call{method: http.MethodPost, path: "/v1/production/orders", body: body, duties: []string{DutyWrite},
		headers: map[string]string{idempotencyName: "k"}}), http.StatusBadRequest, validationCode)
}

func TestRoutes_EnforceDuties(t *testing.T) {
	id := ids.New()
	tests := []struct {
		method, path string
		duty         string
	}{
		{http.MethodGet, "/v1/production/orders", DutyRead},
		{http.MethodPost, "/v1/production/orders", DutyWrite},
		{http.MethodGet, "/v1/production/orders/" + id, DutyRead},
		{http.MethodPatch, "/v1/production/orders/" + id, DutyWrite},
		{http.MethodGet, "/v1/production/orders/" + id + "/status-log", DutyRead},
		{http.MethodGet, "/v1/production/orders/" + id + "/costs", DutyCostRead},
		{http.MethodPost, "/v1/production/orders/" + id + ":estimate", DutyWrite},
		{http.MethodPost, "/v1/production/orders/" + id + ":schedule", DutyWrite},
		{http.MethodPost, "/v1/production/orders/" + id + ":release", DutyRelease},
		{http.MethodPost, "/v1/production/orders/" + id + ":start", DutyRelease},
		{http.MethodPost, "/v1/production/orders/" + id + ":cancel", DutyRelease},
		{http.MethodPost, "/v1/production/orders/" + id + ":reset-status", DutyRelease},
		{http.MethodPost, "/v1/production/orders/" + id + ":report-finished", DutyJournalPost},
		{http.MethodPost, "/v1/production/orders/" + id + ":end", DutyEnd},
		{http.MethodGet, "/v1/production/orders/" + id + "/journals", DutyRead},
		{http.MethodPost, "/v1/production/orders/" + id + "/journals", DutyJournalPost},
		{http.MethodGet, "/v1/production/journals/" + id, DutyRead},
		{http.MethodDelete, "/v1/production/journals/" + id, DutyJournalPost},
		{http.MethodPost, "/v1/production/journals/" + id + ":post", DutyJournalPost},
		{http.MethodGet, "/v1/production/posting-profiles", DutySetupRead},
		{http.MethodPut, "/v1/production/posting-profiles", DutySetupWrite},
		{http.MethodGet, "/v1/production/orders/" + id + "/as-built", DutyRead},
		{http.MethodPost, "/v1/production/orders/" + id + "/as-built", DutyJournalPost},
		{http.MethodGet, "/v1/production/as-built?finishedSerialOrBatch=S-1", DutyRead},
		{http.MethodPost, "/v1/production/as-built/" + id + ":detach", DutyJournalPost},
	}
	others := []string{DutyRead, DutyWrite, DutyRelease, DutyJournalPost, DutyEnd, DutyCostRead, DutySetupRead, DutySetupWrite, DutyOverConsume}
	for _, tt := range tests {
		t.Run(tt.method+" "+tt.path, func(t *testing.T) {
			var without []string
			for _, d := range others {
				if d != tt.duty {
					without = append(without, d)
				}
			}
			f := &fakeStore{}
			_, h := newServer(t, f)
			p := expectProblem(t, do(h, call{method: tt.method, path: tt.path, duties: without}), http.StatusForbidden, "AUTH-0003")
			if !strings.Contains(p.Detail, tt.duty) {
				t.Fatalf("detail %q does not name %s", p.Detail, tt.duty)
			}
			if len(f.transitions)+len(f.created)+len(f.updates)+len(f.journals)+len(f.posts)+len(f.deleted)+len(f.saved)+
				len(f.assigned)+len(f.asBuiltF)+len(f.detached) != 0 {
				t.Fatal("a refused request reached the store")
			}
		})
	}
}

func TestRoutes_RequireATenant(t *testing.T) {
	_, h := newServer(t, &fakeStore{})
	req := httptest.NewRequest(http.MethodGet, "/v1/production/orders", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d", rec.Code)
	}
}

func commandCall(id, cmd, body string, duties ...string) call {
	return call{method: http.MethodPost, path: "/v1/production/orders/" + id + ":" + cmd, body: body, duties: duties,
		headers: map[string]string{idempotencyName: "cmd-1", "If-Match": `"3"`}}
}

func TestCommand_PassesTheRequestToTheStore(t *testing.T) {
	id := ids.New()
	f := &fakeStore{}
	_, h := newServer(t, f)
	rec := do(h, commandCall(id, "end", "", DutyEnd, DutyZeroOutput))
	if rec.Code != http.StatusOK || rec.Header().Get("ETag") != `"4"` {
		t.Fatalf("end: %d %s etag %q", rec.Code, rec.Body.String(), rec.Header().Get("ETag"))
	}
	got := f.transitions[0]
	if got.OrderID != id || got.Command != domain.CmdEnd || got.IfMatch != 3 || !got.ZeroOutputDuty || got.Actor != "planner-1" ||
		got.IdempotencyKey != "cmd-1" || got.RequestHash == "" || got.TraceID == "" {
		t.Fatalf("transition: %+v", got)
	}
}

func TestCommand_ReportFinishedCarriesQuantitiesAndTolerance(t *testing.T) {
	f := &fakeStore{}
	_, h := newServer(t, f)
	rec := do(h, commandCall(ids.New(), "report-finished", `{"quantity":"60","errorQuantity":"2"}`, DutyJournalPost))
	if rec.Code != http.StatusOK {
		t.Fatalf("report-finished: %d %s", rec.Code, rec.Body.String())
	}
	got := f.transitions[0]
	if !got.Good.Equal(decimal.NewFromInt(60)) || !got.Error.Equal(decimal.NewFromInt(2)) || !got.RAFTolerancePct.Equal(decimal.NewFromInt(5)) {
		t.Fatalf("quantities: %+v", got)
	}
	// A different quantity under the same key must hash differently so the store can refuse it.
	do(h, commandCall(got.OrderID, "report-finished", `{"quantity":"61"}`, DutyJournalPost))
	if f.transitions[1].RequestHash == got.RequestHash {
		t.Fatal("request hash ignores the quantity")
	}
}

func TestCommand_ResetCarriesTheTarget(t *testing.T) {
	f := &fakeStore{}
	_, h := newServer(t, f)
	if rec := do(h, commandCall(ids.New(), "reset-status", `{"status":"Created"}`, DutyRelease)); rec.Code != http.StatusOK {
		t.Fatalf("reset: %d %s", rec.Code, rec.Body.String())
	}
	if f.transitions[0].ResetTo != domain.Created {
		t.Fatalf("reset target %q", f.transitions[0].ResetTo)
	}
}

func TestCommand_Refusals(t *testing.T) {
	id := ids.New()
	tests := []struct {
		name   string
		c      call
		status int
		code   string
	}{
		{"unknown command", commandCall(id, "approve", "", DutyRelease), http.StatusNotFound, unknownActionCode},
		{"no command", call{method: http.MethodPost, path: "/v1/production/orders/" + id, duties: []string{DutyRelease}}, http.StatusNotFound, unknownActionCode},
		{"bad id", commandCall("PO-1", "start", "", DutyRelease), http.StatusNotFound, unknownActionCode},
		{"missing If-Match", call{method: http.MethodPost, path: "/v1/production/orders/" + id + ":start", duties: []string{DutyRelease},
			headers: map[string]string{idempotencyName: "k"}}, http.StatusPreconditionRequired, "precondition_required"},
		{"malformed If-Match", call{method: http.MethodPost, path: "/v1/production/orders/" + id + ":start", duties: []string{DutyRelease},
			headers: map[string]string{idempotencyName: "k", "If-Match": `"v3"`}}, http.StatusBadRequest, validationCode},
		{"missing idempotency key", call{method: http.MethodPost, path: "/v1/production/orders/" + id + ":start", duties: []string{DutyRelease},
			headers: map[string]string{"If-Match": `"3"`}}, http.StatusBadRequest, validationCode},
		{"report without quantity", commandCall(id, "report-finished", `{}`, DutyJournalPost), http.StatusBadRequest, validationCode},
		{"report with zero quantity", commandCall(id, "report-finished", `{"quantity":"0"}`, DutyJournalPost), http.StatusBadRequest, validationCode},
		{"quantity on start", commandCall(id, "start", `{"quantity":"5"}`, DutyRelease), http.StatusBadRequest, validationCode},
		{"status on cancel", commandCall(id, "cancel", `{"status":"Created"}`, DutyRelease), http.StatusBadRequest, validationCode},
		{"unknown reset target", commandCall(id, "reset-status", `{"status":"Closed"}`, DutyRelease), http.StatusBadRequest, validationCode},
		{"unknown body field", commandCall(id, "start", `{"force":true}`, DutyRelease), http.StatusBadRequest, validationCode},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := &fakeStore{}
			_, h := newServer(t, f)
			expectProblem(t, do(h, tt.c), tt.status, tt.code)
			if len(f.transitions) != 0 {
				t.Fatal("a refused command reached the store")
			}
		})
	}
}

// AC-21.2 at the HTTP edge: the store's 409 reaches the client and is counted.
func TestCommand_StoreRefusalIsReturnedAndCounted(t *testing.T) {
	f := &fakeStore{transErr: httpx.NewProblem(http.StatusConflict, domain.CodeIllegalTransition, "transition Created via start is not allowed")}
	a, h := newServer(t, f)
	expectProblem(t, do(h, commandCall(ids.New(), "start", "", DutyRelease)), http.StatusConflict, domain.CodeIllegalTransition)
	if a.refused.Load() != 1 || a.moved.Load() != 0 {
		t.Fatalf("refused=%d moved=%d", a.refused.Load(), a.moved.Load())
	}
	metrics := httptest.NewRecorder()
	a.Metrics().ServeHTTP(metrics, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if !strings.Contains(metrics.Body.String(), "production_transitions_refused_total 1") {
		t.Fatalf("metrics: %s", metrics.Body.String())
	}
}

func TestCommand_UnexpectedErrorIsHidden(t *testing.T) {
	f := &fakeStore{transErr: errors.New("connection reset")}
	_, h := newServer(t, f)
	p := expectProblem(t, do(h, commandCall(ids.New(), "start", "", DutyRelease)), http.StatusInternalServerError, "COM-0500")
	if strings.Contains(p.Detail, "connection") {
		t.Fatalf("internal detail leaked: %s", p.Detail)
	}
}

func TestPatch(t *testing.T) {
	id := ids.New()
	patch := func(body string, headers map[string]string) call {
		return call{method: http.MethodPatch, path: "/v1/production/orders/" + id, body: body, duties: []string{DutyWrite}, headers: headers}
	}
	withVersion := map[string]string{"If-Match": `"2"`}

	f := &fakeStore{}
	_, h := newServer(t, f)
	rec := do(h, patch(`{"quantity":"40","deliveryDate":null}`, withVersion))
	if rec.Code != http.StatusOK || rec.Header().Get("ETag") != `"3"` {
		t.Fatalf("patch: %d %s", rec.Code, rec.Body.String())
	}
	u := f.updates[0]
	if u.IfMatch != 2 || u.Quantity == nil || !u.Quantity.Equal(decimal.NewFromInt(40)) || !u.SetDeliveryDate || u.DeliveryDate != nil {
		t.Fatalf("update: %+v", u)
	}
	do(h, patch(`{"deliveryDate":"2026-12-01"}`, withVersion))
	if u := f.updates[1]; u.Quantity != nil || !u.SetDeliveryDate || u.DeliveryDate == nil || *u.DeliveryDate != "2026-12-01" {
		t.Fatalf("date-only update: %+v", u)
	}

	expectProblem(t, do(h, patch(`{"quantity":"40"}`, nil)), http.StatusPreconditionRequired, "precondition_required")
	expectProblem(t, do(h, patch(`{}`, withVersion)), http.StatusBadRequest, validationCode)
	expectProblem(t, do(h, patch(`{"quantity":"-1"}`, withVersion)), http.StatusBadRequest, validationCode)
	expectProblem(t, do(h, patch(`{"deliveryDate":"tomorrow"}`, withVersion)), http.StatusBadRequest, validationCode)
	expectProblem(t, do(h, patch(`{"status":"Released"}`, withVersion)), http.StatusBadRequest, validationCode)
	if len(f.updates) != 2 {
		t.Fatalf("invalid patches reached the store: %d", len(f.updates))
	}
}

func TestList_ForwardsFiltersAndValidates(t *testing.T) {
	f := &fakeStore{}
	_, h := newServer(t, f)
	site, cursor := ids.New(), ids.New()
	rec := do(h, call{method: http.MethodGet, path: "/v1/production/orders?status=Released&siteId=" + site + "&cursor=" + cursor + "&limit=25", duties: []string{DutyRead}})
	if rec.Code != http.StatusOK {
		t.Fatalf("list: %d %s", rec.Code, rec.Body.String())
	}
	if lf := f.listFilter; lf.Status != domain.Released || lf.SiteID != site || lf.Cursor != cursor || lf.Limit != 25 {
		t.Fatalf("filter: %+v", lf)
	}
	do(h, call{method: http.MethodGet, path: "/v1/production/orders", duties: []string{DutyRead}})
	if f.listFilter.Limit != defaultPageSize {
		t.Fatalf("default limit %d", f.listFilter.Limit)
	}
	for _, q := range []string{"status=Open", "limit=0", "limit=201", "siteId=plant-1", "cursor=PO-1"} {
		expectProblem(t, do(h, call{method: http.MethodGet, path: "/v1/production/orders?" + q, duties: []string{DutyRead}}), http.StatusBadRequest, validationCode)
	}
}

func TestGetOrder_NotFoundForMalformedID(t *testing.T) {
	_, h := newServer(t, &fakeStore{})
	expectProblem(t, do(h, call{method: http.MethodGet, path: "/v1/production/orders/PO-1", duties: []string{DutyRead}}), http.StatusNotFound, store.CodeOrderNotFound)
}

func TestGetOrder_ETagAndStoreProblem(t *testing.T) {
	id := ids.New()
	f := &fakeStore{order: store.Order{ID: id, Version: 7}}
	_, h := newServer(t, f)
	rec := do(h, call{method: http.MethodGet, path: "/v1/production/orders/" + id, duties: []string{DutyRead}})
	if rec.Code != http.StatusOK || rec.Header().Get("ETag") != `"7"` {
		t.Fatalf("get: %d etag %q", rec.Code, rec.Header().Get("ETag"))
	}
	f.getErr = httpx.NewProblem(http.StatusNotFound, store.CodeOrderNotFound, "production order not found")
	expectProblem(t, do(h, call{method: http.MethodGet, path: "/v1/production/orders/" + id, duties: []string{DutyRead}}), http.StatusNotFound, store.CodeOrderNotFound)
}

// AC-21.3: planned material per group is the sum of the 4-decimal rows, shown
// to the currency's minor unit; actual cost, WIP and variances come alongside (FR-21.11).
func TestCosts_TotalsByGroup(t *testing.T) {
	d := decimal.RequireFromString
	id := ids.New()
	f := &fakeStore{costs: store.CostSummary{Currency: "USD",
		Planned: []domain.CostRow{
			{Group: domain.GroupMaterial, Key: "a", Amount: d("1000.1250")},
			{Group: domain.GroupMaterial, Key: "b", Amount: d("2000.0050")},
			{Group: domain.GroupLabor, Key: "r@10", Amount: d("1500")},
		},
		Actual:     []domain.CostRow{{Group: domain.GroupMaterial, Key: "a", Qty: d("305"), Amount: d("3111")}},
		WIPBalance: d("-49"),
		Variances:  []domain.Variance{{Type: domain.VarPrice, Group: domain.GroupMaterial, Amount: d("61")}, {Type: domain.VarQuantity, Group: domain.GroupLabor, Amount: d("-120")}},
	}}
	_, h := newServer(t, f)
	rec := do(h, call{method: http.MethodGet, path: "/v1/production/orders/" + id + "/costs", duties: []string{DutyCostRead}})
	var view costView
	if err := json.Unmarshal(rec.Body.Bytes(), &view); err != nil || rec.Code != http.StatusOK {
		t.Fatalf("costs: %d %s", rec.Code, rec.Body.String())
	}
	if view.OrderID != id || view.Totals[domain.GroupMaterial] != "3000.13" || view.Totals[domain.GroupLabor] != "1500.00" || view.Total != "4500.13" || view.Currency != "USD" {
		t.Fatalf("planned totals: %+v", view)
	}
	if view.ActualTotals[domain.GroupMaterial] != "3111.00" || view.ActualTotal != "3111.00" || view.WIPBalance != "-49.00" || view.VarianceTotal != "-59.00" {
		t.Fatalf("actual, WIP and variances: %+v", view)
	}
	f.costs = store.CostSummary{Currency: "JPY", Planned: []domain.CostRow{{Group: domain.GroupMaterial, Key: "a", Amount: d("1234.5")}}, WIPBalance: d("7")}
	rec = do(h, call{method: http.MethodGet, path: "/v1/production/orders/" + id + "/costs", duties: []string{DutyCostRead}})
	if err := json.Unmarshal(rec.Body.Bytes(), &view); err != nil || view.Total != "1235" || view.WIPBalance != "7" {
		t.Fatalf("JPY has no minor unit: %s", rec.Body.String())
	}
}

func TestStatusLog(t *testing.T) {
	id := ids.New()
	f := &fakeStore{log: []store.StatusLogEntry{{Command: domain.CmdEstimate, From: domain.Created, To: domain.Estimated, Actor: "planner-1", TraceID: "req-1"}}}
	_, h := newServer(t, f)
	rec := do(h, call{method: http.MethodGet, path: "/v1/production/orders/" + id + "/status-log", duties: []string{DutyRead}})
	var body struct {
		OrderID string                 `json:"orderId"`
		Items   []store.StatusLogEntry `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body.OrderID != id || len(body.Items) != 1 || body.Items[0].TraceID != "req-1" {
		t.Fatalf("status log: %d %s", rec.Code, rec.Body.String())
	}
}

func TestReady(t *testing.T) {
	f := &fakeStore{}
	_, h := newServer(t, f)
	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("ready: %d", rec.Code)
	}
	f.pingErr = errors.New("down")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	body, _ := io.ReadAll(rec.Body)
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(string(body), "unavailable") {
		t.Fatalf("not ready: %d %s", rec.Code, body)
	}
}

func TestParseIfMatch(t *testing.T) {
	tests := []struct {
		raw    string
		want   int
		status int
	}{
		{`"3"`, 3, 0},
		{"7", 7, 0},
		{`W/"4"`, 4, 0},
		{"", 0, http.StatusPreconditionRequired},
		{`"0"`, 0, http.StatusBadRequest},
		{`"abc"`, 0, http.StatusBadRequest},
	}
	for _, tt := range tests {
		got, err := parseIfMatch(tt.raw)
		if tt.status == 0 {
			if err != nil || got != tt.want {
				t.Errorf("%q: want %d, got %d (%v)", tt.raw, tt.want, got, err)
			}
			continue
		}
		p, ok := err.(*httpx.Problem)
		if !ok || p.Status != tt.status {
			t.Errorf("%q: want status %d, got %v", tt.raw, tt.status, err)
		}
	}
}

func TestWrites_RefuseACallerWithNoLegalEntityScope(t *testing.T) {
	f := &fakeStore{}
	_, h := newServer(t, f)
	noScope := map[string]string{tenant.HeaderLegalEntity: ""}
	create := createBody(func(m map[string]any) { m["legalEntityId"] = entityID })
	rec := do(h, call{method: http.MethodPost, path: "/v1/production/orders", body: create, duties: []string{DutyWrite},
		headers: map[string]string{idempotencyName: "k", tenant.HeaderLegalEntity: ""}})
	if p := expectProblem(t, rec, http.StatusBadRequest, validationCode); p.Errors["legalEntityId"] == "" {
		t.Fatalf("create errors: %v", p.Errors)
	}
	profile := `{"legalEntityId":"` + entityID + `","itemGroup":"All","siteId":"All","wipAccount":"1400","inventoryAccount":"1300","finishedGoodsAccount":"1350",
		"laborAbsorptionAccount":"5100","overheadAbsorptionAccount":"5200","lotSizeVarianceAccount":"5810","quantityVarianceAccount":"5820",
		"priceVarianceAccount":"5830","substitutionVarianceAccount":"5840"}`
	rec = do(h, call{method: http.MethodPut, path: "/v1/production/posting-profiles", body: profile, duties: []string{DutySetupWrite}, headers: noScope})
	if p := expectProblem(t, rec, http.StatusBadRequest, validationCode); p.Errors["legalEntityId"] == "" {
		t.Fatalf("profile errors: %v", p.Errors)
	}
	if len(f.created) != 0 || len(f.saved) != 0 {
		t.Fatal("a caller with no legal-entity scope reached the store")
	}
}
