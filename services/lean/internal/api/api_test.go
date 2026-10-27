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
	"github.com/BenjaminSRussell/passage/services/lean/internal/domain"
	"github.com/BenjaminSRussell/passage/services/lean/internal/store"
)

const (
	tenantID = "00000000-0000-0000-0000-000000000001"
	flowID   = "22222222-0000-4000-8000-000000000001"
	ruleID   = "33333333-0000-4000-8000-000000000001"
	cardID   = "44444444-0000-4000-8000-000000000001"
	streamID = "55555555-0000-4000-8000-000000000001"
	siteID   = "55555555-0000-4000-8000-000000000002"
	itemID   = "55555555-0000-4000-8000-000000000003"
	locID    = "55555555-0000-4000-8000-000000000004"
)

// fakeStore records the calls the handlers make and returns canned results.
// The store's own behaviour is covered by the database tests in internal/store.
type fakeStore struct {
	err        error
	replay     bool
	cards      []store.Kanban
	flow       *store.Flow
	gotFlow    store.Flow
	gotRule    store.Rule
	gotGen     store.Generate
	gotIdem    store.Idem
	gotFrom    time.Time
	gotTo      *time.Time
	gotVersion int
	gotState   domain.State
	gotBoard   store.BoardFilter
	gotStatus  string
	gotFlowID  string
	gotLimit   int
	jobs       []store.Job
	gotJobID   string
	gotDone    domain.Completion
	gotOpen    store.OpenPeriod
	gotSettle  store.SettlePeriod
	activity   store.RuleActivity
	work       store.PeriodWork
	gotRange   [2]time.Time
	gotPeriod  string
	periods    []store.Period
}

func (f *fakeStore) CreateFlow(_ context.Context, in store.Flow, idem store.Idem) (store.Flow, bool, error) {
	f.gotFlow, f.gotIdem = in, idem
	in.ID, in.Status, in.Version = flowID, "draft", 1
	return in, !f.replay, f.err
}

func (f *fakeStore) ListFlows(_ context.Context, status string, limit int) ([]store.Flow, error) {
	f.gotStatus, f.gotLimit = status, limit
	return []store.Flow{}, f.err
}

func (f *fakeStore) GetFlow(context.Context, string) (store.Flow, error) {
	if f.flow != nil {
		return *f.flow, f.err
	}
	return store.Flow{ID: flowID}, f.err
}

func (f *fakeStore) ActivateFlow(_ context.Context, _ string, version int, from time.Time, to *time.Time, idem store.Idem) (store.Flow, bool, error) {
	f.gotVersion, f.gotFrom, f.gotTo, f.gotIdem = version, from, to, idem
	return store.Flow{ID: flowID, Status: "active", Version: version + 1}, !f.replay, f.err
}

func (f *fakeStore) CreateRule(_ context.Context, in store.Rule, idem store.Idem) (store.Rule, bool, error) {
	f.gotRule, f.gotIdem = in, idem
	in.ID = ruleID
	return in, !f.replay, f.err
}

func (f *fakeStore) ListRules(_ context.Context, flow string, limit int) ([]store.Rule, error) {
	f.gotFlowID, f.gotLimit = flow, limit
	return []store.Rule{}, f.err
}

func (f *fakeStore) GetRule(context.Context, string) (store.Rule, error) {
	return store.Rule{ID: ruleID}, f.err
}

func (f *fakeStore) GenerateKanbans(_ context.Context, in store.Generate, idem store.Idem) ([]store.Kanban, bool, error) {
	f.gotGen, f.gotIdem = in, idem
	return f.cards, !f.replay, f.err
}

func (f *fakeStore) GetKanban(context.Context, string) (store.Kanban, error) {
	return store.Kanban{ID: cardID}, f.err
}

func (f *fakeStore) MoveKanban(_ context.Context, _ string, to domain.State, version int, idem store.Idem) (store.Kanban, bool, error) {
	f.gotState, f.gotVersion, f.gotIdem = to, version, idem
	return store.Kanban{ID: cardID, State: to, Version: version + 1}, !f.replay, f.err
}

func (f *fakeStore) Board(_ context.Context, filter store.BoardFilter) ([]store.Kanban, error) {
	f.gotBoard = filter
	return f.cards, f.err
}

