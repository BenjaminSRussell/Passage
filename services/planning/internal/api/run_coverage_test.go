package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/planning/internal/domain"
	"github.com/BenjaminSRussell/passage/services/planning/internal/store"
)

// RunCoverages returns the stored coverage of the requested items; the fake
// holds coverage for siteID only.
func (f *fakeStore) PlanLegalEntity(_ context.Context, _, requested string) (string, error) {
	return requested, nil
}

func (f *fakeStore) RunCoverages(_ context.Context, site, planEntity string, itemIDs []string) (map[string]store.RunCoverage, error) {
	f.runCovSite, f.runCovEntity = site, planEntity
	out := map[string]store.RunCoverage{}
	if site != siteID {
		return out, nil
	}
	for _, id := range itemIDs {
		if c, ok := f.runCovs[id]; ok {
			out[id] = c
		}
	}
	return out, nil
}

func (f *fakeStore) RunWarehouseCoverages(_ context.Context, site, _ string, itemIDs []string) (map[string]store.RunCoverage, error) {
	out := map[string]store.RunCoverage{}
	if site != siteID {
		return out, nil
	}
	for k, c := range f.runWhCovs {
		for _, id := range itemIDs {
			if base, _ := domain.SplitWarehouseKey(k); base == id {
				out[k] = c
			}
		}
	}
	return out, nil
}

// bareBody is runBody with coverage, order type and lead time left out, so
// the run must take them from the stored item coverage (FR-22.2, FR-22.5).
const bareBody = `{"asOf":"2026-11-02","siteId":"` + siteID + `","items":[
 {"itemId":"` + fgID + `","onHand":"20","safetyStock":"10","lot":{},
  "demand":[{"ref":"SO-1","refType":"sales-line","date":"2026-11-30","qty":"120"}],
  "receipts":[],"components":[]}]}`

// runVersion posts a run and returns the created version and the request the
// store received, failing the test otherwise.
func runVersion(t *testing.T, st *fakeStore, body, key string) (store.Version, store.RunRequest) {
	t.Helper()
	rec := do(t, st, http.MethodPost, "/v1/planning/plans/MAIN:run", body, "planning.run", true, key)
	if rec.Code != http.StatusCreated {
		t.Fatalf("run %s: status %d body %s", key, rec.Code, rec.Body.String())
	}
	var v store.Version
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	return v, st.last
}

// onlyOrder returns the single planned order of a run.
func onlyOrder(t *testing.T, req store.RunRequest) domain.PlannedOrder {
	t.Helper()
	if len(req.Result.Orders) != 1 {
		t.Fatalf("planned orders = %+v; want one", req.Result.Orders)
	}
	return req.Result.Orders[0]
}

// requirementCoverage is a stored requirement coverage with a production
// order type and a 7-day lead time, the same values runBody sends inline.
func requirementCoverage() store.RunCoverage {
	lead := 7
	return store.RunCoverage{ItemID: fgID, GroupCode: "REQ", CoverageCode: "requirement", CoverageFenceDays: 100,
		NegativeDays: 1, MinQty: decimal.Zero, MaxQty: decimal.Zero, LeadTimeDays: &lead, PlannedOrderType: "production"}
}

// TestRun_StoredCoverageFillsBareItem: an item sent without coverage, order
// type and lead time plans exactly like the same item sent with its stored
// values inline, and the store is asked for the run's site.
func TestRun_StoredCoverageFillsBareItem(t *testing.T) {
	want, _ := runVersion(t, &fakeStore{}, runBody, "k-inline")
	st := &fakeStore{runCovs: map[string]store.RunCoverage{fgID: requirementCoverage()}}
	got, req := runVersion(t, st, bareBody, "k-stored")
	if st.runCovSite != siteID {
		t.Fatalf("coverage read for site %q, want %q", st.runCovSite, siteID)
	}
	if got.PlanHash != want.PlanHash || got.PlannedOrders != want.PlannedOrders {
		t.Fatalf("stored coverage plans differently: hash %s orders %d, want hash %s orders %d",
			got.PlanHash, got.PlannedOrders, want.PlanHash, want.PlannedOrders)
	}
	o := onlyOrder(t, req)
	if o.OrderType != domain.OrderProduction || o.DeliveryDate-o.OrderDate != 7 || !o.Qty.Equal(decimal.NewFromInt(110)) {
		t.Fatalf("planned order %+v; want production, 7 days lead, qty 110", o)
	}
}

func TestRun_StoredCoverageLookupUsesThePlansEntity(t *testing.T) {
	entity := "0190f0a0-0000-7000-8000-00000000000a"
	st := &fakeStore{runCovs: map[string]store.RunCoverage{fgID: requirementCoverage()}}
	body := strings.Replace(bareBody, `{"asOf"`, `{"legalEntityId":"`+entity+`","asOf"`, 1)
	runVersion(t, st, body, "k-entity")
	if st.runCovEntity != entity {
		t.Fatalf("coverage read for entity %q, want %q", st.runCovEntity, entity)
	}
}

