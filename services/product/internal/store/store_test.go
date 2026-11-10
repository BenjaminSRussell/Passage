package store_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
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
	items, err := app.ListProducts(other, "", "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 0 {
		t.Fatalf("tenant 2 must see 0 products, got %d", len(items))
	}
	if own, err := app.ListProducts(ctx, "rls", "", 10); err != nil || len(own) != 1 {
		t.Fatalf("tenant 1 text search: %d %v", len(own), err)
	}
	if none, err := app.ListProducts(ctx, "%", "", 10); err != nil || len(none) != 0 {
		t.Fatalf("a percent sign is literal text: %d %v", len(none), err)
	}
	if found, err := app.Search(ctx, "rls", 10); err != nil || len(found) != 1 {
		t.Fatalf("search by text: %d %v", len(found), err)
	}
	if none, err := app.Search(ctx, "%", 10); err != nil || len(none) != 0 {
		t.Fatalf("a percent sign in search is literal text: %d %v", len(none), err)
	}
	if past, err := app.ListProducts(ctx, "", "RLS1", 10); err != nil || len(past) != 0 {
		t.Fatalf("after the last number: %d %v", len(past), err)
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

func TestGetSavedConfigurationBelongsToItsMasterAndItsVariants(t *testing.T) {
	pool := testkit.DB(t)
	ctx := tenant.With(context.Background(), principal(1))
	if err := dbx.Migrate(ctx, pool, migrations.FS); err != nil {
		t.Fatal(err)
	}
	s := store.New(pool)
	create := func(number, subtype string) domain.Product {
		p, err := s.CreateProduct(ctx, domain.Product{ProductNumber: number, Subtype: subtype, ProductType: domain.TypeItem, Name: number, BaseUOM: "EA"}, "")
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	master, other := create("BIKE", domain.SubtypeMaster), create("TRIKE", domain.SubtypeProduct)
	variant := testkit.TenantID(70)
	model, config := testkit.TenantID(71), testkit.TenantID(72)
	for _, stmt := range []struct {
		sql  string
		args []any
	}{
		{`insert into product (tenant_id, id, product_number, subtype, product_type, name, base_uom, master_id, variant_hash) values ($1, $2, 'BIKE-V1', 'variant', 'item', 'v', 'EA', $3, 'h1')`, []any{testkit.TenantID(1), variant, master.ID}},
		{`insert into configuration_model (tenant_id, id, master_id, version, status, model) values ($1, $2, $3, 1, 'active', '{}')`, []any{testkit.TenantID(1), model, master.ID}},
		{`insert into saved_configuration (tenant_id, id, model_id, model_version, name, selections, status) values ($1, $2, $3, 1, 'red', '{}', 'valid')`, []any{testkit.TenantID(1), config, model}},
	} {
		if _, err := pool.Exec(ctx, stmt.sql, stmt.args...); err != nil {
			t.Fatal(err)
		}
	}
	for name, tc := range map[string]struct {
		productID string
		want      bool
	}{"master": {master.ID, true}, "variant of the master": {variant, true}, "another product": {other.ID, false}} {
		got, err := s.GetSavedConfiguration(ctx, config, tc.productID)
		if err != nil || got.BelongsToProduct != tc.want || got.MasterID != master.ID || got.Status != "valid" || got.ModelID != model {
			t.Fatalf("%s: %+v %v", name, got, err)
		}
	}
	if _, err := s.GetSavedConfiguration(ctx, testkit.TenantID(99), master.ID); err == nil {
		t.Fatal("an unknown configuration must be refused")
	}
	if _, err := s.GetSavedConfiguration(tenant.With(context.Background(), principal(2)), config, master.ID); err == nil {
		t.Fatal("another tenant must not read the configuration")
	}
}

func TestImportConflictsNameTheExistingProductAndUpdateUsesVersion(t *testing.T) {
	s, ctx := newStore(t, 1)
	first := testkit.TenantID(70)
	p, err := s.CreateProduct(ctx, domain.Product{ID: first, ProductNumber: "LEG-1", Subtype: domain.SubtypeProduct, ProductType: domain.TypeItem, Name: "Legacy", BaseUOM: "EA"}, "")
	if err != nil || p.ID != first {
		t.Fatalf("create with supplied id: %v %+v", err, p)
	}
	_, err = s.CreateProduct(ctx, domain.Product{ID: testkit.TenantID(71), ProductNumber: "LEG-1", Subtype: domain.SubtypeProduct, ProductType: domain.TypeItem, Name: "Dup", BaseUOM: "EA"}, "")
	var pr *httpx.Problem
	if !errors.As(err, &pr) || pr.Status != 409 || pr.Errors["existingId"] != first {
		t.Fatalf("duplicate number must be 409 naming %s, got %v", first, err)
	}
	_, err = s.CreateProduct(ctx, domain.Product{ID: first, ProductNumber: "LEG-2", Subtype: domain.SubtypeProduct, ProductType: domain.TypeItem, Name: "Other", BaseUOM: "EA"}, "")
	if !errors.As(err, &pr) || pr.Status != 409 || pr.Code != "PRD-ID-TAKEN" {
		t.Fatalf("taken id must be 409 PRD-ID-TAKEN, got %v", err)
	}
	name := "Renamed"
	got, err := s.UpdateProduct(ctx, first, domain.ProductPatch{Name: &name}, 1)
	if err != nil || got.Name != name || got.Version != 2 {
		t.Fatalf("update: %v %+v", err, got)
	}
	if _, err := s.UpdateProduct(ctx, first, domain.ProductPatch{Name: &name}, 1); !errors.As(err, &pr) || pr.Status != 412 {
		t.Fatalf("stale version must be 412, got %v", err)
	}
}

// appStore runs as the NOSUPERUSER NOBYPASSRLS passage_app role so tenant
// isolation is enforced by row-level security, not by the queries alone.
func appStore(t *testing.T) (*store.Postgres, *pgxpool.Pool) {
	t.Helper()
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
	return store.New(pool), admin
}

func TestImportIDsAndNumbersAreScopedToTheTenantUnderRLS(t *testing.T) {
	s, admin := appStore(t)
	one := tenant.With(context.Background(), principal(1))
	two := tenant.With(context.Background(), principal(2))
	id := testkit.TenantID(80)
	mk := func(ctx context.Context, number string) error {
		_, err := s.CreateProduct(ctx, domain.Product{ID: id, ProductNumber: number, Subtype: domain.SubtypeProduct, ProductType: domain.TypeItem, Name: "Legacy", BaseUOM: "EA"}, "")
		return err
	}
	if err := mk(one, "IMP-1"); err != nil {
		t.Fatal(err)
	}
	// Another tenant reusing the same id and number neither collides nor learns that tenant 1 has them.
	if err := mk(two, "IMP-1"); err != nil {
		t.Fatalf("same id and number in another tenant must be accepted, got %v", err)
	}
	var pr *httpx.Problem
	if err := mk(one, "IMP-2"); !errors.As(err, &pr) || pr.Status != 409 || pr.Code != "PRD-ID-TAKEN" {
		t.Fatalf("id reused inside the tenant = %v, want 409 PRD-ID-TAKEN", err)
	}
	name := "Tenant two"
	if _, err := s.UpdateProduct(two, id, domain.ProductPatch{Name: &name}, 1); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetProduct(one, id)
	if err != nil || got.Name != "Legacy" || got.Version != 1 {
		t.Fatalf("tenant 2's update must not touch tenant 1's product: %v %+v", err, got)
	}
	var events int
	var fields string
	if err := admin.QueryRow(context.Background(), `select count(*), coalesce(min(payload->'data'->>'changedFields'),'') from outbox where type=$1 and tenant_id=$2`,
		"dev.passage.product.product.updated.v1", testkit.TenantID(2)).Scan(&events, &fields); err != nil || events != 1 || fields != `["name"]` {
		t.Fatalf("one product.updated event for tenant 2 naming the changed field, got %d %s %v", events, fields, err)
	}
	var indexed bool
	if err := admin.QueryRow(context.Background(), `select payload->'data'->>'name' = 'Tenant two' and coalesce(payload->'data'->>'productNumber','') <> '' and coalesce(payload->'data'->>'lifecycleState','') <> '' from outbox where type=$1 and tenant_id=$2`,
		"dev.passage.product.product.updated.v1", testkit.TenantID(2)).Scan(&indexed); err != nil || !indexed {
		t.Fatalf("product.updated must carry name, number and lifecycle state for the search index: %v %v", indexed, err)
	}
	if _, err := s.UpdateProduct(tenant.With(context.Background(), principal(3)), id, domain.ProductPatch{Name: &name}, 1); !errors.As(err, &pr) || pr.Status != 404 {
		t.Fatalf("update from a tenant without the product = %v, want 404", err)
	}
}

func TestUpdateProductFollowsTheLifecycleMachineAndActivationAttributes(t *testing.T) {
	s, ctx := newStore(t, 1)
	if err := s.UpsertAttribute(ctx, domain.AttributeType{Code: "material", Kind: "text", Required: true}); err != nil {
		t.Fatal(err)
	}
	p, err := s.CreateProduct(ctx, domain.Product{ProductNumber: "LC-1", Subtype: domain.SubtypeProduct, ProductType: domain.TypeItem, Name: "LC", BaseUOM: "EA"}, "")
	if err != nil {
		t.Fatal(err)
	}
	state := func(v string) domain.ProductPatch { return domain.ProductPatch{LifecycleState: &v} }
	var pr *httpx.Problem
	if _, err := s.UpdateProduct(ctx, p.ID, state(domain.StatePhaseOut), 1); !errors.As(err, &pr) || pr.Code != domain.CodeLifecycleIllegal {
		t.Fatalf("draft -> phase_out = %v, want %s", err, domain.CodeLifecycleIllegal)
	}
	if _, err := s.UpdateProduct(ctx, p.ID, state(domain.StateActive), 1); !errors.As(err, &pr) || pr.Status != 422 || pr.Code != domain.CodeAttrRequired {
		t.Fatalf("activation without the required attribute = %v, want 422 %s (BR-8.9)", err, domain.CodeAttrRequired)
	}
	if err := s.SetAttributes(ctx, p.ID, map[string]string{"material": "steel"}); err != nil {
		t.Fatal(err)
	}
	steps := []struct {
		to      string
		version int
		ok      bool
	}{
		{domain.StateActive, 1, true},
		{domain.StateDraft, 2, false},
		{domain.StatePhaseOut, 2, true},
		{domain.StateObsolete, 3, true},
		{domain.StateActive, 4, false},
		{domain.StatePhaseOut, 4, false},
	}
	for _, st := range steps {
		got, err := s.UpdateProduct(ctx, p.ID, state(st.to), st.version)
		if st.ok && (err != nil || got.LifecycleState != st.to || got.Version != st.version+1) {
			t.Fatalf("-> %s: %v %+v", st.to, err, got)
		}
		if !st.ok && (!errors.As(err, &pr) || pr.Status != 422 || pr.Code != domain.CodeLifecycleIllegal) {
			t.Fatalf("-> %s must be refused with 422 %s, got %v", st.to, domain.CodeLifecycleIllegal, err)
		}
	}
	got, err := s.GetProduct(ctx, p.ID)
	if err != nil || got.LifecycleState != domain.StateObsolete || got.Version != 4 {
		t.Fatalf("refused moves must write nothing: %v %+v", err, got)
	}
}

func TestItemGroupAndTrackingFlagsRoundTripAndAreGuarded(t *testing.T) {
	s, ctx := newStore(t, 1)
	group, err := s.CreateItemGroup(ctx, domain.ItemGroup{Code: "RAW", Name: "Raw material"})
	if err != nil {
		t.Fatal(err)
	}
	made, err := s.CreateProduct(ctx, domain.Product{ProductNumber: "TRK-1", Subtype: domain.SubtypeProduct, ProductType: domain.TypeItem, Name: "Tracked", BaseUOM: "KG", ItemGroupID: group.ID, BatchTracked: true, CatchWeight: true}, "")
	if err != nil {
		t.Fatal(err)
	}
	read, err := s.GetProduct(ctx, made.ID)
	if err != nil || read.ItemGroupID != group.ID || !read.BatchTracked || read.SerialTracked || !read.CatchWeight {
		t.Fatalf("flags must read back as created: %v %+v", err, read)
	}
	serial, none := true, ""
	got, err := s.UpdateProduct(ctx, made.ID, domain.ProductPatch{SerialTracked: &serial, ItemGroupID: &none}, 1)
	if err != nil || got.ItemGroupID != "" || !got.SerialTracked || got.Version != 2 {
		t.Fatalf("patch must set a flag and clear the group: %v %+v", err, got)
	}
	groups, err := s.ListItemGroups(ctx)
	if err != nil || len(groups) != 1 || groups[0].Code != "RAW" {
		t.Fatalf("item groups: %v %+v", err, groups)
	}

	var pr *httpx.Problem
	unknown := testkit.TenantID(88)
	if _, err := s.UpdateProduct(ctx, made.ID, domain.ProductPatch{ItemGroupID: &unknown}, 2); !errors.As(err, &pr) || pr.Status != 400 || pr.Errors["itemGroupId"] == "" {
		t.Fatalf("an unknown item group must be 400 on itemGroupId, got %v", err)
	}
	if _, err := s.CreateProduct(ctx, domain.Product{ProductNumber: "TRK-2", Subtype: domain.SubtypeProduct, ProductType: domain.TypeItem, Name: "Bad group", BaseUOM: "EA", ItemGroupID: unknown}, ""); !errors.As(err, &pr) || pr.Status != 400 {
		t.Fatalf("create with an unknown item group must be 400, got %v", err)
	}
	if _, err := s.CreateProduct(ctx, domain.Product{ProductNumber: "SVC-T-1", Subtype: domain.SubtypeProduct, ProductType: domain.TypeService, Name: "Consulting", BaseUOM: "H", SerialTracked: true}, ""); !errors.As(err, &pr) || pr.Status != 422 || pr.Code != domain.CodeSvcStocked {
		t.Fatalf("a tracked service must be 422 %s, got %v", domain.CodeSvcStocked, err)
	}
	other := tenant.With(context.Background(), principal(2))
	if _, err := s.CreateProduct(other, domain.Product{ProductNumber: "TRK-3", Subtype: domain.SubtypeProduct, ProductType: domain.TypeItem, Name: "Other tenant", BaseUOM: "EA", ItemGroupID: group.ID}, ""); !errors.As(err, &pr) || pr.Status != 400 {
		t.Fatalf("another tenant's item group must be refused, got %v", err)
	}
}
