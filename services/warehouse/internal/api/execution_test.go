package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/warehouse/internal/domain"
	"github.com/BenjaminSRussell/passage/services/warehouse/internal/store"
	"github.com/BenjaminSRussell/passage/services/warehouse/migrations"
)

// fakeStore answers from the err and result fields and records the last call.
type fakeStore struct {
	err      error
	replayed bool
	work     domain.Work
	idem     store.Idem
	plan     store.PlanInput
	confirm  store.ConfirmInput
	except   store.ExceptionInput
	wfilter  store.WorkFilter
	lfilter  store.LocationFilter
	calls    int
	door     domain.DockDoor
	appt     domain.Appointment
	afilter  store.AppointmentFilter
	receipt  domain.Receipt
	rfilter  store.ReceiptFilter
	receive  store.ReceiveInput
	received store.Received
}

func (f *fakeStore) CreateLocations(_ context.Context, idem store.Idem, _ string, locs []domain.Location) ([]domain.Location, bool, error) {
	f.calls++
	f.idem = idem
	return locs, f.replayed, f.err
}
func (f *fakeStore) ListLocations(_ context.Context, lf store.LocationFilter) ([]domain.Location, error) {
	f.calls++
	f.lfilter = lf
	return nil, f.err
}
func (f *fakeStore) CreateDirective(_ context.Context, idem store.Idem, d domain.Directive) (domain.Directive, bool, error) {
	f.calls++
	f.idem = idem
	return d, f.replayed, f.err
}
func (f *fakeStore) ListDirectives(context.Context, string, string, string) ([]domain.Directive, error) {
	f.calls++
	return nil, f.err
}
func (f *fakeStore) PlanWork(_ context.Context, idem store.Idem, in store.PlanInput) (store.Planned, bool, error) {
	f.calls++
	f.idem, f.plan = idem, in
	return store.Planned{Work: f.work}, f.replayed, f.err
}
func (f *fakeStore) ListWork(_ context.Context, wf store.WorkFilter) ([]domain.Work, error) {
	f.calls++
	f.wfilter = wf
	if f.err != nil {
		return nil, f.err
	}
	return []domain.Work{f.work}, nil
}
func (f *fakeStore) GetWork(context.Context, string) (domain.Work, error) {
	f.calls++
	return f.work, f.err
}
func (f *fakeStore) AssignNext(_ context.Context, idem store.Idem, _, _ string, _ time.Duration, _ time.Time) (store.Assigned, bool, error) {
	f.calls++
	f.idem = idem
	return store.Assigned{}, f.replayed, f.err
}
func (f *fakeStore) ConfirmLine(_ context.Context, idem store.Idem, in store.ConfirmInput) (domain.Work, bool, error) {
	f.calls++
	f.idem, f.confirm = idem, in
	return f.work, f.replayed, f.err
}
func (f *fakeStore) ExceptWork(_ context.Context, idem store.Idem, in store.ExceptionInput) (store.Excepted, bool, error) {
	f.calls++
	f.idem, f.except = idem, in
	return store.Excepted{Work: f.work}, f.replayed, f.err
}

func (f *fakeStore) CreateDoor(_ context.Context, idem store.Idem, d domain.DockDoor) (domain.DockDoor, bool, error) {
	f.calls++
	f.idem, f.door = idem, d
	return d, f.replayed, f.err
}
func (f *fakeStore) ListDoors(context.Context, string) ([]domain.DockDoor, error) {
	f.calls++
	return nil, f.err
}
func (f *fakeStore) CreateAppointment(_ context.Context, idem store.Idem, a domain.Appointment) (domain.Appointment, bool, error) {
	f.calls++
	f.idem, f.appt = idem, a
	return a, f.replayed, f.err
}
func (f *fakeStore) ListAppointments(_ context.Context, af store.AppointmentFilter) ([]domain.Appointment, error) {
	f.calls++
	f.afilter = af
	return nil, f.err
}
func (f *fakeStore) RegisterASN(_ context.Context, idem store.Idem, r domain.Receipt) (domain.Receipt, bool, error) {
	f.calls++
	f.idem, f.receipt = idem, r
	return r, f.replayed, f.err
}
func (f *fakeStore) ListReceipts(_ context.Context, rf store.ReceiptFilter) ([]domain.ReceiptSummary, error) {
	f.calls++
	f.rfilter = rf
	return nil, f.err
}
func (f *fakeStore) GetReceipt(context.Context, string) (domain.Receipt, error) {
	f.calls++
	return f.receipt, f.err
}
func (f *fakeStore) ReceiveLine(_ context.Context, idem store.Idem, in store.ReceiveInput) (store.Received, bool, error) {
	f.calls++
	f.idem, f.receive = idem, in
	return f.received, f.replayed, f.err
}

