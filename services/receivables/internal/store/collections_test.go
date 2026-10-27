package store

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/services/receivables/internal/domain"
	rcvevents "github.com/BenjaminSRussell/passage/services/receivables/internal/events"
)

const (
	custA = "00000000-0000-0000-0000-0000000000ca"
	custB = "00000000-0000-0000-0000-0000000000cb"
	custC = "00000000-0000-0000-0000-0000000000cc"
)

// seedItem posts an invoice with the given number, customer, due date and amount.
func seedItem(t *testing.T, s *Postgres, ctx context.Context, doc, customer, due, amount, currency string) OpenItem {
	t.Helper()
	item, err := s.CreateOpenItem(ctx, OpenItem{LegalEntityID: legalEntity, CustomerID: customer, SourceType: "INVOICE",
		DocumentNumber: doc, DocumentDate: "2027-01-01", DueDate: due, Currency: currency, Amount: amt(amount), OpenAmount: amt(amount)})
	if err != nil {
		t.Fatalf("seed %s: %v", doc, err)
	}
	return item
}

// execTenant runs a statement as the test tenant, so row-level security applies.
func execTenant(t *testing.T, s *Postgres, ctx context.Context, sql string, args ...any) {
	t.Helper()
	if err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, sql, args...)
		return err
	}); err != nil {
		t.Fatalf("exec %q: %v", sql, err)
	}
}

func exclusionReasons(run DunningRun) map[string]string {
	out := map[string]string{}
	for _, e := range run.Exclusions {
		out[e.OpenItemID] = e.Reason
	}
	return out
}

func TestDispute_ExcludesFromDunningUntilResolved(t *testing.T) {
	s := setup(t)
	ctx := tenantCtx(10)
	disputed := seedItem(t, s, ctx, "INV-D1", custA, "2027-02-01", "1000.00", "USD")
	other := seedItem(t, s, ctx, "INV-D2", custB, "2027-02-01", "1000.00", "USD")

	in := Dispute{LegalEntityID: legalEntity, OpenItemID: disputed.ID, ReasonCode: "PRICE", DisputedAmount: amt("500.00"), Owner: "ana"}
	d, err := s.OpenDispute(ctx, in, "dsp-1")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if d.Status != domain.DisputeOpen || d.CustomerID != custA || d.Currency != "USD" || d.DocumentNumber != "INV-D1" || d.Version != 1 {
		t.Fatalf("dispute = %+v", d)
	}
	if replay, err := s.OpenDispute(ctx, in, "dsp-1"); err != nil || replay.ID != d.ID {
		t.Fatalf("replay = %v (%v), want %s", replay.ID, err, d.ID)
	}
	changed := in
	changed.DisputedAmount = amt("400.00")
	if _, err := s.OpenDispute(ctx, changed, "dsp-1"); problemCode(err) != CodeIdempotencyReuse {
		t.Fatalf("same key, other body: %v", err)
	}
	if _, err := s.OpenDispute(ctx, in, "dsp-2"); problemCode(err) != domain.CodeDisputeExists {
		t.Fatalf("second active dispute: %v", err)
	}
	over := in
	over.OpenItemID, over.DisputedAmount = other.ID, amt("1000.01")
	if _, err := s.OpenDispute(ctx, over, "dsp-3"); problemCode(err) != domain.CodeAmountRange {
		t.Fatalf("amount above open: %v", err)
	}

	// AC-30.9: the disputed item is on no letter.
	run, err := s.CreateDunningRun(ctx, DunningRun{LegalEntityID: legalEntity, RunDate: "2027-03-31", GraceDays: 5}, "run-1")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(run.Letters) != 1 || run.Letters[0].CustomerID != custB {
		t.Fatalf("letters = %+v, want one for the undisputed customer", run.Letters)
	}
	if exclusionReasons(run)[disputed.ID] != domain.ReasonDispute {
		t.Errorf("exclusions = %+v, want the disputed item as DISPUTE", run.Exclusions)
	}

	if _, err := s.TransitionDispute(ctx, d.ID, 1, domain.DisputeResolvedCredit, nil); problemCode(err) != domain.CodeDisputeState {
		t.Fatalf("OPEN straight to RESOLVED_CREDIT: %v", err)
	}
	if _, err := s.TransitionDispute(ctx, d.ID, 7, domain.DisputeUnderReview, nil); problemCode(err) != CodeVersionConflict {
		t.Fatalf("stale version: %v", err)
	}
	reviewed, err := s.TransitionDispute(ctx, d.ID, 1, domain.DisputeUnderReview, nil)
	if err != nil || reviewed.Status != domain.DisputeUnderReview || reviewed.Version != 2 || reviewed.ResolvedAt != nil {
		t.Fatalf("review = %+v (%v)", reviewed, err)
	}
	active, err := s.ListDisputes(ctx, DisputeFilter{Active: true}, 50)
	if err != nil || len(active) != 1 {
		t.Fatalf("active disputes = %d (%v), want 1", len(active), err)
	}
	note := "credit memo CM-7"
	resolved, err := s.TransitionDispute(ctx, d.ID, 2, domain.DisputeResolvedCredit, &note)
	if err != nil || resolved.Status != domain.DisputeResolvedCredit || resolved.ResolvedAt == nil || resolved.ResolutionNote == nil {
		t.Fatalf("resolve = %+v (%v)", resolved, err)
	}
	if active, _ := s.ListDisputes(ctx, DisputeFilter{Active: true}, 50); len(active) != 0 {
		t.Errorf("active disputes after resolution = %d, want 0", len(active))
	}
	if got := countOutbox(t, s, ctx, rcvevents.DisputeOpened); got != 1 {
		t.Errorf("dispute.opened events = %d, want 1", got)
	}
	if got := countOutbox(t, s, ctx, rcvevents.DisputeResolved); got != 1 {
		t.Errorf("dispute.resolved events = %d, want 1", got)
	}
	var resolution string
	if err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `select payload->'data'->>'resolution' from outbox where type = $1`, rcvevents.DisputeResolved).Scan(&resolution)
	}); err != nil || resolution != "CREDIT" {
		t.Errorf("resolved event resolution = %q (%v), want CREDIT as the AsyncAPI enum names it", resolution, err)
	}

	// Resolved: the item is dunnable again.
	again, err := s.CreateDunningRun(ctx, DunningRun{LegalEntityID: legalEntity, RunDate: "2027-03-31", GraceDays: 5}, "run-2")
	if err != nil || len(again.Letters) != 2 {
		t.Fatalf("letters after resolution = %+v (%v), want two", again.Letters, err)
	}

	if others, err := s.ListDisputes(tenantCtx(11), DisputeFilter{}, 50); err != nil || len(others) != 0 {
		t.Errorf("another tenant sees %d disputes (%v)", len(others), err)
	}
}

