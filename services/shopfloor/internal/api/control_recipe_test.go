package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/shopfloor/internal/domain"
	"github.com/BenjaminSRussell/passage/services/shopfloor/internal/store"
	"github.com/BenjaminSRussell/passage/services/shopfloor/migrations"
)

func (f *fakeStore) CreateControlRecipe(_ context.Context, in store.ControlRecipeInput, by string, _ store.Idem) (store.ControlRecipe, error) {
	return store.ControlRecipe{ID: edgeID, OrderID: in.OrderID, Destination: in.Destination, Status: domain.RecipeSent, CreatedBy: by}, nil
}

func (f *fakeStore) ListControlRecipes(_ context.Context, fl store.RecipeFilter) ([]store.ControlRecipe, error) {
	f.edge().ring = fl.Status + "|" + fl.Destination
	return []store.ControlRecipe{}, nil
}

func (f *fakeStore) GetControlRecipe(context.Context, string) (store.ControlRecipeDetail, error) {
	return store.ControlRecipeDetail{}, nil
}

func (f *fakeStore) ReportControlRecipe(_ context.Context, id, to string, _ *string, _ string) (store.ControlRecipe, error) {
	return store.ControlRecipe{ID: id, Status: to}, nil
}

func (f *fakeStore) ResendControlRecipe(_ context.Context, id string, force bool, reason, _ string, _ store.Idem) (store.ControlRecipe, error) {
	return store.ControlRecipe{ID: id, Status: domain.RecipeSent, LastError: &reason}, nil
}

func (f *fakeStore) EdgeControlRecipes(_ context.Context, _, subject string) ([]store.ControlRecipe, error) {
	f.edge().subject = subject
	return []store.ControlRecipe{}, nil
}

func (f *fakeStore) EdgeReportControlRecipe(_ context.Context, _, subject, id string, rep store.EdgeRecipeReport) (store.ControlRecipe, error) {
	f.edge().subject = subject
	return store.ControlRecipe{ID: id, Status: rep.Status}, nil
}

const (
	recipeOrder    = "0195f0a0-0000-7000-8000-0000000000c1"
	recipeResource = "0195f0a0-0000-7000-8000-0000000000c2"
	recipeNode     = "ns=2;s=Vessel1.TempSP"
)

func recipeBody(dest, edge, topic string, value float64) string {
	extra := ""
	if edge != "" {
		extra += `,"edgeId":"` + edge + `"`
	}
	if topic != "" {
		extra += `,"topic":"` + topic + `"`
	}
	return fmt.Sprintf(`{"orderId":%q,"resourceId":%q,"destination":%q%s,"recipe":{"procedure":{"name":"Batch","unitProcedures":[{"name":"Reactor","operations":[{"name":"Heat","phases":[{"name":"Ramp","instructions":[
		{"seq":1,"category":"instruction-text","text":"Check the vessel is empty."},
		{"seq":2,"category":"process-parameter-setpoint","node":%q,"value":%v,"characteristic":{"name":"Temperature","type":"numeric","unit":"degC","min":60,"max":90}}]}]}]}]}}}`,
		recipeOrder, recipeResource, dest, extra, recipeNode, value)
}

