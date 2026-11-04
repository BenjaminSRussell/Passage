package store

import (
	"testing"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	sfevents "github.com/BenjaminSRussell/passage/services/shopfloor/internal/events"
)

// D2a': dated cost rates are append-only, one per date, listed newest first, loaded oldest
// first for the calculation, published once each, and refused for the worker's own user.
func TestCostRates_AddListLoadAndRefusals(t *testing.T) {
	s, app, _ := setup(t)
	tenantID := testkit.TenantID(41)
	ctx := testkit.Ctx(tenantID)
	user := "ann@plant"
	w := mustWorker(t, s, ctx, "CR-A", &user)
	note := "annual raise"

	if steps, err := s.LoadRateSteps(ctx, w.ID); err != nil || len(steps) != 0 {
		t.Fatalf("steps of a worker with none = %v, %v", steps, err)
	}
	if got, err := s.ListCostRates(ctx, w.ID); err != nil || len(got) != 0 {
		t.Fatalf("empty list = %v, %v", got, err)
	}
	if _, err := s.ListCostRates(ctx, ids.New()); problemCode(err) != CodeWorkerNotFound {
		t.Fatalf("list of unknown worker = %v", err)
	}
	if _, err := s.AddCostRate(ctx, ids.New(), CostRateInput{EffectiveFrom: "2026-01-01", CostRate: decimal.NewFromInt(1)}, "sup", idem("cr-0")); problemCode(err) != CodeWorkerNotFound {
		t.Fatalf("add to unknown worker = %v", err)
	}

	later, err := s.AddCostRate(ctx, w.ID, CostRateInput{EffectiveFrom: "2026-07-01", CostRate: decimal.RequireFromString("24.5"), Note: &note}, "sup", idem("cr-1"))
	if err != nil {
		t.Fatal(err)
	}
	if later.EffectiveFrom != "2026-07-01" || !later.CostRate.Equal(decimal.RequireFromString("24.5")) || later.Note == nil || *later.Note != note || later.CreatedBy != "sup" {
		t.Fatalf("stored rate = %+v", later)
	}
	if _, err := s.AddCostRate(ctx, w.ID, CostRateInput{EffectiveFrom: "2026-01-01", CostRate: decimal.NewFromInt(22)}, "sup", idem("cr-2")); err != nil {
		t.Fatalf("a rate dated before an existing one: %v", err)
	}
	if _, err := s.AddCostRate(ctx, w.ID, CostRateInput{EffectiveFrom: "2026-07-01", CostRate: decimal.NewFromInt(30)}, "sup", idem("cr-3")); problemCode(err) != CodeCostRateExists {
		t.Fatalf("second rate for one date = %v, want %s", err, CodeCostRateExists)
	}
	if _, err := s.AddCostRate(ctx, w.ID, CostRateInput{EffectiveFrom: "2026-09-01", CostRate: decimal.NewFromInt(30)}, user, idem("cr-4")); problemCode(err) != CodeOwnApproval {
		t.Fatalf("own rate = %v, want %s", err, CodeOwnApproval)
	}

	list, err := s.ListCostRates(ctx, w.ID)
	if err != nil || len(list) != 2 || list[0].EffectiveFrom != "2026-07-01" || list[1].EffectiveFrom != "2026-01-01" {
		t.Fatalf("list = %+v, %v", list, err)
	}
	steps, err := s.LoadRateSteps(ctx, w.ID)
	if err != nil || len(steps) != 2 || steps[0].From.Format("2006-01-02") != "2026-01-01" || !steps[1].Rate.Equal(decimal.RequireFromString("24.5")) {
		t.Fatalf("steps = %+v, %v", steps, err)
	}
	if n := countEvents(t, app, tenantID, sfevents.WorkerCostRateSet); n != 2 {
		t.Fatalf("workercostrate.set events = %d, want 2", n)
	}
	// Another tenant sees none of it.
	other := testkit.Ctx(testkit.TenantID(42))
	if _, err := s.ListCostRates(other, w.ID); problemCode(err) != CodeWorkerNotFound {
		t.Fatalf("other tenant = %v", err)
	}
}
