package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/inventory/internal/domain"
	"github.com/BenjaminSRussell/passage/services/inventory/internal/store"
)

func controlPlan(t *testing.T, s *store.Postgres, ctx context.Context, edit func(*domain.CountPlan)) domain.CountPlan {
	t.Helper()
	p := domain.CountPlan{LegalEntityID: legalEntity(ctx), Name: "Aisle count", Currency: "USD", Unit: "EA",
		TolerancePct: dec("5"), ToleranceValue: dec("100")}
	edit(&p)
	out, _, err := s.CreateCountPlan(ctx, ids.New(), p)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	return out
}

func generate(t *testing.T, s *store.Postgres, ctx context.Context, plan domain.CountPlan) domain.CountSheet {
	t.Helper()
	sheet, _, err := s.GenerateCountSheet(ctx, plan.ID, ids.New())
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	return sheet
}

func TestCountPlanSelectsByLocationRangeAndABCClass(t *testing.T) {
	s, _, _ := setup(t)
	ctx := entityCtx(1, ids.New())
	big, small := ids.New(), ids.New()
	post(t, ctx, s, domain.JournalOpeningBalance,
		line(big, "90", "10", dims("location", "A-01")), line(big, "9", "10", dims("location", "B-07")),
		line(small, "1", "10", dims("location", "A-02")))
	if _, _, err := s.RunABC(ctx, store.AbcRunInput{IdempotencyKey: "abc", LegalEntityID: legalEntity(ctx), Criterion: domain.AbcCriterionValue,
		Thresholds: domain.AbcThresholds{A: dec("80"), B: dec("10"), C: dec("10")}, ZeroClass: domain.ABCN}); err != nil {
		t.Fatal(err)
	}

	byRange := generate(t, s, ctx, controlPlan(t, s, ctx, func(p *domain.CountPlan) { p.LocationFrom, p.LocationTo = "A-01", "A-99" }))
	if len(byRange.Lines) != 2 {
		t.Fatalf("location A range selects %d lines, want 2", len(byRange.Lines))
	}
	byClass := generate(t, s, ctx, controlPlan(t, s, ctx, func(p *domain.CountPlan) { p.ABCClass = "A" }))
	for _, l := range byClass.Lines {
		if l.ProductID != big {
			t.Fatalf("class A plan selected %s", l.ProductID)
		}
	}
	if len(byClass.Lines) != 2 {
		t.Fatalf("class A selects %d lines, want the 2 rows of the big product", len(byClass.Lines))
	}
	both := generate(t, s, ctx, controlPlan(t, s, ctx, func(p *domain.CountPlan) { p.ABCClass, p.LocationFrom, p.LocationTo = "A", "B-00", "B-99" }))
	if len(both.Lines) != 1 || both.Lines[0].Dims["location"] != "B-07" {
		t.Fatalf("class and range together: %+v", both.Lines)
	}
	sampled := generate(t, s, ctx, controlPlan(t, s, ctx, func(p *domain.CountPlan) { p.SamplePct = dec("34") }))
	if len(sampled.Lines) != 2 { // ceil(3 * 0.34) = 2
		t.Fatalf("34%% sample of 3 rows = %d lines, want 2", len(sampled.Lines))
	}
	plan := controlPlan(t, s, ctx, func(p *domain.CountPlan) {
		p.FreezeStock, p.FourEyes, p.ABCClass, p.SamplePct = true, true, "B", dec("50")
	})
	if !plan.FreezeStock || !plan.FourEyes || plan.ABCClass != "B" || !plan.SamplePct.Equal(dec("50")) {
		t.Fatalf("plan round trip: %+v", plan)
	}
}

