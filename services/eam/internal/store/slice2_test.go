package store

import (
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/eam/internal/domain"
)

// inProgress creates a work order of typ on the fixture asset and moves it to IN_PROGRESS.
func (f fixture) inProgress(t *testing.T, typ, key string, extra ...func(*WorkOrder)) (WorkOrder, int) {
	t.Helper()
	in := WorkOrder{AssetID: f.asset, Type: typ, Currency: "USD"}
	for _, fn := range extra {
		fn(&in)
	}
	wo, err := f.s.CreateWorkOrder(f.ctx, in, key)
	if err != nil {
		t.Fatal(err)
	}
	return wo, f.advance(t, wo.ID, wo.Version, domain.StatusScheduled, domain.StatusReleased, domain.StatusInProgress)
}

// TestFault_AC4415: a CORRECTIVE order cannot complete without a fault
// (EAM-FLT-001); codes outside the asset type's catalog are refused
// (EAM-FLT-002); a later registration supersedes the earlier one.
func TestFault_AC4415(t *testing.T) {
	f := setup(t)
	wo, v := f.inProgress(t, "CORRECTIVE", "wo-c")
	if _, err := f.s.TransitionWorkOrder(f.ctx, wo.ID, Transition{To: domain.StatusCompleted, Version: v}, "c-1"); problemCode(err) != CodeFaultMissing {
		t.Fatalf("completion without fault: %v, want %s", err, CodeFaultMissing)
	}
	for name, bad := range map[string]domain.Fault{
		"combination not allowed for PUMP": {Symptom: "LEAK", Area: "BEARING", Type: "WEAR", Cause: "FATIGUE", Remedy: "REPLACE"},
		"cause not in the catalog":         {Symptom: "NOISE", Area: "BEARING", Type: "WEAR", Cause: "MISUSE", Remedy: "REPLACE"},
	} {
		if _, err := f.s.RegisterFault(f.ctx, wo.ID, bad, "", "tech-1", "bad-"+name); problemCode(err) != CodeFaultInvalid {
			t.Fatalf("%s: %v, want %s", name, err, CodeFaultInvalid)
		}
	}
	first, err := f.s.RegisterFault(f.ctx, wo.ID, bearingWear, "grinding at 3 kHz", "tech-1", "flt-1")
	if err != nil || first.Fault == nil || first.Fault.Symptom != "NOISE" || deref(first.Fault.Notes, "") != "grinding at 3 kHz" {
		t.Fatalf("registration = %+v %v", first.Fault, err)
	}
	seal := domain.Fault{Symptom: "LEAK", Area: "SEAL", Type: "WEAR", Cause: "FATIGUE", Remedy: "REPLACE"}
	second, err := f.s.RegisterFault(f.ctx, wo.ID, seal, "", "tech-2", "flt-2")
	if err != nil || second.Fault.Area != "SEAL" {
		t.Fatalf("correction = %+v %v", second.Fault, err)
	}
	// Replaying the first key does not bring the superseded registration back.
	replay, err := f.s.RegisterFault(f.ctx, wo.ID, bearingWear, "", "tech-1", "flt-1")
	if err != nil || replay.Fault.ID != second.Fault.ID {
		t.Fatalf("replay current fault = %+v %v, want %s", replay.Fault, err, second.Fault.ID)
	}
	if n := f.count(t, `select count(*) from work_order_fault where work_order_id = $1`, wo.ID); n != 2 {
		t.Fatalf("registrations = %d, want 2 (append-only)", n)
	}
	other, _ := f.inProgress(t, "CORRECTIVE", "wo-other")
	if _, err := f.s.RegisterFault(f.ctx, other.ID, bearingWear, "", "tech-1", "flt-1"); problemCode(err) != CodeKeyReuse {
		t.Fatalf("key reused on another order: %v, want %s", err, CodeKeyReuse)
	}
	done, err := f.s.TransitionWorkOrder(f.ctx, wo.ID, Transition{To: domain.StatusCompleted, Version: v}, "c-2")
	if err != nil || done.Status != "COMPLETED" {
		t.Fatalf("completion after fault: %+v %v", done, err)
	}
	if _, err := f.s.RegisterFault(f.ctx, wo.ID, bearingWear, "", "tech-1", "flt-late"); problemCode(err) != CodeWOTransition {
		t.Fatalf("fault on a completed order: %v, want %s", err, CodeWOTransition)
	}

	// INSPECTION orders need no fault; an asset type without a catalog takes none.
	insp, iv := f.inProgress(t, "INSPECTION", "wo-i")
	if _, err := f.s.TransitionWorkOrder(f.ctx, insp.ID, Transition{To: domain.StatusCompleted, Version: iv}, "c-3"); err != nil {
		t.Fatalf("inspection completion: %v", err)
	}
	motor, err := f.s.CreateAsset(f.ctx, Asset{Name: "Motor", AssetType: "MOTOR", SiteID: testkit.TenantID(9), Criticality: 3}, "motor")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.ActivateAsset(f.ctx, motor.ID, motor.Version); err != nil {
		t.Fatal(err)
	}
	mwo, err := f.s.CreateWorkOrder(f.ctx, WorkOrder{AssetID: motor.ID, Type: "CORRECTIVE", Currency: "USD"}, "wo-m")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.RegisterFault(f.ctx, mwo.ID, bearingWear, "", "tech-1", "flt-m"); problemCode(err) != CodeFaultInvalid {
		t.Fatalf("fault without a catalog: %v, want %s", err, CodeFaultInvalid)
	}
}

