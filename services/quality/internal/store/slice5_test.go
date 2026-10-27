package store

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/quality/internal/domain"
	qevents "github.com/BenjaminSRussell/passage/services/quality/internal/events"
)

// TestPostgres_StudyLifecycle moves a study PLANNED -> ACTIVE -> TERMINATED under
// If-Match, cancels its PLANNED pulls, and writes stabilitystudy.statuschanged.
func TestPostgres_StudyLifecycle(t *testing.T) {
	s, pool, ctx := setup(t)
	group, _, err := s.CreateTestGroup(ctx, domain.TestGroup{Code: "STAB-LT", Tests: []domain.TestLine{
		{TestID: ids.New(), Kind: domain.KindAttribute, Required: true, PassingOutcomes: []string{ids.New()}}}}, "")
	if err != nil {
		t.Fatal(err)
	}
	st, err := domain.NewStudy(domain.StudySpec{ItemID: ids.New(), BatchID: "B-9", Protocol: "ICH Q1A(R2)", Condition: "25C/60RH",
		StartDate: time.Date(2026, 1, 31, 0, 0, 0, 0, time.UTC), TestGroupID: group.ID, PullMonths: []int{0, 3, 6}, LeadDays: 7, WindowDays: 3})
	if err != nil {
		t.Fatal(err)
	}
	st.ID = ids.New()
	if _, _, err := s.CreateStudy(ctx, st, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `update quality_pull_point set status = 'ORDERED' where study_id = $1 and months = 0`, st.ID); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 2, 1, 9, 0, 0, 0, time.UTC)
	move := func(version int, to, reason string) (domain.Study, error) {
		return s.TransitionStudy(ctx, st.ID, version, func(cur domain.Study) (domain.Study, error) {
			return cur.Transition(to, reason, "stab-1", now)
		})
	}
	if _, err := move(2, domain.StudyActive, ""); problemStatus(err) != http.StatusPreconditionFailed {
		t.Fatalf("a stale version must be 412, got %v", err)
	}
	active, err := move(1, domain.StudyActive, "")
	if err != nil || active.Status != domain.StudyActive || active.Version != 2 || active.StatusChangedBy != "stab-1" ||
		active.StatusChangedAt == nil || !active.StatusChangedAt.Equal(now) {
		t.Fatalf("PLANNED -> ACTIVE: %+v %v", active, err)
	}
	if _, err := move(2, domain.StudyPlanned, ""); err == nil {
		t.Fatal("an ACTIVE study cannot go back to PLANNED")
	}
	ended, err := move(2, domain.StudyTerminated, "chamber 3 failed")
	if err != nil || ended.Status != domain.StudyTerminated || ended.Version != 3 || ended.StatusReason != "chamber 3 failed" {
		t.Fatalf("ACTIVE -> TERMINATED: %+v %v", ended, err)
	}
	got, err := s.GetStudy(ctx, st.ID)
	if err != nil || got.Pulls[0].Status != domain.PullOrdered || got.Pulls[1].Status != domain.PullCancelled || got.Pulls[2].Status != domain.PullCancelled {
		t.Fatalf("stored pulls after termination: %+v %v", got.Pulls, err)
	}
	if types := outboxTypes(t, pool, st.ID); len(types) != 3 || types[1] != qevents.StabilityStudyStatusChanged || types[2] != qevents.StabilityStudyStatusChanged {
		t.Fatalf("study events: %v", types)
	}
	var cancelled string
	if err := pool.QueryRow(ctx, `select (payload->'data'->'cancelled_pulls')::text from outbox where aggregate_id = $1 order by created_at desc, id desc limit 1`, st.ID).Scan(&cancelled); err != nil || cancelled != "[3, 6]" {
		t.Fatalf("the termination event names the cancelled pulls: %q %v", cancelled, err)
	}
	if _, err := s.TransitionStudy(testkit.Ctx(testkit.TenantID(2), "QM_STABILITY_MAINTAIN"), st.ID, 3, func(cur domain.Study) (domain.Study, error) {
		return cur, nil
	}); problemStatus(err) != http.StatusNotFound {
		t.Fatalf("another tenant cannot see the study: %v", err)
	}
}