func TestFreezeRefusesMovementsUntilTheSheetIsFinal(t *testing.T) {
	s, _, admin := setup(t)
	ctx := entityCtx(1, ids.New())
	product, other := ids.New(), ids.New()
	post(t, ctx, s, domain.JournalOpeningBalance, line(product, "50", "2", dims()), line(other, "5", "2", dims()))
	plan := controlPlan(t, s, ctx, func(p *domain.CountPlan) { p.FreezeStock = true })
	sheet := generate(t, s, ctx, plan)

	frozen, err := newJournal(ctx, s, domain.JournalAdjustment, line(product, "-1", "2", dims()))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.PostJournal(ctx, frozen.ID, frozen.Version, postParams()); problemCode(err) != domain.CodeCountFrozen {
		t.Fatalf("movement on a frozen row: %v, want %s", err, domain.CodeCountFrozen)
	}
	if phys, _ := onHandFor(t, s, ctx, product); !phys.Equal(dec("50")) {
		t.Fatalf("a refused posting changed stock: %s", phys)
	}
	elsewhere := dims()
	elsewhere[domain.DimWarehouse] = "W9" // not on the sheet: not frozen
	post(t, ctx, s, domain.JournalOpeningBalance, line(product, "4", "2", elsewhere))

	cancelled, err := s.CancelCountSheet(ctx, sheet.ID, sheet.Version)
	if err != nil || cancelled.Status != domain.CountCancelled {
		t.Fatalf("cancel: %v %+v", err, cancelled)
	}
	if again, err := s.CancelCountSheet(ctx, sheet.ID, 1); err != nil || again.Version != cancelled.Version {
		t.Fatalf("cancel is idempotent: %v", err)
	}
	if _, err := s.PostJournal(ctx, frozen.ID, frozen.Version, postParams()); err != nil {
		t.Fatalf("after cancel the movement posts: %v", err)
	}
	if _, err := s.EnterCount(ctx, sheet.ID, sheet.Lines[0].ID, cancelled.Version, dec("1")); problemCode(err) != domain.CodeCountSheetState {
		t.Fatalf("entry on a cancelled sheet: %v", err)
	}

	// The sheet's own COUNT journal is not blocked by its freeze.
	second := generate(t, s, ctx, plan)
	var mine domain.CountLine
	for _, l := range second.Lines {
		if l.ProductID == product {
			mine = l
		}
	}
	counted, err := s.EnterCount(ctx, second.ID, mine.ID, second.Version, dec("48"))
	if err != nil {
		t.Fatal(err)
	}
	posted, err := s.PostCountSheet(approverCtx(ctx), second.ID, counted.Version, true, postParams())
	if err != nil || posted.Status != domain.CountPosted {
		t.Fatalf("a frozen sheet posts its own journal: %v", err)
	}
	if n := outboxCount(t, admin, "dev.passage.inventory.countsheet.changed.v1"); n != 2 {
		t.Fatalf("countsheet.changed events %d, want 2 (the cancellation and the posting)", n)
	}
}

