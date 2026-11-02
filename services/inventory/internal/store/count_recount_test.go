package store_test

import (
	"context"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/inventory/internal/domain"
)

func lineOf(sheet domain.CountSheet, product string) domain.CountLine {
	for _, l := range sheet.Lines {
		if l.ProductID == product {
			return l
		}
	}
	return domain.CountLine{}
}

// BR-16.8: on a plan without a freeze, a movement after a line was counted
// flags that line for recount, reopens the sheet and clears the approval.
func TestMovementAfterCountFlagsTheLineForRecountWithoutFreeze(t *testing.T) {
	s, _, admin := setup(t)
	ctx := entityCtx(1, ids.New())
	product, other := ids.New(), ids.New()
	post(t, ctx, s, domain.JournalOpeningBalance, line(product, "50", "2", dims()), line(other, "5", "2", dims()))
	sheet := generate(t, s, ctx, controlPlan(t, s, ctx, func(p *domain.CountPlan) {}))

	mine, theirs := lineOf(sheet, product), lineOf(sheet, other)
	counted, err := s.EnterCount(ctx, sheet.ID, mine.ID, sheet.Version, dec("50"))
	if err != nil {
		t.Fatal(err)
	}
	// A movement on a row not yet counted flags nothing.
	post(t, ctx, s, domain.JournalAdjustment, line(other, "-1", "2", dims()))
	if got, _ := s.GetCountSheet(ctx, sheet.ID); got.RecountLines != 0 || got.Version != counted.Version {
		t.Fatalf("uncounted row flagged: recount lines %d, version %d -> %d", got.RecountLines, counted.Version, got.Version)
	}
	before := outboxCount(t, admin, "dev.passage.inventory.countsheet.changed.v1")

	post(t, ctx, s, domain.JournalAdjustment, line(product, "-3", "2", dims()))
	got, err := s.GetCountSheet(ctx, sheet.ID)
	if err != nil {
		t.Fatal(err)
	}
	l := lineOf(got, product)
	if !l.RecountRequested || l.RecountSource != domain.RecountByMovement || l.RecountRounds != 1 || l.RecountReason == "" {
		t.Fatalf("counted line after a movement: %+v", l)
	}
	if lineOf(got, other).RecountRequested || theirs.ID == "" {
		t.Fatalf("the other line must stay unflagged: %+v", lineOf(got, other))
	}
	if got.RecountLines != 1 || got.Status != domain.CountOpen || got.Version <= counted.Version {
		t.Fatalf("sheet after the movement: %+v", got)
	}
	if n := outboxCount(t, admin, "dev.passage.inventory.countsheet.changed.v1"); n != before+1 {
		t.Fatalf("countsheet.changed events %d, want %d", n, before+1)
	}
	var action, source string
	var lineID *string
	if err := admin.QueryRow(context.Background(), `select payload->'data'->>'action', payload->'data'->>'recountSource', payload->'data'->>'lineId'
		from outbox where type = 'dev.passage.inventory.countsheet.changed.v1' and payload->'data'->>'sheetId' = $1
		order by created_at desc, id desc limit 1`, sheet.ID).Scan(&action, &source, &lineID); err != nil {
		t.Fatal(err)
	}
	if action != "RECOUNT_REQUESTED" || source != domain.RecountByMovement || lineID != nil {
		t.Fatalf("movement event: action %s, source %s, line %v", action, source, lineID)
	}
	if _, err := s.SubmitCountSheet(ctx, sheet.ID, got.Version); problemCode(err) != domain.CodeCountRecount {
		t.Fatalf("submit with a movement recount outstanding: %v", err)
	}
	// A second movement does not flag the line again.
	post(t, ctx, s, domain.JournalAdjustment, line(product, "-1", "2", dims()))
	if again, _ := s.GetCountSheet(ctx, sheet.ID); lineOf(again, product).RecountRounds != 1 || again.Version != got.Version {
		t.Fatalf("an already flagged line is flagged again: %+v", lineOf(again, product))
	}

	// Entering the count clears the flag; the posting variance is against the book quantity now.
	re, err := s.EnterCount(ctx, sheet.ID, mine.ID, got.Version, dec("46"))
	if err != nil {
		t.Fatal(err)
	}
	if lineOf(re, product).RecountRequested || lineOf(re, product).RecountSource != "" {
		t.Fatalf("flag after the recount entry: %+v", lineOf(re, product))
	}
	posted, err := s.PostCountSheet(approverCtx(ctx), sheet.ID, re.Version, true, postParams())
	if err != nil || posted.Status != domain.CountPosted {
		t.Fatalf("post: %v", err)
	}
}

