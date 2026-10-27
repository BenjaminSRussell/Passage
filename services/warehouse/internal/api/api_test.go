package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/warehouse/internal/domain"
)

func dec(s string) decimal.Decimal { return decimal.RequireFromString(s) }

func newRouter(t *testing.T) http.Handler {
	t.Helper()
	r := httpx.NewRouter()
	New().Register(r)
	return r
}

func send(t *testing.T, h http.Handler, body []byte, duties string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, simulatePath, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(tenant.HeaderTenant, ids.New())
	if duties != "" {
		req.Header.Set("X-Passage-Duties", duties)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// bodyFor builds the BR-31.1 worked example: BOLT-M8, 1,200 EA, 14.4 kg, one LP.
func bodyFor(t *testing.T, wh string, locs []domain.Location) []byte {
	t.Helper()
	req := simulateRequest{
		Line: domain.Line{ItemID: "BOLT-M8", WorkType: "PURCHASE_PUTAWAY", Direction: domain.DirectionPut, Warehouse: wh,
			Qty: dec("1200"), Weight: dec("14.4"), LPs: 1, Attributes: map[string]string{"item_group": "FASTENERS"}},
		Directives: []domain.Directive{{ID: "DIR-10", Sequence: 10, WorkType: "PURCHASE_PUTAWAY", Direction: domain.DirectionPut, Warehouse: wh,
			Query: domain.Query{"item_group": "FASTENERS"},
			Bands: []domain.Band{{Seq: 3, FromQty: dec("1200"), Actions: []domain.Action{{Seq: 1, Strategy: domain.StrategyEmptyLocation, Zone: "RESERVE"}}}}}},
		Locations: locs,
	}
	b, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func reserveLocation() domain.Location {
	maxW := dec("14.4")
	return domain.Location{ID: "r1", Name: "R-05-02-01", Zone: "RESERVE", PickSequence: 5021, Status: domain.StatusActive,
		Capacity: domain.Capacity{MaxWeight: &maxW}}
}

func TestSimulate_ResolvesLocation(t *testing.T) {
	wh := ids.New()
	rec := send(t, newRouter(t), bodyFor(t, wh, []domain.Location{reserveLocation()}), dutySetupView)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	var got simulateResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Blocked || got.Location != "R-05-02-01" || got.Directive != "DIR-10" || len(got.Trace) == 0 {
		t.Errorf("unexpected response %+v", got)
	}
}

func TestSimulate_BlockedIsAnAnswerNotAnError(t *testing.T) {
	wh := ids.New()
	rec := send(t, newRouter(t), bodyFor(t, wh, nil), dutySetupView)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	var got simulateResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if !got.Blocked || got.Code != "WMS-0101" || got.LocationID != "" {
		t.Errorf("want blocked WMS-0101, got %+v", got)
	}
}

func TestSimulate_RequiresDuty(t *testing.T) {
	rec := send(t, newRouter(t), bodyFor(t, ids.New(), nil), "WMS_SETUP_VIEW")
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "AUTH-0003") {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
}

func TestSimulate_ValidationReportsFields(t *testing.T) {
	body := []byte(`{"line":{"itemId":"","workType":"X","direction":"PACK","warehouseId":"not-a-uuid","qty":"0"},
		"directives":[{"id":"D","workType":"X","direction":"PUT","warehouseId":"` + ids.New() + `","lines":[{"fromQty":"5","toQty":"5","actions":[{"strategy":"guess"}]}]}],
		"locations":[{"id":"l","name":"n","zone":"Z","status":"OPEN"}]}`)
	rec := send(t, newRouter(t), body, dutySetupView)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	for _, field := range []string{"line.itemId", "line.direction", "line.warehouseId", "line.qty",
		"directives[0].lines[0].toQty", "directives[0].lines[0].actions[0].strategy", "locations[0].status"} {
		if !strings.Contains(rec.Body.String(), `"`+field+`"`) {
			t.Errorf("missing field error for %s in %s", field, rec.Body)
		}
	}
}

func TestSimulate_RejectsUnknownFields(t *testing.T) {
	rec := send(t, newRouter(t), []byte(`{"line":{},"surprise":true}`), dutySetupView)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "COM-0001") {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
}

func TestMetrics_CountsSimulations(t *testing.T) {
	a := New()
	r := httpx.NewRouter()
	a.Register(r)
	send(t, r, bodyFor(t, ids.New(), nil), dutySetupView)
	rec := httptest.NewRecorder()
	a.Metrics().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	body := rec.Body.String()
	if !strings.Contains(body, "warehouse_directive_simulations_total 1") || !strings.Contains(body, "warehouse_directive_blocked_total 1") {
		t.Errorf("metrics = %s", body)
	}
}

func TestSimulate_RequiresTenant(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, simulatePath, bytes.NewReader(bodyFor(t, ids.New(), nil)))
	req.Header.Set("X-Passage-Duties", dutySetupView)
	rec := httptest.NewRecorder()
	newRouter(t).ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized || !strings.Contains(rec.Body.String(), "AUTH-0001") {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
}

func TestSimulate_RejectsInconsistentDirectivesAndLocations(t *testing.T) {
	wh := ids.New()
	body := []byte(`{"line":{"itemId":"BOLT-M8","workType":"PURCHASE_PUTAWAY","direction":"PUT","warehouseId":"` + wh + `","qty":"5"},
		"directives":[
		 {"id":"D","sequence":1,"workType":"PURCHASE_PUTAWAY","direction":"PUT","warehouseId":"` + wh + `","lines":[
		  {"seq":1,"fromQty":"0","toQty":"100","actions":[{"seq":1,"strategy":"consolidate"}]},
		  {"seq":2,"fromQty":"50","actions":[]}]},
		 {"id":"D","sequence":2,"workType":"PURCHASE_PUTAWAY","direction":"PUT","warehouseId":"` + wh + `","lines":[
		  {"seq":1,"fromQty":"0","actions":[{"seq":1,"strategy":"consolidate"}]}]}],
		"locations":[
		 {"id":"l","name":"A-01","zone":"Z","status":"ACTIVE","capacity":{"maxLps":-1},"used":{"weight":"-1"},"stock":{"BOLT-M8":"-2"}},
		 {"id":"l","name":"A-02","zone":"Z","status":"ACTIVE"}]}`)
	rec := send(t, newRouter(t), body, dutySetupView)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	var problem struct {
		Code   string            `json:"code"`
		Errors map[string]string `json:"errors"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &problem); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"directives[0].lines[1].fromQty": "overlaps the quantity range of lines[0]",
		"directives[0].lines[1].actions": "must hold at least one action",
		"directives[1].id":               "is used by another directive",
		"locations[0].capacity":          "limits must not be negative",
		"locations[0].used":              "must not be negative",
		"locations[0].stock":             "must map item ids to quantities of zero or more",
		"locations[1].id":                "is used by another location",
	}
	if problem.Code != "COM-0001" || len(problem.Errors) != len(want) {
		t.Fatalf("problem = %+v, want exactly %v", problem, want)
	}
	for field, msg := range want {
		if problem.Errors[field] != msg {
			t.Errorf("%s = %q, want %q", field, problem.Errors[field], msg)
		}
	}
}

func TestSimulate_RefusesDerivedItemCount(t *testing.T) {
	// used.items is derived from stock, so the request shape does not carry it.
	wh := ids.New()
	body := []byte(`{"line":{"itemId":"A","workType":"W","direction":"PUT","warehouseId":"` + wh + `","qty":"1"},"directives":[],
		"locations":[{"id":"l","name":"A-01","zone":"Z","status":"ACTIVE","used":{"items":3}}]}`)
	rec := send(t, newRouter(t), body, dutySetupView)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "COM-0001") {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
}

// scenarioFixture is testdata/directive-scenarios-100.json (AC-31.1).
type scenarioFixture struct {
	Line       map[string]any               `json:"line"`
	Directives []json.RawMessage            `json:"directives"`
	Locations  map[string]map[string]any    `json:"locations"`
	Stock      map[string]map[string]string `json:"stock"`
	Scenarios  []struct {
		Name   string `json:"name"`
		Qty    string `json:"qty"`
		Layout string `json:"layout"`
		Expect struct {
			Blocked   bool   `json:"blocked"`
			Location  string `json:"location"`
			Directive string `json:"directive"`
			Band      int    `json:"band"`
			Action    int    `json:"action"`
		} `json:"expect"`
	} `json:"scenarios"`
}

// TestSimulate_DirectiveFixture is AC-31.1: each of the 100 fixture lines is
// simulated through the endpoint, and the chosen location and the winning
// directive, line and action in the trace equal the fixture.
func TestSimulate_DirectiveFixture(t *testing.T) {
	raw, err := os.ReadFile("../../testdata/directive-scenarios-100.json")
	if err != nil {
		t.Fatal(err)
	}
	var fx scenarioFixture
	if err := json.Unmarshal(raw, &fx); err != nil {
		t.Fatal(err)
	}
	if len(fx.Scenarios) != 100 {
		t.Fatalf("fixture has %d scenarios, want 100", len(fx.Scenarios))
	}
	router := newRouter(t)
	for _, sc := range fx.Scenarios {
		t.Run(sc.Name, func(t *testing.T) {
			line := map[string]any{"qty": sc.Qty}
			for k, v := range fx.Line {
				line[k] = v
			}
			locations := []map[string]any{}
			for i, code := range []string{"F", "C", "R", "O"} {
				state := sc.Layout[i]
				if state == '-' {
					continue
				}
				l := map[string]any{}
				for k, v := range fx.Locations[code] {
					l[k] = v
				}
				if stock, ok := fx.Stock[string(state)]; ok {
					l["stock"] = stock
				}
				locations = append(locations, l)
			}
			body, err := json.Marshal(map[string]any{"line": line, "directives": fx.Directives, "locations": locations})
			if err != nil {
				t.Fatal(err)
			}
			rec := send(t, router, body, dutySetupView)
			if rec.Code != http.StatusOK {
				t.Fatalf("status %d: %s", rec.Code, rec.Body)
			}
			var got simulateResponse
			if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			want := sc.Expect
			if want.Blocked {
				if !got.Blocked || got.Code != domain.CodeNoLocation || got.Location != "" {
					t.Fatalf("want blocked WMS-0101, got %+v", got)
				}
				return
			}
			last := got.Trace[len(got.Trace)-1]
			if got.Blocked || got.Location != want.Location || got.Directive != want.Directive ||
				last.Outcome != "hit" || last.Directive != want.Directive || last.Band != want.Band || last.Action != want.Action {
				t.Fatalf("want %s via %s line %d action %d, got %+v", want.Location, want.Directive, want.Band, want.Action, got)
			}
		})
	}
}

var specOperation = regexp.MustCompile(`^    (get|put|post|patch|delete):$`)

// TestOpenAPIDocumentsEveryOperation checks that every served method and path
// is in api/openapi.yaml and that every business route names its duty.
func TestOpenAPIDocumentsEveryOperation(t *testing.T) {
	spec, err := os.ReadFile("../../api/openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	declared := map[string]string{}
	path, op := "", ""
	for _, line := range strings.Split(string(spec), "\n") {
		switch {
		case strings.HasPrefix(line, "  /") && strings.HasSuffix(line, ":"):
			path, op = strings.TrimSuffix(strings.TrimSpace(line), ":"), ""
		case specOperation.MatchString(line):
			op = strings.ToUpper(specOperation.FindStringSubmatch(line)[1]) + " " + path
			declared[op] = ""
		case op != "" && strings.HasPrefix(line, "      x-passage-duty: "):
			declared[op] = strings.TrimPrefix(line, "      x-passage-duty: ")
		}
	}
	served := 0
	router := httpx.NewRouter()
	New().Register(router)
	_ = chi.Walk(router, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		served++
		key := method + " " + route
		duty, ok := declared[key]
		switch {
		case !ok:
			t.Errorf("openapi.yaml does not document %s", key)
		case route == simulatePath && duty != dutySetupView:
			t.Errorf("%s x-passage-duty = %q, want %s", key, duty, dutySetupView)
		}
		delete(declared, key)
		return nil
	})
	if served == 0 {
		t.Fatal("router serves no routes")
	}
	for op := range declared {
		t.Errorf("openapi.yaml documents %s, which is not served", op)
	}
}