// TestFaultCatalog_Versions: GET returns the designer order and version; PUT
// needs the current version (412 otherwise) and replaces every code.
func TestFaultCatalog_Versions(t *testing.T) {
	f := setup(t)
	cat, err := f.s.GetFaultCatalog(f.ctx, "PUMP")
	if err != nil || cat.Version != 1 || len(cat.Symptoms) != 2 || cat.Symptoms[0].Code != "NOISE" || len(cat.Combinations) != 2 {
		t.Fatalf("catalog = %+v %v", cat, err)
	}
	if _, err := f.s.PutFaultCatalog(f.ctx, "PUMP", pumpCatalog(), 0); problemCode(err) != CodeVersion {
		t.Fatalf("stale If-Match: %v, want %s", err, CodeVersion)
	}
	next := pumpCatalog()
	next.Symptoms = []domain.FaultCode{{Code: "VIBRATION", Text: "Vibration"}}
	next.Combinations = []domain.FaultCombination{{Symptom: "VIBRATION", Area: "BEARING", Type: "WEAR"}}
	got, err := f.s.PutFaultCatalog(f.ctx, "PUMP", next, 1)
	if err != nil || got.Version != 2 || len(got.Symptoms) != 1 || got.Symptoms[0].Code != "VIBRATION" || len(got.Combinations) != 1 {
		t.Fatalf("replaced catalog = %+v %v", got, err)
	}
	if n := f.count(t, `select count(*) from fault_code where asset_type = 'PUMP' and kind = 'SYMPTOM'`); n != 1 {
		t.Fatalf("symptom rows = %d, want 1", n)
	}
	list, err := f.s.ListFaultCatalogs(f.ctx)
	if err != nil || len(list) != 1 || list[0].AssetType != "PUMP" || list[0].Version != 2 || list[0].Codes != 6 || list[0].Combinations != 1 {
		t.Fatalf("catalog list = %+v %v, want PUMP v2 with 6 codes and 1 combination", list, err)
	}
	if _, err := f.s.GetFaultCatalog(f.ctx, "MOTOR"); problemCode(err) != CodeNotFound {
		t.Fatalf("missing catalog: %v, want %s", err, CodeNotFound)
	}
	if _, err := f.s.PutFaultCatalog(f.ctx, "MOTOR", pumpCatalog(), 3); problemCode(err) != CodeVersion {
		t.Fatalf("first PUT with a version: %v, want %s", err, CodeVersion)
	}
}