// TestRun_StoredMinMaxCoverage: a stored min/max coverage reorders up to the
// item coverage's maximum (FR-22.2) and its purchase order type applies.
func TestRun_StoredMinMaxCoverage(t *testing.T) {
	c := requirementCoverage()
	c.CoverageCode, c.MinQty, c.MaxQty, c.PlannedOrderType = "minmax", decimal.NewFromInt(50), decimal.NewFromInt(500), "purchase"
	_, req := runVersion(t, &fakeStore{runCovs: map[string]store.RunCoverage{fgID: c}}, bareBody, "k-minmax")
	if len(req.Result.Orders) == 0 {
		t.Fatal("min/max coverage planned nothing")
	}
	// On-hand 20 less safety stock 10 (BR-22.1 step 4) is PAB 10 on day 0,
	// below the minimum 50: reorder up to 500.
	first := req.Result.Orders[0]
	if first.OrderType != domain.OrderPurchase || first.DeliveryDate != 0 || !first.Qty.Equal(decimal.NewFromInt(490)) {
		t.Fatalf("first planned order %+v; want a purchase of 490 on day 0", first)
	}
}

// TestRun_InlineValuesWinOverStored: inline coverage, order type and an
// explicit lead time of 0 are kept although the stored row differs.
func TestRun_InlineValuesWinOverStored(t *testing.T) {
	lead := 30
	stored := store.RunCoverage{ItemID: fgID, CoverageCode: "period", PeriodDays: 14, MinQty: decimal.NewFromInt(50),
		LeadTimeDays: &lead, PlannedOrderType: "purchase"}
	want, _ := runVersion(t, &fakeStore{}, runBody, "k-inline")
	got, _ := runVersion(t, &fakeStore{runCovs: map[string]store.RunCoverage{fgID: stored}}, runBody, "k-both")
	if got.PlanHash != want.PlanHash {
		t.Fatalf("inline values were overwritten: hash %s, want %s", got.PlanHash, want.PlanHash)
	}

	zeroLead := strings.Replace(bareBody, `"onHand"`, `"leadTimeDays":0,"onHand"`, 1)
	_, req := runVersion(t, &fakeStore{runCovs: map[string]store.RunCoverage{fgID: requirementCoverage()}}, zeroLead, "k-zero")
	if o := onlyOrder(t, req); o.DeliveryDate != o.OrderDate {
		t.Fatalf("an explicit leadTimeDays 0 must win over the stored 7: %+v", o)
	}
}

// TestRun_StoredCoverageWithoutLeadTimeKeepsRequestLead: a stored row without
// a lead-time override leaves the request's (absent, so 0) lead time.
func TestRun_StoredCoverageWithoutLeadTimeKeepsRequestLead(t *testing.T) {
	c := requirementCoverage()
	c.LeadTimeDays = nil
	_, req := runVersion(t, &fakeStore{runCovs: map[string]store.RunCoverage{fgID: c}}, bareBody, "k-nolead")
	if o := onlyOrder(t, req); o.DeliveryDate != o.OrderDate {
		t.Fatalf("no stored lead time: %+v", o)
	}
}

// noCoverageBody is bareBody with the order type and lead time sent inline, so
// only the coverage is left to the default group.
var noCoverageBody = strings.Replace(bareBody, `"onHand"`, `"orderType":"production","leadTimeDays":7,"onHand"`, 1)

// TestRun_BareItemWithoutStoredCoverageIsRefused: with nothing stored, a run
// item without an order type is a 400 naming it; the coverage is no longer
// asked for because the default group REQ applies (FR-22.2, 4.22.13).
func TestRun_BareItemWithoutStoredCoverageIsRefused(t *testing.T) {
	st := &fakeStore{}
	rec := do(t, st, http.MethodPost, "/v1/planning/plans/MAIN:run", bareBody, "planning.run", true, "k-bare")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d body %s; want 400", rec.Code, rec.Body.String())
	}
	var p httpx.Problem
	if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
		t.Fatal(err)
	}
	if p.Errors["items[0].orderType"] == "" || p.Errors["items[0].coverage.code"] != "" {
		t.Fatalf("field errors %v; want only the orderType of items[0]", p.Errors)
	}
	if st.runs != nil {
		t.Fatal("a refused run must not reach the store")
	}
}

// TestRun_DefaultCoverageGroupBuiltin: an item with neither coverage nor stored
// item coverage is planned with requirement coverage and the FR-22.2 defaults
// when the tenant stored no group REQ, like the same item sent that coverage inline.
func TestRun_DefaultCoverageGroupBuiltin(t *testing.T) {
	want, _ := runVersion(t, &fakeStore{}, runBody, "k-inline")
	got, req := runVersion(t, &fakeStore{}, noCoverageBody, "k-default")
	if got.PlanHash != want.PlanHash {
		t.Fatalf("hash %s, want the inline requirement coverage's %s", got.PlanHash, want.PlanHash)
	}
	if len(req.Result.Orders) == 0 {
		t.Fatal("the default coverage planned nothing")
	}
}

