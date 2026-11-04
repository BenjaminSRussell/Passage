package api

import (
	"net/http"
	"strings"
	"testing"

	"github.com/shopspring/decimal"
)

// Database tests for slice 15: configured control-total tolerances (FR-49.2, decision D2). They skip
// without PASSAGE_TEST_DATABASE_URL and run as passage_app when PASSAGE_TEST_APP_DATABASE_URL is set.

const tolerancesPath = "/api/v1/migration/tolerances"

type toleranceView struct {
	Pack      string `json:"pack"`
	Total     string `json:"total"`
	Amount    string `json:"amount"`
	Reason    string `json:"reason"`
	Version   int    `json:"version"`
	UpdatedBy string `json:"updatedBy"`
}

func TestTolerance_ConfigureVersionAndReplay(t *testing.T) {
	h, admin := dbServer(t)
	put := func(tenant, body, key string) (int, string, toleranceView) {
		r := do(t, h, request{method: "PUT", path: tolerancesPath, tenant: tenant, duties: allDuties, user: "reviewer", key: key, body: body})
		var v toleranceView
		if r.Code == http.StatusOK {
			v = decode[toleranceView](t, r)
		}
		return r.Code, r.Body.String(), v
	}
	if code, body, v := put(tenantA, `{"pack":"open_items","total":"open_items.amount","amount":"0.05","reason":"conversion rounding"}`, "tol-1"); code != 200 || v.Version != 1 || v.Amount != "0.05" || v.UpdatedBy != "reviewer" {
		t.Fatalf("create: %d %s", code, body)
	}
	// The same key replays; a changed request under the key is refused.
	r := do(t, h, request{method: "PUT", path: tolerancesPath, tenant: tenantA, duties: allDuties, user: "reviewer", key: "tol-1",
		body: `{"pack":"open_items","total":"open_items.amount","amount":"0.05","reason":"conversion rounding"}`})
	if r.Code != 200 || r.Header().Get("Idempotent-Replayed") != "true" {
		t.Fatalf("replay: %d %v", r.Code, r.Header())
	}
	if code, body, _ := put(tenantA, `{"pack":"open_items","total":"open_items.amount","amount":"0.06","reason":"x"}`, "tol-1"); code != 422 || !strings.Contains(body, "MIG-0007") {
		t.Fatalf("key reuse: %d %s", code, body)
	}
	// A second configuration of an existing total needs its version.
	if code, body, _ := put(tenantA, `{"pack":"open_items","total":"open_items.amount","amount":"0.10","reason":"wider"}`, ""); code != 409 {
		t.Fatalf("missing version: %d %s", code, body)
	}
	if code, body, _ := put(tenantA, `{"pack":"open_items","total":"open_items.amount","amount":"0.10","reason":"wider","version":7}`, ""); code != 409 {
		t.Fatalf("stale version: %d %s", code, body)
	}
	if code, body, _ := put(tenantA, `{"pack":"open_items","total":"open_items.amount_by_account","amount":"1","reason":"x","version":1}`, ""); code != 409 {
		t.Fatalf("version for a total with no tolerance: %d %s", code, body)
	}
	if code, body, v := put(tenantA, `{"pack":"open_items","total":"open_items.amount","amount":"0.10","reason":"wider","version":1}`, ""); code != 200 || v.Version != 2 || v.Amount != "0.1" {
		t.Fatalf("update: %d %s", code, body)
	}
	// Two audit events, in the same transaction as the writes; the second records the previous amount.
	if n := count(t, admin, `select count(*) from outbox where type = 'dev.passage.migration.tolerance.configured.v1' and tenant_id = $1`, tenantA); n != 2 {
		t.Fatalf("outbox events = %d", n)
	}
	if n := count(t, admin, `select count(*) from outbox where type = 'dev.passage.migration.tolerance.configured.v1' and payload->'data'->>'previous_amount' = '0.05'`); n != 1 {
		t.Fatalf("events with the previous amount = %d", n)
	}
	// Tolerances are per tenant.
	list := decode[struct {
		Items []toleranceView `json:"items"`
	}](t, do(t, h, request{method: "GET", path: tolerancesPath, tenant: tenantA, duties: allDuties}))
	if len(list.Items) != 1 || list.Items[0].Version != 2 {
		t.Fatalf("tenant A list %+v", list.Items)
	}
	other := decode[struct {
		Items []toleranceView `json:"items"`
	}](t, do(t, h, request{method: "GET", path: tolerancesPath, tenant: tenantB, duties: allDuties}))
	if len(other.Items) != 0 {
		t.Fatalf("tenant B sees %+v", other.Items)
	}
}