// TestCriticality_AC4416: SAFETY 3, PRODUCTION 5 and likelihood 4 give risk 20
// and criticality 5; the asset follows and a new CORRECTIVE order defaults to
// priority 1. Assessments are versioned and replay by key.
func TestCriticality_AC4416(t *testing.T) {
	f := setup(t)
	before, err := f.s.GetAsset(f.ctx, f.asset)
	if err != nil {
		t.Fatal(err)
	}
	c := domain.Consequences{Safety: 3, Environment: 1, Production: 5, Quality: 1, Cost: 2}
	got, err := f.s.AssessCriticality(f.ctx, f.asset, c, 4, "eng-1", "crit-1")
	if err != nil || got.RiskScore != 20 || got.Criticality != 5 || got.Version != 1 || deref(got.AssessedBy, "") != "eng-1" {
		t.Fatalf("assessment = %+v %v, want risk 20, criticality 5, version 1", got, err)
	}
	again, err := f.s.AssessCriticality(f.ctx, f.asset, c, 4, "eng-1", "crit-1")
	if err != nil || again.ID != got.ID {
		t.Fatalf("replay = %+v %v, want %s", again, err, got.ID)
	}
	after, err := f.s.GetAsset(f.ctx, f.asset)
	if err != nil || after.Criticality != 5 || after.Version != before.Version+1 {
		t.Fatalf("asset = criticality %d version %d (%v), want 5 and %d", after.Criticality, after.Version, err, before.Version+1)
	}
	wo, err := f.s.CreateWorkOrder(f.ctx, WorkOrder{AssetID: f.asset, Type: "CORRECTIVE", Currency: "USD"}, "wo-crit")
	if err != nil || wo.Priority != 1 {
		t.Fatalf("corrective order = %+v %v, want priority 1", wo, err)
	}
	low, err := f.s.AssessCriticality(f.ctx, f.asset, domain.Consequences{Safety: 1, Environment: 1, Production: 2, Quality: 1, Cost: 1}, 2, "eng-1", "crit-2")
	if err != nil || low.Version != 2 || low.RiskScore != 4 || low.Criticality != 1 {
		t.Fatalf("second assessment = %+v %v", low, err)
	}
	list, err := f.s.ListAssessments(f.ctx, f.asset)
	if err != nil || len(list) != 2 || list[0].Version != 2 || list[1].Version != 1 {
		t.Fatalf("assessments = %+v %v, want newest first", list, err)
	}
	other, err := f.s.CreateAsset(f.ctx, Asset{Name: "Fan", AssetType: "FAN", SiteID: testkit.TenantID(9), Criticality: 2}, "fan")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.AssessCriticality(f.ctx, other.ID, c, 4, "eng-1", "crit-1"); problemCode(err) != CodeKeyReuse {
		t.Fatalf("key reused on another asset: %v, want %s", err, CodeKeyReuse)
	}
}

