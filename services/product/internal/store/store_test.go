package store_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/product/internal/domain"
	"github.com/BenjaminSRussell/passage/services/product/internal/store"
	"github.com/BenjaminSRussell/passage/services/product/migrations"
)

func principal(n int) tenant.Principal {
	return tenant.Principal{TenantID: testkit.TenantID(n), LegalEntityID: testkit.TenantID(9), UserID: "user-test"}
}

func newStore(t *testing.T, n int) (*store.Postgres, context.Context) {
	pool := testkit.DB(t)
	ctx := tenant.With(context.Background(), principal(n))
	if err := dbx.Migrate(ctx, pool, migrations.FS); err != nil {
		t.Fatal(err)
	}
	return store.New(pool), ctx
}

func grantApp(ctx context.Context, admin *pgxpool.Pool, schema string) error {
	if _, err := admin.Exec(ctx, "grant usage on schema "+schema+" to passage_app"); err != nil {
		return err
	}
	_, err := admin.Exec(ctx, "grant select, insert, update, delete on all tables in schema "+schema+" to passage_app")
	return err
}

func TestMigrationsApplyWithRLS(t *testing.T) {
	admin, pool := testkit.DBWithApp(t)
	ctx := tenant.With(context.Background(), principal(1))
	if err := dbx.Migrate(ctx, admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	var schema string
	if err := admin.QueryRow(ctx, "select current_schema()").Scan(&schema); err != nil {
		t.Fatal(err)
	}
	if err := grantApp(ctx, admin, schema); err != nil {
		t.Fatal(err)
	}
	var currentUser string
	var roleSuper, roleBypass bool
	metadataErr := pool.QueryRow(ctx, `select current_user, r.rolsuper, r.rolbypassrls from pg_roles r where r.rolname = current_user`).Scan(&currentUser, &roleSuper, &roleBypass)
	if metadataErr != nil || currentUser != "passage_app" || roleSuper || roleBypass {
		t.Fatalf("RLS test must use non-bypass passage_app role: user=%q super=%v bypass=%v err=%v", currentUser, roleSuper, roleBypass, metadataErr)
	}
	for _, table := range []string{"product", "released_product", "product_identifier", "outbox", "idempotency_keys"} {
		var enabled, forced bool
		err := pool.QueryRow(ctx, `select relrowsecurity, relforcerowsecurity from pg_class where relname = $1 and relnamespace = current_schema()::regnamespace`, table).Scan(&enabled, &forced)
		if err != nil || !enabled || !forced {
			t.Errorf("%s: rls enabled=%v forced=%v err=%v", table, enabled, forced, err)
		}
	}
	app := store.New(pool)
	if _, err := app.CreateProduct(ctx, domain.Product{ProductNumber: "RLS1", Subtype: domain.SubtypeProduct, ProductType: domain.TypeItem, Name: "R", BaseUOM: "EA"}, "rls-1"); err != nil {
		t.Fatal(err)
	}
	other := tenant.With(context.Background(), principal(2))
	items, err := app.ListProducts(other, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 0 {
		t.Fatalf("tenant 2 must see 0 products, got %d", len(items))
	}
}

func TestExplodePersistsThousandAndLooksUp(t *testing.T) {
	s, ctx := newStore(t, 1)
	dims := make([]domain.Dimension, 3)
	for i, name := range []string{"size", "color", "style"} {
		vals := make([]string, 10)
		for j := 0; j < 10; j++ {
			vals[j] = fmt.Sprintf("%s%02d", name[:1], j)
		}
		dims[i] = domain.Dimension{Code: name, Name: name, Values: vals}
	}
	g, err := s.CreateDimensionGroup(ctx, domain.DimensionGroup{Code: "APPAREL", Name: "Apparel", Kind: "product", Fields: dims})
	if err != nil {
		t.Fatal(err)
	}
	master, err := s.CreateProduct(ctx, domain.Product{ProductNumber: "SHIRT", Subtype: domain.SubtypeMaster, ProductType: domain.TypeItem, Name: "Shirt", BaseUOM: "EA", DimensionGroupID: g.ID, LifecycleState: domain.StateActive}, "")
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	res, err := s.ExplodeVariants(ctx, master.ID, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Created) != 1000 {
		t.Fatalf("created %d", len(res.Created))
	}
	if time.Since(start) > 5*time.Second {
		t.Fatalf("persist explosion took %s", time.Since(start))
	}
	got, err := s.LookupVariant(ctx, master.ID, res.Created[0].DimensionValues)
	if err != nil || got.VariantHash != res.Created[0].VariantHash {
		t.Fatalf("lookup %v %+v", err, got)
	}
	again, err := s.ExplodeVariants(ctx, master.ID, nil, 0)
	if err != nil || len(again.Created) != 0 || len(again.Skipped) != 1000 {
		t.Fatalf("second explode %+v %v", again, err)
	}
}

func TestReleaseAndIdentifierAndPolicy(t *testing.T) {
	s, ctx := newStore(t, 1)
	if err := s.SeedLifecycle(ctx); err != nil {
		t.Fatal(err)
	}
	ig, err := s.CreateItemGroup(ctx, domain.ItemGroup{Code: "FG", Name: "Finished"})
	if err != nil {
		t.Fatal(err)
	}
	mg, err := s.CreateItemModelGroup(ctx, domain.ItemModelGroup{Code: "STD", Name: "Standard", CostingMethod: "standard", Stocked: true, PostPhysical: true, PostFinancial: true})
	if err != nil {
		t.Fatal(err)
	}
	p, err := s.CreateProduct(ctx, domain.Product{ProductNumber: "BOLT", Subtype: domain.SubtypeProduct, ProductType: domain.TypeItem, Name: "Bolt", BaseUOM: "EA", LifecycleState: domain.StateActive}, "idem-bolt")
	if err != nil {
		t.Fatal(err)
	}
	replay, err := s.CreateProduct(ctx, domain.Product{ProductNumber: "BOLT", Subtype: domain.SubtypeProduct, ProductType: domain.TypeItem, Name: "Bolt", BaseUOM: "EA"}, "idem-bolt")
	if err != nil || replay.ID != p.ID {
		t.Fatalf("idempotency %v %+v", err, replay)
	}
	rp, err := s.Release(ctx, p.ID, domain.ReleaseDefaults{
		LegalEntityID: testkit.TenantID(9), ItemGroupID: ig.ID, ItemModelGroupID: mg.ID, DefaultSiteID: testkit.TenantID(3), DefaultWarehouseID: testkit.TenantID(4),
		SiteDefaults: map[string]domain.SiteDefault{testkit.TenantID(5): {SiteID: testkit.TenantID(5), WarehouseID: testkit.TenantID(6)}},
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	site, err := s.ResolveSiteDefault(ctx, rp.ID, testkit.TenantID(5))
	if err != nil || site.WarehouseID != testkit.TenantID(6) {
		t.Fatalf("site override %+v %v", site, err)
	}
	entity, err := s.ResolveSiteDefault(ctx, rp.ID, testkit.TenantID(3))
	if err != nil || entity.WarehouseID != testkit.TenantID(4) {
		t.Fatalf("entity default %+v %v", entity, err)
	}
	if err := s.SetGS1Prefix(ctx, "0614141", 123457); err != nil {
		t.Fatal(err)
	}
	idn, err := s.AddIdentifier(ctx, p.ID, "", "EA", true)
	if err != nil || idn.Value != "06141411234579" {
		t.Fatalf("allocate %v %+v", err, idn)
	}
	patched, err := s.PatchReleased(ctx, rp.ID, domain.ReleasedProduct{LifecycleState: domain.StatePhaseOut})
	if err != nil {
		t.Fatal(err)
	}
	dec, err := s.EvaluatePolicy(ctx, patched.ID, "purchase_order")
	if err != nil || dec.Code != domain.CodeLCBlocked {
		t.Fatalf("policy %+v %v", dec, err)
	}
}

func TestExplodeLimitPagesAndListReleasedIDsKeyset(t *testing.T) {
	s, ctx := newStore(t, 1)
	dims := []domain.Dimension{{Code: "size", Name: "size", Values: []string{"s1", "s2", "s3"}}, {Code: "color", Name: "color", Values: []string{"c1", "c2"}}}
	g, err := s.CreateDimensionGroup(ctx, domain.DimensionGroup{Code: "PAGED", Name: "Paged", Kind: "product", Fields: dims})
	if err != nil {
		t.Fatal(err)
	}
	master, err := s.CreateProduct(ctx, domain.Product{ProductNumber: "PAGEDM", Subtype: domain.SubtypeMaster, ProductType: domain.TypeItem, Name: "Paged", BaseUOM: "EA", DimensionGroupID: g.ID, LifecycleState: domain.StateActive}, "")
	if err != nil {
		t.Fatal(err)
	}
	first, err := s.ExplodeVariants(ctx, master.ID, nil, 4)
	if err != nil || len(first.Created) != 4 || first.Remaining != 2 {
		t.Fatalf("first page %+v %v", first, err)
	}
	second, err := s.ExplodeVariants(ctx, master.ID, nil, 4)
	if err != nil || len(second.Created) != 2 || len(second.Skipped) != 4 || second.Remaining != 0 {
		t.Fatalf("second page %+v %v", second, err)
	}

	ig, err := s.CreateItemGroup(ctx, domain.ItemGroup{Code: "FG", Name: "Finished"})
	if err != nil {
		t.Fatal(err)
	}
	mg, err := s.CreateItemModelGroup(ctx, domain.ItemModelGroup{Code: "STD", Name: "Standard", CostingMethod: "standard", Stocked: true, PostPhysical: true, PostFinancial: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SeedLifecycle(ctx); err != nil {
		t.Fatal(err)
	}
	for _, number := range []string{"REL1", "REL2", "REL3"} {
		p, err := s.CreateProduct(ctx, domain.Product{ProductNumber: number, Subtype: domain.SubtypeProduct, ProductType: domain.TypeItem, Name: number, BaseUOM: "EA", LifecycleState: domain.StateActive}, "")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.Release(ctx, p.ID, domain.ReleaseDefaults{LegalEntityID: testkit.TenantID(9), ItemGroupID: ig.ID, ItemModelGroupID: mg.ID, DefaultSiteID: testkit.TenantID(3), DefaultWarehouseID: testkit.TenantID(4)}, ""); err != nil {
			t.Fatal(err)
		}
	}
	pageOne, err := s.ListReleasedIDs(ctx, "", 2)
	if err != nil || len(pageOne) != 2 {
		t.Fatalf("page one %v %v", pageOne, err)
	}
	pageTwo, err := s.ListReleasedIDs(ctx, pageOne[1], 2)
	if err != nil || len(pageTwo) != 1 || pageTwo[0] <= pageOne[1] {
		t.Fatalf("page two %v %v", pageTwo, err)
	}
	other := tenant.With(context.Background(), principal(2))
	if foreign, err := s.ListReleasedIDs(other, "", 10); err != nil || len(foreign) != 0 {
		t.Fatalf("tenant 2 must see no released products: %v %v", foreign, err)
	}
}
