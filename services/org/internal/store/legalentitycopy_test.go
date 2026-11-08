package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/org/internal/store"
)

const (
	copiedType      = "dev.passage.org.legalentity.copied.v1"
	copySiteCreated = "dev.passage.org.site.created.v1"
)

func copyContext() context.Context {
	duties := testkit.ScopeDuties("")
	duties["org.legal-entity.write"] = struct{}{}
	duties[store.DutyReference] = struct{}{}
	return tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(1), Duties: duties})
}

type copySource struct {
	entity store.LegalEntity
	cal    store.FiscalCalendar
}

// copySourceEntity builds USMF: a calendar, two sites (one with a default warehouse), three warehouses,
// two locations, one inactive site that must not be copied, and one entity-level rounding rule.
func copySourceEntity(t *testing.T, s *store.Postgres, ctx context.Context) copySource {
	t.Helper()
	cal := retail445(t, s, ctx)
	generate(t, s, ctx, cal.ID, 2026)
	entity := assignCalendar(t, s, ctx, mustCreate(t, s, ctx, newEntity("USMF")), cal.ID)
	main := newSite(entity.ID, "MAIN")
	main.FiscalCalendarID = &cal.ID
	main.DefaultWarehouse = &store.Warehouse{Code: "WH1", Name: "Main warehouse", Type: "quarantine"}
	mainSite := mustCreateSite(t, s, ctx, main)
	east := mustCreateSite(t, s, ctx, newSite(entity.ID, "EAST"))
	closed := mustCreateSite(t, s, ctx, newSite(entity.ID, "OLD"))
	for _, w := range []store.Warehouse{{SiteID: mainSite.ID, Code: "WH2", Name: "Second"}, {SiteID: east.ID, Code: "WH3", Name: "East"}} {
		if _, err := s.CreateWarehouse(ctx, w, ""); err != nil {
			t.Fatal(err)
		}
	}
	inactive := "inactive"
	if _, err := s.UpdateSite(ctx, closed.ID, closed.Version, store.SitePatch{Status: &inactive}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateLocation(ctx, store.Location{WarehouseID: *mainSite.DefaultWarehouseID, Code: "A1", Name: "Aisle 1", Type: "storage"}, ""); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.PutCurrencyRounding(ctx, rule(t, "USD", "cash", "0.05", "half_up", &entity.ID)); err != nil {
		t.Fatal(err)
	}
	return copySource{entity: entity, cal: cal}
}

func copyInput(source, code string) store.LegalEntityCopyInput {
	return store.LegalEntityCopyInput{SourceID: source, Code: code, Name: "Canada " + code, Country: "CA", Currency: "CAD", Sites: true, Locations: true, RoundingRules: true}
}

func countRows(t *testing.T, ctx context.Context, pool *pgxpool.Pool, table, legalEntityID string) int {
	t.Helper()
	var n int
	err := dbx.WithTenantWideAdminTx(ctx, pool, tenant.Principal{TenantID: testkit.TenantID(1), Duties: map[string]struct{}{tenant.DutyTenantAdmin: {}, store.DutyReference: {}}}, store.DutyReference, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `select count(*) from `+table+` where legal_entity_id = $1`, legalEntityID).Scan(&n)
	})
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func TestPostgres_CopyLegalEntityCopiesStructureAndWritesEvents(t *testing.T) {
	s, _, pool := lifecycleStore(t)
	ctx := copyContext()
	src := copySourceEntity(t, s, ctx)
	before := len(outboxRows(t, ctx, pool))

	got, err := s.CopyLegalEntity(ctx, copyInput(src.entity.ID, "CAMF"), "copy-1")
	if err != nil {
		t.Fatal(err)
	}
	target := got.LegalEntity
	if target.ID == src.entity.ID || target.Code != "CAMF" || target.Country != "CA" || target.Currency != "CAD" || target.Version != 1 {
		t.Fatalf("target = %+v", target)
	}
	if target.TimeZone != src.entity.TimeZone || target.DefaultLanguage != src.entity.DefaultLanguage {
		t.Fatalf("time zone and language default from the source: %+v", target)
	}
	if target.FiscalCalendarID == nil || *target.FiscalCalendarID != src.cal.ID || !got.CalendarCopied {
		t.Fatalf("calendar not copied: %+v", got)
	}
	if got.Sites != 2 || got.Warehouses != 3 || got.Locations != 1 || got.RoundingRules != 1 {
		t.Fatalf("counts = %+v; the inactive site must not be copied", got)
	}
	sites, err := s.ListSites(ctx, store.SiteFilter{LegalEntityID: target.ID}, store.SiteKey{}, 10)
	if err != nil || len(sites.Items) != 2 || sites.Items[0].Code != "EAST" || sites.Items[1].Code != "MAIN" {
		t.Fatalf("sites = %+v, %v", sites, err)
	}
	main := sites.Items[1]
	if main.FiscalCalendarID == nil || main.DefaultWarehouseID == nil {
		t.Fatalf("main site lost its calendar or default warehouse: %+v", main)
	}
	def, err := s.GetWarehouse(ctx, *main.DefaultWarehouseID)
	if err != nil || def.Code != "WH1" || def.Type != "quarantine" || def.LegalEntityID != target.ID || def.SiteID != main.ID {
		t.Fatalf("default warehouse = %+v, %v", def, err)
	}
	if n := countRows(t, ctx, pool, "warehouse", target.ID); n != 3 {
		t.Fatalf("warehouses = %d", n)
	}
	if n := countRows(t, ctx, pool, "location", target.ID); n != 1 {
		t.Fatalf("locations = %d", n)
	}
	if n := countRows(t, ctx, pool, "currency_rounding", target.ID); n != 1 {
		t.Fatalf("rounding rules = %d", n)
	}
	// The source is untouched.
	if n := countRows(t, ctx, pool, "site", src.entity.ID); n != 3 {
		t.Fatalf("source sites = %d", n)
	}

	counts := map[string]int{}
	for _, row := range outboxRows(t, ctx, pool)[before:] {
		counts[row.Type]++
	}
	if counts[copiedType] != 1 || counts["dev.passage.org.legalentity.created.v1"] != 1 || counts[copySiteCreated] != 2 ||
		counts["dev.passage.org.warehouse.created.v1"] != 3 || counts["dev.passage.org.location.created.v1"] != 1 || counts["dev.passage.org.currencyrounding.changed.v1"] != 1 {
		t.Fatalf("events = %v", counts)
	}
}