// TestAssetLifecycle_FR442: retirement needs the current version, an INACTIVE
// asset takes no work orders, and disposal waits for open orders to close.
func TestAssetLifecycle_FR442(t *testing.T) {
	f := setup(t)
	a, err := f.s.GetAsset(f.ctx, f.asset)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.ChangeAssetLifecycle(f.ctx, f.asset, "INACTIVE", a.Version+5); problemCode(err) != CodeVersion {
		t.Fatalf("stale version: %v, want %s", err, CodeVersion)
	}
	if _, err := f.s.ChangeAssetLifecycle(f.ctx, f.asset, "DISPOSED", a.Version); problemCode(err) != CodeAssetState {
		t.Fatalf("ACTIVE to DISPOSED: %v, want %s", err, CodeAssetState)
	}
	open, err := f.s.CreateWorkOrder(f.ctx, WorkOrder{AssetID: f.asset, Type: "INSPECTION", Currency: "USD"}, "lc-wo")
	if err != nil {
		t.Fatal(err)
	}
	inactive, err := f.s.ChangeAssetLifecycle(f.ctx, f.asset, "INACTIVE", a.Version)
	if err != nil || inactive.LifecycleState != "INACTIVE" || inactive.Version != a.Version+1 {
		t.Fatalf("retire = %+v %v", inactive, err)
	}
	if _, err := f.s.CreateWorkOrder(f.ctx, WorkOrder{AssetID: f.asset, Type: "INSPECTION", Currency: "USD"}, "lc-wo2"); problemCode(err) != CodeAssetState {
		t.Fatalf("order on an INACTIVE asset: %v, want %s", err, CodeAssetState)
	}
	if _, err := f.s.ChangeAssetLifecycle(f.ctx, f.asset, "DISPOSED", inactive.Version); problemCode(err) != CodeAssetState {
		t.Fatalf("dispose with an open order: %v, want %s", err, CodeAssetState)
	}
	f.advance(t, open.ID, open.Version, domain.StatusCanceled)
	disposed, err := f.s.ChangeAssetLifecycle(f.ctx, f.asset, "DISPOSED", inactive.Version)
	if err != nil || disposed.LifecycleState != "DISPOSED" {
		t.Fatalf("dispose = %+v %v", disposed, err)
	}
	if _, err := f.s.ActivateAsset(f.ctx, f.asset, disposed.Version); problemCode(err) != CodeAssetState {
		t.Fatalf("reactivating a disposed asset: %v, want %s", err, CodeAssetState)
	}
	created, err := f.s.CreateAsset(f.ctx, Asset{Name: "Spare", AssetType: "PUMP", SiteID: testkit.TenantID(9), Criticality: 2}, "spare")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.ChangeAssetLifecycle(f.ctx, created.ID, "INACTIVE", created.Version); problemCode(err) != CodeAssetState {
		t.Fatalf("CREATED to INACTIVE: %v, want %s", err, CodeAssetState)
	}
	if got, err := f.s.ChangeAssetLifecycle(f.ctx, created.ID, "DISPOSED", created.Version); err != nil || got.LifecycleState != "DISPOSED" {
		t.Fatalf("scrap before service = %+v %v", got, err)
	}
}

