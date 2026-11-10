package api

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/mobile/internal/replay"
	"github.com/BenjaminSRussell/passage/services/mobile/internal/store"
	"github.com/BenjaminSRussell/passage/services/mobile/internal/warehouse"
	"github.com/BenjaminSRussell/passage/services/mobile/migrations"
)

const (
	dutyReceive = "warehouse.receipt.register"
	siteA       = "5e5510a0-0000-4000-8000-0000000000a1"
	workOne     = `{"work":{"id":"w-1","status":"ASSIGNED","version":2,"lines":[{"lineNo":1,"itemId":"BOLT","qty":"5","status":"OPEN"}]}}`
)

type fakeWH struct {
	calls     []string
	params    []map[string]string
	respond   map[string]warehouse.Outcome
	conflicts map[string]bool
	during    func(op string)
	readErr   error
}

func (f *fakeWH) ReadWork(ctx context.Context, workID string) (warehouse.Outcome, error) {
	if f.readErr != nil {
		return warehouse.Outcome{}, f.readErr
	}
	return f.Do(ctx, "get-work", "", map[string]string{"workId": workID})
}

func (f *fakeWH) Do(_ context.Context, op, key string, p map[string]string) (warehouse.Outcome, error) {
	f.calls = append(f.calls, op)
	f.params = append(f.params, p)
	if f.during != nil {
		f.during(op)
	}
	if f.conflicts[key] {
		return warehouse.Outcome{Status: 409, Code: "WMS-0110", Detail: "line 1 was CANCELLED by alice"}, nil
	}
	if o, ok := f.respond[op]; ok {
		return o, nil
	}
	return warehouse.Outcome{Status: 200, Body: json.RawMessage(`{}`)}, nil
}

type harness struct {
	t   *testing.T
	h   http.Handler
	api *API
	wh  *fakeWH
	now time.Time
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	admin, app := testkit.DBWithApp(t)
	if err := dbx.Migrate(context.Background(), admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	h := &harness{t: t, wh: &fakeWH{respond: map[string]warehouse.Outcome{"assign": {Status: 200, Body: json.RawMessage(workOne)}}, conflicts: map[string]bool{}},
		now: time.Date(2026, 3, 2, 8, 0, 0, 0, time.UTC)}
	h.api = New(store.New(app), h.wh, 30*time.Minute).WithClock(func() time.Time { return h.now })
	r := httpx.NewRouter()
	h.api.Register(r)
	h.h = r
	return h
}

type req struct {
	method, path, duties, key, body, user, ifMatch, tenant string
}

func (h *harness) do(q req) *httptest.ResponseRecorder {
	r := httptest.NewRequest(q.method, q.path, strings.NewReader(q.body))
	r.Header.Set("Content-Type", "application/json")
	tid := q.tenant
	if tid == "" {
		tid = testkit.TenantID(1)
	}
	r.Header.Set("X-Passage-Tenant", tid)
	r.Header.Set("X-Passage-Legal-Entity", "00000000-0000-0000-0000-0000000000e1")
	if q.user == "" {
		q.user = "alice"
	}
	r.Header.Set("X-Passage-User", q.user)
	r.Header.Set("X-Passage-Duties", q.duties)
	if q.key != "" {
		r.Header.Set("Idempotency-Key", q.key)
	}
	if q.ifMatch != "" {
		r.Header.Set("If-Match", q.ifMatch)
	}
	rec := httptest.NewRecorder()
	h.h.ServeHTTP(rec, r)
	return rec
}

func decode[T any](t *testing.T, rec *httptest.ResponseRecorder) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatalf("body %q: %v", rec.Body, err)
	}
	return v
}

func expect(t *testing.T, rec *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	if rec.Code != status {
		t.Fatalf("status %d, want %d: %s", rec.Code, status, rec.Body)
	}
	if code != "" && decode[httpx.Problem](t, rec).Code != code {
		t.Fatalf("code %q, want %q: %s", decode[httpx.Problem](t, rec).Code, code, rec.Body)
	}
}

func (h *harness) startSession(user, device string) *httptest.ResponseRecorder {
	return h.do(req{method: "POST", path: "/v1/mobile/sessions", duties: dutyExecute, user: user, body: `{"deviceId":"` + device + `","warehouseId":"` + siteA + `"}`})
}

func TestRoutesRequireTheirDutyAndATenant(t *testing.T) {
	h := newHarness(t)
	routes := []struct{ method, path, duty string }{
		{"POST", "/v1/mobile/sessions", dutyExecute},
		{"GET", "/v1/mobile/sessions", dutySupervise},
		{"POST", "/v1/mobile/sessions/" + ids.New() + ":force-signout", dutySupervise},
		{"GET", "/v1/mobile/menus/default", dutyExecute},
		{"POST", "/v1/mobile/flows", dutyAdmin},
		{"POST", "/v1/mobile/flows:install-core", dutyAdmin},
		{"POST", "/v1/mobile/scan:parse", dutyExecute},
		{"POST", "/v1/mobile/offline:replay", dutyExecute},
		{"GET", "/v1/mobile/offline/conflicts", dutyResolve},
		{"POST", "/v1/mobile/offline/conflicts/d:1:resolve", dutyResolve},
	}
	for _, rt := range routes {
		if rec := h.do(req{method: rt.method, path: rt.path, duties: "warehouse.inquiry.view", key: "k", body: `{}`}); rec.Code != http.StatusForbidden {
			t.Errorf("%s %s without %s = %d", rt.method, rt.path, rt.duty, rec.Code)
		}
		if rec := h.do(req{method: rt.method, path: rt.path, duties: rt.duty, tenant: "not-a-uuid", key: "k", body: `{}`}); rec.Code != http.StatusUnauthorized {
			t.Errorf("%s %s with a malformed tenant = %d", rt.method, rt.path, rec.Code)
		}
	}
}

