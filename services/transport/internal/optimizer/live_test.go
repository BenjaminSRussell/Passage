package optimizer

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/services/transport/internal/domain"
)

// TestClient_LiveOptimizerContract runs the client against a real routing-py
// (uvicorn tms_optimizer.api:app) when PASSAGE_TEST_OPTIMIZER_URL is set, so the
// Go and Python wire formats are checked against each other.
func TestClient_LiveOptimizerContract(t *testing.T) {
	url := os.Getenv("PASSAGE_TEST_OPTIMIZER_URL")
	if url == "" {
		t.Skip("PASSAGE_TEST_OPTIMIZER_URL not set; skipping the live optimizer contract test")
	}
	c := New(url, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	req := domain.RouteRequest{
		Depot: domain.RoutePoint{},
		Stops: []domain.RouteStop{
			{ID: "S1", Point: domain.RoutePoint{X: 5}, Latest: 480, ServiceMin: 10, WeightKg: 100, Pallets: 1},
			{ID: "S2", Point: domain.RoutePoint{Y: 5}, Latest: 480, ServiceMin: 10, WeightKg: 100, Pallets: 1},
			{ID: "FAR", Point: domain.RoutePoint{X: 5000}, Latest: 10, WeightKg: 100},
		},
		Vehicles:   []domain.RouteVehicle{{ID: "V1", CapacityKg: 1000, CapacityL: 1000, Pallets: 10}},
		TimeLimitS: 5,
	}
	req.Normalize()
	sol, err := c.Solve(ctx, req)
	if err != nil {
		t.Fatalf("solve: %v", err)
	}
	if msg := domain.CheckSolution(req, sol); msg != "" {
		t.Fatalf("solution is not a plan: %s (%+v)", msg, sol)
	}
	if len(sol.Dropped) != 1 || sol.Dropped[0] != "FAR" {
		t.Fatalf("dropped = %v, want [FAR]", sol.Dropped)
	}

	max := 600
	plan := domain.LoadPlanRequest{
		Trailer: domain.Trailer{LengthMm: 13_600, WidthMm: 2_480, MaxHeightMm: 2_700, MaxWeightKg: 20_000},
		Pallets: []domain.PlanPallet{
			{ID: "P1", LengthMm: 1_200, WidthMm: 1_000, HeightMm: 900, WeightKg: 500, Stackable: true, StopSeq: 2, MaxStackKg: &max},
			{ID: "P2", LengthMm: 1_200, WidthMm: 1_000, HeightMm: 900, WeightKg: 100, Stackable: false, StopSeq: 1},
		},
	}
	got, err := c.Stack(ctx, plan)
	if err != nil {
		t.Fatalf("stack: %v", err)
	}
	if msg := domain.CheckLoadPlan(plan, got); msg != "" {
		t.Fatalf("load plan is not a plan: %s (%+v)", msg, got)
	}
	if got.Axle.KingpinKg+got.Axle.AxleKg != got.TotalWeightKg || got.Placements[0].LengthMm != 1_200 {
		t.Fatalf("plan = %+v", got)
	}
	bad := plan
	bad.Trailer.KingpinMm, bad.Trailer.AxleMm = 9_000, 8_000
	if _, err := c.Stack(ctx, bad); err == nil {
		t.Fatal("a kingpin behind the axle must be rejected")
	}
}
