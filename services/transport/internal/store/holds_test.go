package store_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/transport/internal/domain"
	"github.com/BenjaminSRussell/passage/services/transport/internal/store"
)

func asUser(n int, user string) context.Context {
	return tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(n), UserID: user, Duties: testkit.ScopeDuties("")})
}

func heldParties(statuses ...string) []domain.PartyScreening {
	out := make([]domain.PartyScreening, len(statuses))
	for i, s := range statuses {
		out[i] = domain.PartyScreening{Role: "SHIP_TO", Name: "Northwind " + string(rune('A'+i)), Status: s, Code: domain.CodeScreeningHold}
	}
	return out
}

// TestScreeningHold_AC3416 is AC-34.16 against Postgres: a POTENTIAL_MATCH holds
// the load, the tender is refused with TMS-0133 and nothing is stored; after a
// transport.screening-hold.release user releases it with a reason, tendering proceeds
// and the hold shows user and reason.
func TestScreeningHold_AC3416(t *testing.T) {
	s, _ := setup(t)
	ctx := tenantCtx(1)
	loads, err := s.BuildLoads(ctx, built(t, shipments(1, ids.New(), "800")), store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	load := loads[0].ID
	hold, err := s.RecordScreeningHold(ctx, load, heldParties(domain.ScreenPotentialMatch))
	if err != nil {
		t.Fatal(err)
	}
	if hold.Status != domain.HoldOpen || hold.LoadID != load || hold.CreatedBy != "user-test" || hold.Version != 1 ||
		len(hold.Parties) != 1 || hold.Parties[0].Status != domain.ScreenPotentialMatch {
		t.Fatalf("hold = %+v", hold)
	}
	if _, err := s.CreateTender(ctx, sentTender(load, time.Hour, ids.New()), store.Idem{}, t0); problemCode(t, err) != domain.CodeScreeningHold {
		t.Fatalf("tender of a held load = %v, want TMS-0133", err)
	}
	if list, _ := s.ListTenders(ctx, store.TenderFilter{LoadID: load}); len(list) != 0 {
		t.Fatalf("a tender was stored for a held load: %+v", list)
	}

	officer := asUser(1, "officer-1")
	released, err := s.ReleaseScreeningHold(officer, hold.ID, "date of birth differs from the listed person", t0)
	if err != nil {
		t.Fatal(err)
	}
	if released.Status != domain.HoldReleased || released.ReleasedBy != "officer-1" ||
		released.ReleaseReason != "date of birth differs from the listed person" || released.Version != 2 {
		t.Fatalf("released = %+v", released)
	}
	got, err := s.GetScreeningHold(ctx, hold.ID)
	if err != nil || got.ReleasedBy != "officer-1" || got.ReleasedAt == nil || !got.ReleasedAt.Equal(t0) {
		t.Fatalf("stored release = %+v, %v", got, err)
	}
	if _, err := s.CreateTender(ctx, sentTender(load, time.Hour, ids.New()), store.Idem{}, t0); err != nil {
		t.Fatalf("tender after release = %v", err)
	}

	// The same release again is a safe retry; another user's is refused.
	if again, err := s.ReleaseScreeningHold(officer, hold.ID, "date of birth differs from the listed person", t0.Add(time.Hour)); err != nil || again.Version != 2 {
		t.Fatalf("repeated release = %+v, %v", again, err)
	}
	if _, err := s.ReleaseScreeningHold(asUser(1, "officer-2"), hold.ID, "also fine", t0); problemCode(t, err) != domain.CodeHoldState {
		t.Fatalf("second officer's release = %v, want TMS-0122", err)
	}
}

func TestScreeningHold_OneOpenPerLoadAndBlockedStays(t *testing.T) {
	s, _ := setup(t)
	ctx := tenantCtx(1)
	loads, err := s.BuildLoads(ctx, built(t, shipments(1, ids.New(), "800")), store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	load := loads[0].ID
	first, err := s.RecordScreeningHold(ctx, load, heldParties(domain.ScreenPending))
	if err != nil {
		t.Fatal(err)
	}
	// Concurrent screens of the same load keep one OPEN hold.
	var wg sync.WaitGroup
	errs := make([]error, 4)
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = s.RecordScreeningHold(ctx, load, heldParties(domain.ScreenPotentialMatch, domain.ScreenBlocked))
		}(i)
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	open, err := s.ListScreeningHolds(ctx, store.HoldFilter{Status: domain.HoldOpen, LoadID: load})
	if err != nil || len(open) != 1 || open[0].ID != first.ID || open[0].Version != 5 || len(open[0].Parties) != 2 {
		t.Fatalf("open holds = %+v, %v", open, err)
	}
	if _, err := s.ReleaseScreeningHold(ctx, first.ID, "cleared", t0); problemCode(t, err) != domain.CodeScreeningHold {
		t.Fatalf("release with a BLOCKED party = %v, want TMS-0133", err)
	}
	if _, err := s.RecordScreeningHold(ctx, ids.New(), heldParties(domain.ScreenPending)); problemCode(t, err) != domain.CodeNotFound {
		t.Fatalf("hold on an unknown load = %v", err)
	}
	if _, err := s.ReleaseScreeningHold(ctx, ids.New(), "x", t0); problemCode(t, err) != domain.CodeNotFound {
		t.Fatalf("release of an unknown hold = %v", err)
	}
}

func TestScreeningHold_TenantIsolated(t *testing.T) {
	s, admin := setup(t)
	ctx := tenantCtx(1)
	loads, err := s.BuildLoads(ctx, built(t, shipments(1, ids.New(), "800")), store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	hold, err := s.RecordScreeningHold(ctx, loads[0].ID, heldParties(domain.ScreenPotentialMatch))
	if err != nil {
		t.Fatal(err)
	}
	other := tenantCtx(2)
	if _, err := s.GetScreeningHold(other, hold.ID); problemCode(t, err) != domain.CodeNotFound {
		t.Fatalf("other tenant get = %v", err)
	}
	if list, err := s.ListScreeningHolds(other, store.HoldFilter{}); err != nil || len(list) != 0 {
		t.Fatalf("other tenant list = %+v, %v", list, err)
	}
	if _, err := s.ReleaseScreeningHold(other, hold.ID, "x", t0); problemCode(t, err) != domain.CodeNotFound {
		t.Fatalf("other tenant release = %v", err)
	}
	if _, err := s.RecordScreeningHold(other, loads[0].ID, heldParties(domain.ScreenPending)); problemCode(t, err) != domain.CodeNotFound {
		t.Fatalf("other tenant hold on this load = %v", err)
	}
	var rls, force bool
	if err := admin.QueryRow(context.Background(), `select relrowsecurity, relforcerowsecurity from pg_class
		where relname = 'screening_hold' and relnamespace = current_schema()::regnamespace`).Scan(&rls, &force); err != nil || !rls || !force {
		t.Fatalf("screening_hold RLS enabled %v forced %v (%v)", rls, force, err)
	}
}

func routeRequest(weight int) domain.RouteRequest {
	r := domain.RouteRequest{
		Depot:    domain.RoutePoint{},
		Stops:    []domain.RouteStop{{ID: "S1", Point: domain.RoutePoint{X: 5}, Latest: 480, WeightKg: weight}},
		Vehicles: []domain.RouteVehicle{{ID: "V1", CapacityKg: 1000}},
	}
	r.Normalize()
	return r
}

func TestRoutePlan_StoredOncePerRequestHashAndIsolated(t *testing.T) {
	s, admin := setup(t)
	ctx := tenantCtx(1)
	req := routeRequest(100)
	hash, _ := req.Hash()
	sol := domain.RouteSolution{Routes: []domain.RouteSolutionRoute{{VehicleID: "V1", DistanceKm: 10.25,
		Stops: []domain.RouteSolutionStop{{StopID: "S1", ArrivalMin: 5}}, EndMin: 20}}, Dropped: []string{}}
	if _, found, err := s.FindRoutePlan(ctx, hash, ""); err != nil || found {
		t.Fatalf("find before save = %v, %v", found, err)
	}
	plan, created, err := s.SaveRoutePlan(ctx, domain.NewRoutePlan(req, hash, sol))
	if err != nil || !created || plan.ID == "" || plan.RouteCount != 1 || plan.TotalDistanceKm != 10.25 || plan.CreatedBy != "user-test" {
		t.Fatalf("save = %+v created %v, %v", plan, created, err)
	}
	if *plan.Request.LatenessPerMin != domain.DefaultLatenessPerMin || plan.Solution.Routes[0].Stops[0].StopID != "S1" {
		t.Fatalf("stored request and routes = %+v", plan)
	}
	again, created, err := s.SaveRoutePlan(ctx, domain.NewRoutePlan(req, hash, sol))
	if err != nil || created || again.ID != plan.ID {
		t.Fatalf("second save = %+v created %v, %v", again, created, err)
	}
	found, ok, err := s.FindRoutePlan(ctx, hash, "")
	if err != nil || !ok || found.ID != plan.ID {
		t.Fatalf("find = %+v %v, %v", found, ok, err)
	}
	other := routeRequest(200)
	otherHash, _ := other.Hash()
	if _, _, err := s.SaveRoutePlan(ctx, domain.NewRoutePlan(other, otherHash, sol)); err != nil {
		t.Fatal(err)
	}
	list, err := s.ListRoutePlans(ctx)
	if err != nil || len(list) != 2 || list[0].RequestHash != otherHash {
		t.Fatalf("list newest first = %+v, %v", list, err)
	}

	t2 := tenantCtx(2)
	if _, ok, err := s.FindRoutePlan(t2, hash, ""); err != nil || ok {
		t.Fatalf("other tenant find = %v, %v", ok, err)
	}
	if _, err := s.GetRoutePlan(t2, plan.ID); problemCode(t, err) != domain.CodeNotFound {
		t.Fatalf("other tenant get = %v", err)
	}
	// The same request in another tenant is its own plan.
	if p2, created, err := s.SaveRoutePlan(t2, domain.NewRoutePlan(req, hash, sol)); err != nil || !created || p2.ID == plan.ID {
		t.Fatalf("other tenant save = %+v created %v, %v", p2, created, err)
	}
	var rls, force bool
	if err := admin.QueryRow(context.Background(), `select relrowsecurity, relforcerowsecurity from pg_class
		where relname = 'route_plan' and relnamespace = current_schema()::regnamespace`).Scan(&rls, &force); err != nil || !rls || !force {
		t.Fatalf("route_plan RLS enabled %v forced %v (%v)", rls, force, err)
	}
}
