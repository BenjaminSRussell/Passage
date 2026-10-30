package optimizer

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/services/transport/internal/domain"
)

func sampleRequest() domain.RouteRequest {
	return domain.RouteRequest{
		Depot:      domain.RoutePoint{X: 0, Y: 0},
		Stops:      []domain.RouteStop{{ID: "S1", Point: domain.RoutePoint{X: 5, Y: 0}, Latest: 480}},
		Vehicles:   []domain.RouteVehicle{{ID: "V1", CapacityKg: 1000, MaxRouteMin: 660}},
		SpeedKmh:   60,
		TimeLimitS: 5,
	}
}

func lateness(v int) *int { return &v }

func TestClientSolve_PostsRequestAndDecodesRoutes(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/routes:solve" {
			http.NotFound(w, r)
			return
		}
		var got map[string]any
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil || got["latenessPerMin"] != float64(0) {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		stops, _ := got["stops"].([]any)
		if len(stops) != 1 || stops[0].(map[string]any)["id"] != "S1" {
			http.Error(w, "bad request", http.StatusUnprocessableEntity)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"routes":[{"vehicleId":"V1","stops":[{"stopId":"S1","arrivalMin":10,"lateMin":0}],` +
			`"distanceKm":5.5,"endMin":30}],"dropped":["S9"]}`))
	}))
	defer srv.Close()

	req := sampleRequest()
	req.LatenessPerMin = lateness(0) // an explicit 0 must reach the solver, not its default of 50
	sol, err := New(srv.URL+"/", nil).Solve(context.Background(), req)
	if err != nil {
		t.Fatalf("solve: %v", err)
	}
	if len(sol.Routes) != 1 || sol.Routes[0].VehicleID != "V1" || sol.Routes[0].Stops[0].StopID != "S1" {
		t.Fatalf("unexpected routes %+v", sol.Routes)
	}
	if sol.Routes[0].DistanceKm != 5.5 || sol.Routes[0].EndMin != 30 {
		t.Fatalf("unexpected totals %+v", sol.Routes[0])
	}
	if len(sol.Dropped) != 1 || sol.Dropped[0] != "S9" {
		t.Fatalf("dropped = %v, want [S9]", sol.Dropped)
	}
}

func TestClientSolve_OmitsLegalEntityID(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		_, _ = w.Write([]byte(`{"routes":[],"dropped":[]}`))
	}))
	defer srv.Close()
	req := sampleRequest()
	req.LegalEntityID = "0190f0a0-0000-7000-8000-00000000000a"
	if _, err := New(srv.URL, nil).Solve(context.Background(), req); err != nil {
		t.Fatalf("solve: %v", err)
	}
	if _, present := got["legalEntityId"]; present {
		t.Fatalf("solver body carries legalEntityId: %v", got)
	}
}

func TestClientSolve_FailuresWrapErrUnavailable(t *testing.T) {
	cases := map[string]http.HandlerFunc{
		"http 503": func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusServiceUnavailable) },
		"malformed": func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte("not json"))
		},
	}
	for name, handler := range cases {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(handler)
			defer srv.Close()
			_, err := New(srv.URL, nil).Solve(context.Background(), sampleRequest())
			if !errors.Is(err, ErrUnavailable) {
				t.Fatalf("err = %v, want ErrUnavailable", err)
			}
		})
	}
}

func TestClientSolve_UnreachableSolverWrapsErrUnavailable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := srv.URL
	srv.Close()
	_, err := New(url, nil).Solve(context.Background(), sampleRequest())
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("err = %v, want ErrUnavailable", err)
	}
}

func TestClientSolve_RejectionIsErrRejected(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"detail":"duplicate stop id"}`, http.StatusUnprocessableEntity)
	}))
	defer srv.Close()
	_, err := New(srv.URL, nil).Solve(context.Background(), sampleRequest())
	if !errors.Is(err, ErrRejected) || errors.Is(err, ErrUnavailable) {
		t.Fatalf("err = %v, want ErrRejected only", err)
	}
}

func TestClientSolve_ContextDeadlineEndsTheCall(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()
	defer close(release)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := New(srv.URL, nil).Solve(ctx, sampleRequest())
	if !errors.Is(err, ErrUnavailable) || time.Since(start) > 5*time.Second {
		t.Fatalf("err = %v after %v, want ErrUnavailable at the deadline", err, time.Since(start))
	}
}

func TestClientStack_PostsPlanRequestAndFillsEmptyLists(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/load-plans:stack" {
			http.NotFound(w, r)
			return
		}
		var got domain.LoadPlanRequest
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil || got.Trailer.LengthMm != 13_600 || len(got.Pallets) != 1 {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		_, _ = w.Write([]byte(`{"placements":[{"palletIds":["P1"],"xMm":0,"yMm":0,"lengthMm":1200,"widthMm":1000,` +
			`"heightMm":900,"weightKg":500}],"floorPositionsUsed":1,"lengthUsedMm":1200,"layers":1,"totalWeightKg":500,` +
			`"axle":{"kingpinKg":456,"axleKg":44}}`))
	}))
	defer srv.Close()
	plan, err := New(srv.URL, nil).Stack(context.Background(), domain.LoadPlanRequest{
		Trailer: domain.Trailer{LengthMm: 13_600, WidthMm: 2_480, MaxHeightMm: 2_700, MaxWeightKg: 20_000},
		Pallets: []domain.PlanPallet{{ID: "P1", LengthMm: 1_200, WidthMm: 1_000, HeightMm: 900, WeightKg: 500}},
	})
	if err != nil {
		t.Fatalf("stack: %v", err)
	}
	if len(plan.Placements) != 1 || plan.Placements[0].PalletIDs[0] != "P1" || plan.Axle.KingpinKg != 456 {
		t.Fatalf("plan = %+v", plan)
	}
	if plan.Unplaced == nil || plan.LoadSequence == nil {
		t.Fatal("missing lists must come back empty, not null")
	}
}
