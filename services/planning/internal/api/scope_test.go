package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/planning/internal/domain"
)

const rmID = "00000000-0000-0000-0000-0000000000b2"

// scopeBody plans a finished good with a component. The finished good has demand
// on day 10 and again on 2028-01-01, which is beyond any 365-day horizon.
func scopeBody(extra string) string {
	return `{"asOf":"2026-11-02","siteId":"` + siteID + `",` + extra + `"items":[
 {"itemId":"` + fgID + `","itemGroup":"FIN","planner":"ANN","coverage":{"code":"requirement"},"onHand":"0","safetyStock":"0",
  "orderType":"production","leadTimeDays":5,"lot":{},
  "demand":[{"ref":"SO-1","refType":"sales-line","date":"2026-11-12","qty":"100"},{"ref":"SO-2","refType":"sales-line","date":"2028-01-01","qty":"50"}],
  "receipts":[],"components":[{"itemId":"` + rmID + `","qty":"2"}]},
 {"itemId":"` + rmID + `","itemGroup":"RAW","planner":"BOB","coverage":{"code":"requirement"},"onHand":"0","safetyStock":"0",
  "orderType":"purchase","leadTimeDays":3,"lot":{},"demand":[],"receipts":[],"components":[]}]}`
}

func runWith(t *testing.T, a *API, st *fakeStore, body, key string) *httptest.ResponseRecorder {
	t.Helper()
	router := httpx.NewRouter()
	a.Register(router)
	req := httptest.NewRequest(http.MethodPost, "/v1/planning/plans/MAIN:run", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Passage-Tenant", testkit.TenantID(1))
	req.Header.Set("X-Passage-Duties", "planning.run")
	req.Header.Set("Idempotency-Key", key)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

func TestRun_ScopeReportsOnlyMatchingItems(t *testing.T) {
	st := &fakeStore{}
	rec := runWith(t, New(st), st, scopeBody(`"scope":{"itemGroups":["RAW"]},`), "k-scope")
	if rec.Code != http.StatusCreated {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	if st.last.ItemsPlanned != 1 {
		t.Fatalf("itemsPlanned = %d; want 1", st.last.ItemsPlanned)
	}
	// The component is sized by the finished good outside the scope: 2 x (100 + 50).
	total := decimal.Zero
	for _, o := range st.last.Result.Orders {
		if o.ItemID != rmID {
			t.Fatalf("order of %s is outside the scope", o.ItemID)
		}
		total = total.Add(o.Qty)
	}
	if total.String() != "300" {
		t.Fatalf("component qty = %s; want 300", total)
	}
}

func TestRun_ScopeByLowLevelCode(t *testing.T) {
	st := &fakeStore{}
	rec := runWith(t, New(st), st, scopeBody(`"scope":{"llcMin":0,"llcMax":0},`), "k-llc")
	if rec.Code != http.StatusCreated || len(st.last.Result.Orders) == 0 {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	for _, o := range st.last.Result.Orders {
		if o.ItemID != fgID {
			t.Fatalf("order of %s is outside the end-item scope", o.ItemID)
		}
	}
}

func TestRun_ServiceHorizonIgnoresLaterDemand(t *testing.T) {
	st := &fakeStore{}
	if rec := runWith(t, New(st), st, scopeBody(""), "k-open"); rec.Code != http.StatusCreated {
		t.Fatal(rec.Body.String())
	}
	open := st.last.Result.Orders
	st2 := &fakeStore{}
	if rec := runWith(t, New(st2).WithHorizon(365), st2, scopeBody(""), "k-horizon"); rec.Code != http.StatusCreated {
		t.Fatal(rec.Body.String())
	}
	fg0, fg1 := fgQty(open), fgQty(st2.last.Result.Orders)
	if fg0 != "150" || fg1 != "100" {
		t.Fatalf("finished good planned %s without and %s with the 365-day horizon; want 150 and 100", fg0, fg1)
	}
}

func TestRun_RequestHorizonWinsOverTheDefault(t *testing.T) {
	st := &fakeStore{}
	rec := runWith(t, New(st).WithHorizon(30), st, scopeBody(`"horizonDays":1095,`), "k-own")
	if rec.Code != http.StatusCreated {
		t.Fatal(rec.Body.String())
	}
	if got := fgQty(st.last.Result.Orders); got != "150" {
		t.Fatalf("finished good qty = %s; want 150 under the run's own 1095-day horizon", got)
	}
}

func TestRun_RejectsInvalidScopeAndHorizon(t *testing.T) {
	cases := []struct{ field, extra string }{
		{"scope.llcMax", `"scope":{"llcMin":3,"llcMax":1},`},
		{"scope.itemIds", `"scope":{"itemIds":["nope"]},`},
		{"scope.planners", `"scope":{"planners":[""]},`},
		{"scope.llcMin", `"scope":{"llcMin":-1},`},
		{"scope.llcMax", `"scope":{"llcMax":100},`},
		{"horizonDays", `"horizonDays":10,`},
		{"horizonDays", `"horizonDays":1096,`},
		{"scope.itemGroups", `"scope":{"itemGroups":["` + strings.Repeat("x", 65) + `"]},`},
	}
	for _, c := range cases {
		st := &fakeStore{}
		rec := runWith(t, New(st), st, scopeBody(c.extra), "k-bad")
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: status %d body %s", c.extra, rec.Code, rec.Body.String())
		}
		var p struct {
			Errors map[string]string `json:"errors"`
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &p)
		if p.Errors[c.field] == "" {
			t.Fatalf("%s: errors = %v; want %s", c.extra, p.Errors, c.field)
		}
		if st.last.PlanCode != "" {
			t.Fatalf("%s: an invalid run must not reach the store", c.extra)
		}
	}
}

func TestRun_RejectsTooLongItemAttributes(t *testing.T) {
	st := &fakeStore{}
	body := strings.Replace(scopeBody(""), `"planner":"BOB"`, `"planner":"`+strings.Repeat("p", 65)+`"`, 1)
	rec := runWith(t, New(st), st, body, "k-attr")
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "items[1].itemGroup") {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
}

// A scenario simulation honours the horizon and the scope like an operational run.
func TestScenarioRun_TakesHorizonAndScope(t *testing.T) {
	st := &fakeStore{}
	do(t, st, http.MethodPost, scenarioPath, upBody, dutySim, true, "")
	body := scopeBody(`"horizonDays":365,"scope":{"planners":["ANN"]},`)
	rec := do(t, st, http.MethodPost, scenarioPath+"/UP20:run", body, dutySim, true, "sim-scope")
	if rec.Code != http.StatusCreated {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	if st.lastSim.ItemsPlanned != 1 {
		t.Fatalf("itemsPlanned = %d; want 1", st.lastSim.ItemsPlanned)
	}
	// Only the finished good is reported, and SO-2 (2028) is beyond the horizon: 100 x 1.2.
	for _, o := range st.lastSim.Result.Orders {
		if o.ItemID != fgID {
			t.Fatalf("order of %s is outside the planner scope", o.ItemID)
		}
	}
	if got := fgQty(st.lastSim.Result.Orders); got != "120" {
		t.Fatalf("finished good qty = %s; want 120", got)
	}
}

// fgQty totals the planned quantity of the finished good.
func fgQty(orders []domain.PlannedOrder) string {
	total := decimal.Zero
	for _, o := range orders {
		if o.ItemID == fgID {
			total = total.Add(o.Qty)
		}
	}
	return total.String()
}
