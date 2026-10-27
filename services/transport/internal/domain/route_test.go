package domain

import (
	"fmt"
	"testing"
)

func validRoute() RouteRequest {
	return RouteRequest{
		Depot:      RoutePoint{X: 0, Y: 0},
		Stops:      []RouteStop{{ID: "S1", Point: RoutePoint{X: 1, Y: 1}, Earliest: 0, Latest: 480, ServiceMin: 10}},
		Vehicles:   []RouteVehicle{{ID: "V1", CapacityKg: 1000, CapacityL: 5000, Pallets: 10}},
		SpeedKmh:   60,
		TimeLimitS: 20,
	}
}

func intp(v int) *int { return &v }

func TestRouteRequest_NormalizeAppliesDefaultsAndTrims(t *testing.T) {
	r := RouteRequest{
		Stops:    []RouteStop{{ID: "  S1 "}},
		Vehicles: []RouteVehicle{{ID: " V1 "}},
	}
	r.Normalize()
	if r.SpeedKmh != DefaultSpeedKmh || r.TimeLimitS != DefaultVRPTimeLimitS {
		t.Fatalf("defaults not applied: speed %v, limit %d", r.SpeedKmh, r.TimeLimitS)
	}
	if r.LatenessPerMin == nil || *r.LatenessPerMin != DefaultLatenessPerMin {
		t.Fatalf("lateness penalty = %v, want the default %d", r.LatenessPerMin, DefaultLatenessPerMin)
	}
	if r.Vehicles[0].MaxRouteMin != DefaultMaxRouteMin {
		t.Fatalf("max route minutes = %d, want %d", r.Vehicles[0].MaxRouteMin, DefaultMaxRouteMin)
	}
	if r.Stops[0].ID != "S1" || r.Vehicles[0].ID != "V1" {
		t.Fatalf("ids not trimmed: %q %q", r.Stops[0].ID, r.Vehicles[0].ID)
	}
}

