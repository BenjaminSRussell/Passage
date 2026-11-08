package store_test

import (
	"errors"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/pricing/internal/domain"
	pevents "github.com/BenjaminSRussell/passage/services/pricing/internal/events"
	"github.com/BenjaminSRussell/passage/services/pricing/internal/store"
)

func testFreeGoods(from, to string) domain.FreeGoodsDef {
	return domain.FreeGoodsDef{LegalEntityID: legalEntity, CustomerKind: domain.CustomerKindAll, ProductID: "P1", Unit: "EA",
		Kind: domain.FreeGoodsInclusive, Calc: domain.CalcWholeUnits, Every: decimal.NewFromInt(10), Free: decimal.NewFromInt(1),
		MinQty: decimal.Zero, ValidFrom: day(from), ValidTo: day(to)}
}

// TestPostgres_FreeGoodsRuleLifecycle covers FR-26.22 against the database with RLS enforced
// as the application role: create with replay, the overlap guard, the resolve lookup by
// product and date, the version-checked withdraw and the outbox events.
func TestPostgres_FreeGoodsRuleLifecycle(t *testing.T) {
	pool := appPool(t)
	ctx := principal(1, "")
	s := store.New(pool)
	r, err := s.CreateFreeGoodsRule(ctx, testFreeGoods("2026-01-01", "2026-06-30"), "fg-1")
	if err != nil || r.Version != 1 || r.Withdrawn || !r.Every.Equal(decimal.NewFromInt(10)) {
		t.Fatalf("create = %+v err %v", r, err)
	}
	if again, err := s.CreateFreeGoodsRule(ctx, testFreeGoods("2026-01-01", "2026-06-30"), "fg-1"); err != nil || again.ID != r.ID {
		t.Fatalf("replayed create = %s err %v; want %s", again.ID, err, r.ID)
	}
	if _, err := s.CreateFreeGoodsRule(ctx, testFreeGoods("2026-01-01", "2026-07-31"), "fg-1"); problemCode(err) != store.CodeIdempotencyUse {
		t.Fatalf("same key, other body = %v; want idempotency_key_reuse", err)
	}
	if _, err := s.CreateFreeGoodsRule(ctx, testFreeGoods("2026-06-30", "2026-12-31"), ""); problemCode(err) != domain.CodeOverlap {
		t.Fatalf("overlapping rule = %v; want PRC-0002", err)
	}
	next, err := s.CreateFreeGoodsRule(ctx, testFreeGoods("2026-07-01", "2026-12-31"), "")
	if err != nil {
		t.Fatalf("adjacent rule: %v", err)
	}

	live, err := s.ActiveFreeGoodsRules(ctx, legalEntity, []string{"P1", "P2"}, day("2026-03-01"))
	if err != nil || len(live) != 1 || live[0].ID != r.ID {
		t.Fatalf("live on 2026-03-01 = %+v err %v; want the first rule", live, err)
	}
	if none, err := s.ActiveFreeGoodsRules(ctx, legalEntity, []string{"P2"}, day("2026-03-01")); err != nil || len(none) != 0 {
		t.Fatalf("other product = %+v err %v", none, err)
	}

	if _, err := s.WithdrawFreeGoodsRule(ctx, r.ID, 5, ""); problemCode(err) != store.CodeVersionConflict {
		t.Fatalf("stale withdraw = %v; want conflict_version", err)
	}
	w, err := s.WithdrawFreeGoodsRule(ctx, r.ID, 1, "wd-1")
	if err != nil || !w.Withdrawn || w.Version != 2 {
		t.Fatalf("withdraw = %+v err %v", w, err)
	}
	if again, err := s.WithdrawFreeGoodsRule(ctx, r.ID, 1, "wd-1"); err != nil || again.Version != 2 {
		t.Fatalf("replayed withdraw = %+v err %v", again, err)
	}
	if _, err := s.WithdrawFreeGoodsRule(ctx, r.ID, 2, ""); problemCode(err) != store.CodeStateInvalid {
		t.Fatalf("second withdraw = %v; want PRC-0031", err)
	}
	if live, _ := s.ActiveFreeGoodsRules(ctx, legalEntity, []string{"P1"}, day("2026-03-01")); len(live) != 0 {
		t.Fatalf("withdrawn rule still live: %+v", live)
	}
	// A withdrawn rule frees its key for the same dates.
	if _, err := s.CreateFreeGoodsRule(ctx, testFreeGoods("2026-01-01", "2026-06-30"), ""); err != nil {
		t.Fatalf("recreate over a withdrawn rule: %v", err)
	}

	list, err := s.ListFreeGoodsRules(ctx, legalEntity, "P1", 0)
	if err != nil || len(list) != 3 || list[0].ID != next.ID {
		t.Fatalf("list = %+v err %v; want 3 rules, latest start first", list, err)
	}
	if _, err := s.GetFreeGoodsRule(principal(2, ""), r.ID); problemCode(err) != domain.CodeFreeGoodsRuleNotFound {
		t.Fatalf("another tenant must not see the rule: %v", err)
	}
	if other, err := s.ListFreeGoodsRules(principal(1, otherLegalEntity), legalEntity, "", 0); err != nil || len(other) != 0 {
		t.Fatalf("another legal entity's scope sees %+v err %v", other, err)
	}

	var types []string
	for _, row := range outboxRows(t, ctx, pool) {
		types = append(types, row.Type)
		if row.Type == pevents.FreeGoodsRuleWithdrawn && (row.Data["rule_id"] != r.ID || row.Data["withdrawn"] != true || row.Data["version"] != float64(2)) {
			t.Fatalf("withdrawn event = %v", row.Data)
		}
	}
	want := []string{pevents.FreeGoodsRuleCreated, pevents.FreeGoodsRuleCreated, pevents.FreeGoodsRuleWithdrawn, pevents.FreeGoodsRuleCreated}
	if len(types) != len(want) {
		t.Fatalf("outbox = %v; want %v", types, want)
	}
	for i := range want {
		if types[i] != want[i] {
			t.Fatalf("outbox = %v; want %v", types, want)
		}
	}
}

