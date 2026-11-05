package store

import (
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/quality/internal/domain"
)

func newNCR(t *testing.T) domain.Nonconformance {
	t.Helper()
	n, err := domain.NewNCR(domain.Nonconformance{Source: domain.SourceInternal, ItemID: ids.New(), Qty: decimal.RequireFromString("3.5"),
		UOM: "EA", ProblemType: "SCRATCH", Severity: domain.NCRMinor, Description: "scratched housing"}, "qe-1", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	n.ID = ids.New()
	return n
}

// TestPostgres_NonconformanceAnd8D creates an NCR, replays its key, filters it,
// approves it and runs its 8D to an effective close, checking the version guard,
// the correction key replay and the events written with each step.
func TestPostgres_NonconformanceAnd8D(t *testing.T) {
	s, pool, ctx := setup(t)
	key := ids.New()
	created, inserted, err := s.CreateNonconformance(ctx, newNCR(t), key)
	if err != nil || !inserted || created.Version != 1 || created.Status != domain.NCRNew || !created.Qty.Equal(decimal.RequireFromString("3.5")) {
		t.Fatalf("create: %+v %v", created, err)
	}
	if again, inserted, err := s.CreateNonconformance(ctx, newNCR(t), key); err != nil || inserted || again.ID != created.ID {
		t.Fatalf("a replayed key returns the stored NCR: %+v %v %v", again, inserted, err)
	}
	if list, err := s.ListNonconformances(ctx, NCRFilter{Status: domain.NCRNew, Severity: domain.NCRMinor, Limit: 10}); err != nil || len(list) != 1 || list[0].ID != created.ID {
		t.Fatalf("list NEW MINOR: %+v %v", list, err)
	}
	if list, _ := s.ListNonconformances(ctx, NCRFilter{Source: domain.SourceVendor, Limit: 10}); len(list) != 0 {
		t.Fatalf("vendor filter: %+v", list)
	}
	if _, err := s.GetNonconformance(testkit.Ctx(testkit.TenantID(2), "quality.view"), created.ID); problemStatus(err) != http.StatusNotFound {
		t.Fatalf("another tenant must not see the NCR: %v", err)
	}

	approve := func(n domain.Nonconformance) (domain.Nonconformance, error) { return n.Approve(time.Now()) }
	if _, _, err := s.UpdateNonconformance(ctx, created.ID, 9, "", approve); problemStatus(err) != http.StatusPreconditionFailed {
		t.Fatalf("stale version must be 412, got %v", err)
	}
	approved, applied, err := s.UpdateNonconformance(ctx, created.ID, 1, "", approve)
	if err != nil || !applied || approved.Status != domain.NCRApproved || approved.Version != 2 {
		t.Fatalf("approve: %+v %v", approved, err)
	}
	var de *domain.Error
	if _, _, err := s.UpdateNonconformance(ctx, created.ID, 2, "", approve); !errors.As(err, &de) || de.Code != domain.CodeNCRStatus {
		t.Fatalf("approving twice must fail with QM-0171 and write nothing, got %v", err)
	}
	if cur, _ := s.GetNonconformance(ctx, created.ID); cur.Version != 2 {
		t.Fatalf("a refused change must not move the version: %d", cur.Version)
	}

	start := time.Now()
	startCAPA := func(n domain.Nonconformance) (domain.Nonconformance, error) {
		return n.StartCorrection("qe-1", 30, start)
	}
	started, applied, err := s.UpdateNonconformance(ctx, created.ID, 2, "capa-1", startCAPA)
	if err != nil || !applied || started.Status != domain.NCRInCorrection || started.Correction == nil || started.Correction.ID == "" || started.Version != 3 {
		t.Fatalf("start correction: %+v %v", started, err)
	}
	if replay, applied, err := s.UpdateNonconformance(ctx, created.ID, 2, "capa-1", startCAPA); err != nil || applied || replay.Version != 3 {
		t.Fatalf("replayed start: %+v %v %v", replay, applied, err)
	}
	other, _, _ := s.CreateNonconformance(ctx, newNCR(t), "")
	if _, _, err := s.UpdateNonconformance(ctx, other.ID, 1, "capa-1", startCAPA); problemStatus(err) != http.StatusUnprocessableEntity {
		t.Fatalf("a correction key from another NCR must be refused: %v", err)
	}

	version := started.Version
	for step := 1; step <= 7; step++ {
		n, _, err := s.UpdateNonconformance(ctx, created.ID, version, "", func(n domain.Nonconformance) (domain.Nonconformance, error) {
			return n.CompleteStep(step, "notes", "qe-1", start)
		})
		if err != nil || n.Correction.CurrentStep != step+1 {
			t.Fatalf("D%d: %+v %v", step, n.Correction, err)
		}
		version = n.Version
	}
	got, err := s.GetNonconformance(ctx, created.ID)
	if err != nil || got.Correction.Status != domain.CAPAEffectivenessPending || got.Correction.Steps[6].CompletedBy != "qe-1" ||
		got.Correction.EffectivenessDueAt == nil || !got.Correction.EffectivenessDueAt.Equal(start.UTC().AddDate(0, 0, 30).Truncate(time.Microsecond)) {
		t.Fatalf("after D7: %+v %v", got.Correction, err)
	}
	later := start.AddDate(0, 0, 31)
	closed, _, err := s.UpdateNonconformance(ctx, created.ID, version, "", func(n domain.Nonconformance) (domain.Nonconformance, error) {
		return n.CheckEffectiveness(domain.Effective, "no recurrence", "qa-2", later)
	})
	if err != nil || closed.Status != domain.NCRClosed || closed.Correction.Status != domain.CAPAClosed || closed.Correction.EffectivenessBy != "qa-2" {
		t.Fatalf("effective check: %+v %v", closed, err)
	}
	want := []string{"dev.passage.quality.nonconformance.created.v1", "dev.passage.quality.capa.closed.v1"}
	if types := outboxTypes(t, pool, created.ID); len(types) != 2 || types[0] != want[0] || types[1] != want[1] {
		t.Fatalf("outbox for the NCR: %v, want %v", types, want)
	}
}

// TestPostgres_RejectDecisionRaisesNCR is AC-33.4's NCR leg: accept 90 / reject 10
// on a lot of 100 raises an NCR for 10 in the decision's transaction, and a replay
// of the decision returns the same NCR.
func TestPostgres_RejectDecisionRaisesNCR(t *testing.T) {
	s, pool, ctx := setup(t)
	outcome := ids.New()
	group, _, err := s.CreateTestGroup(ctx, domain.TestGroup{Code: "GRP-NCR", Tests: []domain.TestLine{
		{TestID: ids.New(), Name: "appearance", Kind: domain.KindAttribute, Required: true, PassingOutcomes: []string{outcome}},
	}}, "")
	if err != nil {
		t.Fatal(err)
	}
	order, _, err := s.CreateOrder(ctx, domain.Order{ID: ids.New(), Trigger: domain.TriggerPurchaseReceipt, SourceRef: domain.SourceRef{Type: "receipt", ID: ids.New(), Line: "1"},
		ItemID: ids.New(), SiteID: ids.New(), WarehouseID: ids.New(), Dims: map[string]string{"batch": "B-1"}, LotQty: decimal.NewFromInt(100),
		SampleQty: decimal.NewFromInt(1), UOM: "EA", TestGroupID: group.ID, TestGroupVersion: 1, SamplingMethod: domain.MethodFixed,
		Severity: domain.SeverityNormal, RejectNo: 1}, "")
	if err != nil {
		t.Fatal(err)
	}
	batch := []domain.Result{{TestID: group.Tests[0].TestID, SampleNo: 1, OutcomeID: ids.New(), Passed: false, RecordedBy: "lab"}}
	if _, _, err := s.RecordResults(ctx, order.ID, 1, domain.StatusPending, batch, ""); err != nil {
		t.Fatal(err)
	}
	lines := []domain.DecisionLine{{Decision: domain.DecisionAccept, Quantity: decimal.NewFromInt(90)}, {Decision: domain.DecisionReject, Quantity: decimal.NewFromInt(10)}}
	split, err := domain.SplitDecision(lines, order.LotQty)
	if err != nil {
		t.Fatal(err)
	}
	_, decision, err := s.PostUsageDecision(ctx, order.ID, 2, domain.Decision{Lines: lines, Split: split, DecidedBy: "qa-1"}, "dec-1")
	if err != nil || decision.NonconformanceID == "" {
		t.Fatalf("decision: %+v %v", decision, err)
	}
	ncr, err := s.GetNonconformance(ctx, decision.NonconformanceID)
	if err != nil || !ncr.Qty.Equal(decimal.NewFromInt(10)) || ncr.QualityOrderID != order.ID || ncr.Source != domain.SourceVendor ||
		ncr.ItemID != order.ItemID || ncr.Dims["batch"] != "B-1" || ncr.Status != domain.NCRNew || ncr.CreatedBy != "qa-1" {
		t.Fatalf("NCR from the decision: %+v %v", ncr, err)
	}
	if byOrder, _ := s.ListNonconformances(ctx, NCRFilter{QualityOrderID: order.ID, Limit: 10}); len(byOrder) != 1 {
		t.Fatalf("NCRs of the order: %+v", byOrder)
	}
	if replay, found, err := s.FindDecisionByKey(ctx, "dec-1"); err != nil || !found || replay.NonconformanceID != ncr.ID {
		t.Fatalf("replayed decision keeps its NCR: %+v %v", replay, err)
	}
	if types := outboxTypes(t, pool, ncr.ID); len(types) != 1 || types[0] != "dev.passage.quality.nonconformance.created.v1" {
		t.Fatalf("NCR event: %v", types)
	}
	// An NCR that names an unknown order is refused by the foreign key.
	n := newNCR(t)
	n.QualityOrderID = ids.New()
	if _, _, err := s.CreateNonconformance(ctx, n, ""); problemStatus(err) != http.StatusNotFound {
		t.Fatalf("unknown quality order: %v", err)
	}
}