func (f *fakeStore) BoardJobs(_ context.Context, filter store.BoardFilter) ([]store.Job, error) {
	f.gotBoard = filter
	return f.jobs, f.err
}

func (f *fakeStore) StartJob(_ context.Context, id string, version int, idem store.Idem) (store.Job, bool, error) {
	f.gotJobID, f.gotVersion, f.gotIdem = id, version, idem
	return store.Job{ID: id, State: domain.InProgress, CardState: domain.InProgress, Version: version + 1}, !f.replay, f.err
}

func (f *fakeStore) CompleteJob(_ context.Context, id string, version int, c domain.Completion, idem store.Idem) (store.Job, bool, error) {
	f.gotJobID, f.gotVersion, f.gotDone, f.gotIdem = id, version, c, idem
	return store.Job{ID: id, State: domain.Completed, Version: version + 1}, !f.replay, f.err
}

func (f *fakeStore) OpenPeriod(_ context.Context, in store.OpenPeriod, idem store.Idem) (store.Period, bool, error) {
	f.gotOpen, f.gotIdem = in, idem
	return store.Period{ID: periodID, FlowID: in.FlowID, Status: "open", Version: 1}, !f.replay, f.err
}

func (f *fakeStore) ListPeriods(_ context.Context, flow string, limit int) ([]store.Period, error) {
	f.gotFlowID, f.gotLimit = flow, limit
	if f.periods == nil {
		return []store.Period{}, f.err
	}
	return f.periods, f.err
}

func (f *fakeStore) SettlePeriod(_ context.Context, in store.SettlePeriod, idem store.Idem) (store.Period, bool, error) {
	f.gotSettle, f.gotIdem = in, idem
	return store.Period{ID: in.PeriodID, FlowID: in.FlowID, Status: "settled", Version: in.Version + 1}, !f.replay, f.err
}

func (f *fakeStore) RuleActivity(_ context.Context, ruleID string, from, to time.Time) (store.RuleActivity, error) {
	f.gotRange = [2]time.Time{from, to}
	out := f.activity
	out.Rule.ID = ruleID
	return out, f.err
}

func (f *fakeStore) PeriodWork(_ context.Context, flow, period string) (store.PeriodWork, error) {
	f.gotFlowID, f.gotPeriod = flow, period
	return f.work, f.err
}

func (f *fakeStore) Ping(context.Context) error { return f.err }

func newRouter(s store.Store) http.Handler {
	r := httpx.NewRouter()
	New(s).Register(r)
	return r
}

type call struct {
	method, path, body, duties string
	headers                    map[string]string
	noTenant                   bool
}

func (c call) do(h http.Handler) *httptest.ResponseRecorder {
	req := httptest.NewRequest(c.method, c.path, strings.NewReader(c.body))
	req.Header.Set("Content-Type", "application/json")
	if !c.noTenant {
		req.Header.Set("X-Passage-Tenant", tenantID)
		req.Header.Set("X-Passage-User", "lean-1")
	}
	req.Header.Set("X-Passage-Duties", c.duties)
	for k, v := range c.headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func key(k string) map[string]string { return map[string]string{"Idempotency-Key": k} }

func problemOf(t *testing.T, rec *httptest.ResponseRecorder) httpx.Problem {
	t.Helper()
	var p httpx.Problem
	if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
		t.Fatalf("not a problem body: %s", rec.Body)
	}
	return p
}

const flowBody = `{"valueStreamId":"` + streamID + `","siteId":"` + siteID + `","name":" Line 1 ","taktTimeS":60,"activities":[
 {"sequence":1,"name":"Mix","kind":"process","cycleTimeS":45,"outputLocationId":"` + locID + `"},
 {"sequence":2,"name":"Fill","kind":"process","cycleTimeS":30,"outputLocationId":"` + locID + `","predecessors":[1]}]}`