func TestPromise_HoldsExcludeAndBlockASecond(t *testing.T) {
	s := setup(t)
	ctx := tenantCtx(12)
	item := seedItem(t, s, ctx, "INV-P1", custA, "2027-02-01", "800.00", "USD")
	in := Promise{LegalEntityID: legalEntity, OpenItemID: item.ID, Amount: amt("800.00"), PromiseDate: "2099-01-15"}
	pr, err := s.CreatePromise(ctx, in, "prm-1")
	if err != nil || pr.CustomerID != custA || pr.DocumentNumber != "INV-P1" || pr.Currency != "USD" {
		t.Fatalf("promise = %+v (%v)", pr, err)
	}
	if replay, err := s.CreatePromise(ctx, in, "prm-1"); err != nil || replay.ID != pr.ID {
		t.Fatalf("replay = %v (%v)", replay.ID, err)
	}
	if _, err := s.CreatePromise(ctx, in, "prm-2"); problemCode(err) != domain.CodePromiseExists {
		t.Fatalf("second holding promise: %v", err)
	}
	run, err := s.CreateDunningRun(ctx, DunningRun{LegalEntityID: legalEntity, RunDate: "2027-03-31", GraceDays: 5}, "run-p")
	if err != nil || len(run.Letters) != 0 || exclusionReasons(run)[item.ID] != domain.ReasonPromise {
		t.Fatalf("run = %+v (%v), want the promised item excluded as PROMISE", run, err)
	}
	list, err := s.ListPromises(ctx, PromiseFilter{OpenItemID: item.ID}, 50)
	if err != nil || len(list) != 1 || list[0].ID != pr.ID {
		t.Fatalf("promises = %+v (%v)", list, err)
	}
}

