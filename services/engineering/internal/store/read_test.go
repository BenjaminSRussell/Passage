package store_test

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/engineering/internal/domain"
	"github.com/BenjaminSRussell/passage/services/engineering/internal/store"
	"github.com/BenjaminSRussell/passage/services/engineering/migrations"
)

type seededRoutes struct{ first, second, draft, other domain.RouteVersion }

func seedRoutes(t *testing.T, s *store.Postgres, ctx context.Context) seededRoutes {
	t.Helper()
	jan, jun := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	seeded := seededRoutes{
		first:  newRoute("BIKE", "S1", 1, jan, routeOp(10, 20), routeOp(20, 0)),
		second: newRoute("BIKE", "S1", 2, jun, routeOp(10, 0)),
		draft:  newRoute("BIKE", "S1", 3, jun.AddDate(1, 0, 0), routeOp(10, 0)),
		other:  newRoute("TRIKE", "S2", 1, jan, routeOp(10, 0)),
	}
	seeded.first.EffectiveTo = jun.AddDate(0, 0, -1)
	for _, v := range []domain.RouteVersion{seeded.first, seeded.second, seeded.draft, seeded.other} {
		if _, err := s.CreateRouteVersion(ctx, v, store.Idem{}); err != nil {
			t.Fatal(err)
		}
	}
	for _, v := range []domain.RouteVersion{seeded.first, seeded.second} {
		if _, err := s.ActivateRouteVersion(ctx, v.ID); err != nil {
			t.Fatal(err)
		}
	}
	return seeded
}

func routeIDs(t *testing.T, s *store.Postgres, ctx context.Context, f store.RouteVersionFilter, limit int) (got []string, counts map[string]int) {
	t.Helper()
	counts = map[string]int{}
	after := ""
	for range 10 {
		page, err := s.ListRouteVersions(ctx, f, after, limit)
		if err != nil {
			t.Fatal(err)
		}
		for _, h := range page.Items {
			got = append(got, h.Version.ID)
			counts[h.Version.ID] = h.Operations
		}
		if page.NextID == "" {
			slices.Sort(got)
			return got, counts
		}
		after = page.NextID
	}
	t.Fatal("paging did not terminate")
	return nil, nil
}

func TestPostgres_ListRouteVersions_FiltersPagesAndCounts(t *testing.T) {
	s, _, ctx := newStore(t, 1)
	r := seedRoutes(t, s, ctx)
	sorted := func(vs ...domain.RouteVersion) []string {
		var out []string
		for _, v := range vs {
			out = append(out, v.ID)
		}
		slices.Sort(out)
		return out
	}
	tests := []struct {
		name   string
		filter store.RouteVersionFilter
		want   []string
	}{
		{"no filter", store.RouteVersionFilter{}, sorted(r.first, r.second, r.draft, r.other)},
		{"status", store.RouteVersionFilter{Status: domain.StatusDraft}, sorted(r.draft, r.other)},
		{"asOf in first range", store.RouteVersionFilter{ProductID: "BIKE", AsOf: time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)}, sorted(r.first)},
		{"asOf on second range start", store.RouteVersionFilter{ProductID: "BIKE", AsOf: time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)}, sorted(r.second)},
		{"asOf and status", store.RouteVersionFilter{Status: domain.StatusActive, AsOf: time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)}, sorted(r.second)},
	}
	for _, tc := range tests {
		for _, limit := range []int{1, store.MaxPageSize} {
			if got, _ := routeIDs(t, s, ctx, tc.filter, limit); !slices.Equal(got, tc.want) {
				t.Fatalf("%s (limit %d) = %v; want %v", tc.name, limit, got, tc.want)
			}
		}
	}
	_, counts := routeIDs(t, s, ctx, store.RouteVersionFilter{}, 2)
	if counts[r.first.ID] != 2 || counts[r.second.ID] != 1 {
		t.Fatalf("operation counts = %v; want 2 for the two-operation route and 1 for the other", counts)
	}
}

func TestPostgres_ListRouteVersions_RLSHidesOtherTenant(t *testing.T) {
	admin, app := testkit.DBWithApp(t)
	if err := dbx.Migrate(context.Background(), admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	s := store.New(app)
	seedRoutes(t, s, principalContext(1))
	intruder := principalContext(2)
	if page, err := s.ListRouteVersions(intruder, store.RouteVersionFilter{}, "", store.MaxPageSize); err != nil || len(page.Items) != 0 {
		t.Fatalf("tenant 2 list = %+v, %v; want nothing", page, err)
	}
}

func TestPostgres_ActiveRouteVersion_ByDate(t *testing.T) {
	s, _, ctx := newStore(t, 1)
	r := seedRoutes(t, s, ctx)
	on := func(product, site, date string) (domain.RouteVersion, error) {
		d, _ := time.Parse("2006-01-02", date)
		return s.ActiveRouteVersion(ctx, product, site, d)
	}
	for date, want := range map[string]domain.RouteVersion{"2026-01-01": r.first, "2026-05-31": r.first, "2026-06-01": r.second, "2031-01-01": r.second} {
		if got, err := on("BIKE", "S1", date); err != nil || got.ID != want.ID || len(got.Operations) != len(want.Operations) {
			t.Fatalf("%s = %+v, %v; want %s with %d operations", date, got, err, want.ID, len(want.Operations))
		}
	}
	for _, miss := range [][3]string{{"BIKE", "S1", "2025-12-31"}, {"BIKE", "S2", "2026-03-01"}, {"NOPE", "S1", "2026-03-01"}} {
		if _, err := on(miss[0], miss[1], miss[2]); problemCode(err) != store.CodeRouteNotFound {
			t.Fatalf("%v = %v; want %s", miss, err, store.CodeRouteNotFound)
		}
	}
}

func TestPostgres_GetBomVersion_LinesInOrderAndNotFound(t *testing.T) {
	s, _, ctx := newStore(t, 1)
	v := version("BIKE", domain.StatusDraft, releaseDay, "FRAME", "WHEEL", "SEAT")
	if _, err := s.CreateBomVersion(ctx, v, store.Idem{}, noCheck); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetBomVersion(ctx, v.ID)
	if err != nil || got.ID != v.ID || len(got.Lines) != 3 {
		t.Fatalf("get = %+v, %v", got, err)
	}
	if _, err := s.GetBomVersion(ctx, ids.New()); problemCode(err) != store.CodeBomNotFound {
		t.Fatalf("unknown id = %v; want %s", err, store.CodeBomNotFound)
	}
}