// closedOrder creates an order with its passing result and decision lines and returns it closed.
func closedOrder(t *testing.T, s *Postgres, ctx context.Context, trigger string, source domain.SourceRef, batch string, lines []domain.DecisionLine) (domain.Order, domain.TestGroup) {
	t.Helper()
	outcome := ids.New()
	group, _, err := s.CreateTestGroup(ctx, domain.TestGroup{Code: "GRP-S5", Tests: []domain.TestLine{
		{TestID: ids.New(), Name: "appearance", Kind: domain.KindAttribute, Required: true, PassingOutcomes: []string{outcome}},
	}}, "")
	if err != nil {
		t.Fatal(err)
	}
	lot := decimal.Zero
	for _, l := range lines {
		lot = lot.Add(l.Quantity)
	}
	order := domain.Order{ID: ids.New(), Trigger: trigger, SourceRef: source, ItemID: ids.New(), SiteID: ids.New(), WarehouseID: ids.New(),
		Dims: map[string]string{"batch": batch}, LotQty: lot, SampleQty: decimal.NewFromInt(1), UOM: "EA", TestGroupID: group.ID,
		TestGroupVersion: 1, SamplingMethod: domain.MethodFixed, Severity: domain.SeverityNormal, AcceptNo: 0, RejectNo: 1}
	if _, _, err := s.CreateOrder(ctx, order, ""); err != nil {
		t.Fatal(err)
	}
	batchResults := []domain.Result{{TestID: group.Tests[0].TestID, SampleNo: 1, OutcomeID: outcome, Passed: true, RecordedBy: "lab-1", RecordedAt: time.Now().UTC().Add(-time.Minute)}}
	if _, _, err := s.RecordResults(ctx, order.ID, 1, domain.StatusPending, batchResults, ""); err != nil {
		t.Fatal(err)
	}
	split, err := domain.SplitDecision(lines, lot)
	if err != nil {
		t.Fatal(err)
	}
	closed, _, err := s.PostUsageDecision(ctx, order.ID, 2, domain.Decision{Lines: lines, Split: split, DecidedBy: "qa-release"}, "")
	if err != nil {
		t.Fatal(err)
	}
	return closed, group
}