func TestTolerance_LetsARunWithADeltaReconcile(t *testing.T) {
	h, _ := dbServer(t)
	first := do(t, h, request{method: "POST", path: runsPath, tenant: tenantA, duties: allDuties, user: "e", body: openItemsBody(3)})
	if first.Code != http.StatusCreated {
		t.Fatalf("first run: %d %s", first.Code, first.Body)
	}
	// The legacy system now reports OI-00000 at 10.30, Passage holds 10.25 from the first load.
	changed := strings.Replace(openItemsBody(3), `"legacyId":"OI-00000","party":"V001","account":"2100","currency":"USD","amount":"10.25"`, `"legacyId":"OI-00000","party":"V001","account":"2100","currency":"USD","amount":"10.30"`, 1)
	run := decode[runDetailDTO](t, do(t, h, request{method: "POST", path: runsPath, tenant: tenantA, duties: allDuties, user: "e", body: changed}))
	if run.Status != "LOADED" {
		t.Fatalf("without a tolerance the delta holds the run at LOADED, got %s", run.Status)
	}
	if r := do(t, h, request{method: "PUT", path: tolerancesPath, tenant: tenantA, duties: allDuties, user: "rev", body: `{"pack":"open_items","total":"*","amount":"0.05","reason":"AX rounding of converted amounts"}`}); r.Code != 200 {
		t.Fatalf("configure: %d %s", r.Code, r.Body)
	}
	// The run that started before the change keeps its exact comparison.
	before := decode[runDetailDTO](t, do(t, h, request{method: "GET", path: "/api/v1/migration/runs/" + run.RunID, tenant: tenantA, duties: allDuties}))
	if before.Status != "LOADED" {
		t.Fatalf("a tolerance change must not reconcile an earlier run, got %s", before.Status)
	}
	for _, c := range before.ControlTotals {
		if c.Status == "WITHIN_TOLERANCE" || !decimal.RequireFromString(c.Tolerance).IsZero() {
			t.Fatalf("earlier run's total changed: %+v", c)
		}
	}
	run = decode[runDetailDTO](t, do(t, h, request{method: "POST", path: runsPath, tenant: tenantA, duties: allDuties, user: "e", body: changed}))
	if run.Status != "RECONCILED" {
		t.Fatalf("with a tolerance the run reconciles, got %s %+v", run.Status, run.ControlTotals)
	}
	within := 0
	for _, c := range run.ControlTotals {
		if c.Status == "WITHIN_TOLERANCE" {
			within++
			if !decimal.RequireFromString(c.Tolerance).Equal(decimal.RequireFromString("0.05")) || c.ToleranceReason != "AX rounding of converted amounts" {
				t.Fatalf("total %+v does not carry the allowance", c)
			}
		}
	}
	if within == 0 {
		t.Fatalf("no total is WITHIN_TOLERANCE: %+v", run.ControlTotals)
	}
	for _, c := range run.ControlTotals {
		if c.Name == "open_items.count" && !decimal.RequireFromString(c.Tolerance).IsZero() {
			t.Fatalf("the pack default must not reach the count: %+v", c)
		}
	}
}