func TestCreateFlow_StoresTrimmedDraftWithPredecessors(t *testing.T) {
	fs := &fakeStore{}
	rec := call{method: "POST", path: "/v1/lean/flows", body: flowBody, duties: DutyFlowWrite, headers: key("f-1")}.do(newRouter(fs))
	if rec.Code != http.StatusCreated {
		t.Fatalf("status %d %s", rec.Code, rec.Body)
	}
	if fs.gotFlow.Name != "Line 1" || len(fs.gotFlow.Activities) != 2 || fs.gotFlow.Activities[1].Predecessors[0] != 1 ||
		fs.gotFlow.Activities[0].Predecessors == nil || fs.gotIdem.Key != "f-1" || fs.gotIdem.Fingerprint == "" {
		t.Fatalf("stored %+v idem %+v", fs.gotFlow, fs.gotIdem)
	}
	fs.replay = true
	again := call{method: "POST", path: "/v1/lean/flows", body: flowBody, duties: DutyFlowWrite, headers: key("f-1")}.do(newRouter(fs))
	if again.Code != http.StatusOK || again.Header().Get("Idempotent-Replayed") != "true" {
		t.Fatalf("replay %d %v", again.Code, again.Header())
	}
}

func TestCreateFlow_Validation(t *testing.T) {
	cases := map[string]struct{ body, field string }{
		"unknown predecessor": {strings.Replace(flowBody, `"predecessors":[1]`, `"predecessors":[7]`, 1), "activities[1].predecessors"},
		"self predecessor":    {strings.Replace(flowBody, `"predecessors":[1]`, `"predecessors":[2]`, 1), "activities[1].predecessors"},
		"duplicate sequence":  {strings.Replace(flowBody, `"sequence":2`, `"sequence":1`, 1), "activities[1].sequence"},
		"unknown kind":        {strings.Replace(flowBody, `"kind":"process","cycleTimeS":45`, `"kind":"assembly","cycleTimeS":45`, 1), "activities[0].kind"},
		"bad location":        {strings.Replace(flowBody, `"outputLocationId":"`+locID+`"},`, `"outputLocationId":"x"},`, 1), "activities[0].outputLocationId"},
		"no activities":       {`{"valueStreamId":"` + streamID + `","siteId":"` + siteID + `","name":"L","activities":[]}`, "activities"},
		"zero takt":           {strings.Replace(flowBody, `"taktTimeS":60`, `"taktTimeS":0`, 1), "taktTimeS"},
		"blank name":          {strings.Replace(flowBody, `" Line 1 "`, `"  "`, 1), "name"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			fs := &fakeStore{}
			rec := call{method: "POST", path: "/v1/lean/flows", body: c.body, duties: DutyFlowWrite, headers: key("f")}.do(newRouter(fs))
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status %d %s", rec.Code, rec.Body)
			}
			if p := problemOf(t, rec); p.Errors[c.field] == "" {
				t.Fatalf("no error on %s: %+v", c.field, p.Errors)
			}
			if fs.gotFlow.Name != "" {
				t.Fatal("store called on invalid input")
			}
		})
	}
}

func TestCommands_RequireIdempotencyKeyDutyAndTenant(t *testing.T) {
	h := newRouter(&fakeStore{})
	if rec := (call{method: "POST", path: "/v1/lean/flows", body: flowBody, duties: DutyFlowWrite}).do(h); rec.Code != http.StatusBadRequest {
		t.Fatalf("no key: %d", rec.Code)
	}
	if rec := (call{method: "POST", path: "/v1/lean/flows", body: flowBody, duties: DutyRead, headers: key("k")}).do(h); rec.Code != http.StatusForbidden {
		t.Fatalf("read duty created a flow: %d", rec.Code)
	}
	if rec := (call{method: "GET", path: "/v1/lean/board", noTenant: true, duties: DutyKanbanWork}).do(h); rec.Code != http.StatusUnauthorized {
		t.Fatalf("no tenant: %d", rec.Code)
	}
	if rec := (call{method: "POST", path: "/v1/lean/kanbans/" + cardID + ":move", body: `{"to":"planned","version":1}`, duties: DutyRead, headers: key("k")}).do(h); rec.Code != http.StatusForbidden {
		t.Fatalf("read duty moved a card: %d", rec.Code)
	}
}