func TestPostgres_CopyLegalEntityOptionsAndInheritedValues(t *testing.T) {
	s, _, _ := lifecycleStore(t)
	ctx := copyContext()
	src := copySourceEntity(t, s, ctx)
	in := copyInput(src.entity.ID, "BARE")
	in.Sites, in.Locations, in.RoundingRules = false, false, false
	in.TimeZone, in.DefaultLanguage = "America/Toronto", "fr-CA"
	got, err := s.CopyLegalEntity(ctx, in, "")
	if err != nil {
		t.Fatal(err)
	}
	if got.Sites != 0 || got.Warehouses != 0 || got.Locations != 0 || got.RoundingRules != 0 || got.LegalEntity.TimeZone != "America/Toronto" || got.LegalEntity.DefaultLanguage != "fr-CA" {
		t.Fatalf("copy = %+v", got)
	}
}

func TestPostgres_CopyLegalEntityFailureLeavesNothingBehind(t *testing.T) {
	s, _, pool := lifecycleStore(t)
	ctx := copyContext()
	src := copySourceEntity(t, s, ctx)
	mustCreate(t, s, ctx, newEntity("CAMF"))
	before, events := legalEntityCount(t, ctx, pool), len(outboxRows(t, ctx, pool))

	if _, err := s.CopyLegalEntity(ctx, copyInput(src.entity.ID, "CAMF"), ""); problemCode(t, err) != store.CodeLegalEntityCode {
		t.Fatalf("duplicate code must be %s, got %v", store.CodeLegalEntityCode, err)
	}
	bad := copyInput(src.entity.ID, "BADCUR")
	bad.Currency = "ZZZ"
	if _, err := s.CopyLegalEntity(ctx, bad, ""); problemCode(t, err) != store.CodeReferenceInactive {
		t.Fatalf("unknown currency must be %s, got %v", store.CodeReferenceInactive, err)
	}
	if _, err := s.CopyLegalEntity(ctx, copyInput("00000000-0000-0000-0000-0000000000aa", "NOSRC"), ""); problemCode(t, err) != store.CodeLegalEntityNotFound {
		t.Fatalf("unknown source must be %s, got %v", store.CodeLegalEntityNotFound, err)
	}
	if got := legalEntityCount(t, ctx, pool); got != before {
		t.Fatalf("legal entities %d -> %d after failed copies", before, got)
	}
	if got := len(outboxRows(t, ctx, pool)); got != events {
		t.Fatalf("outbox rows %d -> %d after failed copies", events, got)
	}
}

