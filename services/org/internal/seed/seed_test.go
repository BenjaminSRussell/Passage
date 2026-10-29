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
	ctx := tenant.With(context.Background(), tenant.Principal{TenantID: tenantID, Duties: testkit.ScopeDuties("")})
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
	ctx := tenant.With(context.Background(), tenant.Principal{TenantID: tenantID, Duties: testkit.ScopeDuties("")})
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

func TestRun_ScopesSeedLookupsToTheirDatabaseKeys(t *testing.T) {
	s, _ := seededStore(t)
	tenantID := testkit.TenantID(7)
	ctx := tenant.With(context.Background(), tenant.Principal{TenantID: tenantID, Duties: testkit.ScopeDuties("")})
	other, err := s.CreateLegalEntity(ctx, store.LegalEntity{Code: "OTH", Name: "Other", Country: "US", Currency: "USD", TimeZone: "America/Chicago", DefaultLanguage: "en-US"}, "other-le")
	if err != nil {
		t.Fatal(err)
	}
	otherSite, err := s.CreateSite(ctx, store.Site{LegalEntityID: other.ID, Code: "OTH", Name: "Other", TimeZone: "America/Chicago"}, "other-site")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateWarehouse(ctx, store.Warehouse{SiteID: otherSite.ID, Code: "DET-QA", Name: "Other Detroit Quarantine", Type: "standard"}, "other-warehouse"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateOperatingUnit(ctx, store.OperatingUnit{Code: "OPS", Name: "Other OPS", Type: "department", DefaultLegalEntityID: &other.ID}, "other-ops"); err != nil {
		t.Fatal(err)
	}
	if _, err := seed.Run(ctx, s, tenantID, seed.DemoManufacturer); err != nil {
		t.Fatal(err)
	}
	units, err := s.ListOperatingUnits(ctx, store.OperatingUnitFilter{}, store.OperatingUnitKey{}, 100)
	if err != nil {
		t.Fatal(err)
	}
	foundBusinessUnit := false
	for _, unit := range units.Items {
		foundBusinessUnit = foundBusinessUnit || unit.Code == "OPS" && unit.Type == "business_unit"
	}
	if !foundBusinessUnit {
		t.Fatal("seed skipped business_unit/OPS because department/OPS already existed")
	}
	sites, err := s.ListSites(ctx, store.SiteFilter{}, store.SiteKey{}, 10)
	if err != nil {
		t.Fatal(err)
	}
	var det store.Site
	for _, site := range sites.Items {
		if site.LegalEntityID != other.ID && site.Code == "DET" {
			det = site
			break
		}
	}
	if det.ID == "" {
		t.Fatal("seeded Detroit site not found")
	}
	warehouses, err := s.ListWarehouses(ctx, store.WarehouseFilter{}, store.SiteKey{}, 100)
	if err != nil {
		t.Fatal(err)
	}
	var detQA string
	for _, warehouse := range warehouses.Items {
		if warehouse.SiteID == det.ID && warehouse.Code == "DET-QA" {
			detQA = warehouse.ID
		}
	}
	if detQA == "" {
		t.Fatal("seeded Detroit DET-QA warehouse missing because the other legal entity's DET-QA already existed")
	}
	locations, err := s.ListLocations(ctx, store.LocationFilter{}, store.SiteKey{}, 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, location := range locations.Items {
		if location.WarehouseID == detQA && location.Code == "QA-01" {
			return
		}
	}
	t.Fatal("Detroit quality hold location was not attached to the seeded Detroit DET-QA")
}

func TestRun_RelinksExistingSeedAddressAfterPointerIsCleared(t *testing.T) {
	s, pool := seededStore(t)
	tenantID := testkit.TenantID(7)
	runSeed(t, s, tenantID)
	ctx := tenant.With(context.Background(), tenant.Principal{TenantID: tenantID, Duties: testkit.ScopeDuties("")})
	sites, err := s.ListSites(ctx, store.SiteFilter{}, store.SiteKey{}, 10)
	if err != nil {
		t.Fatal(err)
	}
	site := sites.Items[0]
	if site.AddressID == nil {
		t.Fatal("seeded site address is missing")
	}
	address, err := s.GetAddress(ctx, *site.AddressID)
	if err != nil {
		t.Fatal(err)
	}
	address.City, address.ValidFrom = "Elsewhere", "2026-01-02"
	if _, err := s.ReviseAddress(ctx, address.ID, address.VersionID, address); err != nil {
		t.Fatal(err)
	}
	addressCount := counts(t, ctx, pool)["postal_address"]
	err = dbx.WithTenantTx(ctx, pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `update site set address_id = null where id = $1`, site.ID)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := seed.Run(ctx, s, tenantID, seed.DemoManufacturer); err != nil {
		t.Fatalf("rerun after clearing address pointer: %v", err)
	}
	updated, err := s.GetSite(ctx, site.ID)
	if err != nil || updated.AddressID == nil || *updated.AddressID != *site.AddressID {
		t.Fatalf("site address pointer = %v, err %v; want %s", updated.AddressID, err, *site.AddressID)
	}
	if got := counts(t, ctx, pool)["postal_address"]; got != addressCount {
		t.Fatalf("rerun created another address: count %d, want %d", got, addressCount)
	}
}

func TestRun_RecoversWhenSeedAddressWasRevisedWithoutBusinessPurposeAndPointerCleared(t *testing.T) {
	s, pool := seededStore(t)
	tenantID := testkit.TenantID(7)
	runSeed(t, s, tenantID)
	ctx := tenant.With(context.Background(), tenant.Principal{TenantID: tenantID})
	sites, err := s.ListSites(ctx, store.SiteFilter{}, store.SiteKey{}, 10)
	if err != nil {
		t.Fatal(err)
	}
	site := sites.Items[0]
	address, err := s.GetAddress(ctx, *site.AddressID)
	if err != nil {
		t.Fatal(err)
	}
	address.City, address.ValidFrom = "Elsewhere", "2026-01-02"
	address.Purposes = []store.AddressPurpose{{Purpose: "invoice", Primary: true}}
	if _, err := s.ReviseAddress(ctx, address.ID, address.VersionID, address); err != nil {
		t.Fatal(err)
	}
	addressCount := counts(t, ctx, pool)["postal_address"]
	err = dbx.WithTenantTx(ctx, pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `update site set address_id = null where id = $1`, site.ID)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := seed.Run(ctx, s, tenantID, seed.DemoManufacturer); err != nil {
		t.Fatalf("rerun: %v", err)
	}
	updated, err := s.GetSite(ctx, site.ID)
	if err != nil || updated.AddressID == nil || *updated.AddressID != *site.AddressID {
		t.Fatalf("site address pointer = %v, err %v; want %s", updated.AddressID, err, *site.AddressID)
	}
	if got := counts(t, ctx, pool)["postal_address"]; got != addressCount {
		t.Fatalf("rerun created another address: count %d, want %d", got, addressCount)
	}
}

func TestRun_RerunAfterEditingSeededRows(t *testing.T) {
	s, pool := seededStore(t)
	tenantID := testkit.TenantID(7)
	first := runSeed(t, s, tenantID)
	ctx := tenant.With(context.Background(), tenant.Principal{TenantID: tenantID, Duties: testkit.ScopeDuties("")})
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