// TestPostgres_ReceiptCheckAC3314 is AC-33.14 on the store: an accepted source
// inspection of 1,000 for batch B skips a receipt of 1,000 of B; batch C gets a
// normal order; a later receipt of B finds the cover used up.
func TestPostgres_ReceiptCheckAC3314(t *testing.T) {
	s, _, ctx := setup(t)
	po := ids.New()
	closedOrder(t, s, ctx, domain.TriggerSourceInspect, domain.SourceRef{Type: "po_line", ID: po, Line: "B"}, "B",
		[]domain.DecisionLine{{Decision: domain.DecisionAccept, Quantity: decimal.NewFromInt(1000)}})
	// A rejected source inspection of batch R and an accepted one on another PO line do not count.
	closedOrder(t, s, ctx, domain.TriggerSourceInspect, domain.SourceRef{Type: "po_line", ID: po, Line: "R"}, "R",
		[]domain.DecisionLine{{Decision: domain.DecisionReject, Quantity: decimal.NewFromInt(50)}})
	closedOrder(t, s, ctx, domain.TriggerSourceInspect, domain.SourceRef{Type: "po_line", ID: ids.New()}, "C",
		[]domain.DecisionLine{{Decision: domain.DecisionAccept, Quantity: decimal.NewFromInt(1000)}})
	now := time.Now()
	check := func(in domain.ReceiptCheckInput, key string) (domain.ReceiptCheck, bool, error) {
		return s.CheckReceipt(ctx, in, key, func(accepted, before decimal.Decimal) (domain.ReceiptCheck, error) {
			return domain.CheckReceipt(in, accepted, before, "rcv-1", now)
		})
	}
	b1 := domain.ReceiptCheckInput{POLineID: po, Batch: "B", ReceiptLineID: ids.New(), ReceivedQty: decimal.NewFromInt(1000)}
	got, inserted, err := check(b1, "rk-1")
	if err != nil || !inserted || !got.Skip || got.Code != domain.CodeSourceSkipped || !got.AcceptedQty.Equal(decimal.NewFromInt(1000)) {
		t.Fatalf("batch B is skipped: %+v %v %v", got, inserted, err)
	}
	if again, inserted, err := check(b1, "rk-1"); err != nil || inserted || again.ID != got.ID || again.Code != domain.CodeSourceSkipped {
		t.Fatalf("a replayed key answers the stored check: %+v %v %v", again, inserted, err)
	}
	if again, inserted, err := check(b1, "rk-1b"); err != nil || inserted || again.ID != got.ID {
		t.Fatalf("the same receipt line answers the stored check: %+v %v %v", again, inserted, err)
	}
	changed := b1
	changed.ReceivedQty = decimal.NewFromInt(999)
	if _, _, err := check(changed, "rk-1c"); problemStatus(err) != http.StatusConflict {
		t.Fatalf("the same receipt line with another quantity must be 409, got %v", err)
	}
	c := domain.ReceiptCheckInput{POLineID: po, Batch: "C", ReceiptLineID: ids.New(), ReceivedQty: decimal.NewFromInt(1000)}
	if got, _, err := check(c, "rk-2"); err != nil || got.Skip || !got.NormalQty.Equal(decimal.NewFromInt(1000)) {
		t.Fatalf("batch C gets a normal order: %+v %v", got, err)
	}
	r := domain.ReceiptCheckInput{POLineID: po, Batch: "R", ReceiptLineID: ids.New(), ReceivedQty: decimal.NewFromInt(50)}
	if got, _, err := check(r, "rk-3"); err != nil || got.Skip || !got.AcceptedQty.IsZero() {
		t.Fatalf("a rejected source inspection covers nothing: %+v %v", got, err)
	}
	b2 := domain.ReceiptCheckInput{POLineID: po, Batch: "B", ReceiptLineID: ids.New(), ReceivedQty: decimal.NewFromInt(10)}
	if got, _, err := check(b2, "rk-4"); err != nil || got.Skip || !got.CoveredBefore.Equal(decimal.NewFromInt(1000)) || !got.NormalQty.Equal(decimal.NewFromInt(10)) {
		t.Fatalf("the cover is used up by the first receipt: %+v %v", got, err)
	}
	if _, _, err := check(domain.ReceiptCheckInput{POLineID: po, Batch: "B", ReceiptLineID: ids.New(), ReceivedQty: decimal.NewFromInt(1)}, "rk-1"); problemStatus(err) != http.StatusUnprocessableEntity {
		t.Fatalf("a key reused for another receipt line must be 422, got %v", err)
	}
	list, err := s.ListReceiptChecks(ctx, ReceiptCheckFilter{POLineID: po, Batch: "B", Limit: 10})
	if err != nil || len(list) != 2 || list[0].ReceiptLineID != b2.ReceiptLineID || list[1].Code != domain.CodeSourceSkipped {
		t.Fatalf("list by PO line and batch, newest first: %+v %v", list, err)
	}
	other := testkit.Ctx(testkit.TenantID(2), "QM_VIEW")
	if theirs, err := s.ListReceiptChecks(other, ReceiptCheckFilter{Limit: 10}); err != nil || len(theirs) != 0 {
		t.Fatalf("tenant 2 sees no check of tenant 1: %d %v", len(theirs), err)
	}
}