func TestPostgres_CopyLegalEntityIsIdempotentAndRefusesAChangedBody(t *testing.T) {
	s, _, pool := lifecycleStore(t)
	ctx := copyContext()
	src := copySourceEntity(t, s, ctx)
	first, err := s.CopyLegalEntity(ctx, copyInput(src.entity.ID, "CAMF"), "k1")
	if err != nil {
		t.Fatal(err)
	}
	events := len(outboxRows(t, ctx, pool))
	replay, err := s.CopyLegalEntity(ctx, copyInput(src.entity.ID, "CAMF"), "k1")
	if err != nil || !replay.Replayed || replay.LegalEntity.ID != first.LegalEntity.ID || replay.Sites != first.Sites || replay.Warehouses != first.Warehouses {
		t.Fatalf("replay = %+v, %v", replay, err)
	}
	if got := len(outboxRows(t, ctx, pool)); got != events {
		t.Fatalf("replay wrote events: %d -> %d", events, got)
	}
	if _, err := s.CopyLegalEntity(ctx, copyInput(src.entity.ID, "CAMX"), "k1"); problemCode(t, err) != store.CodeIdempotencyReuse {
		t.Fatalf("changed body must be %s, got %v", store.CodeIdempotencyReuse, err)
	}
	// Same code, name, country and currency but other options is still another request.
	fewer := copyInput(src.entity.ID, "CAMF")
	fewer.Locations = false
	if _, err := s.CopyLegalEntity(ctx, fewer, "k1"); problemCode(t, err) != store.CodeIdempotencyReuse {
		t.Fatalf("changed include flags must be %s, got %v", store.CodeIdempotencyReuse, err)
	}
	zone := copyInput(src.entity.ID, "CAMF")
	zone.TimeZone = "America/Toronto"
	if _, err := s.CopyLegalEntity(ctx, zone, "k1"); problemCode(t, err) != store.CodeIdempotencyReuse {
		t.Fatalf("changed time zone must be %s, got %v", store.CodeIdempotencyReuse, err)
	}
	if got := len(outboxRows(t, ctx, pool)); got != events {
		t.Fatalf("refused replays wrote events: %d -> %d", events, got)
	}
}

func TestPostgres_CopyLegalEntityNeedsATenantWideCaller(t *testing.T) {
	s, _, _ := lifecycleStore(t)
	ctx := copyContext()
	src := copySourceEntity(t, s, ctx)
	scoped := tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(1), LegalEntityID: src.entity.ID, Duties: map[string]struct{}{"org.legal-entity.write": {}}})
	if _, err := s.CopyLegalEntity(scoped, copyInput(src.entity.ID, "CAMF"), ""); problemCode(t, err) != "AUTH-0003" {
		t.Fatalf("scoped caller must be refused, got %v", err)
	}
	noDuty := tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(1), Duties: testkit.ScopeDuties("")})
	if _, err := s.CopyLegalEntity(noDuty, copyInput(src.entity.ID, "CAMF"), ""); problemCode(t, err) != "AUTH-0003" {
		t.Fatalf("a caller without org.legal-entity.write must be refused, got %v", err)
	}
}