// TestRun_DefaultCoverageGroupStored: the stored group REQ replaces the built-in
// settings for items without coverage, and an item with a stored item coverage
// or inline coverage never takes it.
func TestRun_DefaultCoverageGroupStored(t *testing.T) {
	builtin, _ := runVersion(t, &fakeStore{}, noCoverageBody, "k-builtin")
	spec := domain.DefaultCoverageGroup(defaultCoverageCode, domain.CoverageManual)
	st := &fakeStore{groups: []store.CoverageGroup{{ID: "g1", Version: 1, Spec: spec}}}
	got, req := runVersion(t, st, noCoverageBody, "k-stored-req")
	if got.PlanHash == builtin.PlanHash || len(req.Result.Orders) != 0 {
		t.Fatalf("manual REQ must plan no orders: hash %s builtin %s orders %v", got.PlanHash, builtin.PlanHash, req.Result.Orders)
	}
	// An item coverage of its own wins over REQ.
	st.runCovs = map[string]store.RunCoverage{fgID: requirementCoverage()}
	if _, req = runVersion(t, st, noCoverageBody, "k-own"); len(req.Result.Orders) == 0 {
		t.Fatal("the item's own coverage was replaced by REQ")
	}
	// Inline coverage wins over REQ.
	st.runCovs = nil
	if _, req = runVersion(t, st, runBody, "k-inline-wins"); len(req.Result.Orders) == 0 {
		t.Fatal("inline coverage was replaced by REQ")
	}
}

// TestRun_DefaultCoverageGroupLookupFails: a failure reading the group REQ other
// than "not found" fails the run instead of planning with the built-in defaults,
// and REQ is not read at all when every item has coverage of its own.
func TestRun_DefaultCoverageGroupLookupFails(t *testing.T) {
	st := &fakeStore{groupErr: errors.New("database down")}
	rec := do(t, st, http.MethodPost, "/v1/planning/plans/MAIN:run", noCoverageBody, "planning.run", true, "k-req-down")
	if rec.Code != http.StatusInternalServerError || st.runs != nil {
		t.Fatalf("status %d body %s; want 500 before the store", rec.Code, rec.Body.String())
	}
	if _, req := runVersion(t, st, runBody, "k-inline-no-req"); len(req.Result.Orders) == 0 {
		t.Fatal("a run whose items carry coverage must not need REQ")
	}
}

// TestRun_ReplayAfterStoredCoverageChanged: the idempotency fingerprint is the
// request as sent, so a retry with the same key and body replays the first
// version even when the stored coverage changed in between.
func TestRun_ReplayAfterStoredCoverageChanged(t *testing.T) {
	st := &fakeStore{runCovs: map[string]store.RunCoverage{fgID: requirementCoverage()}}
	first, _ := runVersion(t, st, bareBody, "k-retry")
	changed := requirementCoverage()
	lead := 3
	changed.LeadTimeDays = &lead
	st.runCovs[fgID] = changed
	rec := do(t, st, http.MethodPost, "/v1/planning/plans/MAIN:run", bareBody, "planning.run", true, "k-retry")
	if rec.Code != http.StatusOK || rec.Header().Get("Idempotent-Replayed") != "true" {
		t.Fatalf("retry status %d replayed %q body %s; want a 200 replay", rec.Code, rec.Header().Get("Idempotent-Replayed"), rec.Body.String())
	}
	var v store.Version
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	if v.PlanHash != first.PlanHash {
		t.Fatalf("replay hash %s, want the first run's %s", v.PlanHash, first.PlanHash)
	}
}

// TestWithStoredCoverage_LeavesUnknownItemsAlone: items without a stored row
// keep their inline values.
func TestWithStoredCoverage_LeavesUnknownItemsAlone(t *testing.T) {
	lead := 2
	items := []itemDTO{{ItemID: fgID, Coverage: coverageDTO{Code: "manual"}, OrderType: "transfer", LeadTimeDays: &lead}}
	if fields := withStoredCoverage(items, map[string]store.RunCoverage{"other": {PlannedOrderType: "production"}}, nil); len(fields) != 0 {
		t.Fatalf("fields %v", fields)
	}
	if items[0].OrderType != "transfer" || *items[0].LeadTimeDays != 2 || items[0].Coverage.Code != "manual" {
		t.Fatalf("unknown item changed: %+v", items[0])
	}
}

// TestRun_StoredMarginsShiftTheOrder (FR-22.5): the stored issue margin moves
// the demand 3 days earlier and the reorder margin adds 2 days to the lead
// time, so the order is delivered 3 days earlier and released 5 days earlier.
func TestRun_StoredMarginsShiftTheOrder(t *testing.T) {
	_, base := runVersion(t, &fakeStore{runCovs: map[string]store.RunCoverage{fgID: requirementCoverage()}}, bareBody, "k-base")
	c := requirementCoverage()
	c.IssueMarginDays, c.ReorderMarginDays = 3, 2
	_, req := runVersion(t, &fakeStore{runCovs: map[string]store.RunCoverage{fgID: c}}, bareBody, "k-margins")
	b, o := onlyOrder(t, base), onlyOrder(t, req)
	if o.DeliveryDate != b.DeliveryDate-3 || o.OrderDate != b.OrderDate-5 {
		t.Fatalf("with margins %+v; without %+v; want delivery -3 and order -5", o, b)
	}
}