// TestPostgres_CoARevisions issues a batch CoA, its second revision and a shipment
// CoA, each with coa.issued in the outbox, and refuses a closed order whose
// decision released nothing.
func TestPostgres_CoARevisions(t *testing.T) {
	s, pool, ctx := setup(t)
	order, group := closedOrder(t, s, ctx, domain.TriggerManual, domain.SourceRef{Type: "po_line", ID: ids.New()}, "B-7",
		[]domain.DecisionLine{{Decision: domain.DecisionAccept, Quantity: decimal.NewFromInt(90)},
			{Decision: domain.DecisionAcceptWithDeviation, Quantity: decimal.NewFromInt(10), Reason: "label smudged"}})
	decision, found, err := s.GetDecision(ctx, order.ID)
	if err != nil || !found {
		t.Fatal(err)
	}
	results, err := s.ListResults(ctx, order.ID)
	if err != nil {
		t.Fatal(err)
	}
	issue := func(o domain.Order, d *domain.Decision, req domain.CoARequest, key string) (domain.CoA, bool, error) {
		return s.IssueCoA(ctx, req, key, func(prev *domain.CoA) (domain.CoA, error) {
			return domain.BuildCoA(o, group, d, results, prev, req, "clerk-1", time.Now())
		})
	}
	first, inserted, err := issue(order, &decision, domain.CoARequest{QualityOrderID: order.ID}, "coa-1")
	if err != nil || !inserted || first.Revision != 1 || first.SignedBy != "qa-release" || len(first.Lines) != 1 || first.Deviations[0] != "label smudged" {
		t.Fatalf("first CoA: %+v %v", first, err)
	}
	if again, inserted, err := issue(order, &decision, domain.CoARequest{QualityOrderID: order.ID}, "coa-1"); err != nil || inserted || again.ID != first.ID {
		t.Fatalf("a replayed key answers the stored revision: %+v %v", again, err)
	}
	if _, _, err := issue(order, &decision, domain.CoARequest{QualityOrderID: order.ID}, "coa-2"); err == nil {
		t.Fatal("a second revision without a reason must be refused")
	}
	second, _, err := issue(order, &decision, domain.CoARequest{QualityOrderID: order.ID, Reason: "customer PO corrected"}, "coa-3")
	if err != nil || second.Revision != 2 || second.Supersedes != first.ID {
		t.Fatalf("revision 2: %+v %v", second, err)
	}
	shipment := ids.New()
	ship, _, err := issue(order, &decision, domain.CoARequest{QualityOrderID: order.ID, ShipmentID: shipment}, "coa-4")
	if err != nil || ship.Revision != 1 || ship.ShipmentID != shipment {
		t.Fatalf("shipment CoA: %+v %v", ship, err)
	}
	list, err := s.ListCoAs(ctx, order.ID)
	if err != nil || len(list) != 3 || list[0].ID != first.ID || list[1].ID != second.ID || list[2].ID != ship.ID {
		t.Fatalf("list: batch chain first, then the shipment: %+v %v", list, err)
	}
	if !list[1].AcceptQty.Equal(decimal.NewFromInt(90)) || !list[1].DeviationQty.Equal(decimal.NewFromInt(10)) {
		t.Fatalf("the stored document keeps its decimals: %+v", list[1])
	}
	for _, doc := range []domain.CoA{first, second, ship} {
		if types := outboxTypes(t, pool, doc.ID); len(types) != 1 || types[0] != qevents.CoAIssued {
			t.Fatalf("coa.issued for %s: %v", doc.ID, types)
		}
	}
	rejected, _ := closedOrder(t, s, ctx, domain.TriggerManual, domain.SourceRef{Type: "po_line", ID: ids.New()}, "B-8",
		[]domain.DecisionLine{{Decision: domain.DecisionReject, Quantity: decimal.NewFromInt(5)}})
	rd, _, _ := s.GetDecision(ctx, rejected.ID)
	if _, _, err := issue(rejected, &rd, domain.CoARequest{QualityOrderID: rejected.ID}, "coa-5"); err == nil {
		t.Fatal("a rejected lot gets no CoA")
	}
	if _, _, err := issue(rejected, &rd, domain.CoARequest{QualityOrderID: rejected.ID}, "coa-1"); problemStatus(err) != http.StatusUnprocessableEntity {
		t.Fatalf("a key reused on another order must be 422, got %v", err)
	}
	if _, _, err := issue(order, &decision, domain.CoARequest{QualityOrderID: ids.New()}, "coa-6"); problemStatus(err) != http.StatusNotFound {
		t.Fatalf("an unknown order must be 404, got %v", err)
	}
	if theirs, err := s.ListCoAs(testkit.Ctx(testkit.TenantID(2), "QM_VIEW"), order.ID); err != nil || len(theirs) != 0 {
		t.Fatalf("tenant 2 sees no CoA of tenant 1: %d %v", len(theirs), err)
	}
}

func TestPostgres_InstrumentDueList(t *testing.T) {
	s, _, ctx := setup(t)
	now := time.Now().UTC()
	for code, last := range map[string]time.Time{"OVERDUE-1": now.AddDate(0, 0, -40), "SOON-1": now.AddDate(0, 0, -25), "LATER-1": now} {
		in, err := domain.NewInstrument(code, code, 30, last)
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := s.CreateInstrument(ctx, in, ""); err != nil {
			t.Fatal(err)
		}
	}
	due := now.AddDate(0, 0, 14)
	list, err := s.ListInstruments(ctx, InstrumentFilter{DueBefore: &due, Limit: 10})
	if err != nil || len(list) != 2 || list[0].Code != "OVERDUE-1" || list[1].Code != "SOON-1" {
		t.Fatalf("due within 14 days, soonest first: %+v %v", list, err)
	}
	if all, err := s.ListInstruments(ctx, InstrumentFilter{Limit: 10}); err != nil || len(all) != 3 {
		t.Fatalf("no filter: %+v %v", all, err)
	}
}