func TestRouteRequest_Validate(t *testing.T) {
	many := make([]RouteStop, MaxRouteStops+1)
	for i := range many {
		many[i] = RouteStop{ID: fmt.Sprintf("S%d", i), Latest: 10}
	}
	cases := []struct {
		name   string
		mutate func(*RouteRequest)
		field  string // expected key in the error map; empty means valid
	}{
		{"valid", func(*RouteRequest) {}, ""},
		{"no stops", func(r *RouteRequest) { r.Stops = nil }, "stops"},
		{"too many stops", func(r *RouteRequest) { r.Stops = many }, "stops"},
		{"no vehicles", func(r *RouteRequest) { r.Vehicles = nil }, "vehicles"},
		{"time limit below minimum", func(r *RouteRequest) { r.TimeLimitS = 4 }, "timeLimitS"},
		{"time limit at the synchronous maximum", func(r *RouteRequest) { r.TimeLimitS = MaxVRPTimeLimitS }, ""},
		{"time limit above the synchronous maximum", func(r *RouteRequest) { r.TimeLimitS = MaxVRPTimeLimitS + 1 }, "timeLimitS"},
		{"negative speed", func(r *RouteRequest) { r.SpeedKmh = -1 }, "speedKmh"},
		{"explicit zero lateness", func(r *RouteRequest) { r.LatenessPerMin = intp(0) }, ""},
		{"negative lateness", func(r *RouteRequest) { r.LatenessPerMin = intp(-1) }, "latenessPerMin"},
		{"speed above bound", func(r *RouteRequest) { r.SpeedKmh = 1e9 }, "speedKmh"},
		{"window beyond a week", func(r *RouteRequest) { r.Stops[0].Latest = 7*24*60 + 1 }, "stops[0].latest"},
		{"coordinate out of range", func(r *RouteRequest) { r.Depot.X = 1e12 }, "depot"},
		{"negative max route minutes", func(r *RouteRequest) { r.Vehicles[0].MaxRouteMin = -1 }, "vehicles[0].maxRouteMin"},
		{"duplicate stop", func(r *RouteRequest) { r.Stops = append(r.Stops, r.Stops[0]) }, "stops[1].id"},
		{"inverted window", func(r *RouteRequest) { r.Stops[0].Earliest = 100; r.Stops[0].Latest = 10 }, "stops[0].latest"},
		{"negative quantity", func(r *RouteRequest) { r.Stops[0].WeightKg = -1 }, "stops[0]"},
		{"NaN coordinate", func(r *RouteRequest) { r.Stops[0].Point.X = nan() }, "stops[0].point"},
		{"duplicate vehicle", func(r *RouteRequest) { r.Vehicles = append(r.Vehicles, r.Vehicles[0]) }, "vehicles[1].id"},
		{"negative capacity", func(r *RouteRequest) { r.Vehicles[0].CapacityKg = -5 }, "vehicles[0]"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := validRoute()
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

func TestRouteRequest_HashIsStableAndSensitiveToInput(t *testing.T) {
	a := validRoute()
	a.Normalize()
	h1, err := a.Hash()
	if err != nil {
		t.Fatal(err)
	}
	h2, _ := a.Hash()
	if h1 != h2 || len(h1) != 64 {
		t.Fatalf("hash not stable: %q %q", h1, h2)
	}
	b := validRoute()
	b.Stops[0].WeightKg = 1
	b.Normalize()
	h3, _ := b.Hash()
	if h3 == h1 {
		t.Fatal("a changed stop weight must change the request hash")
	}
}

func nan() float64 {
	var zero float64
	return zero / zero
}

func TestRouteRequest_ExplicitZeroLatenessSurvivesNormalize(t *testing.T) {
	r := validRoute()
	r.LatenessPerMin = intp(0)
	r.Normalize()
	if *r.LatenessPerMin != 0 {
		t.Fatalf("an explicit 0 lateness penalty became %d", *r.LatenessPerMin)
	}
	withDefault := validRoute()
	withDefault.Normalize()
	h0, _ := r.Hash()
	h50, _ := withDefault.Hash()
	if h0 == h50 {
		t.Fatal("0 and the default lateness penalty must hash differently")
	}
}

func twoStopRequest() RouteRequest {
	r := validRoute()
	r.Stops = append(r.Stops, RouteStop{ID: "S2", Point: RoutePoint{X: 2, Y: 2}, Latest: 480})
	r.Vehicles = append(r.Vehicles, RouteVehicle{ID: "V2", CapacityKg: 1000, MaxRouteMin: 660})
	r.Normalize()
	return r
}

func TestCheckSolution(t *testing.T) {
	req := twoStopRequest()
	route := func(v string, stops ...string) RouteSolutionRoute {
		rt := RouteSolutionRoute{VehicleID: v, DistanceKm: 3}
		for _, s := range stops {
			rt.Stops = append(rt.Stops, RouteSolutionStop{StopID: s})
		}
		return rt
	}
	cases := []struct {
		name string
		sol  RouteSolution
		ok   bool
	}{
		{"both routed", RouteSolution{Routes: []RouteSolutionRoute{route("V1", "S1", "S2")}}, true},
		{"one dropped", RouteSolution{Routes: []RouteSolutionRoute{route("V1", "S1")}, Dropped: []string{"S2"}}, true},
		{"all dropped", RouteSolution{Dropped: []string{"S1", "S2"}}, true},
		{"stop lost", RouteSolution{Routes: []RouteSolutionRoute{route("V1", "S1")}}, false},
		{"stop twice", RouteSolution{Routes: []RouteSolutionRoute{route("V1", "S1"), route("V2", "S1", "S2")}}, false},
		{"routed and dropped", RouteSolution{Routes: []RouteSolutionRoute{route("V1", "S1", "S2")}, Dropped: []string{"S2"}}, false},
		{"unknown stop", RouteSolution{Routes: []RouteSolutionRoute{route("V1", "S1", "S2", "S9")}}, false},
		{"unknown vehicle", RouteSolution{Routes: []RouteSolutionRoute{route("V9", "S1", "S2")}}, false},
		{"vehicle twice", RouteSolution{Routes: []RouteSolutionRoute{route("V1", "S1"), route("V1", "S2")}}, false},
		{"negative distance", RouteSolution{Routes: []RouteSolutionRoute{{VehicleID: "V1", DistanceKm: -1,
			Stops: []RouteSolutionStop{{StopID: "S1"}, {StopID: "S2"}}}}}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			msg := CheckSolution(req, tc.sol)
			if (msg == "") != tc.ok {
				t.Fatalf("CheckSolution = %q, want ok = %v", msg, tc.ok)
			}
		})
	}
}

func TestNewRoutePlan_SummarizesRoutes(t *testing.T) {
	req := twoStopRequest()
	sol := RouteSolution{
		Routes: []RouteSolutionRoute{
			{VehicleID: "V1", DistanceKm: 1.2346, Stops: []RouteSolutionStop{{StopID: "S1"}}},
			{VehicleID: "V2", DistanceKm: 0},
		},
		Dropped: []string{"S2"},
	}
	plan := NewRoutePlan(req, "abc", sol)
	if plan.StopCount != 2 || plan.VehicleCount != 2 || plan.RouteCount != 1 || plan.DroppedCount != 1 {
		t.Fatalf("summary = %+v", plan.RoutePlanSummary)
	}
	if plan.TotalDistanceKm != 1.235 || plan.TimeLimitS != req.TimeLimitS || plan.RequestHash != "abc" {
		t.Fatalf("distance %v, limit %d, hash %q", plan.TotalDistanceKm, plan.TimeLimitS, plan.RequestHash)
	}
}