// TestDowntime_BR445: a window on the production resource adds its minutes to
// the order once, emits downtime.recorded for wave 23, refuses overlaps, and
// a completion cannot report less downtime than the recorded windows.
func TestDowntime_BR445(t *testing.T) {
	f := setup(t)
	wo, v := f.inProgress(t, "INSPECTION", "dt-wo")
	from, to := clock.Add(-6*time.Hour), clock.Add(-2*time.Hour)
	if _, err := f.s.RecordDowntime(f.ctx, wo.ID, DowntimeInput{From: from, To: to}, "dt-0"); problemCode(err) != CodeDowntime {
		t.Fatalf("no production resource: %v, want %s", err, CodeDowntime)
	}
	m12 := testkit.TenantID(12)
	got, err := f.s.RecordDowntime(f.ctx, wo.ID, DowntimeInput{ResourceID: m12, From: from, To: to, Actor: "tech-1"}, "dt-1")
	if err != nil || got.DowntimeMinutes != 240 || len(got.Downtime) != 1 || got.Downtime[0].ResourceID != m12 {
		t.Fatalf("downtime = %+v %v, want 240 minutes on M-12", got, err)
	}
	if again, err := f.s.RecordDowntime(f.ctx, wo.ID, DowntimeInput{ResourceID: m12, From: from, To: to}, "dt-1"); err != nil || again.DowntimeMinutes != 240 {
		t.Fatalf("replay = %d %v, want 240", again.DowntimeMinutes, err)
	}
	if _, err := f.s.RecordDowntime(f.ctx, wo.ID, DowntimeInput{ResourceID: m12, From: to.Add(-time.Hour), To: to.Add(time.Hour)}, "dt-2"); problemCode(err) != CodeDowntime {
		t.Fatalf("overlapping window: %v, want %s", err, CodeDowntime)
	}
	if got, err := f.s.RecordDowntime(f.ctx, wo.ID, DowntimeInput{ResourceID: m12, From: to, To: to.Add(time.Hour)}, "dt-3"); err != nil || got.DowntimeMinutes != 300 {
		t.Fatalf("adjacent window = %d %v, want 300", got.DowntimeMinutes, err)
	}
	if n := f.count(t, `select count(*) from outbox where aggregate_id = $1 and type = 'dev.passage.eam.downtime.recorded.v1'
	    and payload->'data'->>'resourceId' = $2`, wo.ID, m12); n != 2 {
		t.Fatalf("downtime events = %d, want 2", n)
	}
	if _, err := f.s.TransitionWorkOrder(f.ctx, wo.ID, Transition{To: domain.StatusCompleted, Version: v, DowntimeMinutes: ptr(100)}, "dt-c1"); problemCode(err) != CodeDowntime {
		t.Fatalf("completion below the windows: %v, want %s", err, CodeDowntime)
	}
	done, err := f.s.TransitionWorkOrder(f.ctx, wo.ID, Transition{To: domain.StatusCompleted, Version: v}, "dt-c2")
	if err != nil || done.DowntimeMinutes != 300 {
		t.Fatalf("completion = %+v %v, want the 300 recorded minutes", done, err)
	}
	if _, err := f.s.RecordDowntime(f.ctx, wo.ID, DowntimeInput{ResourceID: m12, From: clock, To: clock.Add(time.Hour)}, "dt-4"); problemCode(err) != CodeWOTransition {
		t.Fatalf("downtime on a completed order: %v, want %s", err, CodeWOTransition)
	}

	// The asset's production resource is the default.
	press, err := f.s.CreateAsset(f.ctx, Asset{Name: "Press", AssetType: "PRESS", SiteID: testkit.TenantID(9), Criticality: 3,
		ProductionResourceID: &m12}, "press")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.ActivateAsset(f.ctx, press.ID, press.Version); err != nil {
		t.Fatal(err)
	}
	pwo, err := f.s.CreateWorkOrder(f.ctx, WorkOrder{AssetID: press.ID, Type: "INSPECTION", Currency: "USD"}, "press-wo")
	if err != nil {
		t.Fatal(err)
	}
	pd, err := f.s.RecordDowntime(f.ctx, pwo.ID, DowntimeInput{From: from, To: to}, "press-dt")
	if err != nil || len(pd.Downtime) != 1 || pd.Downtime[0].ResourceID != m12 {
		t.Fatalf("default resource = %+v %v", pd.Downtime, err)
	}
	if _, err := f.s.RecordDowntime(f.ctx, pwo.ID, DowntimeInput{From: from, To: from}, "press-dt2"); problemCode(err) != CodeValidation {
		t.Fatalf("empty window: %v, want %s", err, CodeValidation)
	}
}

