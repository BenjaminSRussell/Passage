package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/BenjaminSRussell/passage/services/iot-edge/internal/domain"
)

// fakeRecipes is the shopfloor side of the control recipe API as the connector sees it.
type fakeRecipes struct {
	mu      sync.Mutex
	recipes []Recipe
	reports []string // "<id>:<status>" in order
	writes  []WriteReport
	failing bool
}

func (f *fakeRecipes) PullRecipes(context.Context) ([]Recipe, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failing {
		return nil, fmt.Errorf("server down")
	}
	return append([]Recipe(nil), f.recipes...), nil
}

func (f *fakeRecipes) ReportRecipe(_ context.Context, id string, rep RecipeReport) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reports = append(f.reports, id+":"+rep.Status)
	f.writes = append(f.writes, rep.Writes...)
	for i := range f.recipes {
		if f.recipes[i].ID == id && rep.Status != "" {
			f.recipes[i].Status = rep.Status
		}
	}
	return nil
}

const node = "ns=2;s=Vessel1.TempSP"

// recipeRuntime applies a configuration whose write allowlist holds node within 60..90.
func recipeRuntime(t *testing.T, rec *fakeRecipes, w *SimWriter, auto bool) *Runtime {
	t.Helper()
	r, _ := simRuntime(t, newServer(), 1, Options{Recipes: rec, Writer: w, AutoComplete: auto})
	cfg, _ := SimConfig(2, 1, func(int) string { return resA })
	cfg.WriteRules = []domain.WriteRule{{Node: node, Min: 60, Max: 90, Unit: "degC"}}
	if _, err := r.Apply(cfg); err != nil {
		t.Fatal(err)
	}
	return r
}

func sentRecipe(value float64, n string) Recipe {
	return Recipe{ID: "r1", OrderID: "o1", Destination: "pcs-opcua", Status: "sent",
		Setpoints: []Setpoint{{PhasePath: "Batch/Reactor/Heat/Ramp", Node: n, Value: value}}}
}

// AC-25.11: a sent recipe with a setpoint of 72.0 on an allowlisted node is acknowledged, started, written
// once, read back and left in-execution; the next poll does not write it again.
func TestExecuteRecipes_WritesTheSetpointOnceAndLeavesItInExecution(t *testing.T) {
	rec := &fakeRecipes{recipes: []Recipe{sentRecipe(72, node)}}
	w := &SimWriter{}
	r := recipeRuntime(t, rec, w, false)
	if n, err := r.ExecuteRecipes(context.Background()); err != nil || n != 1 {
		t.Fatalf("execute = %d, %v", n, err)
	}
	if got := strings.Join(rec.reports, " "); got != "r1:received r1:in-execution r1:" {
		t.Fatalf("reports = %q", got)
	}
	if len(rec.writes) != 1 || !rec.writes[0].Written || rec.writes[0].Readback == nil || *rec.writes[0].Readback != 72 || rec.writes[0].PhasePath != "Batch/Reactor/Heat/Ramp" {
		t.Fatalf("write report = %+v", rec.writes)
	}
	if fmt.Sprint(w.WriteLog()) != "["+node+"=72]" {
		t.Fatalf("written = %v", w.WriteLog())
	}
	if n, _ := r.ExecuteRecipes(context.Background()); n != 0 || len(w.WriteLog()) != 1 {
		t.Fatalf("second poll wrote again: advanced %d, writes %v", n, w.WriteLog())
	}
	if s := r.Stats(); s.RecipesExecuted != 1 || s.ReadbackDeviations != 0 {
		t.Fatalf("stats = %+v", s)
	}
}

// BR-25.15: a node off the allowlist or a value outside its limits is refused, reported and never written;
// the recipe goes to error.
func TestExecuteRecipes_RefusesWhatTheAllowlistForbids(t *testing.T) {
	for name, sp := range map[string]Setpoint{
		"node not on the allowlist": {PhasePath: "p", Node: "ns=2;s=Other", Value: 70},
		"value outside the limits":  {PhasePath: "p", Node: node, Value: 95},
	} {
		rec := &fakeRecipes{recipes: []Recipe{{ID: "r1", Destination: "pcs-opcua", Status: "sent", Setpoints: []Setpoint{sp}}}}
		w := &SimWriter{}
		r := recipeRuntime(t, rec, w, true)
		if _, err := r.ExecuteRecipes(context.Background()); err != nil {
			t.Fatal(err)
		}
		if len(w.WriteLog()) != 0 {
			t.Errorf("%s: wrote %v", name, w.WriteLog())
		}
		if len(rec.writes) != 1 || rec.writes[0].Written || !strings.Contains(rec.writes[0].Reason, "SF-0017") {
			t.Errorf("%s: write report = %+v", name, rec.writes)
		}
		if got := rec.reports[len(rec.reports)-1]; got != "r1:error" {
			t.Errorf("%s: last report %q", name, got)
		}
	}
}