func TestPostgres_CopyLegalEntityOtherTenantCannotCopy(t *testing.T) {
	s, _, _ := lifecycleStore(t)
	ctx := copyContext()
	src := copySourceEntity(t, s, ctx)
	duties := testkit.ScopeDuties("")
	duties["org.legal-entity.write"] = struct{}{}
	other := tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(2), Duties: duties})
	if _, err := s.CopyLegalEntity(other, copyInput(src.entity.ID, "CAMF"), ""); problemCode(t, err) != store.CodeLegalEntityNotFound {
		t.Fatalf("another tenant must not see the source, got %v", err)
	}
}

// adminExec runs statements tenant-wide as a tenant administrator of tenant 1.
func adminExec(t *testing.T, ctx context.Context, pool *pgxpool.Pool, fn func(tx pgx.Tx) error) {
	t.Helper()
	p := tenant.Principal{TenantID: testkit.TenantID(1), Duties: map[string]struct{}{tenant.DutyTenantAdmin: {}, store.DutyReference: {}}}
	if err := dbx.WithTenantWideAdminTx(ctx, pool, p, store.DutyReference, fn); err != nil {
		t.Fatal(err)
	}
}

func TestPostgres_CopyLegalEntityNeverReferencesSourceRows(t *testing.T) {
	s, _, pool := lifecycleStore(t)
	ctx := copyContext()
	src := copySourceEntity(t, s, ctx)
	got, err := s.CopyLegalEntity(ctx, copyInput(src.entity.ID, "CAMF"), "")
	if err != nil {
		t.Fatal(err)
	}
	target := got.LegalEntity.ID
	adminExec(t, ctx, pool, func(tx pgx.Tx) error {
		var leaks int
		// Every pointer of a copied row must land on a row of the new entity, never on a source row.
		err := tx.QueryRow(ctx, `select
			(select count(*) from site s where s.legal_entity_id = $1 and s.default_warehouse_id is not null
			   and not exists (select 1 from warehouse w where w.id = s.default_warehouse_id and w.legal_entity_id = $1))
		  + (select count(*) from warehouse w where w.legal_entity_id = $1
			   and not exists (select 1 from site s where s.id = w.site_id and s.legal_entity_id = $1))
		  + (select count(*) from location l where l.legal_entity_id = $1
			   and not exists (select 1 from warehouse w where w.id = l.warehouse_id and w.legal_entity_id = $1))`,
			target).Scan(&leaks)
		if err == nil && leaks != 0 {
			t.Errorf("%d copied rows point at source rows", leaks)
		}
		var defaults int
		if err == nil {
			err = tx.QueryRow(ctx, `select count(*) from site where legal_entity_id = $1 and default_warehouse_id is not null`, target).Scan(&defaults)
		}
		if err == nil && defaults != 1 {
			t.Errorf("copied default warehouses = %d, want 1", defaults)
		}
		return err
	})
}

func TestPostgres_CopyLegalEntityCopiesThousandsOfLocations(t *testing.T) {
	s, _, pool := lifecycleStore(t)
	ctx := copyContext()
	src := copySourceEntity(t, s, ctx)
	const bulk = 3000
	adminExec(t, ctx, pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `insert into location (tenant_id, id, legal_entity_id, warehouse_id, code, name, type)
			select w.tenant_id, gen_random_uuid(), w.legal_entity_id, w.id, 'B' || g, 'Bin ' || g, 'storage'
			from warehouse w, generate_series(1, $2::int) g where w.legal_entity_id = $1 and w.code = 'WH2'`, src.entity.ID, bulk)
		return err
	})
	before := len(outboxRows(t, ctx, pool))
	start := time.Now()
	got, err := s.CopyLegalEntity(ctx, copyInput(src.entity.ID, "CAMF"), "bulk")
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("copied %d locations in %s", got.Locations, time.Since(start))
	if got.Locations != bulk+1 || countRows(t, ctx, pool, "location", got.LegalEntity.ID) != bulk+1 {
		t.Fatalf("locations = %d, want %d", got.Locations, bulk+1)
	}
	located := 0
	for _, row := range outboxRows(t, ctx, pool)[before:] {
		if row.Type == "dev.passage.org.location.created.v1" {
			located++
		}
	}
	if located != bulk+1 {
		t.Fatalf("location.created events = %d, want %d", located, bulk+1)
	}
}