func TestActivateFlow_DefaultsToTodayAndChecksDates(t *testing.T) {
	fs := &fakeStore{}
	rec := call{method: "POST", path: "/v1/lean/flows/" + flowID + ":activate", body: `{"version":1}`, duties: DutyFlowWrite, headers: key("a-1")}.do(newRouter(fs))
	if rec.Code != http.StatusOK || fs.gotVersion != 1 || fs.gotFrom.Format("2006-01-02") != time.Now().UTC().Format("2006-01-02") || fs.gotTo != nil {
		t.Fatalf("activate %d from %v to %v: %s", rec.Code, fs.gotFrom, fs.gotTo, rec.Body)
	}
	rec = call{method: "POST", path: "/v1/lean/flows/" + flowID + ":activate", body: `{"version":1,"activeFrom":"2026-11-02","activeTo":"2026-11-01"}`,
		duties: DutyFlowWrite, headers: key("a-2")}.do(newRouter(fs))
	if rec.Code != http.StatusBadRequest || problemOf(t, rec).Errors["activeTo"] == "" {
		t.Fatalf("to before from: %d %s", rec.Code, rec.Body)
	}
	// FR-24.8: a flow that is not connected is 422 with its LEAN code.
	fs.err = &domain.Error{Code: domain.CodeNoOutput, Detail: "flow activity Mix has no output location"}
	rec = call{method: "POST", path: "/v1/lean/flows/" + flowID + ":activate", body: `{"version":1}`, duties: DutyFlowWrite, headers: key("a-3")}.do(newRouter(fs))
	if rec.Code != http.StatusUnprocessableEntity || problemOf(t, rec).Code != "LEAN-0004" {
		t.Fatalf("unconnected flow: %d %s", rec.Code, rec.Body)
	}
}

const ruleBody = `{"code":"MIX-A","itemId":"` + itemID + `","siteId":"` + siteID + `","ruleType":"manufacturing","replenishment":"fixed",
 "fixedQty":"25","numberOfKanbans":3,"leadTimeH":8}`

func TestCreateRule_DefaultsEffectiveFromAndChecksStrategyFields(t *testing.T) {
	fs := &fakeStore{}
	rec := call{method: "POST", path: "/v1/lean/kanban-rules", body: ruleBody, duties: DutyRuleWrite, headers: key("r-1")}.do(newRouter(fs))
	if rec.Code != http.StatusCreated || fs.gotRule.EffectiveFrom != time.Now().UTC().Format("2006-01-02") || *fs.gotRule.NumberOfKanbans != 3 {
		t.Fatalf("create %d %+v %s", rec.Code, fs.gotRule, rec.Body)
	}
	cases := map[string]struct{ body, field string }{
		"kanbans on event rule": {strings.Replace(ruleBody, `"replenishment":"fixed"`, `"replenishment":"event"`, 1), "numberOfKanbans"},
		"source on fixed rule":  {strings.Replace(ruleBody, `"leadTimeH":8`, `"leadTimeH":8,"eventSource":"sales"`, 1), "eventSource"},
		"lowercase code":        {strings.Replace(ruleBody, `"MIX-A"`, `"mix-a"`, 1), "code"},
		"zero quantity":         {strings.Replace(ruleBody, `"25"`, `"0"`, 1), "fixedQty"},
		"too many decimals":     {strings.Replace(ruleBody, `"25"`, `"0.0000001"`, 1), "fixedQty"},
		"to before from":        {strings.Replace(ruleBody, `"leadTimeH":8`, `"leadTimeH":8,"effectiveFrom":"2026-11-02","effectiveTo":"2026-11-01"`, 1), "effectiveTo"},
		"lead time":             {strings.Replace(ruleBody, `"leadTimeH":8`, `"leadTimeH":9000`, 1), "leadTimeH"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			rec := call{method: "POST", path: "/v1/lean/kanban-rules", body: c.body, duties: DutyRuleWrite, headers: key("r")}.do(newRouter(&fakeStore{}))
			if rec.Code != http.StatusBadRequest || problemOf(t, rec).Errors[c.field] == "" {
				t.Fatalf("%d %s", rec.Code, rec.Body)
			}
		})
	}
	// The FR-24.9 rule check lives in the store: its domain code comes back as 400.
	fs = &fakeStore{err: &domain.Error{Code: domain.CodeInvalid, Detail: "a fixed rule needs 1 to 1000 kanbans"}}
	rec = call{method: "POST", path: "/v1/lean/kanban-rules", body: ruleBody, duties: DutyRuleWrite, headers: key("r-2")}.do(newRouter(fs))
	if rec.Code != http.StatusBadRequest || problemOf(t, rec).Code != "COM-0001" {
		t.Fatalf("domain invalid: %d %s", rec.Code, rec.Body)
	}
}

