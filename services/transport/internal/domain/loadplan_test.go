package domain

import (
	"fmt"
	"testing"
)

func validPlanRequest() LoadPlanRequest {
	return LoadPlanRequest{
		Trailer: Trailer{LengthMm: 13_600, WidthMm: 2_480, MaxHeightMm: 2_700, MaxWeightKg: 20_000},
		Pallets: []PlanPallet{
			{ID: "P1", LengthMm: 1_200, WidthMm: 1_000, HeightMm: 900, WeightKg: 500, Stackable: true},
			{ID: "P2", LengthMm: 1_200, WidthMm: 1_000, HeightMm: 900, WeightKg: 500, Stackable: false},
		},
	}
}

func TestLoadPlanRequest_Validate(t *testing.T) {
	many := make([]PlanPallet, MaxPlanPallets+1)
	for i := range many {
		many[i] = PlanPallet{ID: fmt.Sprintf("P%d", i), LengthMm: 1, WidthMm: 1, HeightMm: 1}
	}
	neg := -1
	cases := []struct {
		name   string
		mutate func(*LoadPlanRequest)
		field  string
	}{
		{"valid", func(*LoadPlanRequest) {}, ""},
		{"no pallets", func(r *LoadPlanRequest) { r.Pallets = nil }, "pallets"},
		{"too many pallets", func(r *LoadPlanRequest) { r.Pallets = many }, "pallets"},
		{"zero trailer length", func(r *LoadPlanRequest) { r.Trailer.LengthMm = 0 }, "trailer"},
		{"zero trailer weight", func(r *LoadPlanRequest) { r.Trailer.MaxWeightKg = 0 }, "trailer.maxWeightKg"},
		{"axle ahead of kingpin", func(r *LoadPlanRequest) { r.Trailer.KingpinMm, r.Trailer.AxleMm = 5_000, 4_000 }, "trailer.axleMm"},
		{"axle off the trailer", func(r *LoadPlanRequest) { r.Trailer.AxleMm = 20_000 }, "trailer.axleMm"},
		{"axle supports set", func(r *LoadPlanRequest) { r.Trailer.KingpinMm, r.Trailer.AxleMm = 900, 12_000 }, ""},
		{"kingpin at the door", func(r *LoadPlanRequest) { r.Trailer.KingpinMm = 13_600 }, "trailer.axleMm"},
		{"duplicate id", func(r *LoadPlanRequest) { r.Pallets[1].ID = "P1" }, "pallets[1].id"},
		{"blank id", func(r *LoadPlanRequest) { r.Pallets[0].ID = "" }, "pallets[0].id"},
		{"zero height", func(r *LoadPlanRequest) { r.Pallets[0].HeightMm = 0 }, "pallets[0]"},
		{"negative weight", func(r *LoadPlanRequest) { r.Pallets[0].WeightKg = -1 }, "pallets[0].weightKg"},
		{"negative stack limit", func(r *LoadPlanRequest) { r.Pallets[0].MaxStackKg = &neg }, "pallets[0].weightKg"},
		{"negative stop", func(r *LoadPlanRequest) { r.Pallets[0].StopSeq = -1 }, "pallets[0].stopSeq"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := validPlanRequest()
			tc.mutate(&r)
			r.Normalize()
			errs := r.Validate()
			if tc.field == "" {
				if len(errs) != 0 {
					t.Fatalf("expected valid, got %v", errs)
				}
				return
			}
			if _, ok := errs[tc.field]; !ok {
				t.Fatalf("expected error on %q, got %v", tc.field, errs)
			}
		})
	}
}

func TestCheckLoadPlan(t *testing.T) {
	req := validPlanRequest()
	place := func(ids ...string) Placement { return Placement{PalletIDs: ids} }
	cases := []struct {
		name string
		plan LoadPlan
		ok   bool
	}{
		{"stackable carries", LoadPlan{Placements: []Placement{place("P1", "P2")}, FloorPositionsUsed: 1}, true},
		{"side by side", LoadPlan{Placements: []Placement{place("P1"), place("P2")}, FloorPositionsUsed: 2}, true},
		{"one unplaced", LoadPlan{Placements: []Placement{place("P1")}, Unplaced: []string{"P2"}, FloorPositionsUsed: 1}, true},
		{"on a non-stackable", LoadPlan{Placements: []Placement{place("P2", "P1")}, FloorPositionsUsed: 1}, false},
		{"pallet lost", LoadPlan{Placements: []Placement{place("P1")}, FloorPositionsUsed: 1}, false},
		{"pallet twice", LoadPlan{Placements: []Placement{place("P1"), place("P1", "P2")}, FloorPositionsUsed: 2}, false},
		{"placed and unplaced", LoadPlan{Placements: []Placement{place("P1"), place("P2")}, Unplaced: []string{"P2"}, FloorPositionsUsed: 2}, false},
		{"unknown pallet", LoadPlan{Placements: []Placement{place("P1"), place("P2"), place("P9")}, FloorPositionsUsed: 3}, false},
		{"empty position", LoadPlan{Placements: []Placement{place("P1"), place("P2"), place()}, FloorPositionsUsed: 3}, false},
		{"floor count off", LoadPlan{Placements: []Placement{place("P1"), place("P2")}, FloorPositionsUsed: 1}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			msg := CheckLoadPlan(req, tc.plan)
			if (msg == "") != tc.ok {
				t.Fatalf("CheckLoadPlan = %q, want ok = %v", msg, tc.ok)
			}
		})
	}
}