// A COUNTED and APPROVED sheet goes back to OPEN when stock moves; the sheet's
// own COUNT journal and a frozen plan do not flag.
func TestMovementReopensCountedSheetAndSupervisorSourceIsKept(t *testing.T) {
	s, _, _ := setup(t)
	ctx := entityCtx(1, ids.New())
	product := ids.New()
	post(t, ctx, s, domain.JournalOpeningBalance, line(product, "20", "2", dims()))
	sheet := generate(t, s, ctx, controlPlan(t, s, ctx, func(p *domain.CountPlan) {}))
	counted, err := s.EnterCount(ctx, sheet.ID, sheet.Lines[0].ID, sheet.Version, dec("20"))
	if err != nil {
		t.Fatal(err)
	}
	submitted, err := s.SubmitCountSheet(ctx, sheet.ID, counted.Version)
	if err != nil || submitted.Status != domain.CountCounted {
		t.Fatalf("submit: %v", err)
	}
	post(t, ctx, s, domain.JournalAdjustment, line(product, "-2", "2", dims()))
	got, _ := s.GetCountSheet(ctx, sheet.ID)
	if got.Status != domain.CountOpen || got.SubmittedBy != "" || got.RecountLines != 1 {
		t.Fatalf("COUNTED sheet after a movement: %+v", got)
	}
	// A supervisor recount on a line stamps the supervisor as the source.
	re, _ := s.EnterCount(ctx, sheet.ID, sheet.Lines[0].ID, got.Version, dec("18"))
	asked, err := s.RequestRecount(ctx, sheet.ID, sheet.Lines[0].ID, re.Version, "check the bin")
	if err != nil || lineOf(asked, product).RecountSource != domain.RecountBySupervisor {
		t.Fatalf("supervisor recount: %v %+v", err, lineOf(asked, product))
	}
}

// Another sheet's COUNT journal is a movement like any other: posting a
// variance on one sheet flags the counted line of a second sheet on the same
// row, while the posting sheet itself is exempt.
func TestAnotherSheetsCountPostingFlagsTheLine(t *testing.T) {
	s, _, _ := setup(t)
	ctx := entityCtx(1, ids.New())
	product := ids.New()
	post(t, ctx, s, domain.JournalOpeningBalance, line(product, "20", "2", dims()))
	plan := controlPlan(t, s, ctx, func(p *domain.CountPlan) {})
	first, second := generate(t, s, ctx, plan), generate(t, s, ctx, plan)
	if _, err := s.EnterCount(ctx, second.ID, second.Lines[0].ID, second.Version, dec("20")); err != nil {
		t.Fatal(err)
	}
	counted, err := s.EnterCount(ctx, first.ID, first.Lines[0].ID, first.Version, dec("18"))
	if err != nil {
		t.Fatal(err)
	}
	posted, err := s.PostCountSheet(approverCtx(ctx), first.ID, counted.Version, true, postParams())
	if err != nil || posted.Status != domain.CountPosted || posted.RecountLines != 0 {
		t.Fatalf("posting the first sheet: %v %+v", err, posted)
	}
	if got, _ := s.GetCountSheet(ctx, second.ID); !lineOf(got, product).RecountRequested || lineOf(got, product).RecountSource != domain.RecountByMovement {
		t.Fatalf("the second sheet after the first one's variance posted: %+v", lineOf(got, product))
	}
}

