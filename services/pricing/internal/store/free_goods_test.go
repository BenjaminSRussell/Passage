package store_test

import (
	"testing"

	"github.com/shopspring/decimal"

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
