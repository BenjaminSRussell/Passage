package seed_test

import (
	"context"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/org/internal/seed"
	"github.com/BenjaminSRussell/passage/services/org/internal/store"
	"github.com/BenjaminSRussell/passage/services/org/migrations"
)

func seededStore(t *testing.T) (*store.Postgres, *pgxpool.Pool) {
	t.Helper()
	admin, app := testkit.DBWithApp(t)
	ctx := context.Background()
	if err := dbx.Migrate(ctx, admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	if err := store.SeedReference(ctx, admin, migrations.Reference); err != nil {
		t.Fatal(err)
	}
	return store.New(app), app
}

func runSeed(t *testing.T, s *store.Postgres, tenantID string) seed.Result {
	t.Helper()
	result, err := seed.Run(context.Background(), s, tenantID, seed.DemoManufacturer)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func counts(t *testing.T, ctx context.Context, pool *pgxpool.Pool) map[string]int {
	t.Helper()
	got := map[string]int{}
	err := dbx.WithTenantTx(ctx, pool, func(tx pgx.Tx) error {
		for _, table := range []string{"legal_entity", "operating_unit", "site", "warehouse", "location", "postal_address", "hierarchy_type", "team_type", "team", "outbox"} {
			var n int
			if err := tx.QueryRow(ctx, "select count(*) from "+table).Scan(&n); err != nil {
				return err
			}
			got[table] = n
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func TestRun_DemoManufacturerIsComplete(t *testing.T) {
	s, pool := seededStore(t)
	tenantID := testkit.TenantID(7)
	result := runSeed(t, s, tenantID)
	ctx := tenant.With(context.Background(), tenant.Principal{TenantID: tenantID})
	got := counts(t, ctx, pool)
	want := map[string]int{"legal_entity": 3, "operating_unit": 40, "site": 2, "warehouse": 5, "location": 6, "postal_address": 2, "hierarchy_type": 3, "team_type": 2, "team": 3}
	total := 0
	for table, n := range want {
		total += n
		if got[table] != n {
			t.Errorf("%s rows = %d, want %d", table, got[table], n)
		}
	}
	if result.Created != total || result.Present != 0 {
		t.Errorf("first run = %+v, want %d created and none present", result, total)
	}
	sites, err := s.ListSites(ctx, store.SiteFilter{}, store.SiteKey{}, 10)
	if err != nil {
		t.Fatal(err)
	}
	for _, site := range sites.Items {
		if site.AddressID == nil || site.DefaultWarehouseID == nil {
			t.Errorf("site %s address %v default warehouse %v, want both set", site.Code, site.AddressID, site.DefaultWarehouseID)
		}
	}
}

func TestRun_RerunChangesNothing(t *testing.T) {
	s, pool := seededStore(t)
	tenantID := testkit.TenantID(7)
	first := runSeed(t, s, tenantID)
	ctx := tenant.With(context.Background(), tenant.Principal{TenantID: tenantID})
	before := counts(t, ctx, pool)
	rerun := runSeed(t, s, tenantID)
	after := counts(t, ctx, pool)
	for table, n := range before {
		if after[table] != n {
			t.Errorf("%s rows %d before the rerun and %d after", table, n, after[table])
		}
	}
	if rerun.Created != 0 || rerun.Present != first.Created {
		t.Errorf("rerun = %+v, want 0 created and %d present", rerun, first.Created)
	}
}

func TestRun_RerunAfterEditingSeededRows(t *testing.T) {
	s, pool := seededStore(t)
	tenantID := testkit.TenantID(7)
	first := runSeed(t, s, tenantID)
	ctx := tenant.With(context.Background(), tenant.Principal{TenantID: tenantID})
	renamed := "Renamed"

	entities, err := s.ListLegalEntities(ctx, store.LegalEntityFilter{}, "", 10)
	units, unitsErr := s.ListOperatingUnits(ctx, store.OperatingUnitFilter{}, store.OperatingUnitKey{}, 100)
	sites, sitesErr := s.ListSites(ctx, store.SiteFilter{}, store.SiteKey{}, 10)
	warehouses, warehousesErr := s.ListWarehouses(ctx, store.WarehouseFilter{}, store.SiteKey{}, 10)
	teams, teamsErr := s.ListTeams(ctx, store.TeamFilter{}, "", 10)
	for _, e := range []error{err, unitsErr, sitesErr, warehousesErr, teamsErr} {
		if e != nil {
			t.Fatal(e)
		}
	}
	steps := map[string]func() error{
		"legal entity": func() error {
			_, e := s.UpdateLegalEntity(ctx, entities.Items[0].ID, entities.Items[0].Version, store.LegalEntityPatch{Name: &renamed})
			return e
		},
		"operating unit": func() error {
			_, e := s.UpdateOperatingUnit(ctx, units.Items[0].ID, units.Items[0].Version, store.OperatingUnitPatch{Name: &renamed})
			return e
		},
		"site": func() error {
			_, e := s.UpdateSite(ctx, sites.Items[0].ID, sites.Items[0].Version, store.SitePatch{Name: &renamed})
			return e
		},
		"default warehouse": func() error {
			for _, w := range warehouses.Items {
				if w.ID == *sites.Items[0].DefaultWarehouseID {
					_, e := s.UpdateWarehouse(ctx, w.ID, w.Version, store.WarehousePatch{Name: &renamed})
					return e
				}
			}
			return nil
		},
		"team": func() error {
			_, e := s.UpdateTeam(ctx, teams.Items[0].ID, teams.Items[0].Version, store.TeamPatch{Name: &renamed})
			return e
		},
		"site address": func() error {
			address, e := s.GetAddress(ctx, *sites.Items[0].AddressID)
			if e != nil {
				return e
			}
			address.City, address.ValidFrom = "Elsewhere", "2026-01-02"
			_, e = s.ReviseAddress(ctx, address.ID, address.VersionID, address)
			return e
		},
	}
	for name, edit := range steps {
		if e := edit(); e != nil {
			t.Fatalf("edit %s: %v", name, e)
		}
	}
	before := counts(t, ctx, pool)
	rerun, err := seed.Run(context.Background(), s, tenantID, seed.DemoManufacturer)
	if err != nil {
		t.Fatalf("rerun after edits: %v", err)
	}
	for table, n := range counts(t, ctx, pool) {
		if before[table] != n {
			t.Errorf("%s rows %d before the rerun and %d after", table, before[table], n)
		}
	}
	if rerun.Created != 0 || rerun.Present != first.Created {
		t.Errorf("rerun = %+v, want 0 created and %d present", rerun, first.Created)
	}
}

func TestRun_UnknownPackage(t *testing.T) {
	_, err := seed.Run(context.Background(), nil, testkit.TenantID(7), "nope")
	if err == nil || !strings.Contains(err.Error(), `unknown seed package "nope"`) {
		t.Fatalf("err = %v, want an unknown package error", err)
	}
}