// Two deliveries of the same create race: the advisory lock is taken before the replay
// lookup, so the second waits and replays the first instead of reporting an overlap.
func TestPostgres_FreeGoodsConcurrentRetryReplays(t *testing.T) {
	pool := appPool(t)
	ctx := principal(1, "")
	s := store.New(pool)
	in := testFreeGoods("2026-01-01", "2026-12-31")
	in.ProductID = "P-RACE"
	type res struct {
		id  string
		err error
	}
	out := make(chan res, 2)
	for i := 0; i < 2; i++ {
		go func() {
			r, err := s.CreateFreeGoodsRule(ctx, in, "fg-race")
			out <- res{r.ID, err}
		}()
	}
	a, b := <-out, <-out
	if a.err != nil || b.err != nil || a.id != b.id {
		t.Fatalf("concurrent retries = %+v / %+v; want one rule replayed", a, b)
	}
}

// TestPostgres_ReviseFreeGoodsRule covers the revise of a live rule against the database with
// RLS enforced: values replaced at the next version, replay by key, the version check, the
// overlap guard against a neighbour rule (the rule itself excluded), validation with field
// errors, the past-days guard on a started rule, the withdrawn and unchanged refusals, tenant
// isolation, and the revised event.
func TestPostgres_ReviseFreeGoodsRule(t *testing.T) {
	pool := appPool(t)
	ctx := principal(1, "")
	s := store.New(pool)
	at := func(n int) string { return time.Now().UTC().AddDate(0, 0, n).Format("2006-01-02") }
	first, err := s.CreateFreeGoodsRule(ctx, testFreeGoods(at(10), at(60)), "")
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.CreateFreeGoodsRule(ctx, testFreeGoods(at(61), at(120)), "")
	if err != nil {
		t.Fatal(err)
	}
	rev := domain.FreeGoodsRevision{ID: first.ID, Version: 1, Calc: domain.CalcProportional, Every: decimal.NewFromInt(5), Free: decimal.NewFromInt(2),
		MinQty: decimal.NewFromInt(20), ValidFrom: day(at(10)), ValidTo: day(at(60))}
	got, err := s.ReviseFreeGoodsRule(ctx, rev, "rv-1")
	if err != nil || got.Version != 2 || got.Calc != domain.CalcProportional || !got.Every.Equal(decimal.NewFromInt(5)) ||
		!got.Free.Equal(decimal.NewFromInt(2)) || !got.MinQty.Equal(decimal.NewFromInt(20)) || got.Kind != domain.FreeGoodsInclusive || got.ProductID != "P1" {
		t.Fatalf("revise = %+v err %v", got, err)
	}
	if again, err := s.ReviseFreeGoodsRule(ctx, rev, "rv-1"); err != nil || again.Version != 2 {
		t.Fatalf("replayed revise = %+v err %v; want the first response", again, err)
	}
	live, err := s.ActiveFreeGoodsRules(ctx, legalEntity, []string{"P1"}, day(at(20)))
	if err != nil || len(live) != 1 || !live[0].Every.Equal(decimal.NewFromInt(5)) {
		t.Fatalf("live after revise = %+v err %v; want the revised rule", live, err)
	}
	if _, err := s.ReviseFreeGoodsRule(ctx, rev, ""); problemCode(err) != store.CodeVersionConflict {
		t.Fatalf("stale revise = %v; want conflict_version", err)
	}
	// Stretching the validity into the neighbour's dates is refused; ending it earlier is fine
	// (the rule's own row never counts as an overlap).
	clash := rev
	clash.Version, clash.ValidTo = 2, day(at(70))
	if _, err := s.ReviseFreeGoodsRule(ctx, clash, ""); problemCode(err) != domain.CodeOverlap {
		t.Fatalf("overlapping revise = %v; want PRC-0002", err)
	}
	short := rev
	short.Version, short.ValidTo = 2, day(at(50))
	if r, err := s.ReviseFreeGoodsRule(ctx, short, ""); err != nil || r.Version != 3 || dayStrTest(r.ValidTo) != at(50) {
		t.Fatalf("shorter validity = %+v err %v", r, err)
	}
	// An inclusive rule must give fewer than it counts; the refusal names the field.
	bad := short
	bad.Version, bad.Free, bad.Every = 3, decimal.NewFromInt(5), decimal.NewFromInt(5)
	if _, err := s.ReviseFreeGoodsRule(ctx, bad, ""); problemCode(err) != domain.CodeValidation || problemFields(err)["freeQuantity"] == "" {
		t.Fatalf("free = every = %v; want COM-0001 naming freeQuantity", err)
	}
	// A rule that has not started cannot be moved to start in the past.
	past := short
	past.Version, past.ValidFrom = 3, day(at(-1))
	if _, err := s.ReviseFreeGoodsRule(ctx, past, ""); problemCode(err) != domain.CodeValidation || problemFields(err)["validFrom"] == "" {
		t.Fatalf("start in the past = %v; want COM-0001 naming validFrom", err)
	}
	same := short
	same.Version = 3
	if _, err := s.ReviseFreeGoodsRule(ctx, same, ""); problemCode(err) != store.CodeStateInvalid {
		t.Fatalf("unchanged revise = %v; want PRC-0031", err)
	}
	if _, err := s.ReviseFreeGoodsRule(ctx, domain.FreeGoodsRevision{ID: "00000000-0000-4000-8000-0000000000ff", Version: 1}, ""); problemCode(err) != domain.CodeFreeGoodsRuleNotFound {
		t.Fatalf("unknown rule = %v; want PRC-0047", err)
	}
	if _, err := s.ReviseFreeGoodsRule(principal(2, ""), short, ""); problemCode(err) != domain.CodeFreeGoodsRuleNotFound {
		t.Fatalf("another tenant must not revise the rule: %v", err)
	}
	// A started rule keeps what it proposed on past days: its offer is fixed, its end moves
	// only within yesterday and later.
	started, err := s.CreateFreeGoodsRule(ctx, testFreeGoods(at(-30), at(5)), "")
	if err != nil {
		t.Fatal(err)
	}
	keep := domain.FreeGoodsRevision{ID: started.ID, Version: 1, Calc: started.Calc, Every: started.Every, Free: started.Free,
		MinQty: started.MinQty, ValidFrom: started.ValidFrom, ValidTo: started.ValidTo}
	offer := keep
	offer.Free = decimal.NewFromInt(2)
	if _, err := s.ReviseFreeGoodsRule(ctx, offer, ""); problemCode(err) != domain.CodeValidation || problemFields(err)["freeQuantity"] == "" {
		t.Fatalf("offer change on a started rule = %v; want COM-0001 naming freeQuantity", err)
	}
	early := keep
	early.ValidTo = day(at(-10))
	if _, err := s.ReviseFreeGoodsRule(ctx, early, ""); problemCode(err) != domain.CodeValidation || problemFields(err)["validTo"] == "" {
		t.Fatalf("end before yesterday = %v; want COM-0001 naming validTo", err)
	}
	stop := keep
	stop.ValidTo = day(at(-1))
	if r, err := s.ReviseFreeGoodsRule(ctx, stop, ""); err != nil || dayStrTest(r.ValidTo) != at(-1) {
		t.Fatalf("end it yesterday = %+v err %v", r, err)
	}
	if _, err := s.WithdrawFreeGoodsRule(ctx, second.ID, 1, ""); err != nil {
		t.Fatal(err)
	}
	wd := domain.FreeGoodsRevision{ID: second.ID, Version: 2, Calc: second.Calc, Every: second.Every, Free: decimal.NewFromInt(2),
		MinQty: second.MinQty, ValidFrom: second.ValidFrom, ValidTo: second.ValidTo}
	if _, err := s.ReviseFreeGoodsRule(ctx, wd, ""); problemCode(err) != store.CodeStateInvalid {
		t.Fatalf("revise of a withdrawn rule = %v; want PRC-0031", err)
	}
	var revised []map[string]any
	for _, row := range outboxRows(t, ctx, pool) {
		if row.Type == pevents.FreeGoodsRuleRevised {
			revised = append(revised, row.Data)
		}
	}
	if len(revised) != 3 || revised[0]["rule_id"] != first.ID || revised[0]["version"] != float64(2) || revised[0]["calc_rule"] != "PROPORTIONAL" ||
		revised[1]["valid_to"] != at(50) || revised[2]["rule_id"] != started.ID {
		t.Fatalf("revised events = %v; want two for the first rule and one for the started rule", revised)
	}
}

func dayStrTest(t time.Time) string { return t.Format("2006-01-02") }

// problemFields returns a problem's field errors (nil for any other error).
func problemFields(err error) map[string]string {
	var p *httpx.Problem
	if errors.As(err, &p) {
		return p.Errors
	}
	return nil
}
