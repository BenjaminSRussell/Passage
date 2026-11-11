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
	"github.com/BenjaminSRussell/passage/services/shopfloor/internal/store"
	"github.com/BenjaminSRussell/passage/services/shopfloor/migrations"
)

// recipeEnv is a migrated database, the full router and an edge node bound to the connector edge-7.
type recipeEnv struct {
	t     *testing.T
	h     http.Handler
	calls int
	node  store.EdgeNode
	db    func(query string, dest ...any)
}

func newRecipeEnv(t *testing.T) *recipeEnv {
	t.Helper()
	admin, app := testkit.DBWithApp(t)
	if err := dbx.Migrate(context.Background(), admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	e := &recipeEnv{t: t, h: fullRouter(New(store.New(app)))}
	e.db = func(query string, dest ...any) {
		t.Helper()
		if err := admin.QueryRow(context.Background(), query).Scan(dest...); err != nil {
			t.Fatalf("%s: %v", query, err)
		}
	}
	rec := e.send(http.MethodPost, "/v1/shopfloor/edge-nodes", `{"name":"Reactor gateway","platform":"k3s","connectorSubject":"edge-7"}`,
		DutyControlRecipeCreate+","+DutyView+","+DutyEdgeConfigure, "supervisor-1")
	if rec.Code != http.StatusCreated {
		t.Fatalf("edge node: %d %s", rec.Code, rec.Body)
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &e.node)
	return e
}

func (e *recipeEnv) send(method, path, body, duties, user string) *httptest.ResponseRecorder {
	e.calls++
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("X-Passage-Tenant", testkit.TenantID(1))
	req.Header.Set("X-Passage-User", user)
	req.Header.Set("X-Passage-Duties", duties)
	req.Header.Set("Idempotency-Key", fmt.Sprintf("call-%d", e.calls))
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	return rec
}

// start creates a recipe for the destination and moves it to in-execution through the connector.
func (e *recipeEnv) start(dest, topic string) store.ControlRecipe {
	e.t.Helper()
	rec := e.send(http.MethodPost, "/v1/shopfloor/control-recipes", recipeBody(dest, e.node.ID, topic, 72), DutyControlRecipeCreate, "supervisor-1")
	var c store.ControlRecipe
	_ = json.Unmarshal(rec.Body.Bytes(), &c)
	if rec.Code != http.StatusCreated {
		e.t.Fatalf("recipe: %d %s", rec.Code, rec.Body)
	}
	for _, s := range []string{"received", "in-execution"} {
		if r := e.report(c.ID, `{"status":"`+s+`"}`); r.Code != http.StatusOK {
			e.t.Fatalf("edge %s: %d %s", s, r.Code, r.Body)
		}
	}
	return c
}

func (e *recipeEnv) report(id, body string) *httptest.ResponseRecorder {
	return e.send(http.MethodPost, "/v1/shopfloor/edge-nodes/"+e.node.ID+"/control-recipes/"+id+":report", body, DutyEdgeConnect, "edge-7")
}

func (e *recipeEnv) message(body string) *httptest.ResponseRecorder {
	return e.send(http.MethodPost, "/v1/shopfloor/process-messages", body, processDuty, "pcs-gateway")
}

func (e *recipeEnv) status(id string) (status string) {
	e.t.Helper()
	e.db(`select status from control_recipe where id = '`+id+`'`, &status)
	return status
}

func orderMessage(sourceID, status, reason string) string {
	vals := fmt.Sprintf(`{"name":"status","type":"text","value":%q}`, status)
	if reason != "" {
		vals += fmt.Sprintf(`,{"name":"message","type":"text","value":%q}`, reason)
	}
	return fmt.Sprintf(`{"source":"edge","sourceMessageId":%q,"categoryCode":"order-status","orderId":%q,"occurredAt":"2026-10-20T09:00:00Z","values":[%s]}`, sourceID, recipeOrder, vals)
}

// 25-5: the process control system's own messages end the recipe it runs. An order-status message completes
// it (or sends it to error with the reason), the last phase's end completes it, and anything else leaves it
// alone. A repeated message changes nothing; a kiosk recipe is never ended this way.
func TestProcessMessagesEndControlRecipes(t *testing.T) {
	e := newRecipeEnv(t)

	completed := e.start("pcs-opcua", "")
	if rec := e.message(fmt.Sprintf(`{"source":"edge","sourceMessageId":"m1","categoryCode":"phase-end","orderId":%q,"phasePath":"Batch/Reactor/Heat/Other","occurredAt":"2026-10-20T09:00:00Z","values":[]}`, recipeOrder)); rec.Code != http.StatusCreated {
		t.Fatalf("phase-end message: %d %s", rec.Code, rec.Body)
	}
	if got := e.status(completed.ID); got != "in-execution" {
		t.Fatalf("a phase-end for another phase ended the recipe: %s", got)
	}
	if rec := e.message(orderMessage("m2", "running", "")); rec.Code != http.StatusCreated {
		t.Fatalf("running message: %d %s", rec.Code, rec.Body)
	}
	if got := e.status(completed.ID); got != "in-execution" {
		t.Fatalf("a running status ended the recipe: %s", got)
	}
	if rec := e.message(orderMessage("m3", "completed", "")); rec.Code != http.StatusCreated {
		t.Fatalf("completed message: %d %s", rec.Code, rec.Body)
	}
	if got := e.status(completed.ID); got != "completed" {
		t.Fatalf("status after the completed message: %s", got)
	}
	var actor string
	e.db(`select actor from control_recipe_event where recipe_id = '`+completed.ID+`' order by at desc, id desc limit 1`, &actor)
	if !strings.HasPrefix(actor, "process-message:") {
		t.Fatalf("the history names %q, want the process message", actor)
	}
	// The same message again changes nothing and does not fail.
	if rec := e.message(orderMessage("m3", "completed", "")); rec.Code != http.StatusOK && rec.Code != http.StatusCreated {
		t.Fatalf("replayed message: %d %s", rec.Code, rec.Body)
	}

	// An aborted batch sends the recipe to error with the reason the message gave.
	aborted := e.start("pcs-opcua", "")
	if rec := e.message(orderMessage("m4", "aborted", "reactor tripped on high pressure")); rec.Code != http.StatusCreated {
		t.Fatalf("aborted message: %d %s", rec.Code, rec.Body)
	}
	var lastErr string
	e.db(`select status || '|' || coalesce(last_error, '') from control_recipe where id = '`+aborted.ID+`'`, &lastErr)
	if lastErr != "error|reactor tripped on high pressure" {
		t.Fatalf("aborted recipe: %q", lastErr)
	}

	// The end of the last phase completes the recipe.
	last := e.start("pcs-mqtt", "plant/reactor1/recipe")
	if rec := e.message(fmt.Sprintf(`{"source":"edge","sourceMessageId":"m5","categoryCode":"phase-end","orderId":%q,"phasePath":"Batch/Reactor/Heat/Ramp","occurredAt":"2026-10-20T09:00:00Z","values":[]}`, recipeOrder)); rec.Code != http.StatusCreated {
		t.Fatalf("phase-end message: %d %s", rec.Code, rec.Body)
	}
	if got := e.status(last.ID); got != "completed" {
		t.Fatalf("status after the last phase ended: %s", got)
	}

	// A kiosk recipe of the same order is not ended by a process control system's message.
	var kiosk store.ControlRecipe
	rec := e.send(http.MethodPost, "/v1/shopfloor/control-recipes", recipeBody("kiosk", "", "", 72), DutyControlRecipeCreate, "supervisor-1")
	_ = json.Unmarshal(rec.Body.Bytes(), &kiosk)
	for _, s := range []string{"received", "in-execution"} {
		e.send(http.MethodPost, "/v1/shopfloor/control-recipes/"+kiosk.ID+":report", `{"status":"`+s+`"}`, DutyControlRecipeExecute, "operator-1")
	}
	e.message(orderMessage("m6", "completed", ""))
	if got := e.status(kiosk.ID); got != "in-execution" {
		t.Fatalf("a kiosk recipe was ended by a message: %s", got)
	}
}

// A setpoint whose readback differs becomes a deviation process message of the order, once. MQTT has no
// readback, and a write without one is then no deviation; for OPC-UA a missing readback is one.
func TestSetpointDeviationBecomesAProcessMessage(t *testing.T) {
	e := newRecipeEnv(t)
	opc := e.start("pcs-opcua", "")
	phase := "Batch/Reactor/Heat/Ramp"
	write := func(readback string) string {
		rb := ""
		if readback != "" {
			rb = `,"readback":` + readback
		}
		return `{"writes":[{"phasePath":"` + phase + `","node":"` + recipeNode + `","value":72` + rb + `,"written":true,"at":"2026-10-20T09:00:00Z"}]}`
	}
	if r := e.report(opc.ID, write("72")); r.Code != http.StatusOK {
		t.Fatalf("good write: %d %s", r.Code, r.Body)
	}
	var n int
	e.db(`select count(*) from process_message where category_code = 'deviation'`, &n)
	if n != 0 {
		t.Fatalf("a write that read back fine made %d deviation messages", n)
	}
	if r := e.report(opc.ID, write("70")); r.Code != http.StatusOK {
		t.Fatalf("deviating write: %d %s", r.Code, r.Body)
	}
	if r := e.report(opc.ID, write("70")); r.Code != http.StatusOK { // the connector repeats its report
		t.Fatalf("repeated report: %d %s", r.Code, r.Body)
	}
	var info string
	e.db(`select count(*)::text || '|' || min(order_id::text) || '|' || min(phase_path) || '|' || min(source) from process_message where category_code = 'deviation'`, &info)
	if info != "1|"+recipeOrder+"|"+phase+"|edge" {
		t.Fatalf("deviation messages: %q, want one for the order and phase", info)
	}
	var dest int
	e.db(`select count(*) from process_message_delivery d join process_message m on m.id = d.message_id where m.category_code = 'deviation'`, &dest)
	if dest != 4 { // ebr, quality, kafka, webhook
		t.Fatalf("the deviation message has %d deliveries, want the category's 4", dest)
	}
	if r := e.report(opc.ID, write("")); r.Code != http.StatusOK {
		t.Fatalf("write without readback: %d %s", r.Code, r.Body)
	}
	e.db(`select count(*) from edge_write_audit where recipe_id = '`+opc.ID+`' and deviation`, &n)
	if n != 3 {
		t.Fatalf("OPC-UA deviations: %d, want the differing readback (twice audited) and the missing one", n)
	}

	mq := e.start("pcs-mqtt", "plant/reactor1/recipe")
	if r := e.report(mq.ID, write("")); r.Code != http.StatusOK {
		t.Fatalf("mqtt write: %d %s", r.Code, r.Body)
	}
	e.db(`select count(*) from edge_write_audit where recipe_id = '`+mq.ID+`' and deviation`, &n)
	if n != 0 {
		t.Fatalf("an MQTT write without a readback counted as a deviation (%d)", n)
	}
}