func TestFrozenPlanRefusesInsteadOfFlagging(t *testing.T) {
	s, _, _ := setup(t)
	ctx := entityCtx(1, ids.New())
	product := ids.New()
	post(t, ctx, s, domain.JournalOpeningBalance, line(product, "20", "2", dims()))
	sheet := generate(t, s, ctx, controlPlan(t, s, ctx, func(p *domain.CountPlan) { p.FreezeStock = true }))
	if _, err := s.EnterCount(ctx, sheet.ID, sheet.Lines[0].ID, sheet.Version, dec("20")); err != nil {
		t.Fatal(err)
	}
	j, err := newJournal(ctx, s, domain.JournalAdjustment, line(product, "-2", "2", dims()))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.PostJournal(ctx, j.ID, j.Version, postParams()); problemCode(err) != domain.CodeCountFrozen {
		t.Fatalf("frozen: %v", err)
	}
}

func TestPlanSelectsRowsNotCountedForTheGivenDays(t *testing.T) {
	s, _, admin := setup(t)
	ctx := entityCtx(1, ids.New())
	fresh, stale, never := ids.New(), ids.New(), ids.New()
	post(t, ctx, s, domain.JournalOpeningBalance, line(fresh, "10", "2", dims()), line(stale, "10", "2", dims()), line(never, "10", "2", dims()))
	all := generate(t, s, ctx, controlPlan(t, s, ctx, func(p *domain.CountPlan) {}))
	for _, p := range []string{fresh, stale} {
		l := lineOf(all, p)
		c, err := s.EnterCount(ctx, all.ID, l.ID, all.Version, dec("10"))
		if err != nil {
			t.Fatal(err)
		}
		all = c
	}
	// An unposted count does not make a row "recently counted".
	aged := controlPlan(t, s, ctx, func(p *domain.CountPlan) { p.MinDaysSinceCount = 30 })
	if aged.MinDaysSinceCount != 30 {
		t.Fatalf("plan round trip: %+v", aged)
	}
	if n := len(generate(t, s, ctx, aged).Lines); n != 3 {
		t.Fatalf("before any posted count %d rows are selected, want 3", n)
	}
	if _, err := s.PostCountSheet(approverCtx(ctx), all.ID, all.Version, true, postParams()); err != nil {
		t.Fatal(err)
	}
	// The stale product was counted 40 days ago.
	if _, err := admin.Exec(context.Background(), `update count_sheet_line set counted_at = now() - interval '40 days' where product_id = $1`, stale); err != nil {
		t.Fatal(err)
	}
	picked := generate(t, s, ctx, aged)
	if len(picked.Lines) != 2 || lineOf(picked, fresh).ID != "" || lineOf(picked, stale).ID == "" || lineOf(picked, never).ID == "" {
		t.Fatalf("selected lines: %+v", picked.Lines)
	}
	// A recent count of the same product and dimensions in another legal entity
	// of the tenant does not make this entity's row recently counted, even for a
	// tenant-wide caller whom row-level security does not narrow.
	elsewhere := entityCtx(1, ids.New())
	post(t, elsewhere, s, domain.JournalOpeningBalance, line(stale, "10", "2", dims()))
	theirs := generate(t, s, elsewhere, controlPlan(t, s, elsewhere, func(p *domain.CountPlan) {}))
	c, err := s.EnterCount(elsewhere, theirs.ID, theirs.Lines[0].ID, theirs.Version, dec("10"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.PostCountSheet(approverCtx(elsewhere), theirs.ID, c.Version, true, postParams()); err != nil {
		t.Fatal(err)
	}
	if lineOf(generate(t, s, tenantWideCtx(1), aged), stale).ID == "" {
		t.Fatal("a count in another legal entity hid the stale row")
	}
	if _, _, err := s.CreateCountPlan(ctx, ids.New(), domain.CountPlan{LegalEntityID: legalEntity(ctx), Name: "x", Currency: "USD", Unit: "EA", MinDaysSinceCount: 4000}); problemCode(err) != domain.CodeInvalid {
		t.Fatalf("age beyond the maximum: %v", err)
	}
}
