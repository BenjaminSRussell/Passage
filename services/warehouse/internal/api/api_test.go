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
	"time"

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

func hazmatBody(t *testing.T, wh, rel string) []byte {
	t.Helper()
	oxen := map[string]decimal.Decimal{"OX-51": dec("10")}
	req := simulateRequest{
		Line: domain.Line{ItemID: "SOLV-3", WorkType: "PURCHASE_PUTAWAY", Direction: domain.DirectionPut, Warehouse: wh,
			Qty: dec("10"), Weight: dec("20")},
		Directives: []domain.Directive{{ID: "DIR-20", Sequence: 20, WorkType: "PURCHASE_PUTAWAY", Direction: domain.DirectionPut, Warehouse: wh,
			Bands: []domain.Band{{Seq: 1, FromQty: dec("0"), Actions: []domain.Action{{Seq: 1, Strategy: domain.StrategyEmptyLocation}}}}}},
		Locations: []domain.Location{
			{ID: "h1", Name: "H-01-01-01", Zone: "HAZ", PickSequence: 1, Status: domain.StatusActive, FireSection: "FCS-02", Stock: oxen, Used: domain.Usage{LPs: 1}},
			{ID: "h2", Name: "H-01-01-02", Zone: "HAZ", PickSequence: 2, Status: domain.StatusActive, FireSection: "FCS-02"},
			{ID: "h3", Name: "H-03-01-01", Zone: "HAZ", PickSequence: 30, Status: domain.StatusActive, FireSection: "FCS-01"},
		},
		Compliance: &complianceRequest{
			HazmatProfiles: []domain.HazmatProfile{{ItemID: "SOLV-3", StorageClass: "3"}, {ItemID: "OX-51", StorageClass: "5.1"}},
			Segregation:    []domain.SegregationPair{{A: "3", B: "5.1", Rel: domain.HazmatRel(rel)}},
		},
	}
	b, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestSimulate_HazmatComplianceExcludesIncompatibleSection(t *testing.T) {
	// AC-31.14 over HTTP: FCS-02 holds 5.1, so the class 3 line lands in FCS-01.
	rec := send(t, newRouter(t), hazmatBody(t, ids.New(), "prohibited"), dutySetupView)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	var got simulateResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Blocked || got.Location != "H-03-01-01" {
		t.Fatalf("want H-03-01-01, got %+v", got)
	}
	if !strings.Contains(rec.Body.String(), "hazmat: class 3 vs 5.1 prohibited in FCS-02") {
		t.Errorf("trace lacks the segregation reason: %s", rec.Body)
	}
}

func TestSimulate_InvalidComplianceIsValidationError(t *testing.T) {
	rec := send(t, newRouter(t), hazmatBody(t, ids.New(), "maybe"), dutySetupView)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), `"compliance"`) {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
}

func TestSimulate_WithoutComplianceIgnoresSegregation(t *testing.T) {
	// The compliance block is optional: without it the class 3 line takes the
	// first empty location, even in the section that holds class 5.1.
	var req map[string]any
	if err := json.Unmarshal(hazmatBody(t, ids.New(), "prohibited"), &req); err != nil {
		t.Fatal(err)
	}
	delete(req, "compliance")
	body, _ := json.Marshal(req)
	rec := send(t, newRouter(t), body, dutySetupView)
	var got simulateResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	if got.Location != "H-01-01-02" {
		t.Errorf("want H-01-01-02 without hazmat data, got %+v", got)
	}
}

func TestSimulate_RejectsBlankStorageClassOnLocation(t *testing.T) {
	wh := ids.New()
	body := []byte(`{"line":{"itemId":"A","workType":"W","direction":"PUT","warehouseId":"` + wh + `","qty":"1"},"directives":[],
		"locations":[{"id":"l","name":"A-01","zone":"Z","status":"ACTIVE","fireSection":"FCS-01","allowedStorageClasses":["3",""]}]}`)
	rec := send(t, newRouter(t), body, dutySetupView)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), `"locations[0].allowedStorageClasses"`) {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
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
	duties := map[string]string{
		"POST " + simulatePath:                                       dutySetupView,
		"POST /v1/warehouse/locations:bulk":                          dutySetupMaintain,
		"GET /v1/warehouse/locations":                                dutySetupView,
		"POST /v1/warehouse/location-directives":                     dutySetupMaintain,
		"GET /v1/warehouse/location-directives":                      dutySetupView,
		"POST /v1/warehouse/work":                                    dutyWorkSupervise,
		"GET /v1/warehouse/work":                                     dutyInquiry,
		"GET /v1/warehouse/work/{id}":                                dutyInquiry,
		"POST /v1/warehouse/work:assign":                             dutyWorkExecute,
		"POST /v1/warehouse/work/{id}/lines/{n}:confirm":             dutyWorkExecute,
		"POST /v1/warehouse/work/{id}:exception":                     dutyWorkExecute,
		"POST /v1/warehouse/dock-doors":                              dutySetupMaintain,
		"GET /v1/warehouse/dock-doors":                               dutyInquiry,
		"POST /v1/warehouse/dock-appointments":                       dutyReceive + " or " + dutyShipConfirm,
		"GET /v1/warehouse/dock-appointments":                        dutyInquiry + " or " + dutyGateOperate,
		"GET /v1/warehouse/dock-appointments/{id}":                   dutyInquiry + " or " + dutyGateOperate,
		"POST /v1/warehouse/receipts:from-asn":                       dutyReceive,
		"POST /v1/warehouse/shipments/{id}:ship":                     dutyShipConfirm,
		"GET /v1/warehouse/shipments/{id}/shipped":                   dutyInquiry,
		"GET /v1/warehouse/receipts":                                 dutyInquiry,
		"GET /v1/warehouse/receipts/{id}":                            dutyInquiry,
		"POST /v1/warehouse/receipts/{id}/lines/{n}:receive":         dutyReceive,
		"GET /v1/warehouse/pallets/{sscc}/label":                     dutyInquiry,
		"PUT /v1/warehouse/gs1-settings":                             dutySetupMaintain,
		"GET /v1/warehouse/gs1-settings":                             dutySetupView,
		"POST /v1/warehouse/rfid-reads:ingest":                       dutyRFIDIngest,
		"GET /v1/warehouse/rfid-reads":                               dutyInquiry,
		"POST /v1/warehouse/automation/transport-orders":             dutyWorkSupervise,
		"GET /v1/warehouse/automation/transport-orders":              dutyInquiry + " or " + dutyAutomation,
		"POST /v1/warehouse/automation/transport-orders/{id}:report": dutyAutomation,
		"POST /v1/warehouse/automation/events":                       dutyAutomation,
		"POST /v1/warehouse/automation/transport-orders/{id}:cancel": dutyWorkSupervise,
		"GET /v1/warehouse/rfid-rules":                               dutySetupView,
		"POST /v1/warehouse/rfid-rules":                              dutySetupMaintain,
		"DELETE /v1/warehouse/rfid-rules/{id}":                       dutySetupMaintain,
	}
	served := 0
	router := httpx.NewRouter()
	NewWithStore(&fakeStore{}, time.Minute).Register(router)
	_ = chi.Walk(router, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		served++
		key := method + " " + route
		duty, ok := declared[key]
		want, business := duties[key]
		switch {
		case !ok:
			t.Errorf("openapi.yaml does not document %s", key)
		case business && duty != want:
			t.Errorf("%s x-passage-duty = %q, want %s", key, duty, want)
		case !business && route != "/healthz" && route != "/readyz":
			t.Errorf("%s has no expected duty in this test", key)
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