func persistedRouter(st Store) http.Handler {
	r := httpx.NewRouter()
	NewWithStore(st, 30*time.Minute).Register(r)
	return r
}

type req struct {
	method, path, duties, key, body, user, ifMatch, tenant string
}

func call(h http.Handler, q req) *httptest.ResponseRecorder {
	r := httptest.NewRequest(q.method, q.path, strings.NewReader(q.body))
	r.Header.Set("Content-Type", "application/json")
	tid := q.tenant
	if tid == "" {
		tid = testkit.TenantID(1)
	}
	r.Header.Set("X-Passage-Tenant", tid)
	if q.user != "-" {
		user := q.user
		if user == "" {
			user = "u1"
		}
		r.Header.Set("X-Passage-User", user)
	}
	r.Header.Set("X-Passage-Duties", q.duties)
	if q.key != "" {
		r.Header.Set("Idempotency-Key", q.key)
	}
	if q.ifMatch != "" {
		r.Header.Set("If-Match", q.ifMatch)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	return rec
}

func problemOf(t *testing.T, rec *httptest.ResponseRecorder) httpx.Problem {
	t.Helper()
	var p httpx.Problem
	if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
		t.Fatalf("not a problem: %s", rec.Body)
	}
	return p
}

func TestPersistedRoutes_Refusals(t *testing.T) {
	wh := ids.New()
	work := `{"warehouseId":"` + wh + `","workType":"SALES_PICK","lines":[{"itemId":"BOLT","direction":"PICK","qty":"10"}]}`
	workID := ids.New()
	confirmPath := "/v1/warehouse/work/" + workID + "/lines/1:confirm"
	exceptPath := "/v1/warehouse/work/" + workID + ":exception"
	cases := []struct {
		name   string
		req    req
		status int
		field  string
	}{
		{"work needs the idempotency key", req{method: "POST", path: "/v1/warehouse/work", duties: dutyWorkSupervise, body: work}, 400, headerKey},
		{"work needs the supervise duty", req{method: "POST", path: "/v1/warehouse/work", duties: dutyWorkExecute, key: "k", body: work}, 403, ""},
		{"tenant must be a uuid", req{method: "POST", path: "/v1/warehouse/work", duties: dutyWorkSupervise, key: "k", body: work, tenant: "acme"}, 401, ""},
		{"work type must be known", req{method: "POST", path: "/v1/warehouse/work", duties: dutyWorkSupervise, key: "k",
			body: `{"warehouseId":"` + wh + `","workType":"NOPE","lines":[{"itemId":"B","direction":"PICK","qty":"1"}]}`}, 400, "workType"},
		{"work lines need a positive quantity", req{method: "POST", path: "/v1/warehouse/work", duties: dutyWorkSupervise, key: "k",
			body: `{"warehouseId":"` + wh + `","workType":"SALES_PICK","lines":[{"itemId":"B","direction":"PICK","qty":"0"}]}`}, 400, "lines[0].qty"},
		{"work lines need an item", req{method: "POST", path: "/v1/warehouse/work", duties: dutyWorkSupervise, key: "k",
			body: `{"warehouseId":"` + wh + `","workType":"SALES_PICK","lines":[{"itemId":" ","direction":"PICK","qty":"1"}]}`}, 400, "lines[0].itemId"},
		{"work priority range", req{method: "POST", path: "/v1/warehouse/work", duties: dutyWorkSupervise, key: "k",
			body: `{"warehouseId":"` + wh + `","workType":"SALES_PICK","priority":100,"lines":[{"itemId":"B","direction":"PICK","qty":"1"}]}`}, 400, "priority"},
		{"work rejects unknown fields", req{method: "POST", path: "/v1/warehouse/work", duties: dutyWorkSupervise, key: "k", body: `{"bogus":1}`}, 400, ""},
		{"assign needs a warehouse uuid", req{method: "POST", path: "/v1/warehouse/work:assign", duties: dutyWorkExecute, key: "k", body: `{"warehouseId":"WH1"}`}, 400, "warehouseId"},
		{"assign needs a user", req{method: "POST", path: "/v1/warehouse/work:assign", duties: dutyWorkExecute, key: "k", user: "-", body: `{"warehouseId":"` + wh + `"}`}, 400, "X-Passage-User"},
		{"assign needs the execute duty", req{method: "POST", path: "/v1/warehouse/work:assign", duties: dutyInquiry, key: "k", body: `{"warehouseId":"` + wh + `"}`}, 403, ""},
		{"confirm needs the idempotency key", req{method: "POST", path: confirmPath, duties: dutyWorkExecute, body: `{"qty":"10"}`}, 400, headerKey},
		{"confirm needs a positive quantity", req{method: "POST", path: confirmPath, duties: dutyWorkExecute, key: "k", body: `{"qty":"0"}`}, 400, "qty"},
		{"confirm needs a uuid work id", req{method: "POST", path: "/v1/warehouse/work/WORK-1/lines/1:confirm", duties: dutyWorkExecute, key: "k", body: `{"qty":"1"}`}, 400, "id"},
		{"confirm needs a line number", req{method: "POST", path: "/v1/warehouse/work/" + workID + "/lines/x:confirm", duties: dutyWorkExecute, key: "k", body: `{"qty":"1"}`}, 400, "n"},
		{"confirm If-Match must be a version", req{method: "POST", path: confirmPath, duties: dutyWorkExecute, key: "k", ifMatch: `"abc"`, body: `{"qty":"1"}`}, 400, "If-Match"},
		{"exception action must be known", req{method: "POST", path: exceptPath, duties: dutyWorkExecute, key: "k", body: `{"action":"SKIP","reason":"X"}`}, 400, "action"},
		{"short pick needs a reason", req{method: "POST", path: exceptPath, duties: dutyWorkExecute, key: "k", body: `{"action":"SHORT_PICK","lineNo":1,"confirmedQty":"6"}`}, 400, "reason"},
		{"cancel needs a reason", req{method: "POST", path: exceptPath, duties: dutyWorkExecute, key: "k", body: `{"action":"CANCEL"}`}, 400, "reason"},
		{"short pick quantity not negative", req{method: "POST", path: exceptPath, duties: dutyWorkExecute, key: "k", body: `{"action":"SHORT_PICK","lineNo":1,"confirmedQty":"-1","reason":"EMPTY"}`}, 400, "confirmedQty"},
		{"locations need at least one", req{method: "POST", path: "/v1/warehouse/locations:bulk", duties: dutySetupMaintain, key: "k",
			body: `{"warehouseId":"` + wh + `","locations":[]}`}, 400, "locations"},
		{"location capacity must not be negative", req{method: "POST", path: "/v1/warehouse/locations:bulk", duties: dutySetupMaintain, key: "k",
			body: `{"warehouseId":"` + wh + `","locations":[{"name":"A-01","zone":"A","capacity":{"maxLps":-1}}]}`}, 400, "locations[0].capacity"},
		{"location names unique in the request", req{method: "POST", path: "/v1/warehouse/locations:bulk", duties: dutySetupMaintain, key: "k",
			body: `{"warehouseId":"` + wh + `","locations":[{"name":"A-01","zone":"A"},{"name":"A-01","zone":"A"}]}`}, 400, "locations[1].name"},
		{"locations need the maintain duty", req{method: "POST", path: "/v1/warehouse/locations:bulk", duties: dutySetupView, key: "k",
			body: `{"warehouseId":"` + wh + `","locations":[{"name":"A-01","zone":"A"}]}`}, 403, ""},
		{"directive sequence must be positive", req{method: "POST", path: "/v1/warehouse/location-directives", duties: dutySetupMaintain, key: "k",
			body: `{"warehouseId":"` + wh + `","sequence":0,"workType":"SALES_PICK","direction":"PICK","lines":[{"seq":1,"fromQty":"0","actions":[{"seq":1,"strategy":"fifo"}]}]}`}, 400, "sequence"},
		{"directive strategy must be known", req{method: "POST", path: "/v1/warehouse/location-directives", duties: dutySetupMaintain, key: "k",
			body: `{"warehouseId":"` + wh + `","sequence":1,"workType":"SALES_PICK","direction":"PICK","lines":[{"seq":1,"fromQty":"0","actions":[{"seq":1,"strategy":"RANDOM","zone":"A"}]}]}`},
			400, "lines[0].actions[0].strategy"},
		{"directive bands must not overlap", req{method: "POST", path: "/v1/warehouse/location-directives", duties: dutySetupMaintain, key: "k",
			body: `{"warehouseId":"` + wh + `","sequence":1,"workType":"SALES_PICK","direction":"PICK","lines":[{"seq":1,"fromQty":"0","actions":[{"seq":1,"strategy":"fifo"}]},{"seq":2,"fromQty":"5","actions":[{"seq":1,"strategy":"fifo"}]}]}`},
			400, "lines[1].fromQty"},
		{"directive id is assigned by the server", req{method: "POST", path: "/v1/warehouse/location-directives", duties: dutySetupMaintain, key: "k",
			body: `{"id":"D1","warehouseId":"` + wh + `","sequence":1,"workType":"SALES_PICK","direction":"PICK","lines":[]}`}, 400, ""},
		{"list locations needs a warehouse uuid", req{method: "GET", path: "/v1/warehouse/locations", duties: dutySetupView}, 400, "warehouseId"},
		{"list locations limit range", req{method: "GET", path: "/v1/warehouse/locations?warehouseId=" + wh + "&limit=5000", duties: dutySetupView}, 400, "limit"},
		{"list directives rejects a bad direction", req{method: "GET", path: "/v1/warehouse/location-directives?warehouseId=" + wh + "&direction=SIDE", duties: dutySetupView}, 400, "direction"},
		{"list work rejects an unknown status", req{method: "GET", path: "/v1/warehouse/work?warehouseId=" + wh + "&status=DONE", duties: dutyInquiry}, 400, "status"},
		{"list work rejects a bad cursor", req{method: "GET", path: "/v1/warehouse/work?warehouseId=" + wh + "&cursor=" + base64.RawURLEncoding.EncodeToString([]byte("x/y/z")), duties: dutyInquiry}, 400, "cursor"},
		{"list work needs the inquiry duty", req{method: "GET", path: "/v1/warehouse/work?warehouseId=" + wh, duties: dutyWorkExecute}, 403, ""},
		{"work id must be a uuid", req{method: "GET", path: "/v1/warehouse/work/WORK-1", duties: dutyInquiry}, 404, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fake := &fakeStore{}
			rec := call(persistedRouter(fake), tc.req)
			if rec.Code != tc.status {
				t.Fatalf("status = %d, want %d; body %s", rec.Code, tc.status, rec.Body.String())
			}
			if fake.calls != 0 {
				t.Fatalf("a refused request reached the store")
			}
			if tc.field != "" {
				if p := problemOf(t, rec); p.Errors[tc.field] == "" {
					t.Fatalf("no error on field %q: %+v", tc.field, p.Errors)
				}
			}
		})
	}
}

