package store

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/costing/migrations"
)

// scopedCtx returns a tenant-wide principal (no legal-entity narrowing), so the
// restrictive legal-entity policy sees the '*' scope.
func scopedCtx(tenantID string) context.Context {
	return tenant.With(context.Background(), tenant.Principal{TenantID: tenantID, Duties: map[string]struct{}{}})
}

func setup(t *testing.T) (*Postgres, *pgxpool.Pool) {
	t.Helper()
	admin, app := testkit.DBWithApp(t)
	if err := dbx.Migrate(context.Background(), admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	return New(app), app
}

func problemCode(err error) string {
	var p *httpx.Problem
	if errors.As(err, &p) {
		return p.Code
	}
	return ""
}

func TestPostgres_CostingVersionIdempotentAndUnique(t *testing.T) {
	s, _ := setup(t)
	ctx := scopedCtx(testkit.TenantID(1))
	in := CostingVersion{LegalEntityID: ids.New(), Code: "STD-2026", Type: "STANDARD", Currency: "EUR", EffectiveFrom: "2026-01-01"}

	first, created, err := s.CreateCostingVersion(ctx, in, "key-1")
	if err != nil || !created {
		t.Fatalf("create: created=%v, %v", created, err)
	}
	replay, created, err := s.CreateCostingVersion(ctx, in, "key-1")
	if err != nil {
		t.Fatal(err)
	}
	if replay.ID != first.ID || created {
		t.Errorf("replay id %s created=%v, want %s and false", replay.ID, created, first.ID)
	}
	other := in
	other.Code = "STD-2027"
	if _, _, err := s.CreateCostingVersion(ctx, other, "key-1"); problemCode(err) != CodeIdempotencyReuse {
		t.Errorf("same key, other body: %v, want %s", err, CodeIdempotencyReuse)
	}
	if _, _, err := s.CreateCostingVersion(ctx, in, "key-2"); problemCode(err) != CodeVersionCodeDuplicate {
		t.Errorf("duplicate code: %v, want %s", err, CodeVersionCodeDuplicate)
	}
	// Each legal entity has its own STD-{year}: the same code in another
	// entity is a different version.
	otherEntity := in
	otherEntity.LegalEntityID = ids.New()
	if v, created, err := s.CreateCostingVersion(ctx, otherEntity, "key-3"); err != nil || !created || v.ID == first.ID {
		t.Errorf("same code, other legal entity: %+v created=%v, %v", v, created, err)
	}
}

func TestPostgres_ImportItemCosts(t *testing.T) {
	s, app := setup(t)
	ctx := scopedCtx(testkit.TenantID(2))
	v, _, err := s.CreateCostingVersion(ctx, CostingVersion{LegalEntityID: ids.New(), Code: "PLAN-1", Type: "PLANNED", Currency: "EUR", EffectiveFrom: "2026-01-01"}, "key-p")
	if err != nil {
		t.Fatal(err)
	}
	items := []ItemCost{{ProductID: ids.New(), SiteID: ids.New(), UnitCost: "10"}, {ProductID: ids.New(), SiteID: ids.New(), UnitCost: "4.125"}}
	if n, err := s.ImportItemCosts(ctx, v.ID, items); err != nil || n != 2 {
		t.Fatalf("import = %d, %v", n, err)
	}
	// Re-import replaces the pending cost; it does not add a row.
	items[0].UnitCost = "11.25"
	if _, err := s.ImportItemCosts(ctx, v.ID, items); err != nil {
		t.Fatalf("re-import: %v", err)
	}
	var rows, version int
	var cost, status, effective string
	err = dbx.WithTenantTx(ctx, app, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `select count(*) from item_cost where version_id = $1`, v.ID).Scan(&rows); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `select unit_cost::text, status, effective_from::text from item_cost where version_id = $1 and product_id = $2`,
			v.ID, items[0].ProductID).Scan(&cost, &status, &effective); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `select version from costing_version where id = $1`, v.ID).Scan(&version)
	})
	if err != nil {
		t.Fatal(err)
	}
	if rows != 2 || cost != "11.250000" || status != "PENDING" || effective != "2026-01-01" {
		t.Errorf("stored rows=%d cost=%s status=%s effective=%s, want 2, 11.250000, PENDING, 2026-01-01", rows, cost, status, effective)
	}
	if version != 3 {
		t.Errorf("version after two imports = %d, want 3", version)
	}
	if _, err := s.ImportItemCosts(ctx, ids.New(), items); problemCode(err) != CodeVersionNotFound {
		t.Errorf("unknown version: %v, want %s", err, CodeVersionNotFound)
	}
}

func TestPostgres_RowLevelSecurityHidesOtherTenants(t *testing.T) {
	s, _ := setup(t)
	v, _, err := s.CreateCostingVersion(scopedCtx(testkit.TenantID(3)), CostingVersion{LegalEntityID: ids.New(), Code: "RLS-1", Type: "STANDARD", Currency: "EUR", EffectiveFrom: "2026-01-01"}, "key-rls")
	if err != nil {
		t.Fatal(err)
	}
	// Another tenant cannot import into this version: the lookup finds no row.
	_, err = s.ImportItemCosts(scopedCtx(testkit.TenantID(4)), v.ID, []ItemCost{{ProductID: ids.New(), SiteID: ids.New(), UnitCost: "1"}})
	if problemCode(err) != CodeVersionNotFound {
		t.Errorf("cross-tenant import: %v, want %s", err, CodeVersionNotFound)
	}
}

func TestPostgres_LegalEntityScopeHidesOtherEntities(t *testing.T) {
	s, _ := setup(t)
	tenantID := testkit.TenantID(5)
	entityA, entityB := ids.New(), ids.New()
	v, _, err := s.CreateCostingVersion(scopedCtx(tenantID), CostingVersion{LegalEntityID: entityA, Code: "STD-2026", Type: "STANDARD", Currency: "EUR", EffectiveFrom: "2026-01-01"}, "key-le")
	if err != nil {
		t.Fatal(err)
	}
	narrowed := tenant.With(context.Background(), tenant.Principal{TenantID: tenantID, LegalEntityID: entityB, Duties: map[string]struct{}{}})
	_, err = s.ImportItemCosts(narrowed, v.ID, []ItemCost{{ProductID: ids.New(), SiteID: ids.New(), UnitCost: "1"}})
	if problemCode(err) != CodeVersionNotFound {
		t.Errorf("import from another legal entity's scope: %v, want %s", err, CodeVersionNotFound)
	}
}