func TestDunningRun_IssueRaisesLevelsPostsFeesAndKeepsTheInterval(t *testing.T) {
	s := setup(t)
	ctx := tenantCtx(13)
	item := seedItem(t, s, ctx, "INV-R1", custA, "2027-02-01", "1000.00", "USD")
	execTenant(t, s, ctx, `update open_item set dunning_level = 2 where id = $1`, item.ID)
	blocked := seedItem(t, s, ctx, "INV-R2", custB, "2027-02-01", "1000.00", "USD")
	execTenant(t, s, ctx, `update open_item set dunning_block = true where id = $1`, blocked.ID)
	small := seedItem(t, s, ctx, "INV-R3", custC, "2027-02-01", "40.00", "USD")

	run, err := s.CreateDunningRun(ctx, DunningRun{LegalEntityID: legalEntity, RunDate: "2027-03-31", GraceDays: 5}, "run-r")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if run.Status != "DRAFT" || len(run.Letters) != 1 {
		t.Fatalf("run = %+v, want one DRAFT letter", run)
	}
	l := run.Letters[0]
	// 58 days overdue earns level 4, but an item rises one level per run: 2 -> 3, with the 25.00 fee.
	if l.CustomerID != custA || l.Level != 3 || !l.FeeAmount.Equal(amt("25")) || len(l.Items) != 1 || l.Items[0].DaysOverdue != 58 {
		t.Fatalf("letter = %+v", l)
	}
	reasons := exclusionReasons(run)
	if reasons[blocked.ID] != domain.ReasonBlocked || reasons[small.ID] != domain.ReasonBelowMinimum {
		t.Errorf("exclusions = %v", reasons)
	}
	if replay, err := s.CreateDunningRun(ctx, DunningRun{LegalEntityID: legalEntity, RunDate: "2027-03-31", GraceDays: 5}, "run-r"); err != nil || replay.ID != run.ID {
		t.Fatalf("replay = %v (%v)", replay.ID, err)
	}

	if _, err := s.TransitionDunningRun(ctx, run.ID, 1, "ISSUED"); problemCode(err) != domain.CodeRunNotReviewed {
		t.Fatalf("issue a DRAFT: %v", err)
	}
	if _, err := s.TransitionDunningRun(ctx, run.ID, 9, "REVIEWED"); problemCode(err) != CodeVersionConflict {
		t.Fatalf("stale version: %v", err)
	}
	if countOutbox(t, s, ctx, rcvevents.DunningLetterIssued) != 0 {
		t.Fatal("nothing may be published before issue")
	}
	reviewed, err := s.TransitionDunningRun(ctx, run.ID, 1, "REVIEWED")
	if err != nil || reviewed.Status != "REVIEWED" {
		t.Fatalf("review = %+v (%v)", reviewed, err)
	}
	issued, err := s.TransitionDunningRun(ctx, run.ID, reviewed.Version, "ISSUED")
	if err != nil || issued.Status != "ISSUED" || issued.IssuedAt == nil {
		t.Fatalf("issue = %+v (%v)", issued, err)
	}
	if lt := issued.Letters[0]; lt.Status != "ISSUED" || lt.DocumentRef == nil {
		t.Errorf("issued letter = %+v", lt)
	}
	items, err := s.ListOpenItems(ctx, OpenItemFilter{CustomerID: custA, OpenOnly: true}, 50)
	if err != nil {
		t.Fatal(err)
	}
	var fee *OpenItem
	for i := range items {
		switch items[i].SourceType {
		case "INVOICE":
			if items[i].DunningLevel != 3 {
				t.Errorf("item level after issue = %d, want 3", items[i].DunningLevel)
			}
		case "FEE":
			fee = &items[i]
		}
	}
	if fee == nil || !fee.Amount.Equal(amt("25")) || fee.Currency != "USD" {
		t.Fatalf("fee item = %+v, want a 25.00 USD FEE open item", fee)
	}
	if got := countOutbox(t, s, ctx, rcvevents.DunningLetterIssued); got != 1 {
		t.Errorf("dunningletter.issued events = %d, want 1", got)
	}
	if _, err := s.TransitionDunningRun(ctx, run.ID, issued.Version, "CANCELLED"); problemCode(err) != domain.CodeRunState {
		t.Fatalf("cancel an issued run: %v", err)
	}

	// AC-30.4: a run 7 days after the letter sends nothing (10-day interval).
	next, err := s.CreateDunningRun(ctx, DunningRun{LegalEntityID: legalEntity, RunDate: "2027-04-07", GraceDays: 5}, "run-r2")
	if err != nil {
		t.Fatal(err)
	}
	if len(next.Letters) != 0 || exclusionReasons(next)[item.ID] != domain.ReasonInterval {
		t.Errorf("run within the interval = %+v, want no letter and an INTERVAL exclusion", next)
	}
	runs, err := s.ListDunningRuns(ctx, "", 50)
	if err != nil || len(runs) != 2 || runs[0].ID != next.ID || runs[1].LetterCount != 1 {
		t.Errorf("runs = %+v (%v)", runs, err)
	}
}

