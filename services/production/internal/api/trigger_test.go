package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/production/internal/domain"
	"github.com/BenjaminSRussell/passage/services/production/internal/store"
	"github.com/BenjaminSRussell/passage/services/production/migrations"
)

func (f *fakeStore) CreateTriggerPoint(_ context.Context, in store.NewTriggerPoint) (store.TriggerPoint, bool, error) {
	f.triggers = append(f.triggers, in)
	if f.triggerErr != nil {
		return store.TriggerPoint{}, false, f.triggerErr
	}
	return store.TriggerPoint{TriggerPoint: domain.TriggerPoint{ID: "tp-1", OrderID: in.OrderID, OperNumber: in.OperNumber, Sequence: in.Sequence,
		Condition: in.Condition, Event: in.Event, Function: in.Function, Operations: in.Operations, Repeatable: in.Repeatable}, Firings: []store.TriggerFiring{}}, !f.triggerOld, nil
}

func (f *fakeStore) ListTriggerPoints(context.Context, string) ([]store.TriggerPoint, error) {
	return f.triggerList, f.getErr
}

func (f *fakeStore) DeleteTriggerPoint(_ context.Context, id string) error {
	f.triggerDel = append(f.triggerDel, id)
	return f.triggerErr
}

func triggerCall(orderID, body string, duties ...string) call {
	return call{method: http.MethodPost, path: "/v1/production/orders/" + orderID + "/trigger-points", body: body, duties: duties,
		headers: map[string]string{idempotencyName: "trigger-1"}}
}

func TestTrigger_CreatePassesTheDefinition(t *testing.T) {
	f := &fakeStore{}
	_, h := newServer(t, f)
	id, res := ids.New(), ids.New()
	body := `{"operNumber":20,"sequence":10,"condition":"  qty_error   >  0 ","function":"insert-operations","repeatable":true,
		"operations":[{"resourceId":"` + res + `","runTimeHPer":"0.5","laborRate":"40"}]}`
	rec := do(h, triggerCall(id, body, DutySetupWrite))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	var got store.TriggerPoint
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || got.ID != "tp-1" || got.Firings == nil {
		t.Fatalf("body: %s", rec.Body.String())
	}
	if len(f.triggers) != 1 {
		t.Fatalf("store calls: %d", len(f.triggers))
	}
	in := f.triggers[0]
	// The condition is stored in its canonical form.
	if in.OrderID != id || in.OperNumber != 20 || in.Sequence != 10 || in.Condition != "qty_error > 0" || in.Function != domain.FnInsertOperations ||
		!in.Repeatable || len(in.Operations) != 1 || in.Operations[0].ResourceID != res || !in.Operations[0].LaborRate.Equal(decimal.NewFromInt(40)) ||
		in.Actor != "planner-1" || in.IdempotencyKey != "trigger-1" || in.RequestHash == "" {
		t.Fatalf("passed: %+v", in)
	}

	// A replay answers 200 with Idempotent-Replayed.
	f.triggerOld = true
	rec = do(h, triggerCall(id, body, DutySetupWrite))
	if rec.Code != http.StatusOK || rec.Header().Get("Idempotent-Replayed") != "true" {
		t.Fatalf("replay: %d %v", rec.Code, rec.Header())
	}
}

// A compound condition is stored canonically, the event defaults to feedback and can be
// operation-finished.
func TestTrigger_CreateEventAndCompoundCondition(t *testing.T) {
	f := &fakeStore{}
	_, h := newServer(t, f)
	id := ids.New()
	for _, tc := range []struct{ body, event, condition string }{
		{`{"operNumber":20,"sequence":1,"condition":"qty_error>0","function":"notify"}`, "", ""},
		{`{"operNumber":20,"sequence":1,"condition":"hours >= 2.0  and error_pct > 5 or qty_total == 0","function":"notify"}`, "feedback", "hours >= 2 and error_pct > 5 or qty_total == 0"},
		{`{"operNumber":20,"sequence":2,"event":"operation-finished","condition":"qty_error > 0","function":"notify"}`, "operation-finished", "qty_error > 0"},
	} {
		if tc.event == "" {
			expectProblem(t, do(h, triggerCall(id, tc.body, DutySetupWrite)), http.StatusUnprocessableEntity, domain.CodeTriggerCondition)
			continue
		}
		rec := do(h, triggerCall(id, tc.body, DutySetupWrite))
		if rec.Code != http.StatusCreated {
			t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
		}
		in := f.triggers[len(f.triggers)-1]
		if string(in.Event) != tc.event || in.Condition != tc.condition {
			t.Fatalf("passed: %+v", in)
		}
	}
	rec := do(h, triggerCall(id, `{"operNumber":20,"sequence":3,"event":"start","condition":"hours > 1","function":"notify"}`, DutySetupWrite))
	p := expectProblem(t, rec, http.StatusBadRequest, validationCode)
	if _, ok := p.Errors["event"]; !ok {
		t.Fatalf("no error for event: %v", p.Errors)
	}
}

