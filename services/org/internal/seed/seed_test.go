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

func counts(t *testing.T, ctx context.Context, pool *pgxpool.Pool) map[string]int {
	t.Helper()
	got := map[string]int{}
	err := dbx.WithTenantTx(ctx, pool, func(tx pgx.Tx) error {
		for _, table := range []string{"legal_entity", "operating_unit", "site", "warehouse", "location", "postal_address", "team_type", "team", "outbox"} {
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
	if err := seed.Run(context.Background(), s, tenantID, seed.DemoManufacturer); err != nil {
		t.Fatal(err)
	}
	ctx := tenant.With(context.Background(), tenant.Principal{TenantID: tenantID})
	got := counts(t, ctx, pool)
	want := map[string]int{"legal_entity": 3, "operating_unit": 12, "site": 2, "warehouse": 5, "location": 6, "postal_address": 2, "team_type": 2, "team": 3}
	for table, n := range want {
		if got[table] != n {
			t.Errorf("%s rows = %d, want %d", table, got[table], n)
		}
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
	if err := seed.Run(context.Background(), s, tenantID, seed.DemoManufacturer); err != nil {
		t.Fatal(err)
	}
	ctx := tenant.With(context.Background(), tenant.Principal{TenantID: tenantID})
	before := counts(t, ctx, pool)
	if err := seed.Run(context.Background(), s, tenantID, seed.DemoManufacturer); err != nil {
		t.Fatalf("rerun: %v", err)
	}
	after := counts(t, ctx, pool)
	for table, n := range before {
		if after[table] != n {
			t.Errorf("%s rows %d before the rerun and %d after", table, n, after[table])
		}
	}
}

func TestRun_UnknownPackage(t *testing.T) {
	err := seed.Run(context.Background(), nil, testkit.TenantID(7), "nope")
	if err == nil || !strings.Contains(err.Error(), `unknown seed package "nope"`) {
		t.Fatalf("err = %v, want an unknown package error", err)
	}
}