// Store and domain errors keep their catalog code and get the status the
// README lists.
func TestPersistedRoutes_ErrorMapping(t *testing.T) {
	path := "/v1/warehouse/work/" + ids.New() + "/lines/1:confirm"
	cases := []struct {
		err    error
		status int
		code   string
	}{
		{&domain.Error{Code: domain.CodeLineConfirmed, Detail: "x"}, 409, "WMS-0105"},
		{&domain.Error{Code: domain.CodeOverPick, Detail: "x"}, 422, "WMS-0106"},
		{&domain.Error{Code: domain.CodeCapacity, Detail: "x"}, 422, "WMS-0102"},
		{&domain.Error{Code: domain.CodeIllegalTransition, Detail: "x"}, 409, "WMS-0110"},
		{domain.ErrNotAssignee, 403, codeNotAssignee},
		{domain.ErrLineNotFound, 404, codeLineNotFound},
		{store.ErrNotFound, 404, codeWorkNotFound},
		{fmt.Errorf("%w: current version is 4", store.ErrVersionConflict), 409, codeVersionConflict},
		{store.ErrIdempotencyReuse, 422, codeIdempotencyReuse},
		{errors.New("boom"), 500, "COM-0500"},
	}
	for _, tc := range cases {
		t.Run(tc.code, func(t *testing.T) {
			rec := call(persistedRouter(&fakeStore{err: tc.err}), req{method: "POST", path: path, duties: dutyWorkExecute, key: "k", body: `{"qty":"1"}`})
			if rec.Code != tc.status || problemOf(t, rec).Code != tc.code {
				t.Fatalf("status %d body %s, want %d %s", rec.Code, rec.Body, tc.status, tc.code)
			}
		})
	}
}