func TestTrigger_CreateRefusals(t *testing.T) {
	id := ids.New()
	rework := `{"operNumber":20,"sequence":1,"condition":"qty_error > 0","function":"create-rework-order","operations":[{"resourceId":"` + ids.New() + `"}]}`
	cases := []struct {
		name   string
		call   call
		status int
		code   string
		fields []string
	}{
		{name: "missing duty", call: triggerCall(id, `{}`, DutyRead, DutyWrite), status: http.StatusForbidden, code: "AUTH-0003"},
		{name: "rework needs the rework duty", call: triggerCall(id, rework, DutySetupWrite), status: http.StatusForbidden, code: "AUTH-0003"},
		{name: "unknown order id", call: triggerCall("nope", `{}`, DutySetupWrite), status: http.StatusNotFound, code: store.CodeOrderNotFound},
		{name: "unknown field", call: triggerCall(id, `{"operNumber":20,"when":"always"}`, DutySetupWrite), status: http.StatusBadRequest, code: validationCode},
		{name: "missing key", call: call{method: http.MethodPost, path: "/v1/production/orders/" + id + "/trigger-points",
			body: `{"operNumber":20,"sequence":1,"condition":"hours > 1","function":"notify"}`, duties: []string{DutySetupWrite}},
			status: http.StatusBadRequest, code: validationCode, fields: []string{idempotencyName}},
		{name: "bad shapes", call: triggerCall(id, `{"operNumber":0,"sequence":10000,"condition":" ","function":"explode",
			"operations":[{"resourceId":"x","laborRate":"-1","scrapPct":"0.1234567"}]}`, DutySetupWrite),
			status: http.StatusBadRequest, code: validationCode,
			fields: []string{"operNumber", "sequence", "condition", "function", "operations[0].resourceId", "operations[0].laborRate", "operations[0].scrapPct"}},
		{name: "bad condition", call: triggerCall(id, `{"operNumber":20,"sequence":1,"condition":"scrap > 0","function":"notify"}`, DutySetupWrite),
			status: http.StatusUnprocessableEntity, code: domain.CodeTriggerCondition},
		{name: "release-next is not available", call: triggerCall(id, `{"operNumber":20,"sequence":1,"condition":"qty_good > 0","function":"release-next"}`, DutySetupWrite),
			status: http.StatusUnprocessableEntity, code: domain.CodeTriggerFunctionUnsupported},
		{name: "start-workflow is not available", call: triggerCall(id, `{"operNumber":20,"sequence":1,"condition":"qty_good > 0","function":"start-workflow"}`, DutySetupWrite),
			status: http.StatusUnprocessableEntity, code: domain.CodeTriggerFunctionUnsupported},
		{name: "insert without operations", call: triggerCall(id, `{"operNumber":20,"sequence":1,"condition":"qty_error > 0","function":"insert-operations"}`, DutySetupWrite),
			status: http.StatusUnprocessableEntity, code: domain.CodeInvalidInput},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeStore{}
			_, h := newServer(t, f)
			p := expectProblem(t, do(h, tc.call), tc.status, tc.code)
			for _, name := range tc.fields {
				if _, ok := p.Errors[name]; !ok {
					t.Errorf("no error for %s: %v", name, p.Errors)
				}
			}
			if len(f.triggers) != 0 {
				t.Fatal("a refused request reached the store")
			}
		})
	}

	// With the rework duty the definition reaches the store, and a store refusal passes through.
	f := &fakeStore{triggerErr: httpx.NewProblem(http.StatusConflict, store.CodeTriggerPointExists, "taken")}
	_, h := newServer(t, f)
	expectProblem(t, do(h, triggerCall(id, rework, DutySetupWrite, DutyRework)), http.StatusConflict, store.CodeTriggerPointExists)
	if len(f.triggers) != 1 || f.triggers[0].Function != domain.FnCreateReworkOrder {
		t.Fatalf("store calls: %+v", f.triggers)
	}
}

