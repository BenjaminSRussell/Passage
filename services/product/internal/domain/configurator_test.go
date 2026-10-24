package domain_test

import (
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/services/product/internal/domain"
)

func TestBicycleFiftyConstraintFixture(t *testing.T) {
	model := domain.BicycleModel()
	if len(model.Constraints) != 50 {
		t.Fatalf("constraints %d, want 50", len(model.Constraints))
	}
	tables := map[string]domain.DecisionTable{"frame_fork_wheel": domain.FrameForkWheelTable()}
	valid, invalid := domain.BicycleSelectionSets()
	if len(valid) != 60 || len(invalid) != 40 {
		t.Fatalf("sets %d/%d, want 60/40", len(valid), len(invalid))
	}
	for i, sel := range valid {
		start := time.Now()
		res, err := domain.SolveConfiguration(model, sel, tables)
		if err != nil || !res.Feasible {
			t.Fatalf("valid[%d] %+v: %v %+v", i, sel, err, res)
		}
		if time.Since(start) > 200*time.Millisecond {
			t.Fatalf("valid[%d] took %s", i, time.Since(start))
		}
		for k, v := range sel {
			if res.Assignment[k] != v {
				t.Fatalf("valid[%d] changed %s from %s to %s", i, k, v, res.Assignment[k])
			}
		}
	}
	for i, sel := range invalid {
		start := time.Now()
		res, err := domain.SolveConfiguration(model, sel, tables)
		if err == nil || res.Feasible {
			t.Fatalf("invalid[%d] %+v should fail, got %+v", i, sel, res)
		}
		if len(res.IIS) == 0 {
			t.Fatalf("invalid[%d] missing IIS", i)
		}
		if time.Since(start) > 200*time.Millisecond {
			t.Fatalf("invalid[%d] took %s", i, time.Since(start))
		}
	}
}

func TestDecisionTableOutsideRowNamesTableConstraint(t *testing.T) {
	model := domain.BicycleModel()
	tables := map[string]domain.DecisionTable{"frame_fork_wheel": domain.FrameForkWheelTable()}
	res, err := domain.SolveConfiguration(model, map[string]string{"frame": "steel", "fork": "suspension", "wheel": "29"}, tables)
	if err == nil || res.Feasible {
		t.Fatal("steel+suspension must be rejected by the table")
	}
	found := false
	for _, name := range res.IIS {
		if name == "c50" || name == "c2" {
			found = true
		}
	}
	if !found {
		t.Fatalf("IIS %v should name the table or steel/rigid constraint", res.IIS)
	}
}

func TestRevalidateSavedMarksStale(t *testing.T) {
	model := domain.BicycleModel()
	tables := map[string]domain.DecisionTable{"frame_fork_wheel": domain.FrameForkWheelTable()}
	saved := domain.SavedConfiguration{Name: "road", ModelID: model.ID, Version: 1, Selections: map[string]string{"frame": "carbon", "brake": "rim"}}
	got := domain.RevalidateSaved(model, saved, tables)
	if got.Status != "stale" {
		t.Fatalf("status %s", got.Status)
	}
}