func TestGenerate_PassesDemandAndEventReference(t *testing.T) {
	fs := &fakeStore{cards: []store.Kanban{{ID: cardID, State: domain.Planned, Qty: decimal.NewFromInt(20)}}}
	body := `{"ruleId":"` + ruleID + `","demandQty":"50","eventRefType":"sales-line","eventRefId":"SO-1/10"}`
	rec := call{method: "POST", path: "/v1/lean/kanbans:generate", body: body, duties: DutyKanbanWrite, headers: key("g-1")}.do(newRouter(fs))
	if rec.Code != http.StatusCreated || !fs.gotGen.DemandQty.Equal(decimal.NewFromInt(50)) || *fs.gotGen.EventRefID != "SO-1/10" ||
		!strings.Contains(rec.Body.String(), `"items":[`) {
		t.Fatalf("generate %d %+v %s", rec.Code, fs.gotGen, rec.Body)
	}
	half := `{"ruleId":"` + ruleID + `","demandQty":"50","eventRefType":"sales-line"}`
	if rec := (call{method: "POST", path: "/v1/lean/kanbans:generate", body: half, duties: DutyKanbanWrite, headers: key("g-2")}).do(newRouter(fs)); rec.Code != http.StatusBadRequest {
		t.Fatalf("ref type without id: %d", rec.Code)
	}
	// BR-24.7: a full fixed rule is 409 LEAN-0002.
	fs.err = &domain.Error{Code: domain.CodeCapacity, Detail: "rule MIX-A already has 3 active kanbans (max 3)"}
	rec = call{method: "POST", path: "/v1/lean/kanbans:generate", body: `{"ruleId":"` + ruleID + `"}`, duties: DutyKanbanWrite, headers: key("g-3")}.do(newRouter(fs))
	if rec.Code != http.StatusConflict || problemOf(t, rec).Code != "LEAN-0002" {
		t.Fatalf("capacity: %d %s", rec.Code, rec.Body)
	}
}

// AC-24.7: an illegal move is 409 LEAN-0001; the board reverts on that answer.
func TestMove_MapsStateMachineRefusal(t *testing.T) {
	fs := &fakeStore{}
	rec := call{method: "POST", path: "/v1/lean/kanbans/" + cardID + ":move", body: `{"to":"prepared","version":2}`, duties: DutyKanbanWork, headers: key("m-1")}.do(newRouter(fs))
	if rec.Code != http.StatusOK || fs.gotState != domain.Prepared || fs.gotVersion != 2 {
		t.Fatalf("move %d %s", rec.Code, rec.Body)
	}
	fs.err = &domain.Error{Code: domain.CodeMove, Detail: "kanban move planned → completed is not allowed"}
	rec = call{method: "POST", path: "/v1/lean/kanbans/" + cardID + ":move", body: `{"to":"completed","version":2}`, duties: DutyKanbanWork, headers: key("m-2")}.do(newRouter(fs))
	if rec.Code != http.StatusConflict || problemOf(t, rec).Code != "LEAN-0001" {
		t.Fatalf("illegal move: %d %s", rec.Code, rec.Body)
	}
	bad := call{method: "POST", path: "/v1/lean/kanbans/" + cardID + ":move", body: `{"to":"walking","version":0}`, duties: DutyKanbanWork, headers: key("m-3")}.do(newRouter(&fakeStore{}))
	if p := problemOf(t, bad); bad.Code != http.StatusBadRequest || p.Errors["to"] == "" || p.Errors["version"] == "" {
		t.Fatalf("bad move: %d %s", bad.Code, bad.Body)
	}
	if rec := (call{method: "POST", path: "/v1/lean/kanbans/not-a-uuid:move", body: `{"to":"planned","version":1}`, duties: DutyKanbanWork, headers: key("m")}).do(newRouter(&fakeStore{})); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad id: %d", rec.Code)
	}
}