// A readback that differs is reported and counted as a deviation; the recipe stays in-execution (or
// completes under AutoComplete) because the write itself succeeded.
func TestExecuteRecipes_ReportsAReadbackDeviation(t *testing.T) {
	rec := &fakeRecipes{recipes: []Recipe{sentRecipe(72, node)}}
	w := &SimWriter{Drift: map[string]float64{node: -2}}
	r := recipeRuntime(t, rec, w, true)
	if _, err := r.ExecuteRecipes(context.Background()); err != nil {
		t.Fatal(err)
	}
	if rb := rec.writes[0].Readback; rb == nil || *rb != 70 || r.Stats().ReadbackDeviations != 1 {
		t.Fatalf("readback %v stats %+v", rec.writes[0].Readback, r.Stats())
	}
	if got := rec.reports[len(rec.reports)-1]; got != "r1:completed" {
		t.Fatalf("last report %q", got)
	}
}

// A failing PCS write is an error report with the reason; an mqtt recipe is an error (no publisher); a
// recipe already in-execution after a restart is written again; a server outage is an error and changes nothing.
func TestExecuteRecipes_FailuresAndRestart(t *testing.T) {
	rec := &fakeRecipes{recipes: []Recipe{sentRecipe(72, node)}}
	w := &SimWriter{Fail: map[string]bool{node: true}}
	r := recipeRuntime(t, rec, w, false)
	_, _ = r.ExecuteRecipes(context.Background())
	if rec.writes[0].Written || !strings.Contains(rec.writes[0].Reason, "write failed") || rec.reports[len(rec.reports)-1] != "r1:error" {
		t.Fatalf("failed write: %+v %v", rec.writes, rec.reports)
	}

	mq := &fakeRecipes{recipes: []Recipe{{ID: "m1", Destination: "pcs-mqtt", Status: "sent"}}}
	r2 := recipeRuntime(t, mq, &SimWriter{}, false)
	_, _ = r2.ExecuteRecipes(context.Background())
	if len(mq.reports) != 1 || mq.reports[0] != "m1:error" {
		t.Fatalf("mqtt recipe reports = %v", mq.reports)
	}

	again := &fakeRecipes{recipes: []Recipe{{ID: "r9", Destination: "pcs-opcua", Status: "in-execution", Setpoints: sentRecipe(72, node).Setpoints}}}
	w3 := &SimWriter{}
	r3 := recipeRuntime(t, again, w3, false)
	if n, _ := r3.ExecuteRecipes(context.Background()); n != 1 || len(w3.WriteLog()) != 1 || again.reports[0] != "r9:" {
		t.Fatalf("after a restart: advanced %d writes %v reports %v", n, w3.WriteLog(), again.reports)
	}

	down := &fakeRecipes{failing: true}
	if _, err := recipeRuntime(t, down, &SimWriter{}, false).ExecuteRecipes(context.Background()); err == nil {
		t.Fatal("server outage not reported")
	}
}

func TestSetpointsOfAndClientMapping(t *testing.T) {
	doc := `{"procedure":{"name":"Batch","unitProcedures":[{"name":"Reactor","operations":[{"name":"Heat","phases":[{"name":"Ramp","instructions":[
		{"seq":1,"category":"instruction-text","text":"x"},
		{"seq":2,"category":"process-parameter-setpoint","node":"` + node + `","value":72}]}]}]}]}}`
	var reported map[string]any
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/shopfloor/edge-nodes/{id}/control-recipes", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"items":[{"id":"r1","orderId":"o1","destination":"pcs-opcua","status":"sent","recipe":` + doc + `}]}`))
	})
	mux.HandleFunc("POST /v1/shopfloor/edge-nodes/{id}/control-recipes/{rid}", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&reported)
		_, _ = w.Write([]byte(`{}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	c := &Client{BaseURL: srv.URL, EdgeID: "e1"}
	got, err := c.PullRecipes(context.Background())
	if err != nil || len(got) != 1 || len(got[0].Setpoints) != 1 || got[0].Setpoints[0] != (Setpoint{"Batch/Reactor/Heat/Ramp", node, 72}) {
		t.Fatalf("pull = %+v, %v", got, err)
	}
	back := 72.0
	if err := c.ReportRecipe(context.Background(), "r1:x", RecipeReport{Status: "in-execution", Writes: []WriteReport{{PhasePath: "p", Node: node, Value: 72, Readback: &back, Written: true}}}); err != nil {
		t.Fatal(err)
	}
	w := reported["writes"].([]any)[0].(map[string]any)
	if reported["status"] != "in-execution" || w["node"] != node || w["readback"] != 72.0 || w["written"] != true {
		t.Fatalf("report body = %v", reported)
	}
}