func TestTrigger_ListAndDelete(t *testing.T) {
	id := ids.New()
	reason := "no room for 2 operations between operation 10 and operation 11"
	f := &fakeStore{triggerList: []store.TriggerPoint{{TriggerPoint: domain.TriggerPoint{ID: "tp-1", OrderID: id, OperNumber: 10, Sequence: 1,
		Condition: "qty_error > 0", Function: domain.FnInsertOperations, Operations: []domain.TriggerOperation{}},
		Firings: []store.TriggerFiring{{ID: "f-1", Status: store.FiringFailed, Reason: &reason, InsertedOperations: []int{}}}}}}
	_, h := newServer(t, f)
	rec := do(h, call{method: http.MethodGet, path: "/v1/production/orders/" + id + "/trigger-points", duties: []string{DutyRead}})
	var body struct {
		OrderID string               `json:"orderId"`
		Items   []store.TriggerPoint `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || rec.Code != http.StatusOK || body.OrderID != id || len(body.Items) != 1 ||
		body.Items[0].Firings[0].Reason == nil || *body.Items[0].Firings[0].Reason != reason {
		t.Fatalf("list: %d %s", rec.Code, rec.Body.String())
	}
	expectProblem(t, do(h, call{method: http.MethodGet, path: "/v1/production/orders/" + id + "/trigger-points"}), http.StatusForbidden, "AUTH-0003")

	tp := ids.New()
	if rec := do(h, call{method: http.MethodDelete, path: "/v1/production/trigger-points/" + tp, duties: []string{DutySetupWrite}}); rec.Code != http.StatusNoContent {
		t.Fatalf("delete: %d %s", rec.Code, rec.Body.String())
	}
	if len(f.triggerDel) != 1 || f.triggerDel[0] != tp {
		t.Fatalf("deleted: %v", f.triggerDel)
	}
	expectProblem(t, do(h, call{method: http.MethodDelete, path: "/v1/production/trigger-points/x", duties: []string{DutySetupWrite}}),
		http.StatusNotFound, store.CodeTriggerPointNotFound)
	expectProblem(t, do(h, call{method: http.MethodDelete, path: "/v1/production/trigger-points/" + tp, duties: []string{DutyRead}}),
		http.StatusForbidden, "AUTH-0003")
	f.triggerErr = httpx.NewProblem(http.StatusConflict, store.CodeTriggerPointFired, "fired")
	expectProblem(t, do(h, call{method: http.MethodDelete, path: "/v1/production/trigger-points/" + tp, duties: []string{DutySetupWrite}}),
		http.StatusConflict, store.CodeTriggerPointFired)
}

// AC-21.13 over HTTP and a real database: define the trigger, post a route card with 3
// error units on operation 20 and read the firing on the post response and the list.
// Skipped without PASSAGE_TEST_DATABASE_URL.
func TestHTTP_TriggerPointThroughTheDatabase(t *testing.T) {
	pool := testkit.DB(t)
	if err := dbx.Migrate(context.Background(), pool, migrations.FS); err != nil {
		t.Fatal(err)
	}
	r := httpx.NewRouter()
	New(store.New(pool), Config{AutoStartOnFeedback: true, OverConsumptionPct: decimal.NewFromInt(10)}).Register(r)
	all := []string{DutyRead, DutyWrite, DutyRelease, DutyJournalPost, DutySetupWrite, DutySetupRead}
	body := createBody(func(m map[string]any) {
		m["operations"] = []any{
			map[string]any{"operNumber": 10, "resourceId": ids.New(), "runTimeHPer": "0.1", "laborRate": "30"},
			map[string]any{"operNumber": 20, "resourceId": ids.New(), "runTimeHPer": "0.1", "laborRate": "30"},
			map[string]any{"operNumber": 30, "resourceId": ids.New(), "runTimeHPer": "0.1", "laborRate": "30"},
		}
	})
	rec := do(r, call{method: http.MethodPost, path: "/v1/production/orders", body: body, duties: all, headers: map[string]string{idempotencyName: "tp-create"}})
	var order store.Order
	if err := json.Unmarshal(rec.Body.Bytes(), &order); err != nil || rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	if rec := do(r, call{method: http.MethodPut, path: "/v1/production/posting-profiles", body: profileBody(nil), duties: all}); rec.Code != http.StatusCreated {
		t.Fatalf("profile: %d %s", rec.Code, rec.Body.String())
	}
	for i, cmd := range []string{"estimate", "schedule", "release"} {
		c := commandCall(order.ID, cmd, "", all...)
		c.headers["If-Match"] = fmt.Sprintf(`"%d"`, i+1)
		c.headers[idempotencyName] = fmt.Sprintf("tp-%d", i)
		if rec := do(r, c); rec.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", cmd, rec.Code, rec.Body.String())
		}
	}
	def := `{"operNumber":20,"sequence":10,"condition":"qty_error > 0","function":"insert-operations",
		"operations":[{"resourceId":"` + ids.New() + `","runTimeHPer":"0.5"},{"resourceId":"` + ids.New() + `"}]}`
	if rec := do(r, triggerCall(order.ID, def, all...)); rec.Code != http.StatusCreated {
		t.Fatalf("trigger: %d %s", rec.Code, rec.Body.String())
	}
	// A route card on the Released order starts it (FR-21.6) and fires the trigger.
	rec = do(r, journalCall(order.ID, `{"type":"route-card","hours":[{"operNumber":20,"hours":"1.5","qtyGood":"97","qtyError":"3"}]}`, all...))
	var j store.Journal
	if err := json.Unmarshal(rec.Body.Bytes(), &j); err != nil || rec.Code != http.StatusCreated {
		t.Fatalf("route card: %d %s", rec.Code, rec.Body.String())
	}
	rec = do(r, call{method: http.MethodPost, path: "/v1/production/journals/" + j.ID + ":post", duties: all, headers: map[string]string{idempotencyName: "tp-post"}})
	var posted struct {
		Posted         bool `json:"posted"`
		TriggerFirings []struct {
			Status             string `json:"status"`
			InsertedOperations []int  `json:"insertedOperations"`
			Feedback           struct {
				QtyError string `json:"qtyError"`
			} `json:"feedback"`
		} `json:"triggerFirings"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &posted); err != nil || rec.Code != http.StatusOK || !posted.Posted || len(posted.TriggerFirings) != 1 ||
		posted.TriggerFirings[0].Status != "fired" || len(posted.TriggerFirings[0].InsertedOperations) != 2 ||
		posted.TriggerFirings[0].InsertedOperations[0] != 25 || posted.TriggerFirings[0].Feedback.QtyError != "3" {
		t.Fatalf("post: %d %s", rec.Code, rec.Body.String())
	}
	rec = do(r, call{method: http.MethodGet, path: "/v1/production/orders/" + order.ID, duties: all})
	if err := json.Unmarshal(rec.Body.Bytes(), &order); err != nil || len(order.Operations) != 5 || order.Operations[2].OperNumber != 25 ||
		order.Operations[3].OperNumber != 26 || order.Status != domain.Started {
		t.Fatalf("order after the post: %s", rec.Body.String())
	}
	rec = do(r, call{method: http.MethodGet, path: "/v1/production/orders/" + order.ID + "/trigger-points", duties: all})
	var list struct {
		Items []store.TriggerPoint `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil || len(list.Items) != 1 || list.Items[0].FiredCount != 1 || len(list.Items[0].Firings) != 1 {
		t.Fatalf("list: %d %s", rec.Code, rec.Body.String())
	}
	// A fired trigger point is kept.
	expectProblem(t, do(r, call{method: http.MethodDelete, path: "/v1/production/trigger-points/" + list.Items[0].ID, duties: all}),
		http.StatusConflict, store.CodeTriggerPointFired)
}