func TestAC328SessionTakeoverAfterIdleAndEndedSessionCannotBeat(t *testing.T) {
	h := newHarness(t)
	first := h.startSession("alice", "dev-1")
	expect(t, first, http.StatusCreated, "")
	if again := h.startSession("alice", "dev-1"); again.Code != http.StatusOK || !decode[sessionResponse](t, again).Resumed {
		t.Fatalf("same user resumes: %d %s", again.Code, again.Body)
	}
	h.now = h.now.Add(10 * time.Minute)
	expect(t, h.startSession("bob", "dev-1"), http.StatusConflict, codeSessionBusy)
	h.now = h.now.Add(25 * time.Minute)
	expect(t, h.startSession("bob", "dev-1"), http.StatusCreated, "")
	id := decode[sessionResponse](t, first).Session.SessionID
	expect(t, h.do(req{method: "POST", path: "/v1/mobile/sessions/" + id + ":heartbeat", duties: dutyExecute}), http.StatusConflict, codeSessionEnded)
}

func TestForceSignOutNeedsSuperviseAndAReason(t *testing.T) {
	h := newHarness(t)
	id := decode[sessionResponse](t, h.startSession("alice", "dev-2")).Session.SessionID
	path := "/v1/mobile/sessions/" + id + ":force-signout"
	expect(t, h.do(req{method: "POST", path: path, duties: dutySupervise, body: `{}`}), http.StatusBadRequest, codeValidation)
	rec := h.do(req{method: "POST", path: path, duties: dutySupervise, user: "sup", body: `{"reason":"lost device"}`})
	if rec.Code != http.StatusOK || decode[store.Session](t, rec).EndReason != forcedReason {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	expect(t, h.do(req{method: "POST", path: "/v1/mobile/sessions/" + id + ":end", duties: dutyExecute, user: "bob"}), http.StatusForbidden, codeNotOwner)
}

func TestStaleSessionResumeKeepsWorkOnlyWhileWarehouseStillHasIt(t *testing.T) {
	for _, tc := range []struct {
		status   string
		relocked bool
	}{{"ASSIGNED", true}, {"COMPLETED", false}} {
		t.Run(tc.status, func(t *testing.T) {
			h := newHarness(t)
			id := decode[sessionResponse](t, h.startSession("alice", "dev-3")).Session.SessionID
			assign := h.do(req{method: "POST", path: "/v1/mobile/sessions/" + id + ":assign-work", duties: dutyExecute, key: "as-1"})
			if assign.Code != http.StatusOK || decode[assignResponse](t, assign).Session.CurrentWorkID != "w-1" {
				t.Fatalf("assign: %d %s", assign.Code, assign.Body)
			}
			h.now = h.now.Add(31 * time.Minute)
			if rec := h.do(req{method: "POST", path: "/v1/mobile/sessions:sweep", duties: dutySupervise, user: "sup"}); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"marked":1`) {
				t.Fatalf("sweep: %s", rec.Body)
			}
			h.wh.respond["get-work"] = warehouse.Outcome{Status: 200, Body: json.RawMessage(strings.Replace(workOne, "ASSIGNED", tc.status, 1))}
			rec := h.do(req{method: "POST", path: "/v1/mobile/sessions/" + id + ":heartbeat", duties: dutyExecute})
			got := decode[heartbeatResponse](t, rec)
			if rec.Code != 200 || !got.Resumed || got.Relocked != tc.relocked || (got.Session.CurrentWorkID == "w-1") != tc.relocked {
				t.Fatalf("heartbeat: %d %+v", rec.Code, got)
			}
		})
	}
}

func TestPickFlowRunsScanChecksAndCallsTheWarehouse(t *testing.T) {
	h := newHarness(t)
	expect(t, h.do(req{method: "POST", path: "/v1/mobile/flows:install-core", duties: dutyAdmin, key: "core"}), http.StatusOK, "")
	id := decode[sessionResponse](t, h.startSession("alice", "dev-4")).Session.SessionID
	h.do(req{method: "POST", path: "/v1/mobile/sessions/" + id + ":assign-work", duties: dutyExecute, key: "as"})
	expect(t, h.do(req{method: "POST", path: "/v1/mobile/sessions/" + id + "/runs", duties: dutyExecute, body: `{"flowCode":"NOPE"}`}), http.StatusNotFound, codeNotFound)
	start := h.do(req{method: "POST", path: "/v1/mobile/sessions/" + id + "/runs", duties: dutyExecute, body: `{"flowCode":"PICK"}`})
	if start.Code != http.StatusCreated || decode[runResponse](t, start).Step.ID != "scan_location" {
		t.Fatalf("start: %d %s", start.Code, start.Body)
	}
	submit := func(step, body, key string) *httptest.ResponseRecorder {
		return h.do(req{method: "POST", path: "/v1/mobile/sessions/" + id + "/runs/current/steps/" + step + ":submit", duties: dutyExecute, key: key, body: body})
	}
	expect(t, submit("scan_location", `{"value":"nope"}`, "s1"), http.StatusUnprocessableEntity, "MOB-0101")
	expect(t, submit("scan_item", `{"value":"A-01-02"}`, "s2"), http.StatusConflict, codeStep)
	for i, in := range []struct{ step, body string }{
		{"scan_location", `{"value":"A-01-02"}`}, {"scan_item", `{"value":"0112345678901231"}`}, {"enter_qty", `{"value":"5"}`},
		{"qty_check", `{"outcome":"full"}`}, {"confirm_pick", `{}`},
	} {
		if rec := submit(in.step, in.body, "k"+string(rune('a'+i))); rec.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", in.step, rec.Code, rec.Body)
		}
	}
	done := submit("call_confirm", `{}`, "call-1")
	if done.Code != http.StatusOK || !decode[runResponse](t, done).Run.Done {
		t.Fatalf("call: %d %s", done.Code, done.Body)
	}
	last := h.wh.params[len(h.wh.params)-1]
	if h.wh.calls[len(h.wh.calls)-1] != "confirm" || last["workId"] != "w-1" || last["lineNo"] != "1" || last["qty"] != "5" {
		t.Fatalf("warehouse call %v %v", h.wh.calls, last)
	}
}

func TestAC324PublishReachesTheNextMenuLoadAndTheEventStream(t *testing.T) {
	h := newHarness(t)
	flow := func(prompt string) string {
		return `{"code":"RECEIVE_LOT","entryStepId":"scan_lp","steps":[{"id":"scan_lp","type":"scan","prompt":"` + prompt + `","expectedScan":["LP"],"next":{"default":null}}]}`
	}
	publish := func(key, prompt string) store.Flow {
		saved := decode[store.Flow](t, h.do(req{method: "POST", path: "/v1/mobile/flows", duties: dutyAdmin, key: "d" + key, body: flow(prompt)}))
		rec := h.do(req{method: "POST", path: "/v1/mobile/flows/" + saved.FlowID + ":publish", duties: dutyAdmin, key: "p" + key, ifMatch: `"` + string(rune('0'+saved.Version)) + `"`})
		if rec.Code != http.StatusOK {
			t.Fatalf("publish: %d %s", rec.Code, rec.Body)
		}
		return decode[store.Flow](t, rec)
	}
	srv := httptest.NewServer(chi.Chain().Handler(h.h))
	defer srv.Close()
	reqStream, _ := http.NewRequest("GET", srv.URL+"/v1/mobile/events", nil)
	reqStream.Header.Set("X-Passage-Tenant", testkit.TenantID(1))
	reqStream.Header.Set("X-Passage-Legal-Entity", "00000000-0000-0000-0000-0000000000e1")
	reqStream.Header.Set("X-Passage-User", "alice")
	reqStream.Header.Set("X-Passage-Duties", dutyExecute)
	stream, err := http.DefaultClient.Do(reqStream)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stream.Body.Close() }()
	lines := make(chan string, 8)
	go func() {
		sc := bufio.NewScanner(stream.Body)
		for sc.Scan() {
			lines <- sc.Text()
		}
	}()
	publish("1", "Scan the LP")
	menu := decode[menuResponse](t, h.do(req{method: "GET", path: "/v1/mobile/menus/default?warehouseId=" + siteA, duties: dutyExecute}))
	if len(menu.Flows) != 1 || menu.Flows[0].Version != 1 {
		t.Fatalf("menu v1: %+v", menu)
	}
	v2 := publish("2", "Scan the receiving LP")
	menu = decode[menuResponse](t, h.do(req{method: "GET", path: "/v1/mobile/menus/default?warehouseId=" + siteA, duties: dutyExecute}))
	if len(menu.Flows) != 1 || menu.Flows[0].Version != 2 || menu.Flows[0].Steps[0].Prompt != "Scan the receiving LP" || v2.Version != 2 {
		t.Fatalf("menu v2 without an app release: %+v", menu)
	}
	var seen []string
	deadline := time.After(3 * time.Second)
	for len(seen) < 2 || !strings.Contains(strings.Join(seen, "\n"), `"version":2`) {
		select {
		case l := <-lines:
			seen = append(seen, l)
		case <-deadline:
			t.Fatalf("event stream lines: %v", seen)
		}
	}
	if !strings.Contains(strings.Join(seen, "\n"), "event: flow.published") {
		t.Fatalf("stream: %v", seen)
	}
	other := h.do(req{method: "GET", path: "/v1/mobile/menus/default", duties: dutyExecute, tenant: testkit.TenantID(2)})
	if m := decode[menuResponse](t, other); len(m.Flows) != 0 {
		t.Fatalf("another tenant sees %d flows", len(m.Flows))
	}
}

func TestPublishRefusesAnInvalidGraphAndReplaysItsKey(t *testing.T) {
	h := newHarness(t)
	bad := `{"code":"BAD_FLOW","entryStepId":"a","steps":[{"id":"a","type":"confirm","prompt":"x","next":{"default":null}},{"id":"orphan","type":"confirm","prompt":"y","next":{"default":null}}]}`
	saved := decode[store.Flow](t, h.do(req{method: "POST", path: "/v1/mobile/flows", duties: dutyAdmin, key: "bad", body: bad}))
	path := "/v1/mobile/flows/" + saved.FlowID + ":publish"
	expect(t, h.do(req{method: "POST", path: path, duties: dutyAdmin, key: "pb"}), http.StatusUnprocessableEntity, codeGraph)
	expect(t, h.do(req{method: "POST", path: "/v1/mobile/flows", duties: dutyAdmin, key: "lower", body: `{"code":"bad","entryStepId":"a","steps":[]}`}), http.StatusBadRequest, codeValidation)
	good := decode[store.Flow](t, h.do(req{method: "POST", path: "/v1/mobile/flows", duties: dutyAdmin, key: "g", body: strings.Replace(bad, "BAD_FLOW", "GOOD_FLOW", 1)}))
	expect(t, h.do(req{method: "POST", path: "/v1/mobile/flows/" + good.FlowID + ":publish", duties: dutyAdmin, key: "gp", ifMatch: `"9"`}), http.StatusPreconditionFailed, codeStale)
}

func TestScanParseReportsFieldsAndCheckDigitFailures(t *testing.T) {
	h := newHarness(t)
	ok := decode[scanResponse](t, h.do(req{method: "POST", path: "/v1/mobile/scan:parse", duties: dutyExecute, body: `{"raw":"]d20112345678901231","expect":"GTIN"}`}))
	if ok.GS1 == nil || !ok.GS1.OK || ok.GS1.Fields["01"] != "12345678901231" || ok.Verdict != nil {
		t.Fatalf("%+v", ok)
	}
	bad := decode[scanResponse](t, h.do(req{method: "POST", path: "/v1/mobile/scan:parse", duties: dutyExecute, body: `{"raw":"]d20112345678901234","expect":"GTIN"}`}))
	if bad.Verdict == nil || bad.Verdict.Code != "MOB-0102" {
		t.Fatalf("%+v", bad)
	}
	expect(t, h.do(req{method: "POST", path: "/v1/mobile/scan:parse", duties: dutyExecute, body: `{"raw":"x","expect":"BOX"}`}), http.StatusBadRequest, codeValidation)
}

func replayBody(device string, seq int, line int) string {
	return `{"idempotencyKey":"` + device + `:` + string(rune('0'+seq)) + `","command":"ConfirmWorkLine","capturedAt":"2026-03-02T07:00:00Z","payload":{"workId":"w-1","lineNo":` + string(rune('0'+line)) + `,"qty":"5"}}`
}

func TestAC326ConflictIsParkedBlocksTheQueueAndResolves(t *testing.T) {
	h := newHarness(t)
	h.wh.conflicts["dev-6:2"] = true
	h.startSession("alice", "dev-6")
	send := func(seq int) *httptest.ResponseRecorder {
		return h.do(req{method: "POST", path: "/v1/mobile/offline:replay", duties: dutyExecute, key: "dev-6:" + string(rune('0'+seq)), body: replayBody("dev-6", seq, seq)})
	}
	expect(t, send(1), http.StatusOK, "")
	expect(t, send(2), http.StatusConflict, codeConflict)
	expect(t, send(3), http.StatusTooManyRequests, codeBlocked)
	expect(t, send(5), http.StatusUnprocessableEntity, "MOB-0108")
	list := h.do(req{method: "GET", path: "/v1/mobile/offline/conflicts", duties: dutyResolve, user: "sup"})
	items := decode[struct{ Items []store.OfflineTx }](t, list).Items
	if len(items) != 1 || items[0].IdempotencyKey != "dev-6:2" || items[0].ConflictReason != "line 1 was CANCELLED by alice" {
		t.Fatalf("queue: %s", list.Body)
	}
	other := h.do(req{method: "GET", path: "/v1/mobile/offline/conflicts", duties: dutyResolve, tenant: testkit.TenantID(2)})
	if got := decode[struct{ Items []store.OfflineTx }](t, other).Items; len(got) != 0 {
		t.Fatalf("another tenant sees %d conflicts", len(got))
	}
	resolve := func(body, ifMatch, key string) *httptest.ResponseRecorder {
		return h.do(req{method: "POST", path: "/v1/mobile/offline/conflicts/dev-6:2:resolve", duties: dutyResolve, user: "sup", key: key, body: body, ifMatch: ifMatch})
	}
	expect(t, resolve(`{"action":"reject"}`, "", "r0"), http.StatusBadRequest, codeValidation)
	expect(t, resolve(`{"action":"maybe"}`, `"2"`, "r1"), http.StatusBadRequest, codeValidation)
	expect(t, resolve(`{"action":"reject"}`, `"9"`, "r2"), http.StatusPreconditionFailed, codeStale)
	ok := resolve(`{"action":"reject"}`, `"`+string(rune('0'+items[0].Version))+`"`, "r3")
	if ok.Code != http.StatusOK || decode[store.OfflineTx](t, ok).Status != store.TxRejected {
		t.Fatalf("resolve: %d %s", ok.Code, ok.Body)
	}
	expect(t, send(3), http.StatusOK, "")
	expect(t, h.do(req{method: "POST", path: "/v1/mobile/offline/conflicts/dev-6:77:resolve", duties: dutyResolve, key: "r4", body: `{"action":"reject"}`, ifMatch: `"1"`}), http.StatusNotFound, codeNotFound)
}

func TestBatchReplayAnswersPerTransactionAndWarehouseOutageIs503(t *testing.T) {
	h := newHarness(t)
	h.startSession("alice", "dev-7")
	batch := `{"deviceId":"dev-7","transactions":[` +
		`{"seq":1,"command":"ConfirmWorkLine","capturedAt":"2026-03-02T07:00:00Z","payload":{"workId":"w","lineNo":1,"qty":"1"}},` +
		`{"seq":2,"command":"ConfirmWorkLine","capturedAt":"2026-03-02T07:00:01Z","payload":{"workId":"w","lineNo":2,"qty":"1"}}]}`
	rec := h.do(req{method: "POST", path: "/v1/mobile/offline:replay", duties: dutyExecute, body: batch})
	res := decode[struct {
		Results []struct {
			Seq    int64
			Status string
		}
	}](t, rec)
	if rec.Code != 200 || len(res.Results) != 2 || res.Results[0].Status != store.TxApplied || res.Results[1].Status != store.TxApplied {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if n := len(h.wh.calls); n != 2 {
		t.Fatalf("calls %d", n)
	}
	expect(t, h.do(req{method: "POST", path: "/v1/mobile/offline:replay", duties: dutyExecute, body: `{"idempotencyKey":"nocolon","command":"x","payload":{},"capturedAt":"2026-03-02T07:00:00Z"}`}), http.StatusBadRequest, codeValidation)
}

func TestMetricsExposeCounters(t *testing.T) {
	h := newHarness(t)
	h.startSession("alice", "dev-8")
	rec := httptest.NewRecorder()
	h.api.Metrics().ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	if !strings.Contains(rec.Body.String(), "mobile_sessions_started_total 1") {
		t.Fatalf("%s", rec.Body)
	}
}

func (h *harness) runReceive(id, duties, keyPrefix string) *httptest.ResponseRecorder {
	h.do(req{method: "POST", path: "/v1/mobile/sessions/" + id + "/runs", duties: duties, body: `{"flowCode":"RECEIVE"}`})
	var last *httptest.ResponseRecorder
	for i, in := range []struct{ step, body string }{
		{"scan_receipt", `{"value":"R-1"}`}, {"enter_line", `{"value":"1"}`}, {"enter_qty", `{"value":"4"}`}, {"confirm_receive", `{}`}, {"call_receive", `{}`},
	} {
		last = h.do(req{method: "POST", path: "/v1/mobile/sessions/" + id + "/runs/current/steps/" + in.step + ":submit", duties: duties, key: keyPrefix + string(rune('a'+i)), body: in.body})
	}
	return last
}

func TestAWarehouseCallNeedsTheUsersOwnDutyOnTheStepPath(t *testing.T) {
	h := newHarness(t)
	expect(t, h.do(req{method: "POST", path: "/v1/mobile/flows:install-core", duties: dutyAdmin, key: "core"}), http.StatusOK, "")
	id := decode[sessionResponse](t, h.startSession("alice", "dev-r")).Session.SessionID
	expect(t, h.runReceive(id, dutyExecute, "rcv1-"), http.StatusForbidden, "AUTH-0003")
	if slicesContain(h.wh.calls, "receive") {
		t.Fatalf("the warehouse was asked to post a receipt for a user without warehouse.receipt.register: %v", h.wh.calls)
	}
	done := h.runReceive(id, dutyExecute+","+dutyReceive, "rcv2-")
	if done.Code != http.StatusOK || !slicesContain(h.wh.calls, "receive") {
		t.Fatalf("a user holding the duty posts the receipt: %d %s %v", done.Code, done.Body, h.wh.calls)
	}
}

func sameJSON(t *testing.T, a, b *httptest.ResponseRecorder) bool {
	t.Helper()
	return reflect.DeepEqual(decode[any](t, a), decode[any](t, b))
}

func slicesContain(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

func TestAWarehouseCallNeedsTheUsersOwnDutyOnTheReplayPath(t *testing.T) {
	h := newHarness(t)
	h.startSession("alice", "dev-rr")
	body := `{"idempotencyKey":"dev-rr:1","command":"ReceiveLine","capturedAt":"2026-03-02T07:00:00Z","payload":{"receiptId":"r-1","lineNo":1,"qty":"3"}}`
	rejected := h.do(req{method: "POST", path: "/v1/mobile/offline:replay", duties: dutyExecute, key: "dev-rr:1", body: body})
	expect(t, rejected, http.StatusUnprocessableEntity, "MOB-REJECTED")
	if len(h.wh.calls) != 0 || !strings.Contains(decode[httpx.Problem](t, rejected).Detail, "AUTH-0003") {
		t.Fatalf("warehouse called: %v; %s", h.wh.calls, rejected.Body)
	}
	if tx, err := h.api.st.GetOffline(tenantCtx(), "dev-rr:1"); err != nil || tx.Status != store.TxRejected {
		t.Fatalf("the transaction is closed as REJECTED with its reason: %+v %v", tx, err)
	}
}

func TestOneCommandWithoutItsDutyDoesNotBlockTheRestOfTheBatch(t *testing.T) {
	h := newHarness(t)
	h.startSession("alice", "dev-mx")
	batch := `{"deviceId":"dev-mx","transactions":[` +
		`{"seq":1,"command":"ConfirmWorkLine","capturedAt":"2026-03-02T07:00:00Z","payload":{"workId":"w","lineNo":1,"qty":"1"}},` +
		`{"seq":2,"command":"ReceiveLine","capturedAt":"2026-03-02T07:00:01Z","payload":{"receiptId":"r-1","lineNo":1,"qty":"2"}},` +
		`{"seq":3,"command":"ConfirmWorkLine","capturedAt":"2026-03-02T07:00:02Z","payload":{"workId":"w","lineNo":2,"qty":"1"}}]}`
	rec := h.do(req{method: "POST", path: "/v1/mobile/offline:replay", duties: dutyExecute, body: batch})
	res := decode[struct {
		Results []struct {
			Seq            int64
			Status, Reason string
		}
	}](t, rec)
	if rec.Code != 200 || len(res.Results) != 3 || res.Results[0].Status != store.TxApplied || res.Results[1].Status != store.TxRejected ||
		!strings.Contains(res.Results[1].Reason, "AUTH-0003") || res.Results[2].Status != store.TxApplied {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if !reflect.DeepEqual(h.wh.calls, []string{"confirm", "confirm"}) {
		t.Fatalf("the receive never reached the warehouse: %v", h.wh.calls)
	}
}

func TestReplayRefusesAnEntryQueuedByAnotherUserPerTransaction(t *testing.T) {
	h := newHarness(t)
	h.startSession("bob", "dev-tk")
	entry := func(user string) string {
		return `{"deviceId":"dev-tk","transactions":[{"seq":1,"userId":"` + user + `","command":"ConfirmWorkLine","capturedAt":"2026-03-02T07:00:00Z","payload":{"workId":"w","lineNo":1,"qty":"1"}}]}`
	}
	rec := h.do(req{method: "POST", path: "/v1/mobile/offline:replay", duties: dutyExecute, user: "bob", body: entry("alice")})
	res := decode[struct {
		Results []struct{ Status, Code string }
	}](t, rec)
	if rec.Code != 200 || len(res.Results) != 1 || res.Results[0].Status != "NOT_OWNER" || res.Results[0].Code != "MOB-NOT-OWNER" || len(h.wh.calls) != 0 {
		t.Fatalf("%d %s calls=%v", rec.Code, rec.Body, h.wh.calls)
	}
	single := `{"idempotencyKey":"dev-tk:1","userId":"alice","command":"ConfirmWorkLine","capturedAt":"2026-03-02T07:00:00Z","payload":{"workId":"w","lineNo":1,"qty":"1"}}`
	expect(t, h.do(req{method: "POST", path: "/v1/mobile/offline:replay", duties: dutyExecute, user: "bob", body: single}), http.StatusForbidden, codeNotOwner)
	expect(t, h.do(req{method: "POST", path: "/v1/mobile/offline:replay", duties: dutyExecute, user: "bob", body: entry("bob")}), http.StatusOK, "")
}

func tenantCtx() context.Context { return testkit.Ctx(testkit.TenantID(1), dutyExecute) }

func TestReplayNeedsTheCallersOpenSessionOnTheDevice(t *testing.T) {
	h := newHarness(t)
	h.startSession("bob", "dev-b")
	h.startSession("alice", "dev-a")
	send := func(user, device string, seq int) *httptest.ResponseRecorder {
		key := device + ":" + string(rune('0'+seq))
		return h.do(req{method: "POST", path: "/v1/mobile/offline:replay", duties: dutyExecute, user: user, key: key, body: replayBody(device, seq, seq)})
	}
	expect(t, send("mallory", "dev-a", 1), http.StatusForbidden, codeNotOwner)
	expect(t, send("bob", "dev-a", 1), http.StatusForbidden, codeNotOwner)
	if len(h.wh.calls) != 0 {
		t.Fatalf("another user's replay reached the warehouse: %v", h.wh.calls)
	}
	expect(t, send("alice", "dev-a", 1), http.StatusOK, "")
	other := `{"deviceId":"dev-a","sessionId":"` + decode[sessionResponse](t, h.startSession("bob", "dev-b")).Session.SessionID + `","transactions":[{"seq":2,"command":"ConfirmWorkLine","capturedAt":"2026-03-02T07:00:00Z","payload":{"workId":"w","lineNo":1,"qty":"1"}}]}`
	expect(t, h.do(req{method: "POST", path: "/v1/mobile/offline:replay", duties: dutyExecute, user: "alice", body: other}), http.StatusForbidden, codeNotOwner)
}

func TestSubmitStepRepeatReturnsTheFirstAnswerAndSurvivesAHeartbeat(t *testing.T) {
	h := newHarness(t)
	expect(t, h.do(req{method: "POST", path: "/v1/mobile/flows:install-core", duties: dutyAdmin, key: "core"}), http.StatusOK, "")
	id := decode[sessionResponse](t, h.startSession("alice", "dev-i")).Session.SessionID
	h.do(req{method: "POST", path: "/v1/mobile/sessions/" + id + ":assign-work", duties: dutyExecute, key: "as"})
	h.do(req{method: "POST", path: "/v1/mobile/sessions/" + id + "/runs", duties: dutyExecute, body: `{"flowCode":"PICK"}`})
	submit := func(step, body, key string) *httptest.ResponseRecorder {
		return h.do(req{method: "POST", path: "/v1/mobile/sessions/" + id + "/runs/current/steps/" + step + ":submit", duties: dutyExecute, key: key, body: body})
	}
	for i, in := range []struct{ step, body string }{
		{"scan_location", `{"value":"A-01-02"}`}, {"scan_item", `{"value":"0112345678901231"}`}, {"enter_qty", `{"value":"5"}`}, {"qty_check", `{"outcome":"full"}`}, {"confirm_pick", `{}`},
	} {
		expect(t, submit(in.step, in.body, "i"+string(rune('a'+i))), http.StatusOK, "")
	}
	h.wh.during = func(op string) {
		if op == "confirm" {
			expect(t, h.do(req{method: "POST", path: "/v1/mobile/sessions/" + id + ":heartbeat", duties: dutyExecute}), http.StatusOK, "")
		}
	}
	first := submit("call_confirm", `{}`, "call-1")
	if first.Code != http.StatusOK || !decode[runResponse](t, first).Run.Done {
		t.Fatalf("a heartbeat during the warehouse call must not fail the step: %d %s", first.Code, first.Body)
	}
	h.wh.during = nil
	calls := len(h.wh.calls)
	again := submit("call_confirm", `{}`, "call-1")
	if again.Code != http.StatusOK || again.Header().Get(headerReplayed) != "true" || !sameJSON(t, again, first) || len(h.wh.calls) != calls {
		t.Fatalf("a repeat of a successful step returns the first answer without calling again: %d %s calls=%d/%d", again.Code, again.Body, len(h.wh.calls), calls)
	}
	expect(t, submit("scan_location", `{"value":"A-01-02"}`, "call-1"), http.StatusUnprocessableEntity, codeReuse)
}

func TestAssignWorkRepeatReturnsTheFirstAnswer(t *testing.T) {
	h := newHarness(t)
	id := decode[sessionResponse](t, h.startSession("alice", "dev-as")).Session.SessionID
	assign := func() *httptest.ResponseRecorder {
		return h.do(req{method: "POST", path: "/v1/mobile/sessions/" + id + ":assign-work", duties: dutyExecute, key: "as-1"})
	}
	first := assign()
	again := assign()
	if first.Code != 200 || again.Header().Get(headerReplayed) != "true" || !sameJSON(t, again, first) || len(h.wh.calls) != 1 {
		t.Fatalf("%d %s | %d %s | calls %v", first.Code, first.Body, again.Code, again.Body, h.wh.calls)
	}
}

func TestResumingAStaleSessionByLoginRelocksOrReleasesItsWork(t *testing.T) {
	for _, taken := range []bool{false, true} {
		h := newHarness(t)
		expect(t, h.do(req{method: "POST", path: "/v1/mobile/flows:install-core", duties: dutyAdmin, key: "core"}), http.StatusOK, "")
		h.wh.respond["get-work"] = warehouse.Outcome{Status: 200, Body: json.RawMessage(workOne)}
		id := decode[sessionResponse](t, h.startSession("alice", "dev-s1")).Session.SessionID
		h.do(req{method: "POST", path: "/v1/mobile/sessions/" + id + ":assign-work", duties: dutyExecute, key: "as"})
		h.do(req{method: "POST", path: "/v1/mobile/sessions/" + id + "/runs", duties: dutyExecute, body: `{"flowCode":"PICK"}`})
		h.now = h.now.Add(31 * time.Minute)
		if taken {
			carol := decode[sessionResponse](t, h.startSession("carol", "dev-s2")).Session.SessionID
			h.do(req{method: "POST", path: "/v1/mobile/sessions/" + carol + ":assign-work", duties: dutyExecute, user: "carol", key: "as-c"})
		}
		resumed := decode[sessionResponse](t, h.startSession("alice", "dev-s1"))
		holds := resumed.Session.CurrentWorkID == "w-1"
		if !resumed.Resumed || holds == taken {
			t.Fatalf("taken=%v: resumed=%v currentWorkId=%q", taken, resumed.Resumed, resumed.Session.CurrentWorkID)
		}
		if taken && (docOf(resumed.Session).Run != nil || docOf(resumed.Session).Seed != nil) {
			t.Fatalf("the run seeded with the lost work must be cleared: %s", resumed.Session.State)
		}
		if !taken && docOf(resumed.Session).Run == nil {
			t.Fatalf("a run on work that was re-locked is kept: %s", resumed.Session.State)
		}
	}
}

func (h *harness) assigned(user, device string) string {
	id := decode[sessionResponse](h.t, h.startSession(user, device)).Session.SessionID
	expect(h.t, h.do(req{method: "POST", path: "/v1/mobile/sessions/" + id + ":assign-work", duties: dutyExecute, user: user, key: "as-" + device}), http.StatusOK, "")
	return id
}

func (h *harness) heartbeat(id, user string) heartbeatResponse {
	rec := h.do(req{method: "POST", path: "/v1/mobile/sessions/" + id + ":heartbeat", duties: dutyExecute, user: user})
	expect(h.t, rec, http.StatusOK, "")
	return decode[heartbeatResponse](h.t, rec)
}

func TestAnIdleSessionLosesItsWorkToAnotherPickerWithoutAnySweep(t *testing.T) {
	h := newHarness(t)
	h.wh.respond["get-work"] = warehouse.Outcome{Status: 200, Body: json.RawMessage(workOne)}
	alice := h.assigned("alice", "dev-n1")
	h.now = h.now.Add(31 * time.Minute)
	carol := h.assigned("carol", "dev-n2")
	got := h.heartbeat(alice, "alice")
	if !got.Resumed || got.Relocked || got.Session.CurrentWorkID != "" || got.Reason == "" {
		t.Fatalf("alice idle for 31 minutes with no sweep must not keep work carol now holds: %+v", got)
	}
	held, _ := h.api.st.SessionsForWork(testkit.Ctx(testkit.TenantID(1), dutyExecute), "w-1")
	if len(held) != 1 || held[0].SessionID != carol {
		t.Fatalf("only carol holds w-1: %+v", held)
	}
}

func TestStaleResumeNeedsOnlyTheExecuteDutyAndKeepsWorkItCannotConfirmGone(t *testing.T) {
	h := newHarness(t)
	h.wh.respond["get-work"] = warehouse.Outcome{Status: 200, Body: json.RawMessage(workOne)}
	id := h.assigned("alice", "dev-x1")
	h.now = h.now.Add(31 * time.Minute)
	if got := h.heartbeat(id, "alice"); !got.Relocked || got.Session.CurrentWorkID != "w-1" {
		t.Fatalf("a picker with only warehouse.work.execute keeps assigned work on resume: %+v", got)
	}

	h.now = h.now.Add(31 * time.Minute)
	h.wh.readErr = errors.New("connection reset")
	got := h.heartbeat(id, "alice")
	if got.Relocked || got.Session.CurrentWorkID != "w-1" || docOf(got.Session).Seed == nil || got.Reason == "" {
		t.Fatalf("an unreachable warehouse neither confirms nor releases: %+v", got)
	}

	h.now = h.now.Add(31 * time.Minute)
	h.wh.readErr = nil
	h.wh.respond["get-work"] = warehouse.Outcome{Status: 404, Code: "WMS-0404"}
	if got := h.heartbeat(id, "alice"); got.Relocked || got.Session.CurrentWorkID != "" {
		t.Fatalf("work the warehouse says is gone is released: %+v", got)
	}
}

func TestLoginResumeReportsWhetherTheWorkWasKept(t *testing.T) {
	h := newHarness(t)
	h.wh.respond["get-work"] = warehouse.Outcome{Status: 200, Body: json.RawMessage(strings.Replace(workOne, "ASSIGNED", "COMPLETED", 1))}
	h.assigned("alice", "dev-l1")
	h.now = h.now.Add(31 * time.Minute)
	resumed := decode[sessionResponse](t, h.startSession("alice", "dev-l1"))
	if !resumed.Resumed || resumed.Relocked || resumed.Reason == "" || resumed.Session.CurrentWorkID != "" {
		t.Fatalf("the device must be told the work was released: %+v", resumed)
	}
}

func TestAStepOnASessionIdlePastTheTimeoutIsRefusedUntilItBeats(t *testing.T) {
	for name, sweep := range map[string]bool{"swept": true, "not swept": false} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			expect(t, h.do(req{method: "POST", path: "/v1/mobile/flows:install-core", duties: dutyAdmin, key: "core"}), http.StatusOK, "")
			h.wh.respond["get-work"] = warehouse.Outcome{Status: 200, Body: json.RawMessage(workOne)}
			id := h.assigned("alice", "dev-st")
			h.do(req{method: "POST", path: "/v1/mobile/sessions/" + id + "/runs", duties: dutyExecute, body: `{"flowCode":"PICK"}`})
			submit := func(key string) *httptest.ResponseRecorder {
				return h.do(req{method: "POST", path: "/v1/mobile/sessions/" + id + "/runs/current/steps/scan_location:submit", duties: dutyExecute, key: key, body: `{"value":"A-01-02"}`})
			}
			h.now = h.now.Add(31 * time.Minute)
			if sweep {
				expect(t, h.do(req{method: "POST", path: "/v1/mobile/sessions:sweep", duties: dutySupervise, user: "sup"}), http.StatusOK, "")
			}
			expect(t, submit("st-1"), http.StatusConflict, codeSessionStale)
			if got := h.heartbeat(id, "alice"); !got.Relocked {
				t.Fatalf("heartbeat: %+v", got)
			}
			expect(t, submit("st-1"), http.StatusOK, "")
		})
	}
}

func TestAStepRetriedAfterItsKeyRecordWasLostGetsTheSameAnswer(t *testing.T) {
	h := newHarness(t)
	expect(t, h.do(req{method: "POST", path: "/v1/mobile/flows:install-core", duties: dutyAdmin, key: "core"}), http.StatusOK, "")
	id := h.assigned("alice", "dev-lk")
	h.do(req{method: "POST", path: "/v1/mobile/sessions/" + id + "/runs", duties: dutyExecute, body: `{"flowCode":"PICK"}`})
	ctx := testkit.Ctx(testkit.TenantID(1), dutyExecute)
	p, _ := tenant.FromContext(ctx)
	p.UserID = "alice"
	ctx = tenant.With(ctx, p)
	first, err := h.api.applyStep(ctx, id, "scan_location", "lost-1", submitRequest{Value: "A-01-02"})
	if err != nil {
		t.Fatal(err)
	}
	again, err := h.api.applyStep(ctx, id, "scan_location", "lost-1", submitRequest{Value: "A-01-02"})
	if err != nil || !reflect.DeepEqual(again, first) {
		t.Fatalf("the retry must repeat the first answer, not 409 wrong step: %+v %v", again, err)
	}
	if _, err := h.api.applyStep(ctx, id, "scan_location", "other-key", submitRequest{Value: "A-01-02"}); err == nil {
		t.Fatal("a different key on the spent step is still a wrong step")
	}
	firstAssign, err := h.api.applyAssign(ctx, id, "lost-as")
	if err != nil {
		t.Fatal(err)
	}
	var want, got any
	againAssign, err := h.api.applyAssign(ctx, id, "lost-as")
	if err != nil || json.Unmarshal(firstAssign.Work, &want) != nil || json.Unmarshal(againAssign.Work, &got) != nil || !reflect.DeepEqual(got, want) || len(h.wh.calls) != 2 {
		t.Fatalf("assign retry: %+v %v calls=%v", againAssign, err, h.wh.calls)
	}
}

func TestMenuWithoutWarehouseHidesRestrictedFlowsAndRejectsABadWarehouse(t *testing.T) {
	h := newHarness(t)
	restricted := `{"code":"SITE_ONLY","entryStepId":"s","warehouses":["` + siteA + `"],"steps":[{"id":"s","type":"scan","prompt":"x","expectedScan":["LP"],"next":{"default":null}}]}`
	saved := decode[store.Flow](t, h.do(req{method: "POST", path: "/v1/mobile/flows", duties: dutyAdmin, key: "d", body: restricted}))
	expect(t, h.do(req{method: "POST", path: "/v1/mobile/flows/" + saved.FlowID + ":publish", duties: dutyAdmin, key: "p"}), http.StatusOK, "")
	if m := decode[menuResponse](t, h.do(req{method: "GET", path: "/v1/mobile/menus/default", duties: dutyExecute})); len(m.Flows) != 0 {
		t.Fatalf("a warehouse-restricted flow is offered without a warehouse: %+v", m.Flows)
	}
	if m := decode[menuResponse](t, h.do(req{method: "GET", path: "/v1/mobile/menus/default?warehouseId=" + siteA, duties: dutyExecute})); len(m.Flows) != 1 {
		t.Fatalf("the flow is offered at its warehouse: %+v", m.Flows)
	}
	expect(t, h.do(req{method: "GET", path: "/v1/mobile/menus/default?warehouseId=nope", duties: dutyExecute}), http.StatusBadRequest, codeValidation)
}

func TestInstallCoreNotifiesTheEventHub(t *testing.T) {
	h := newHarness(t)
	ch, stop := h.api.hub.subscribe(testkit.TenantID(1))
	defer stop()
	expect(t, h.do(req{method: "POST", path: "/v1/mobile/flows:install-core", duties: dutyAdmin, key: "core"}), http.StatusOK, "")
	select {
	case e := <-ch:
		if e.Code == "" || e.Version != 1 {
			t.Fatalf("event %+v", e)
		}
	case <-time.After(time.Second):
		t.Fatal("installing the core flows published nothing to the event stream")
	}
}

func TestEventHubKeepsTenantsApartAndDropsEmptyTenants(t *testing.T) {
	hub := newHub()
	chA, stopA := hub.subscribe("tenant-a")
	chB, stopB := hub.subscribe("tenant-b")
	hub.publish("tenant-a", flowEvent{Code: "PICK", Version: 1})
	select {
	case <-chA:
	default:
		t.Fatal("tenant A's own stream gets its publish")
	}
	select {
	case e := <-chB:
		t.Fatalf("tenant B received tenant A's event: %+v", e)
	default:
	}
	stopA()
	stopB()
	if len(hub.subs) != 0 {
		t.Fatalf("empty tenant maps remain: %v", hub.subs)
	}
}

func TestReplayFailureMapsErrorsByKind(t *testing.T) {
	if _, ok := replayFailure(replay.WarehouseError{}).(transportError); !ok {
		t.Error("a warehouse outage is a 503")
	}
	dbErr := errors.New("connection refused")
	if _, ok := replayFailure(dbErr).(transportError); ok || !errors.Is(replayFailure(dbErr), dbErr) {
		t.Error("a database error is not reported as an unreachable warehouse")
	}
	rec := httptest.NewRecorder()
	fail(rec, httptest.NewRequest("GET", "/", nil), replayFailure(store.ErrConflict))
	if rec.Code != http.StatusConflict {
		t.Errorf("a unique violation is a 409, got %d", rec.Code)
	}
}