func TestFourEyesRecountAndApprovalFlow(t *testing.T) {
	s, _, admin := setup(t)
	counter := entityCtx(1, ids.New())
	product := ids.New()
	post(t, counter, s, domain.JournalOpeningBalance, line(product, "50", "2", dims()))
	plan := controlPlan(t, s, counter, func(p *domain.CountPlan) { p.FourEyes = true })
	sheet := generate(t, s, counter, plan)
	if !sheet.FourEyes {
		t.Fatal("sheet must carry the plan's four-eyes flag")
	}
	if _, err := s.SubmitCountSheet(counter, sheet.ID, sheet.Version); problemCode(err) != domain.CodeCountNothing {
		t.Fatalf("submit with nothing counted: %v", err)
	}
	counted, err := s.EnterCount(counter, sheet.ID, sheet.Lines[0].ID, sheet.Version, dec("50"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.PostCountSheet(counter, sheet.ID, counted.Version, true, postParams()); problemCode(err) != domain.CodeCountNotReady {
		t.Fatalf("four-eyes post before approval: %v", err)
	}
	if _, err := s.ApproveCountSheet(counter, sheet.ID, counted.Version); problemCode(err) != domain.CodeCountNotReady {
		t.Fatalf("approve before submit: %v", err)
	}
	submitted, err := s.SubmitCountSheet(counter, sheet.ID, counted.Version)
	if err != nil || submitted.Status != domain.CountCounted || submitted.SubmittedBy == "" {
		t.Fatalf("submit: %v %+v", err, submitted)
	}
	if again, err := s.SubmitCountSheet(counter, sheet.ID, 1); err != nil || again.Version != submitted.Version {
		t.Fatalf("submit is idempotent: %v", err)
	}
	if _, err := s.EnterCount(counter, sheet.ID, sheet.Lines[0].ID, submitted.Version, dec("49")); problemCode(err) != domain.CodeCountSheetState {
		t.Fatalf("entry after submit: %v", err)
	}
	if _, err := s.ApproveCountSheet(counter, sheet.ID, submitted.Version); problemCode(err) != domain.CodeCountFourEyes {
		t.Fatalf("the counter approving their own sheet: %v", err)
	}

	supervisor := approverCtx(counter)
	reopened, err := s.RequestRecount(supervisor, sheet.ID, sheet.Lines[0].ID, submitted.Version, "shelf looked light")
	if err != nil || reopened.Status != domain.CountOpen || reopened.RecountLines != 1 || !reopened.Lines[0].RecountRequested {
		t.Fatalf("recount: %v %+v", err, reopened)
	}
	if _, err := s.RequestRecount(supervisor, sheet.ID, ids.New(), reopened.Version, "x"); problemCode(err) != domain.CodeCountNotFound {
		t.Fatalf("recount of an unknown line: %v", err)
	}
	if _, err := s.RequestRecount(supervisor, sheet.ID, sheet.Lines[0].ID, reopened.Version, "  "); problemCode(err) != domain.CodeInvalid {
		t.Fatalf("recount without a reason: %v", err)
	}
	if _, err := s.SubmitCountSheet(counter, sheet.ID, reopened.Version); problemCode(err) != domain.CodeCountRecount {
		t.Fatalf("submit with a recount outstanding: %v", err)
	}
	if _, err := s.PostCountSheet(counter, sheet.ID, reopened.Version, true, postParams()); problemCode(err) != domain.CodeCountRecount {
		t.Fatalf("post with a recount outstanding: %v", err)
	}
	recounted, err := s.EnterCount(counter, sheet.ID, sheet.Lines[0].ID, reopened.Version, dec("46"))
	if err != nil || recounted.RecountLines != 0 || recounted.Lines[0].RecountRounds != 1 {
		t.Fatalf("re-entry clears the flag: %v %+v", err, recounted)
	}
	resubmitted, err := s.SubmitCountSheet(counter, sheet.ID, recounted.Version)
	if err != nil {
		t.Fatal(err)
	}
	approved, err := s.ApproveCountSheet(supervisor, sheet.ID, resubmitted.Version)
	if err != nil || approved.Status != domain.CountApproved || approved.ApprovedBy == "" {
		t.Fatalf("approve: %v %+v", err, approved)
	}
	// An 8 % variance is above tolerance; the approved sheet posts it for a
	// poster without the approve duty.
	posted, err := s.PostCountSheet(counter, sheet.ID, approved.Version, false, postParams())
	if err != nil || posted.Status != domain.CountPosted || posted.ApprovedBy != approved.ApprovedBy || posted.JournalID == "" {
		t.Fatalf("post approved sheet: %v %+v", err, posted)
	}
	if phys, _ := onHandFor(t, s, counter, product); !phys.Equal(dec("46")) {
		t.Fatalf("on hand %s, want 46", phys)
	}
	if _, err := s.CancelCountSheet(counter, sheet.ID, posted.Version); problemCode(err) != domain.CodeCountSheetState {
		t.Fatalf("cancel a posted sheet: %v", err)
	}
	// submitted, recount requested, submitted, approved, posted
	if n := outboxCount(t, admin, "dev.passage.inventory.countsheet.changed.v1"); n != 5 {
		t.Fatalf("countsheet.changed events %d, want 5", n)
	}
}

func TestPlanWithoutFourEyesStillPostsDirectlyFromOpen(t *testing.T) {
	s, _, _ := setup(t)
	ctx := entityCtx(1, ids.New())
	product := ids.New()
	post(t, ctx, s, domain.JournalOpeningBalance, line(product, "50", "2", dims()))
	sheet := generate(t, s, ctx, controlPlan(t, s, ctx, func(p *domain.CountPlan) {}))
	counted, err := s.EnterCount(ctx, sheet.ID, sheet.Lines[0].ID, sheet.Version, dec("50"))
	if err != nil {
		t.Fatal(err)
	}
	if posted, err := s.PostCountSheet(ctx, sheet.ID, counted.Version, false, postParams()); err != nil || posted.Status != domain.CountPosted {
		t.Fatalf("direct post: %v", err)
	}
}

func TestABCSelectionMatchesTheClassOfTheRowsSite(t *testing.T) {
	s, _, _ := setup(t)
	ctx := entityCtx(1, ids.New())
	product, other := ids.New(), ids.New()
	atS2 := dims()
	atS2[domain.DimSite] = "S2"
	// product is class A at S1 (900 of 1000) and class C at S2 (10 of 1000).
	post(t, ctx, s, domain.JournalOpeningBalance, line(product, "90", "10", dims()), line(product, "1", "10", atS2), line(other, "9", "10", dims()))
	if _, _, err := s.RunABC(ctx, store.AbcRunInput{IdempotencyKey: "abc-site", LegalEntityID: legalEntity(ctx), Criterion: domain.AbcCriterionValue,
		Thresholds: domain.AbcThresholds{A: dec("80"), B: dec("10"), C: dec("10")}, ZeroClass: domain.ABCN}); err != nil {
		t.Fatal(err)
	}
	sheet := generate(t, s, ctx, controlPlan(t, s, ctx, func(p *domain.CountPlan) { p.ABCClass = "A" }))
	if len(sheet.Lines) != 1 || sheet.Lines[0].ProductID != product || sheet.Lines[0].Dims[domain.DimSite] != "S1" {
		t.Fatalf("class A selects only the S1 row of the product, got %+v", sheet.Lines)
	}
}

func TestFreezeCoversEveryMovementPathButTheSheetsOwnCount(t *testing.T) {
	s, _, _ := setup(t)
	ctx := entityCtx(1, ids.New())
	product := ids.New()
	post(t, ctx, s, domain.JournalOpeningBalance, line(product, "50", "2", dims()))
	loose := generate(t, s, ctx, controlPlan(t, s, ctx, func(p *domain.CountPlan) {}))
	frozen := generate(t, s, ctx, controlPlan(t, s, ctx, func(p *domain.CountPlan) { p.FreezeStock = true }))

	day := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	block := store.BlockingInput{IdempotencyKey: "blk-frozen", LegalEntityID: legalEntity(ctx), ProductID: product, Dims: dims(), Qty: dec("5"),
		Unit: "EA", Target: domain.StockBlocked, Reason: "QUALITY", PhysicalDate: day}
	if _, _, err := s.CreateBlocking(ctx, block); problemCode(err) != domain.CodeCountFrozen {
		t.Fatalf("blocking a frozen row: %v, want %s", err, domain.CodeCountFrozen)
	}
	// Another sheet's COUNT journal moves the frozen row too, so it waits.
	counted, err := s.EnterCount(ctx, loose.ID, loose.Lines[0].ID, loose.Version, dec("49"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.PostCountSheet(ctx, loose.ID, counted.Version, true, postParams()); problemCode(err) != domain.CodeCountFrozen {
		t.Fatalf("another sheet's count on a frozen row: %v, want %s", err, domain.CodeCountFrozen)
	}
	// A receipt step that does not change physical stock passes; one that does is refused.
	created, err := register(ctx, s, "PURCH_LINE", product, "4", "2", domain.StatusOrdered, dims())
	if err != nil {
		t.Fatalf("an ordered receipt is not a movement of counted stock: %v", err)
	}
	if _, err := step(ctx, s, created.TransactionID, domain.CommandStep, domain.StatusOrdered, domain.StatusReceived); problemCode(err) != domain.CodeCountFrozen {
		t.Fatalf("receiving onto a frozen row: %v, want %s", err, domain.CodeCountFrozen)
	}

	if _, err := s.CancelCountSheet(ctx, frozen.ID, frozen.Version); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.CreateBlocking(ctx, block); err != nil {
		t.Fatalf("blocking after the freeze is lifted: %v", err)
	}
}