func TestBoard_GroupsCardsIntoStateColumns(t *testing.T) {
	fs := &fakeStore{cards: []store.Kanban{
		{ID: "a", State: domain.InProgress}, {ID: "b", State: domain.Planned}, {ID: "c", State: domain.Planned}, {ID: "d", State: domain.Emptied},
	}}
	rec := call{method: "GET", path: "/v1/lean/board?ruleId=" + ruleID, duties: DutyKanbanWork}.do(newRouter(fs))
	var body struct {
		Columns []struct {
			State   string `json:"state"`
			Kanbans []struct {
				ID string `json:"id"`
			} `json:"kanbans"`
		} `json:"columns"`
		Truncated bool `json:"truncated"`
	}
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &body) != nil || len(body.Columns) != 6 || fs.gotBoard.RuleID != ruleID {
		t.Fatalf("board %d %s", rec.Code, rec.Body)
	}
	want := map[string]int{"not-planned": 0, "planned": 2, "prepared": 0, "in-progress": 1, "completed": 0, "emptied": 1}
	order := []string{"not-planned", "planned", "prepared", "in-progress", "completed", "emptied"}
	for i, c := range body.Columns {
		if c.State != order[i] || len(c.Kanbans) != want[c.State] {
			t.Fatalf("column %d = %s with %d cards", i, c.State, len(c.Kanbans))
		}
	}
	if rec := (call{method: "GET", path: "/v1/lean/board?siteId=x", duties: DutyKanbanWork}).do(newRouter(fs)); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad site filter: %d", rec.Code)
	}
}

func TestLists_ValidateFiltersAndLimits(t *testing.T) {
	fs := &fakeStore{}
	h := newRouter(fs)
	if rec := (call{method: "GET", path: "/v1/lean/flows?status=active&limit=5", duties: DutyRead}).do(h); rec.Code != http.StatusOK || fs.gotStatus != "active" || fs.gotLimit != 5 {
		t.Fatalf("flows %d %s %d", rec.Code, fs.gotStatus, fs.gotLimit)
	}
	if rec := (call{method: "GET", path: "/v1/lean/flows?status=gone", duties: DutyRead}).do(h); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad status: %d", rec.Code)
	}
	if rec := (call{method: "GET", path: "/v1/lean/kanban-rules?flowId=" + flowID, duties: DutyRead}).do(h); rec.Code != http.StatusOK || fs.gotFlowID != flowID || fs.gotLimit != defaultLimit {
		t.Fatalf("rules %d %s", rec.Code, fs.gotFlowID)
	}
	if rec := (call{method: "GET", path: "/v1/lean/kanban-rules?limit=999", duties: DutyRead}).do(h); rec.Code != http.StatusBadRequest {
		t.Fatalf("limit: %d", rec.Code)
	}
	fs.err = httpx.NewProblem(http.StatusNotFound, store.CodeFlowNotFound, "production flow not found")
	if rec := (call{method: "GET", path: "/v1/lean/flows/" + flowID, duties: DutyRead}).do(h); rec.Code != http.StatusNotFound || problemOf(t, rec).Code != "LEAN-0101" {
		t.Fatalf("missing flow: %d %s", rec.Code, rec.Body)
	}
}

func TestFixedCount_BR247(t *testing.T) {
	rec := call{method: "POST", path: "/v1/lean/kanban-rules:fixed-count", body: `{"dailyDemand":"100","leadTimeDays":"2","safetyPct":"20","fixedQty":"50"}`,
		duties: DutyRuleWrite}.do(newRouter(&fakeStore{}))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"numberOfKanbans":5`) {
		t.Fatalf("fixed count %d %s", rec.Code, rec.Body)
	}
	rec = call{method: "POST", path: "/v1/lean/kanban-rules:fixed-count", body: `{"dailyDemand":"100","leadTimeDays":"2","safetyPct":"20","fixedQty":"0"}`,
		duties: DutyRuleWrite}.do(newRouter(&fakeStore{}))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("zero fixed qty: %d", rec.Code)
	}
}

func TestReady_ReportsDatabase(t *testing.T) {
	if rec := (call{method: "GET", path: "/readyz", noTenant: true}).do(newRouter(&fakeStore{})); rec.Code != http.StatusOK {
		t.Fatalf("ready: %d", rec.Code)
	}
	if rec := (call{method: "GET", path: "/readyz", noTenant: true}).do(newRouter(&fakeStore{err: errors.New("down")})); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("down: %d", rec.Code)
	}
}