// TestTriage_FR446: triage, defer and reject with an append-only history,
// one event per change, replay by key, and final states.
func TestTriage_FR446(t *testing.T) {
	f := setup(t)
	req, err := f.s.CreateRequest(f.ctx, Request{AssetID: f.asset, Source: "USER", Severity: 3, Description: "Rattle"}, "req")
	if err != nil {
		t.Fatal(err)
	}
	deferred, err := f.s.TriageRequest(f.ctx, req.ID, TriageInput{Action: "DEFER", Note: "next shutdown", Actor: "pl-1"}, "tr-1")
	if err != nil || deferred.Status != "DEFERRED" || deref(deferred.TriageNote, "") != "next shutdown" {
		t.Fatalf("defer = %+v %v", deferred, err)
	}
	if replay, err := f.s.TriageRequest(f.ctx, req.ID, TriageInput{Action: "REJECT"}, "tr-1"); err != nil || replay.Status != "DEFERRED" {
		t.Fatalf("replay with another action = %+v %v, want the request unchanged", replay, err)
	}
	triaged, err := f.s.TriageRequest(f.ctx, req.ID, TriageInput{Action: "TRIAGE"}, "tr-2")
	if err != nil || triaged.Status != "TRIAGED" || deref(triaged.TriageNote, "") != "next shutdown" {
		t.Fatalf("triage = %+v %v", triaged, err)
	}
	if same, err := f.s.TriageRequest(f.ctx, req.ID, TriageInput{Action: "TRIAGE"}, "tr-3"); err != nil || same.Status != "TRIAGED" {
		t.Fatalf("repeat triage = %+v %v", same, err)
	}
	if n := f.count(t, `select count(*) from outbox where aggregate_id = $1 and type = 'dev.passage.eam.request.triaged.v1'`, req.ID); n != 2 {
		t.Fatalf("triage events = %d, want 2", n)
	}
	if n := f.count(t, `select count(*) from maintenance_request_triage where request_id = $1`, req.ID); n != 2 {
		t.Fatalf("history rows = %d, want 2", n)
	}
	wo, err := f.s.ConvertRequest(f.ctx, req.ID, "USD", "conv")
	if err != nil || wo.RequestID == nil {
		t.Fatalf("convert a triaged request = %+v %v", wo, err)
	}
	if _, err := f.s.TriageRequest(f.ctx, req.ID, TriageInput{Action: "DEFER"}, "tr-4"); problemCode(err) != CodeRequestState {
		t.Fatalf("triage a converted request: %v, want %s", err, CodeRequestState)
	}
	other, err := f.s.CreateRequest(f.ctx, Request{AssetID: f.asset, Source: "MOBILE", Severity: 4, Description: "Drip"}, "req-2")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.TriageRequest(f.ctx, other.ID, TriageInput{Action: "TRIAGE"}, "tr-1"); problemCode(err) != CodeKeyReuse {
		t.Fatalf("key reused on another request: %v, want %s", err, CodeKeyReuse)
	}
	if rej, err := f.s.TriageRequest(f.ctx, other.ID, TriageInput{Action: "REJECT"}, "tr-5"); err != nil || rej.Status != "REJECTED" {
		t.Fatalf("reject = %+v %v", rej, err)
	}
	if _, err := f.s.TriageRequest(f.ctx, other.ID, TriageInput{Action: "TRIAGE"}, "tr-6"); problemCode(err) != CodeRequestState {
		t.Fatalf("triage a rejected request: %v, want %s", err, CodeRequestState)
	}
	if _, err := f.s.ConvertRequest(f.ctx, other.ID, "USD", "conv-2"); problemCode(err) != CodeRequestState {
		t.Fatalf("convert a rejected request: %v, want %s", err, CodeRequestState)
	}
}

