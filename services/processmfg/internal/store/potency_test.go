package store_test

import (
	"testing"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/processmfg/internal/domain"
	"github.com/BenjaminSRussell/passage/services/processmfg/internal/store"
)

// Potency adjustments are checked against the order's lines, read back with the order, replaced as a whole, and
// refused once the order's control recipe is made.
func TestPotencyAdjustmentsOfABatchOrder(t *testing.T) {
	s, _ := setup(t)
	ctx := ctxFor(1, "")
	f := ac241("API-POT")
	f.Lines[2].Type = domain.LineActiveIngredient // 50 kg of the active ingredient, line 1 the filler
	f, _, err := s.CreateFormula(ctx, f, store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	o, _, err := s.CreateBatchOrder(ctx, store.NewBatchOrder{LegalEntityID: entityA, FormulaID: f.ID, BatchSize: dec("1000")}, store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	adj := []domain.PotencyAdjustment{{LineNumber: 3, StandardPotency: dec("90"), BatchPotency: dec("100"), CompensatingLine: 1, BatchRef: "API-0042"}}
	got, err := s.SetPotencyAdjustments(ctx, o.ID, adj, "planner")
	if err != nil || len(got.PotencyAdjustments) != 1 || got.PotencyAdjustments[0].BatchRef != "API-0042" {
		t.Fatalf("set: %+v %v", got.PotencyAdjustments, err)
	}
	read, err := s.GetBatchOrder(ctx, o.ID)
	if err != nil || len(read.PotencyAdjustments) != 1 {
		t.Fatalf("read back: %+v %v", read.PotencyAdjustments, err)
	}
	q, err := store.PotencyAdjusted(read)
	if err != nil || !q[3].Equal(dec("45")) || !q[1].Equal(dec("605")) {
		t.Fatalf("adjusted %v %v", q, err)
	}
	if _, err := s.SetPotencyAdjustments(ctx, o.ID, []domain.PotencyAdjustment{{LineNumber: 4, StandardPotency: dec("1"), BatchPotency: dec("1"), CompensatingLine: 1}}, "planner"); problemCode(err) != domain.CodeInvalid {
		t.Fatalf("a co-product line: %v", err)
	}
	if got, err := s.SetPotencyAdjustments(ctx, o.ID, nil, "planner"); err != nil || len(got.PotencyAdjustments) != 0 {
		t.Fatalf("clear: %+v %v", got.PotencyAdjustments, err)
	}
	recipe := ids.New()
	if err := s.SetControlRecipe(ctx, o.ID, &recipe, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetPotencyAdjustments(ctx, o.ID, adj, "planner"); problemCode(err) != store.CodePotencyState {
		t.Fatalf("after the recipe is made: %v", err)
	}
}