func TestPersistedRoutes_PassTheCommand(t *testing.T) {
	wh, workID := ids.New(), ids.New()
	fake := &fakeStore{work: domain.Work{ID: workID, Status: domain.WorkInProgress, Version: 4}}
	h := persistedRouter(fake)

	body := `{"qty":"2.5"}`
	rec := call(h, req{method: "POST", path: "/v1/warehouse/work/" + workID + "/lines/3:confirm", duties: dutyWorkExecute, key: "k1", ifMatch: `"3"`, body: body})
	if rec.Code != 200 || fake.confirm.LineNo != 3 || !fake.confirm.Qty.Equal(dec("2.5")) || fake.confirm.User != "u1" || fake.confirm.IfMatch != 3 || fake.idem.Key != "k1" {
		t.Fatalf("confirm: %d %+v %+v", rec.Code, fake.confirm, fake.idem)
	}
	first := fake.idem.Hash
	call(h, req{method: "POST", path: "/v1/warehouse/work/" + workID + "/lines/3:confirm", duties: dutyWorkExecute, key: "k1", user: "u2", body: body})
	if fake.idem.Hash == first {
		t.Fatal("another user reusing a key must hash differently")
	}

	fake.replayed = true
	rec = call(h, req{method: "POST", path: "/v1/warehouse/work", duties: dutyWorkSupervise, key: "k2",
		body: `{"warehouseId":"` + wh + `","workType":"SALES_PICK","lines":[{"itemId":" BOLT ","direction":"PICK","qty":"10","weight":"1.5"}]}`})
	if rec.Code != 201 || rec.Header().Get(headerReplayed) != "true" || fake.plan.Lines[0].ItemID != "BOLT" || fake.plan.WorkType != "SALES_PICK" {
		t.Fatalf("create work replay: %d %q %+v", rec.Code, rec.Header().Get(headerReplayed), fake.plan)
	}
	var planned store.Planned
	if err := json.Unmarshal(rec.Body.Bytes(), &planned); err != nil || planned.Work.ID != workID || planned.Traces == nil {
		t.Fatalf("planned body %s", rec.Body)
	}

	fake.replayed = false
	call(h, req{method: "POST", path: "/v1/warehouse/work/" + workID + ":exception", duties: dutyWorkExecute + "," + dutyWorkSupervise, key: "k3",
		body: `{"action":"CANCEL","reason":" DAMAGED "}`})
	if !fake.except.Supervise || fake.except.Reason != "DAMAGED" || fake.except.Action != store.ActionCancel {
		t.Fatalf("exception input = %+v", fake.except)
	}
	call(h, req{method: "POST", path: "/v1/warehouse/work/" + workID + ":exception", duties: dutyWorkExecute, key: "k4",
		body: `{"action":"SHORT_PICK","lineNo":1,"confirmedQty":"0","reason":"EMPTY"}`})
	if fake.except.Supervise || fake.except.Action != store.ActionShortPick || !fake.except.Qty.IsZero() {
		t.Fatalf("short pick input = %+v", fake.except)
	}

	rec = call(h, req{method: "GET", path: "/v1/warehouse/work?warehouseId=" + wh + "&status=OPEN&limit=1", duties: dutyInquiry})
	var pg page[domain.Work]
	if err := json.Unmarshal(rec.Body.Bytes(), &pg); err != nil || rec.Code != 200 || len(pg.Items) != 1 || pg.NextCursor == "" {
		t.Fatalf("list work: %d %s", rec.Code, rec.Body)
	}
	call(h, req{method: "GET", path: "/v1/warehouse/work?warehouseId=" + wh + "&cursor=" + pg.NextCursor, duties: dutyInquiry})
	if fake.wfilter.After == nil || fake.wfilter.After.ID != workID || fake.wfilter.Limit != defaultPageSize {
		t.Fatalf("cursor did not round-trip: %+v", fake.wfilter)
	}

	rec = call(h, req{method: "GET", path: "/v1/warehouse/locations?warehouseId=" + wh + "&zone=A", duties: dutySetupView})
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"items":[]`) || fake.lfilter.Zone != "A" {
		t.Fatalf("empty location list must be an array: %s", rec.Body)
	}
}

// Without a store the persisted routes are not mounted, and simulation stays available.
func TestStatelessMode_KeepsSimulateOnly(t *testing.T) {
	r := httpx.NewRouter()
	New().Register(r)
	rec := call(r, req{method: "POST", path: "/v1/warehouse/work", duties: dutyWorkSupervise, key: "k", body: `{}`})
	if rec.Code != http.StatusNotFound && rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("persisted route mounted without a store: %d", rec.Code)
	}
}

// End to end over HTTP and Postgres: set up a location and a directive,
// create put-away work, assign it, confirm it, and read it back.
func TestPersistedRoutes_PutAwayFlow(t *testing.T) {
	pool := testkit.DB(t)
	if err := dbx.Migrate(context.Background(), pool, migrations.FS); err != nil {
		t.Fatal(err)
	}
	h := persistedRouter(store.New(pool))
	wh := ids.New()
	all := strings.Join([]string{dutySetupMaintain, dutySetupView, dutyWorkSupervise, dutyWorkExecute, dutyInquiry}, ",")
	post := func(path, key, body string) *httptest.ResponseRecorder {
		return call(h, req{method: "POST", path: path, duties: all, key: key, body: body})
	}
	if rec := post("/v1/warehouse/locations:bulk", "l1", `{"warehouseId":"`+wh+`","locations":[{"name":"A-01","zone":"A","capacity":{"maxWeight":"10","allowMixedItems":true}}]}`); rec.Code != 201 {
		t.Fatalf("locations: %d %s", rec.Code, rec.Body)
	}
	if rec := post("/v1/warehouse/location-directives", "d1", `{"warehouseId":"`+wh+`","sequence":10,"workType":"PURCHASE_PUTAWAY","direction":"PUT","lines":[{"seq":1,"fromQty":"0","actions":[{"seq":1,"strategy":"consolidate","zone":"A"}]}]}`); rec.Code != 201 {
		t.Fatalf("directive: %d %s", rec.Code, rec.Body)
	}
	rec := post("/v1/warehouse/work", "w1", `{"warehouseId":"`+wh+`","workType":"PURCHASE_PUTAWAY","lines":[{"itemId":"BOLT","direction":"PUT","qty":"4","weight":"8"}]}`)
	var planned store.Planned
	if err := json.Unmarshal(rec.Body.Bytes(), &planned); err != nil || rec.Code != 201 || planned.Work.Status != domain.WorkOpen || len(planned.Traces[0].Trace) == 0 {
		t.Fatalf("work: %d %s", rec.Code, rec.Body)
	}
	if rec := post("/v1/warehouse/work:assign", "a1", `{"warehouseId":"`+wh+`"}`); rec.Code != 200 || !strings.Contains(rec.Body.String(), planned.Work.ID) {
		t.Fatalf("assign: %d %s", rec.Code, rec.Body)
	}
	path := "/v1/warehouse/work/" + planned.Work.ID + "/lines/1:confirm"
	if rec := post(path, "c1", `{"qty":"5"}`); rec.Code != 422 || problemOf(t, rec).Code != domain.CodeOverPick {
		t.Fatalf("over-pick: %d %s", rec.Code, rec.Body)
	}
	if rec := post(path, "c2", `{"qty":"4"}`); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"status":"COMPLETED"`) {
		t.Fatalf("confirm: %d %s", rec.Code, rec.Body)
	}
	if rec := post(path, "c2", `{"qty":"4"}`); rec.Code != 200 || rec.Header().Get(headerReplayed) != "true" {
		t.Fatalf("confirm replay: %d %s", rec.Code, rec.Body)
	}
	if rec := post(path, "c3", `{"qty":"4"}`); rec.Code != 409 || problemOf(t, rec).Code != domain.CodeLineConfirmed {
		t.Fatalf("second confirmation: %d %s", rec.Code, rec.Body)
	}
	rec = call(h, req{method: "GET", path: "/v1/warehouse/locations?warehouseId=" + wh, duties: all})
	if !strings.Contains(rec.Body.String(), `"stock":{"BOLT":"4"}`) {
		t.Fatalf("location after put-away: %s", rec.Body)
	}
	if rec := call(h, req{method: "GET", path: "/v1/warehouse/work/" + ids.New(), duties: all}); rec.Code != 404 || problemOf(t, rec).Code != codeWorkNotFound {
		t.Fatalf("missing work: %d %s", rec.Code, rec.Body)
	}
}