// Generating needs the create duty, validates destination and structure before the store, and answers with
// field paths; the monitor filters validate; kiosk reports need the execute duty.
func TestControlRecipeRoutes_DutiesAndValidation(t *testing.T) {
	h := newRouter(&fakeStore{})
	path := "/v1/shopfloor/control-recipes"
	if rec := call(t, h, http.MethodPost, path, recipeBody("kiosk", "", "", 72), DutyView); rec.Code != http.StatusForbidden {
		t.Fatalf("without the create duty: %d", rec.Code)
	}
	if rec := call(t, h, http.MethodPost, path, recipeBody("kiosk", "", "", 72), DutyControlRecipeCreate); rec.Code != http.StatusCreated {
		t.Fatalf("kiosk recipe: %d %s", rec.Code, rec.Body)
	}
	if rec := call(t, h, http.MethodPost, path, recipeBody("pcs-opcua", edgeID, "", 72), DutyControlRecipeCreate); rec.Code != http.StatusCreated {
		t.Fatalf("pcs-opcua recipe: %d %s", rec.Code, rec.Body)
	}
	for name, tc := range map[string]struct{ body, field string }{
		"setpoint out of limits": {recipeBody("pcs-opcua", edgeID, "", 99), "value"},
		"pcs without an edge":    {recipeBody("pcs-opcua", "", "", 72), "edgeId"},
		"mqtt without a topic":   {recipeBody("pcs-mqtt", edgeID, "", 72), "topic"},
		"kiosk with an edge":     {recipeBody("kiosk", edgeID, "", 72), "destination"},
		"unknown destination":    {recipeBody("fax", "", "", 72), "destination"},
	} {
		rec := call(t, h, http.MethodPost, path, tc.body, DutyControlRecipeCreate)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), tc.field) {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body)
		}
	}
	if rec := call(t, h, http.MethodGet, path+"?status=nope", "", DutyView); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad status filter: %d", rec.Code)
	}
	if rec := call(t, h, http.MethodGet, path+"?status=error&destination=kiosk", "", DutyView); rec.Code != http.StatusOK {
		t.Fatalf("list: %d", rec.Code)
	}
	report := path + "/" + edgeID + ":report"
	if rec := call(t, h, http.MethodPost, report, `{"status":"received"}`, DutyControlRecipeCreate); rec.Code != http.StatusForbidden {
		t.Fatalf("report with the create duty: %d", rec.Code)
	}
	if rec := call(t, h, http.MethodPost, report, `{"status":"error"}`, DutyControlRecipeExecute); rec.Code != http.StatusBadRequest {
		t.Fatalf("error without a message: %d %s", rec.Code, rec.Body)
	}
	if rec := call(t, h, http.MethodPost, report, `{"status":"completed"}`, DutyControlRecipeExecute); rec.Code != http.StatusOK {
		t.Fatalf("report: %d %s", rec.Code, rec.Body)
	}
	edgePath := "/v1/shopfloor/edge-nodes/" + edgeID + "/control-recipes"
	if rec := call(t, h, http.MethodGet, edgePath, "", DutyView); rec.Code != http.StatusForbidden {
		t.Fatalf("edge pull with the view duty: %d", rec.Code)
	}
	if rec := call(t, h, http.MethodGet, edgePath, "", DutyEdgeConnect); rec.Code != http.StatusOK {
		t.Fatalf("edge pull: %d", rec.Code)
	}
	if rec := call(t, h, http.MethodPost, edgePath+"/"+edgeID+":report", `{}`, DutyEdgeConnect); rec.Code != http.StatusBadRequest {
		t.Fatalf("empty edge report: %d", rec.Code)
	}
	if rec := call(t, h, http.MethodPost, edgePath+"/"+edgeID+":report", `{"status":"in-execution"}`, DutyEdgeConnect); rec.Code != http.StatusOK {
		t.Fatalf("edge report: %d %s", rec.Code, rec.Body)
	}
}