func TestDunningRun_IssueDropsALetterWhoseItemWasDisputedAfterTheProposal(t *testing.T) {
	s := setup(t)
	ctx := tenantCtx(14)
	item := seedItem(t, s, ctx, "INV-X1", custA, "2027-02-01", "1000.00", "USD")
	run, err := s.CreateDunningRun(ctx, DunningRun{LegalEntityID: legalEntity, RunDate: "2027-03-31", GraceDays: 5}, "run-x")
	if err != nil || len(run.Letters) != 1 {
		t.Fatalf("run = %+v (%v)", run, err)
	}
	if _, err := s.OpenDispute(ctx, Dispute{LegalEntityID: legalEntity, OpenItemID: item.ID, ReasonCode: "DAMAGE",
		DisputedAmount: amt("100.00"), Owner: "ana"}, "dsp-x"); err != nil {
		t.Fatal(err)
	}
	reviewed, err := s.TransitionDunningRun(ctx, run.ID, 1, "REVIEWED")
	if err != nil {
		t.Fatal(err)
	}
	issued, err := s.TransitionDunningRun(ctx, run.ID, reviewed.Version, "ISSUED")
	if err != nil {
		t.Fatal(err)
	}
	if issued.Letters[0].Status != "SKIPPED" {
		t.Errorf("letter = %+v, want SKIPPED", issued.Letters[0])
	}
	if got := countOutbox(t, s, ctx, rcvevents.DunningLetterIssued); got != 0 {
		t.Errorf("events = %d, want none for a skipped letter", got)
	}
	items, _ := s.ListOpenItems(ctx, OpenItemFilter{CustomerID: custA, OpenOnly: true}, 50)
	if len(items) != 1 || items[0].DunningLevel != 0 {
		t.Errorf("items = %+v, want the disputed item untouched and no fee", items)
	}
}

func TestInterest_PostOncePerPeriodChargesNewDaysOnlyAndReplays(t *testing.T) {
	s := setup(t)
	ctx := tenantCtx(15)
	item := seedItem(t, s, ctx, "INV-I1", custA, "2027-01-31", "10000.00", "USD")
	req := InterestRequest{LegalEntityID: legalEntity, CalculationDate: "2027-03-31", AnnualRate: amt("0.12"), Basis: domain.BasisAct365, GraceDays: 5}

	preview, err := s.CalculateInterest(ctx, req)
	if err != nil || len(preview.Candidates) != 1 || len(preview.Posted) != 0 {
		t.Fatalf("preview = %+v (%v)", preview, err)
	}
	if notes, _ := s.ListInterestNotes(ctx, "", 50); len(notes) != 0 {
		t.Fatalf("calculate wrote %d notes", len(notes))
	}

	first, err := s.PostInterest(ctx, req, "int-1")
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	// 10,000.00 x 0.12 x 54 / 365 (due 2027-01-31 + 5 grace to 2027-03-31) = 177.53
	if len(first.Posted) != 1 || first.Posted[0].OpenItemID != item.ID || !first.Posted[0].Amount.Equal(amt("177.53")) || first.Posted[0].Days != 54 {
		t.Fatalf("posted = %+v", first.Posted)
	}
	replay, err := s.PostInterest(ctx, req, "int-1")
	if err != nil || len(replay.Posted) != 1 || replay.Posted[0].ID != first.Posted[0].ID {
		t.Fatalf("replay = %+v (%v)", replay.Posted, err)
	}
	other := req
	other.AnnualRate = amt("0.10")
	if _, err := s.PostInterest(ctx, other, "int-1"); problemCode(err) != CodeIdempotencyReuse {
		t.Fatalf("same key, other body: %v", err)
	}
	same, err := s.PostInterest(ctx, req, "int-2")
	if err != nil || len(same.Posted) != 0 {
		t.Fatalf("second post in the period = %+v (%v), want nothing", same.Posted, err)
	}

	next := req
	next.CalculationDate = "2027-04-30"
	april, err := s.PostInterest(ctx, next, "int-3")
	if err != nil {
		t.Fatal(err)
	}
	// Only the 30 days after the March note: 10,000.00 x 0.12 x 30 / 365 = 98.63.
	if len(april.Posted) != 1 || april.Posted[0].Days != 30 || !april.Posted[0].Amount.Equal(amt("98.63")) {
		t.Fatalf("april = %+v", april.Posted)
	}
	notes, err := s.ListInterestNotes(ctx, custA, 50)
	if err != nil || len(notes) != 2 || notes[0].CalculationDate != "2027-04-30" || notes[0].DocumentNumber != "INV-I1" {
		t.Fatalf("notes = %+v (%v)", notes, err)
	}
	items, _ := s.ListOpenItems(ctx, OpenItemFilter{CustomerID: custA, OpenOnly: true}, 50)
	interest := 0
	for _, it := range items {
		if it.SourceType == "INTEREST" {
			interest++
		}
	}
	if interest != 2 {
		t.Errorf("INTEREST open items = %d, want 2", interest)
	}
}