// TestWarranty_AC4413: a 24-month parts-only warranty from 2025-11-01 covers
// an order created on 2026-10-21; completion claims the parts amount and none
// of the labor. Excluded job types and dates past the end are not covered.
func TestWarranty_AC4413(t *testing.T) {
	f := setup(t)
	vendor := testkit.TenantID(8)
	w, err := f.s.CreateWarranty(f.ctx, f.asset, Warranty{WarrantyType: "OEM", VendorPartyID: vendor, StartBasis: "PURCHASE",
		StartsOn: "2025-11-01", Months: 24, PartsCovered: true, Exclusions: []string{"PAINT"}}, "war-1")
	if err != nil || w.EndsOn != "2027-11-01" || w.Status != "ACTIVE" {
		t.Fatalf("warranty = %+v %v", w, err)
	}
	if again, err := f.s.CreateWarranty(f.ctx, f.asset, Warranty{WarrantyType: "OEM", VendorPartyID: vendor, StartBasis: "PURCHASE",
		StartsOn: "2025-11-01", Months: 24, PartsCovered: true}, "war-1"); err != nil || again.ID != w.ID {
		t.Fatalf("replay = %+v %v", again, err)
	}
	if _, err := f.s.CreateWarranty(f.ctx, f.asset, Warranty{WarrantyType: "OEM", VendorPartyID: vendor, StartBasis: "PURCHASE",
		StartsOn: "2025-11-01", Months: 24}, "war-x"); problemCode(err) != CodeValidation {
		t.Fatalf("no coverage: %v, want %s", err, CodeValidation)
	}

	wo, v := f.inProgress(t, "CORRECTIVE", "war-wo", func(w *WorkOrder) { w.JobType = "BEARING" })
	if !wo.WarrantyCovered || wo.WarrantyID == nil || *wo.WarrantyID != w.ID {
		t.Fatalf("order = %+v, want covered by %s", wo, w.ID)
	}
	if excluded, err := f.s.CreateWorkOrder(f.ctx, WorkOrder{AssetID: f.asset, Type: "CORRECTIVE", Currency: "USD", JobType: "PAINT"}, "war-paint"); err != nil || excluded.WarrantyCovered {
		t.Fatalf("excluded job type = %+v %v, want not covered", excluded, err)
	}
	late := time.Date(2027, 11, 1, 0, 0, 0, 0, time.UTC)
	if after, err := f.s.CreateWorkOrder(f.ctx, WorkOrder{AssetID: f.asset, Type: "INSPECTION", Currency: "USD", DueAt: &late}, "war-late"); err != nil || after.WarrantyCovered {
		t.Fatalf("due after the end = %+v %v, want not covered", after, err)
	}

	// 350.00 of parts (the wave-16 issue is not built, so the test sets the
	// cost) and 8 h of labor at 45.00.
	f.inTenant(t, func(tx pgx.Tx) {
		if _, err := tx.Exec(f.ctx, `update work_order set parts_cost = 350 where id = $1`, wo.ID); err != nil {
			t.Fatal(err)
		}
	})
	if _, err := f.s.RecordLabor(f.ctx, wo.ID, decimal.NewFromInt(8), decimal.RequireFromString("45.00"), nil, "war-lab"); err != nil {
		t.Fatal(err)
	}
	f.fault(t, wo.ID)
	if _, err := f.s.TransitionWorkOrder(f.ctx, wo.ID, Transition{To: domain.StatusCompleted, Version: v}, "war-c"); err != nil {
		t.Fatal(err)
	}
	if n := f.count(t, `select count(*) from outbox where aggregate_id = $1 and type = 'dev.passage.eam.warranty.claimable.v1'
	    and payload->'data'->>'coveredParts' = '350.00' and payload->'data'->>'coveredLabor' = '0.00'
	    and payload->'data'->>'vendorPartyId' = $2 and payload->'data'->>'warrantyId' = $3`, wo.ID, vendor, w.ID); n != 1 {
		t.Fatalf("claimable events with the part amount = %d, want 1", n)
	}

	// A covered order with nothing covered to claim emits no claim.
	none, nv := f.inProgress(t, "INSPECTION", "war-none")
	if !none.WarrantyCovered {
		t.Fatal("inspection order should be covered")
	}
	if _, err := f.s.RecordLabor(f.ctx, none.ID, decimal.NewFromInt(1), decimal.NewFromInt(50), nil, "war-lab2"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.TransitionWorkOrder(f.ctx, none.ID, Transition{To: domain.StatusCompleted, Version: nv}, "war-c2"); err != nil {
		t.Fatal(err)
	}
	if n := f.count(t, `select count(*) from outbox where aggregate_id = $1 and type = 'dev.passage.eam.warranty.claimable.v1'`, none.ID); n != 0 {
		t.Fatalf("claim for labor on a parts-only warranty = %d, want 0", n)
	}
	list, err := f.s.ListWarranties(f.ctx, f.asset)
	if err != nil || len(list) != 1 {
		t.Fatalf("warranties = %+v %v", list, err)
	}
}