// End to end over Postgres, AC-25.11 and BR-25.11/BR-25.15: a kiosk recipe is reported through its
// lifecycle; a pcs-opcua recipe is pulled by the bound connector, executed, its setpoint written and read
// back with a deviation recorded; a recipe in error is resent five times, then refused, and with process
// messages for the order a resend needs force and a reason.
func TestHTTP_ControlRecipeLifecycleWriteAuditAndResend(t *testing.T) {
	admin, app := testkit.DBWithApp(t)
	if err := dbx.Migrate(context.Background(), admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	h := fullRouter(New(store.New(app)))
	calls := 0
	send := func(method, path, body, duties, user string) *httptest.ResponseRecorder {
		calls++
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("X-Passage-Tenant", testkit.TenantID(1))
		req.Header.Set("X-Passage-User", user)
		req.Header.Set("X-Passage-Duties", duties)
		req.Header.Set("Idempotency-Key", fmt.Sprintf("call-%d", calls))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	sup := func(method, path, body string) *httptest.ResponseRecorder {
		return send(method, path, body, DutyControlRecipeCreate+","+DutyView+","+DutyEdgeConfigure, "supervisor-1")
	}
	decode := func(rec *httptest.ResponseRecorder, v any) {
		t.Helper()
		if err := json.Unmarshal(rec.Body.Bytes(), v); err != nil {
			t.Fatalf("%s: %v", rec.Body, err)
		}
	}

	// An edge node bound to the connector identity edge-7.
	var node store.EdgeNode
	rec := sup(http.MethodPost, "/v1/shopfloor/edge-nodes", `{"name":"Reactor gateway","platform":"k3s","connectorSubject":"edge-7"}`)
	decode(rec, &node)
	if rec.Code != http.StatusCreated {
		t.Fatalf("edge node: %d %s", rec.Code, rec.Body)
	}
	edgeBase := "/v1/shopfloor/edge-nodes/" + node.ID + "/control-recipes"
	connector := func(method, path, body string) *httptest.ResponseRecorder {
		return send(method, path, body, DutyEdgeConnect, "edge-7")
	}

	// A kiosk recipe: sent, then reported received, in-execution, completed; skipping a step is 409.
	var kiosk store.ControlRecipe
	rec = sup(http.MethodPost, "/v1/shopfloor/control-recipes", recipeBody("kiosk", "", "", 72))
	decode(rec, &kiosk)
	if rec.Code != http.StatusCreated || kiosk.Status != "sent" || kiosk.SendCount != 1 || kiosk.EdgeID != nil {
		t.Fatalf("kiosk recipe: %d %s", rec.Code, rec.Body)
	}
	kioskReport := func(status string) *httptest.ResponseRecorder {
		return send(http.MethodPost, "/v1/shopfloor/control-recipes/"+kiosk.ID+":report", `{"status":"`+status+`"}`, DutyControlRecipeExecute, "operator-1")
	}
	if rec := kioskReport("completed"); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), store.CodeRecipeTransition) {
		t.Fatalf("sent to completed: %d %s", rec.Code, rec.Body)
	}
	for _, s := range []string{"received", "in-execution", "completed"} {
		if rec := kioskReport(s); rec.Code != http.StatusOK {
			t.Fatalf("report %s: %d %s", s, rec.Code, rec.Body)
		}
	}
	if rec := kioskReport("completed"); rec.Code != http.StatusOK {
		t.Fatalf("repeating the last report: %d", rec.Code)
	}

	// A recipe for a process control system is not reported from the kiosk, and needs an edge node.
	var pcs store.ControlRecipe
	if rec := sup(http.MethodPost, "/v1/shopfloor/control-recipes", recipeBody("pcs-opcua", "0195f0a0-0000-7000-8000-00000000ffff", "", 72)); rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), store.CodeRecipeDestination) {
		t.Fatalf("recipe for an unknown edge node: %d %s", rec.Code, rec.Body)
	}
	rec = sup(http.MethodPost, "/v1/shopfloor/control-recipes", recipeBody("pcs-opcua", node.ID, "", 72))
	decode(rec, &pcs)
	if rec.Code != http.StatusCreated {
		t.Fatalf("pcs recipe: %d %s", rec.Code, rec.Body)
	}
	if rec := send(http.MethodPost, "/v1/shopfloor/control-recipes/"+pcs.ID+":report", `{"status":"received"}`, DutyControlRecipeExecute, "operator-1"); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("kiosk report on a pcs recipe: %d %s", rec.Code, rec.Body)
	}

	// Only the bound connector pulls it.
	if rec := send(http.MethodGet, edgeBase, "", DutyEdgeConnect, "someone-else"); rec.Code != http.StatusForbidden {
		t.Fatalf("pull as another identity: %d", rec.Code)
	}
	var pulled struct{ Items []store.ControlRecipe }
	rec = connector(http.MethodGet, edgeBase, "")
	decode(rec, &pulled)
	if rec.Code != http.StatusOK || len(pulled.Items) != 1 || pulled.Items[0].ID != pcs.ID {
		t.Fatalf("pull: %d %s", rec.Code, rec.Body)
	}
	report := func(body string) *httptest.ResponseRecorder {
		return connector(http.MethodPost, edgeBase+"/"+pcs.ID+":report", body)
	}
	phase := "Batch/Reactor/Heat/Ramp"
	write := func(value, readback string, written bool) string {
		rb := ""
		if readback != "" {
			rb = `,"readback":` + readback
		}
		return `{"writes":[{"phasePath":"` + phase + `","node":"` + recipeNode + `","value":` + value + rb + `,"written":` + fmt.Sprint(written) + `}]}`
	}
	// BR-25.15: no write before the recipe is in-execution.
	if rec := report(write("72", "72", true)); rec.Code != http.StatusConflict {
		t.Fatalf("write before execution: %d %s", rec.Code, rec.Body)
	}
	for _, s := range []string{"received", "in-execution"} {
		if rec := report(`{"status":"` + s + `"}`); rec.Code != http.StatusOK {
			t.Fatalf("edge %s: %d %s", s, rec.Code, rec.Body)
		}
	}
	if rec := report(write("73", "73", true)); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("write of a value the recipe does not ask for: %d %s", rec.Code, rec.Body)
	}
	if rec := report(write("72", "72.0004", true)); rec.Code != http.StatusOK {
		t.Fatalf("write within tolerance: %d %s", rec.Code, rec.Body)
	}
	if rec := report(write("72", "70", true)); rec.Code != http.StatusOK {
		t.Fatalf("write with a deviating readback: %d %s", rec.Code, rec.Body)
	}
	if rec := report(write("72", "", false)); rec.Code != http.StatusOK {
		t.Fatalf("a refused write is audited too: %d %s", rec.Code, rec.Body)
	}
	var detail store.ControlRecipeDetail
	rec = sup(http.MethodGet, "/v1/shopfloor/control-recipes/"+pcs.ID, "")
	decode(rec, &detail)
	deviations := 0
	for _, w := range detail.Writes {
		if w.Deviation {
			deviations++
		}
	}
	if detail.Status != "in-execution" || len(detail.Writes) != 3 || deviations != 1 || len(detail.History) != 3 {
		t.Fatalf("detail: status %s writes %d deviations %d history %d", detail.Status, len(detail.Writes), deviations, len(detail.History))
	}
	var devEvents int
	if err := admin.QueryRow(context.Background(), `select count(*) from outbox where type like '%controlrecipe.deviation%'`).Scan(&devEvents); err != nil || devEvents != 1 {
		t.Fatalf("deviation events = %d, %v", devEvents, err)
	}
	if rec := report(`{"status":"completed"}`); rec.Code != http.StatusOK {
		t.Fatalf("edge completed: %d %s", rec.Code, rec.Body)
	}
	if rec := connector(http.MethodGet, edgeBase, ""); !strings.Contains(rec.Body.String(), `"items":[]`) {
		t.Fatalf("a completed recipe is still pulled: %s", rec.Body)
	}

	// Monitor filters.
	var list struct{ Items []store.ControlRecipe }
	rec = sup(http.MethodGet, "/v1/shopfloor/control-recipes?status=completed&destination=kiosk", "")
	decode(rec, &list)
	if len(list.Items) != 1 || list.Items[0].ID != kiosk.ID {
		t.Fatalf("monitor: %s", rec.Body)
	}

	// BR-25.11: a recipe in error is resent up to five times.
	var failing store.ControlRecipe
	rec = sup(http.MethodPost, "/v1/shopfloor/control-recipes", recipeBody("kiosk", "", "", 71))
	decode(rec, &failing)
	if rec := send(http.MethodPost, "/v1/shopfloor/control-recipes/"+failing.ID+":resend", `{}`, DutyControlRecipeCreate, "supervisor-1"); rec.Code != http.StatusConflict {
		t.Fatalf("resend of a recipe not in error: %d", rec.Code)
	}
	fail := func() {
		if rec := send(http.MethodPost, "/v1/shopfloor/control-recipes/"+failing.ID+":report", `{"status":"error","error":"printer offline"}`, DutyControlRecipeExecute, "operator-1"); rec.Code != http.StatusOK {
			t.Fatalf("error report: %d %s", rec.Code, rec.Body)
		}
	}
	resend := func(body string) *httptest.ResponseRecorder {
		return send(http.MethodPost, "/v1/shopfloor/control-recipes/"+failing.ID+":resend", body, DutyControlRecipeCreate, "supervisor-1")
	}
	for i := 2; i <= 6; i++ {
		fail()
		var out store.ControlRecipe
		rec := resend(fmt.Sprintf(`{"reason":"attempt %d"}`, i))
		decode(rec, &out)
		if rec.Code != http.StatusOK || out.Status != "sent" || out.SendCount != i || out.LastError != nil {
			t.Fatalf("resend %d: %d %s", i, rec.Code, rec.Body)
		}
	}
	fail()
	if rec := resend(`{"reason":"one more"}`); rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), store.CodeResendLimit) {
		t.Fatalf("sixth resend: %d %s", rec.Code, rec.Body)
	}

	// With a process message for the order, a resend needs force and a reason.
	var forced store.ControlRecipe
	other := strings.Replace(recipeBody("kiosk", "", "", 70), recipeOrder, "0195f0a0-0000-7000-8000-0000000000c9", 1)
	rec = sup(http.MethodPost, "/v1/shopfloor/control-recipes", other)
	decode(rec, &forced)
	if _, err := admin.Exec(context.Background(), `insert into process_message (tenant_id, id, source, source_message_id, category_code, order_id, occurred_at, status)
		values ($1, '0195f0a0-0000-7000-8000-0000000000d1', 'edge', 'pcs-1', 'phase-start', '0195f0a0-0000-7000-8000-0000000000c9', now(), 'received')`, testkit.TenantID(1)); err != nil {
		t.Fatal(err)
	}
	send(http.MethodPost, "/v1/shopfloor/control-recipes/"+forced.ID+":report", `{"status":"error","error":"lost"}`, DutyControlRecipeExecute, "operator-1")
	resendForced := func(body string) *httptest.ResponseRecorder {
		return send(http.MethodPost, "/v1/shopfloor/control-recipes/"+forced.ID+":resend", body, DutyControlRecipeCreate, "supervisor-1")
	}
	if rec := resendForced(`{}`); rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), store.CodeResendForce) {
		t.Fatalf("resend after messages without force: %d %s", rec.Code, rec.Body)
	}
	if rec := resendForced(`{"force":true,"reason":"PCS confirmed it never started"}`); rec.Code != http.StatusOK {
		t.Fatalf("forced resend: %d %s", rec.Code, rec.Body)
	}
	rec = sup(http.MethodGet, "/v1/shopfloor/control-recipes/"+forced.ID, "")
	decode(rec, &detail)
	last := detail.History[len(detail.History)-1]
	if !last.Forced || last.Note == nil || !strings.Contains(*last.Note, "never started") || last.ToStatus != "sent" {
		t.Fatalf("forced resend history = %+v", last)
	}
}
