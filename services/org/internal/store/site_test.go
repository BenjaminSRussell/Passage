package store_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/org/internal/store"
	"github.com/BenjaminSRussell/passage/services/org/migrations"
)

const (
	siteCreatedType = "dev.passage.org.site.created.v1"
	siteUpdatedType = "dev.passage.org.site.updated.v1"
)

func newSite(legalEntityID, code string) store.Site {
	return store.Site{LegalEntityID: legalEntityID, Code: code, Name: "Site " + code}
}

func mustCreateSite(t *testing.T, s *store.Postgres, ctx context.Context, in store.Site) store.Site {
	t.Helper()
	created, err := s.CreateSite(ctx, in, "")
	if err != nil {
		t.Fatal(err)
	}
	return created
}

func scopedTo(legalEntityID string) context.Context {
	return tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(1), LegalEntityID: legalEntityID})
}

func TestPostgres_SiteCreateWritesOneEventAndReplayWritesNone(t *testing.T) {
	s, ctx, pool := lifecycleStore(t)
	a := mustCreate(t, s, ctx, newEntity("US01"))
	first, err := s.CreateSite(ctx, newSite(a.ID, "S1"), "key-1")
	if err != nil {
		t.Fatal(err)
	}
	replay, err := s.CreateSite(ctx, newSite(a.ID, "S1"), "key-1")
	if err != nil || replay.ID != first.ID {
		t.Fatalf("replay = %+v, %v; want the original site", replay, err)
	}
	changed := newSite(a.ID, "S1")
	changed.Name = "Other"
	if _, err := s.CreateSite(ctx, changed, "key-1"); problemCode(t, err) != store.CodeIdempotencyReuse {
		t.Fatalf("replay with a different name error = %v", err)
	}
	events := unitEvents(t, ctx, pool, siteCreatedType)
	if len(events) != 1 || events[0]["id"] != first.ID || events[0]["legal_entity_id"] != a.ID {
		t.Fatalf("created events = %v; want exactly one for the site", events)
	}
	for _, key := range []string{"parent_id", "type", "address_id"} {
		if value, present := events[0][key]; !present || value != nil {
			t.Fatalf("%s = %v, present %v; want present and null", key, value, present)
		}
	}
}

func TestPostgres_SiteTimeZoneDefaultsToTheLegalEntity(t *testing.T) {
	s, ctx, _ := lifecycleStore(t)
	in := newEntity("US01")
	in.TimeZone = "America/New_York"
	a := mustCreate(t, s, ctx, in)
	if got := mustCreateSite(t, s, ctx, newSite(a.ID, "S1")); got.TimeZone != "America/New_York" {
		t.Fatalf("default time zone = %q", got.TimeZone)
	}
	berlin := newSite(a.ID, "S2")
	berlin.TimeZone = "Europe/Berlin"
	if got := mustCreateSite(t, s, ctx, berlin); got.TimeZone != "Europe/Berlin" {
		t.Fatalf("explicit time zone = %q", got.TimeZone)
	}
}

func TestPostgres_SiteCodeIsUniquePerLegalEntity(t *testing.T) {
	s, ctx, _ := lifecycleStore(t)
	a, b := mustCreate(t, s, ctx, newEntity("US01")), mustCreate(t, s, ctx, newEntity("US02"))
	mustCreateSite(t, s, ctx, newSite(a.ID, "S1"))
	mustCreateSite(t, s, ctx, newSite(b.ID, "S1"))
	if _, err := s.CreateSite(ctx, newSite(a.ID, "S1"), ""); problemCode(t, err) != store.CodeSiteCodeDuplicate {
		t.Fatalf("duplicate code error = %v", err)
	}
}

func TestPostgres_SiteTenantIsolation(t *testing.T) {
	s, ctx, _ := lifecycleStore(t)
	a := mustCreate(t, s, ctx, newEntity("US01"))
	site := mustCreateSite(t, s, ctx, newSite(a.ID, "S1"))
	other := principalContext(2)
	if _, err := s.GetSite(other, site.ID); problemCode(t, err) != store.CodeSiteNotFound {
		t.Fatalf("tenant 2 get error = %v", err)
	}
	if page, err := s.ListSites(other, store.SiteFilter{}, store.SiteKey{}, store.MaxPageSize); err != nil || len(page.Items) != 0 {
		t.Fatalf("tenant 2 list = %+v, %v; want empty", page, err)
	}
}

func TestPostgres_SiteLegalEntityMustBeActiveAndVisible(t *testing.T) {
	s, ctx, _ := lifecycleStore(t)
	b := mustCreate(t, s, ctx, newEntity("US02"))
	if _, err := s.CreateSite(ctx, newSite(testkit.TenantID(9), "S1"), ""); problemCode(t, err) != store.CodeSiteLegalEntityInvalid {
		t.Fatalf("unknown legal entity error = %v", err)
	}
	inactive := "inactive"
	if _, err := s.UpdateLegalEntity(ctx, b.ID, 1, store.LegalEntityPatch{Status: &inactive}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateSite(ctx, newSite(b.ID, "S2"), ""); problemCode(t, err) != store.CodeSiteLegalEntityInvalid {
		t.Fatalf("inactive legal entity error = %v", err)
	}
}

func TestPostgres_SiteScopedCaller(t *testing.T) {
	s, ctx, _ := lifecycleStore(t)
	a, b := mustCreate(t, s, ctx, newEntity("US01")), mustCreate(t, s, ctx, newEntity("US02"))
	siteA, siteB := mustCreateSite(t, s, ctx, newSite(a.ID, "S1")), mustCreateSite(t, s, ctx, newSite(b.ID, "S2"))
	scoped := scopedTo(a.ID)
	if _, err := s.CreateSite(scoped, newSite(b.ID, "S3"), ""); problemCode(t, err) != store.CodeSiteLegalEntityInvalid {
		t.Fatalf("create under another legal entity error = %v", err)
	}
	page, err := s.ListSites(scoped, store.SiteFilter{}, store.SiteKey{}, store.MaxPageSize)
	if err != nil || len(page.Items) != 1 || page.Items[0].ID != siteA.ID {
		t.Fatalf("scoped list = %+v, %v; want only A's site", page, err)
	}
	if _, err := s.GetSite(scoped, siteB.ID); problemCode(t, err) != store.CodeSiteNotFound {
		t.Fatalf("get of B's site error = %v", err)
	}
	name := "Hijacked"
	if _, err := s.UpdateSite(scoped, siteB.ID, 1, store.SitePatch{Name: &name}); problemCode(t, err) != store.CodeSiteNotFound {
		t.Fatalf("update of B's site error = %v", err)
	}
	if untouched, _ := s.GetSite(ctx, siteB.ID); untouched.Name != "Site S2" || untouched.Version != 1 {
		t.Fatalf("site B changed: %+v", untouched)
	}
}

func TestPostgres_ListSitesFiltersAndPages(t *testing.T) {
	s, ctx, _ := lifecycleStore(t)
	a, b := mustCreate(t, s, ctx, newEntity("US01")), mustCreate(t, s, ctx, newEntity("US02"))
	one, two := mustCreateSite(t, s, ctx, newSite(a.ID, "S1")), mustCreateSite(t, s, ctx, newSite(b.ID, "S1"))
	inactive := "inactive"
	if _, err := s.UpdateSite(ctx, two.ID, 1, store.SitePatch{Status: &inactive}); err != nil {
		t.Fatal(err)
	}
	if page, err := s.ListSites(ctx, store.SiteFilter{LegalEntityID: a.ID}, store.SiteKey{}, store.MaxPageSize); err != nil || len(page.Items) != 1 || page.Items[0].ID != one.ID {
		t.Fatalf("legal entity filter = %+v, %v", page, err)
	}
	if page, err := s.ListSites(ctx, store.SiteFilter{Status: "inactive"}, store.SiteKey{}, store.MaxPageSize); err != nil || len(page.Items) != 1 || page.Items[0].ID != two.ID {
		t.Fatalf("status filter = %+v, %v", page, err)
	}
	seen := map[string]bool{}
	var after store.SiteKey
	for pages := 0; pages < 3; pages++ {
		page, err := s.ListSites(ctx, store.SiteFilter{}, after, 1)
		if err != nil || len(page.Items) != 1 {
			t.Fatalf("page %d = %+v, %v", pages, page, err)
		}
		seen[page.Items[0].ID] = true
		if page.Next == nil {
			break
		}
		after = *page.Next
	}
	if len(seen) != 2 || !seen[one.ID] || !seen[two.ID] {
		t.Fatalf("walk saw %v; want both sites with the same code once", seen)
	}
}

func TestPostgres_ListSitesSearchesCodeAndName(t *testing.T) {
	s, ctx, _ := lifecycleStore(t)
	a := mustCreate(t, s, ctx, newEntity("US01"))
	plain := mustCreateSite(t, s, ctx, newSite(a.ID, "S1"))
	named := newSite(a.ID, "HARB")
	named.Name = "Harbour Depot"
	harbour := mustCreateSite(t, s, ctx, named)
	for query, want := range map[string]string{"harbour d": harbour.ID, "harb": harbour.ID, "s1": plain.ID} {
		if page, err := s.ListSites(ctx, store.SiteFilter{Query: query}, store.SiteKey{}, store.MaxPageSize); err != nil || len(page.Items) != 1 || page.Items[0].ID != want {
			t.Fatalf("query %q = %+v, %v", query, page, err)
		}
	}
	if page, err := s.ListSites(ctx, store.SiteFilter{Query: "%"}, store.SiteKey{}, store.MaxPageSize); err != nil || len(page.Items) != 0 {
		t.Fatalf("a bare percent sign must match nothing: %+v, %v", page, err)
	}
}

func TestPostgres_UpdateSiteVersionsAndEmitsOneEvent(t *testing.T) {
	s, ctx, pool := lifecycleStore(t)
	a := mustCreate(t, s, ctx, newEntity("US01"))
	site := mustCreateSite(t, s, ctx, newSite(a.ID, "S1"))
	name := "Renamed"
	renamed, err := s.UpdateSite(ctx, site.ID, 1, store.SitePatch{Name: &name})
	if err != nil || renamed.Version != 2 || renamed.Name != name {
		t.Fatalf("rename = %+v, %v", renamed, err)
	}
	events := unitEvents(t, ctx, pool, siteUpdatedType)
	if len(events) != 1 || !reflect.DeepEqual(events[0]["changed_fields"], []any{"name"}) || events[0]["version"] != float64(2) {
		t.Fatalf("updated events = %v", events)
	}
	if _, err := s.UpdateSite(ctx, site.ID, 1, store.SitePatch{Name: &name}); problemCode(t, err) != store.CodeVersionConflict {
		t.Fatalf("stale version error = %v", err)
	}
	same, err := s.UpdateSite(ctx, site.ID, 2, store.SitePatch{Name: &name})
	if err != nil || same.Version != 2 {
		t.Fatalf("no-op = %+v, %v; want version 2", same, err)
	}
	if got := len(unitEvents(t, ctx, pool, siteUpdatedType)); got != 1 {
		t.Fatalf("stale and no-op patches left %d updated events; want 1", got)
	}
	zone, status := "Europe/Berlin", "inactive"
	if _, err := s.UpdateSite(ctx, site.ID, 2, store.SitePatch{TimeZone: &zone, Status: &status}); err != nil {
		t.Fatal(err)
	}
	events = unitEvents(t, ctx, pool, siteUpdatedType)
	if len(events) != 2 || !reflect.DeepEqual(events[1]["changed_fields"], []any{"status", "time_zone"}) {
		t.Fatalf("updated events = %v", events)
	}
	if got := len(unitEvents(t, ctx, pool, siteCreatedType)); got != 1 {
		t.Fatalf("created events = %d; want 1", got)
	}
}

func TestPostgres_SiteCalendarAssignmentWritesOneEventAndRefusalsWriteNothing(t *testing.T) {
	s, ctx, pool := lifecycleStore(t)
	entity := mustCreate(t, s, ctx, newEntity("US01"))
	calendar := calendarOf(t, s, ctx, "FY", "calendar", 2026)
	neverGenerated := calendarOf(t, s, ctx, "EMPTY", "calendar")
	inactive := calendarOf(t, s, ctx, "OLD", "calendar", 2026)
	if _, err := s.UpdateFiscalCalendar(ctx, inactive.ID, 1, store.FiscalCalendarPatch{Status: ptr("inactive")}); err != nil {
		t.Fatal(err)
	}
	site := mustCreateSite(t, s, ctx, newSite(entity.ID, "S1"))
	if site.FiscalCalendarID != nil {
		t.Fatalf("new site calendar = %v; want none", *site.FiscalCalendarID)
	}
	for _, id := range []string{neverGenerated.ID, inactive.ID, "00000000-0000-0000-0000-0000000000ff"} {
		_, err := s.UpdateSite(ctx, site.ID, 1, store.SitePatch{FiscalCalendarID: ptr(id)})
		var problem *httpx.Problem
		if !errors.As(err, &problem) || problem.Code != store.CodeSiteCalendarInvalid || problem.Errors["fiscalCalendarId"] == "" {
			t.Fatalf("assign %s error = %v; want ORG-SITE-CALENDAR-INVALID on fiscalCalendarId", id, err)
		}
		if _, err := s.CreateSite(ctx, store.Site{LegalEntityID: entity.ID, Code: "S2", Name: "Two", FiscalCalendarID: ptr(id)}, ""); problemCode(t, err) != store.CodeSiteCalendarInvalid {
			t.Fatalf("create with %s error = %v", id, err)
		}
	}
	if got := len(unitEvents(t, ctx, pool, siteUpdatedType)); got != 0 {
		t.Fatalf("refused assignments wrote %d events", got)
	}
	if page, _ := s.ListSites(ctx, store.SiteFilter{}, store.SiteKey{}, store.MaxPageSize); len(page.Items) != 1 {
		t.Fatalf("refused creates left %d sites; want 1", len(page.Items))
	}
	assigned := assignSiteCalendar(t, s, ctx, site, calendar.ID)
	if assigned.Version != 2 || assigned.FiscalCalendarID == nil || *assigned.FiscalCalendarID != calendar.ID {
		t.Fatalf("assigned = %+v", assigned)
	}
	events := unitEvents(t, ctx, pool, siteUpdatedType)
	if len(events) != 1 || !reflect.DeepEqual(events[0]["changed_fields"], []any{"fiscal_calendar_id"}) {
		t.Fatalf("events = %v", events)
	}
	if page, _ := s.ListSites(ctx, store.SiteFilter{}, store.SiteKey{}, store.MaxPageSize); len(page.Items) != 1 || page.Items[0].FiscalCalendarID == nil {
		t.Fatalf("list = %+v; want the calendar", page.Items)
	}
	same := assignSiteCalendar(t, s, ctx, assigned, calendar.ID)
	if same.Version != 2 || len(unitEvents(t, ctx, pool, siteUpdatedType)) != 1 {
		t.Fatalf("re-assigning the same calendar bumped version or wrote an event: %+v", same)
	}
	cleared, err := s.UpdateSite(ctx, same.ID, 2, store.SitePatch{FiscalCalendarID: ptr("")})
	if err != nil || cleared.Version != 3 || cleared.FiscalCalendarID != nil {
		t.Fatalf("cleared = %+v, %v", cleared, err)
	}
	if got := len(unitEvents(t, ctx, pool, siteUpdatedType)); got != 2 {
		t.Fatalf("events after clear = %d; want 2", got)
	}
}

func TestPostgres_SiteCreateWithCalendarStoresItAndReplayWithAnotherCalendarIsReuse(t *testing.T) {
	s, ctx, _ := lifecycleStore(t)
	entity := mustCreate(t, s, ctx, newEntity("US01"))
	first := calendarOf(t, s, ctx, "FY", "calendar", 2026)
	second := calendarOf(t, s, ctx, "FY2", "calendar", 2026)
	in := store.Site{LegalEntityID: entity.ID, Code: "S1", Name: "One", FiscalCalendarID: ptr(first.ID)}
	created, err := s.CreateSite(ctx, in, "key-1")
	if err != nil || created.FiscalCalendarID == nil || *created.FiscalCalendarID != first.ID {
		t.Fatalf("created = %+v, %v", created, err)
	}
	if replay, err := s.CreateSite(ctx, in, "key-1"); err != nil || replay.ID != created.ID {
		t.Fatalf("replay = %+v, %v", replay, err)
	}
	in.FiscalCalendarID = ptr(second.ID)
	if _, err := s.CreateSite(ctx, in, "key-1"); problemCode(t, err) != store.CodeIdempotencyReuse {
		t.Fatalf("replay with another calendar error = %v", err)
	}
	in.FiscalCalendarID = nil
	if _, err := s.CreateSite(ctx, in, "key-1"); problemCode(t, err) != store.CodeIdempotencyReuse {
		t.Fatalf("replay without a calendar error = %v", err)
	}
}

func TestPostgres_SiteCannotUseAnotherTenantsCalendar(t *testing.T) {
	s, ctx, _ := lifecycleStore(t)
	foreign := calendarOf(t, s, ctx, "FY", "calendar", 2026)
	other := principalContext(2)
	entity := mustCreate(t, s, other, newEntity("US01"))
	if _, err := s.CreateSite(other, store.Site{LegalEntityID: entity.ID, Code: "S1", Name: "One", FiscalCalendarID: ptr(foreign.ID)}, ""); problemCode(t, err) != store.CodeSiteCalendarInvalid {
		t.Fatalf("tenant 2 create with tenant 1 calendar error = %v", err)
	}
}

func siteWithDefault(legalEntityID, code, warehouseCode string) store.Site {
	in := newSite(legalEntityID, code)
	in.DefaultWarehouse = &store.Warehouse{Code: warehouseCode, Name: "Warehouse " + warehouseCode}
	return in
}

func outboxTypesInOrder(t *testing.T, ctx context.Context, pool *pgxpool.Pool) []string {
	t.Helper()
	var types []string
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `select set_config('passage.tenant_id', $1, true), set_config('passage.legal_entity_scopes', '*', true)`, testkit.TenantID(1)); err != nil {
		t.Fatal(err)
	}
	rows, err := tx.Query(ctx, `select type from outbox order by id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var typ string
		if err := rows.Scan(&typ); err != nil {
			t.Fatal(err)
		}
		types = append(types, typ)
	}
	return types
}

func TestPostgres_SiteCreateWithDefaultWarehouseWritesBothRowsAndEventsInOrder(t *testing.T) {
	s, ctx, pool := lifecycleStore(t)
	in := newEntity("US01")
	in.TimeZone = "America/New_York"
	a := mustCreate(t, s, ctx, in)
	site := mustCreateSite(t, s, ctx, siteWithDefault(a.ID, "S1", "MAIN"))
	if site.DefaultWarehouseID == nil || site.Replayed {
		t.Fatalf("site = %+v; want a default warehouse id on a fresh create", site)
	}
	warehouse, err := s.GetWarehouse(ctx, *site.DefaultWarehouseID)
	if err != nil || warehouse.SiteID != site.ID || warehouse.LegalEntityID != a.ID || warehouse.Type != "standard" || warehouse.TimeZone != "America/New_York" {
		t.Fatalf("default warehouse = %+v, %v", warehouse, err)
	}
	stored, err := s.GetSite(ctx, site.ID)
	if err != nil || stored.DefaultWarehouseID == nil || *stored.DefaultWarehouseID != warehouse.ID || stored.Version != 1 {
		t.Fatalf("stored site = %+v, %v", stored, err)
	}
	if got := outboxTypesInOrder(t, ctx, pool); !reflect.DeepEqual(got, []string{"dev.passage.org.legalentity.created.v1", siteCreatedType, warehouseCreatedType}) {
		t.Fatalf("event order = %v", got)
	}
	if events := unitEvents(t, ctx, pool, siteCreatedType); len(events) != 1 || events[0]["default_warehouse_id"] != warehouse.ID {
		t.Fatalf("site.created = %v; want default_warehouse_id %s", events, warehouse.ID)
	}
}

func TestPostgres_SiteCreateWithoutDefaultWarehouseLeavesNone(t *testing.T) {
	s, ctx, pool := lifecycleStore(t)
	a := mustCreate(t, s, ctx, newEntity("US01"))
	site := mustCreateSite(t, s, ctx, newSite(a.ID, "S1"))
	page, err := s.ListWarehouses(ctx, store.WarehouseFilter{}, store.SiteKey{}, store.MaxPageSize)
	if site.DefaultWarehouseID != nil || err != nil || len(page.Items) != 0 {
		t.Fatalf("site = %+v, warehouses = %+v, %v; want neither", site, page, err)
	}
	if events := unitEvents(t, ctx, pool, siteCreatedType); len(events) != 1 {
		t.Fatalf("created events = %v", events)
	} else if _, present := events[0]["default_warehouse_id"]; present {
		t.Fatalf("payload = %v; default_warehouse_id must be omitted", events[0])
	}
}

func TestPostgres_SiteCreateWithDuplicateWarehouseCodeLeavesNothingBehind(t *testing.T) {
	s, ctx, pool := lifecycleStore(t)
	a := mustCreate(t, s, ctx, newEntity("US01"))
	mustCreateSite(t, s, ctx, siteWithDefault(a.ID, "S1", "MAIN"))
	if _, err := s.CreateSite(ctx, siteWithDefault(a.ID, "S2", "MAIN"), ""); problemCode(t, err) != store.CodeWarehouseCodeDuplicate {
		t.Fatalf("duplicate warehouse code error = %v", err)
	}
	sites, _ := s.ListSites(ctx, store.SiteFilter{}, store.SiteKey{}, store.MaxPageSize)
	warehouses, _ := s.ListWarehouses(ctx, store.WarehouseFilter{}, store.SiteKey{}, store.MaxPageSize)
	if len(sites.Items) != 1 || len(warehouses.Items) != 1 {
		t.Fatalf("sites = %d, warehouses = %d; want 1 and 1", len(sites.Items), len(warehouses.Items))
	}
	if got := len(unitEvents(t, ctx, pool, siteCreatedType)) + len(unitEvents(t, ctx, pool, warehouseCreatedType)); got != 2 {
		t.Fatalf("rolled-back create left %d site/warehouse events in total; want 2", got)
	}
	if _, err := s.CreateSite(ctx, newSite(a.ID, "S1"), ""); problemCode(t, err) != store.CodeSiteCodeDuplicate {
		t.Fatalf("duplicate site code error = %v", err)
	}
}

func TestPostgres_SiteCreateWithDefaultWarehouseReplayCreatesNothingAndOtherBodyIsReuse(t *testing.T) {
	s, ctx, pool := lifecycleStore(t)
	a := mustCreate(t, s, ctx, newEntity("US01"))
	first, err := s.CreateSite(ctx, siteWithDefault(a.ID, "S1", "MAIN"), "key-1")
	if err != nil {
		t.Fatal(err)
	}
	replay, err := s.CreateSite(ctx, siteWithDefault(a.ID, "S1", "MAIN"), "key-1")
	if err != nil || replay.ID != first.ID || !replay.Replayed || deref(replay.DefaultWarehouseID) != deref(first.DefaultWarehouseID) {
		t.Fatalf("replay = %+v, %v; want the original site", replay, err)
	}
	for name, in := range map[string]store.Site{"other code": siteWithDefault(a.ID, "S1", "OTHER"), "no default": newSite(a.ID, "S1")} {
		if _, err := s.CreateSite(ctx, in, "key-1"); problemCode(t, err) != store.CodeIdempotencyReuse {
			t.Fatalf("%s: error = %v; want idempotency reuse", name, err)
		}
	}
	sites, _ := s.ListSites(ctx, store.SiteFilter{}, store.SiteKey{}, store.MaxPageSize)
	warehouses, _ := s.ListWarehouses(ctx, store.WarehouseFilter{}, store.SiteKey{}, store.MaxPageSize)
	events := len(unitEvents(t, ctx, pool, siteCreatedType)) + len(unitEvents(t, ctx, pool, warehouseCreatedType))
	if len(sites.Items) != 1 || len(warehouses.Items) != 1 || events != 2 {
		t.Fatalf("sites = %d, warehouses = %d, events = %d; want 1, 1, 2", len(sites.Items), len(warehouses.Items), events)
	}
}

func TestPostgres_SiteDefaultWarehouseCanBeRepointedAndClearedAndRefusalsChangeNothing(t *testing.T) {
	s, ctx, pool := lifecycleStore(t)
	a := mustCreate(t, s, ctx, newEntity("US01"))
	site := mustCreateSite(t, s, ctx, siteWithDefault(a.ID, "S1", "MAIN"))
	other := mustCreateSite(t, s, ctx, siteWithDefault(a.ID, "S2", "OTHER"))
	spare := mustCreateWarehouse(t, s, ctx, newWarehouse(site.ID, "SPARE"))
	inactive := mustCreateWarehouse(t, s, ctx, newWarehouse(site.ID, "OFF"))
	status := "inactive"
	if _, err := s.UpdateWarehouse(ctx, inactive.ID, 1, store.WarehousePatch{Status: &status}); err != nil {
		t.Fatal(err)
	}
	for name, id := range map[string]string{"another site": *other.DefaultWarehouseID, "inactive": inactive.ID, "unknown": "00000000-0000-0000-0000-00000000dead"} {
		id := id
		if _, err := s.UpdateSite(ctx, site.ID, 1, store.SitePatch{DefaultWarehouseID: &id}); problemCode(t, err) != store.CodeSiteDefaultWarehouseInvalid {
			t.Fatalf("%s: error = %v; want ORG-SITE-DEFAULT-WAREHOUSE-INVALID", name, err)
		}
	}
	if untouched, _ := s.GetSite(ctx, site.ID); untouched.Version != 1 || deref(untouched.DefaultWarehouseID) != deref(site.DefaultWarehouseID) {
		t.Fatalf("refusals changed the site: %+v", untouched)
	}
	moved, err := s.UpdateSite(ctx, site.ID, 1, store.SitePatch{DefaultWarehouseID: &spare.ID})
	if err != nil || moved.Version != 2 || deref(moved.DefaultWarehouseID) != spare.ID {
		t.Fatalf("re-point = %+v, %v", moved, err)
	}
	empty := ""
	cleared, err := s.UpdateSite(ctx, site.ID, 2, store.SitePatch{DefaultWarehouseID: &empty})
	if err != nil || cleared.Version != 3 || cleared.DefaultWarehouseID != nil {
		t.Fatalf("clear = %+v, %v", cleared, err)
	}
	fields := updatedChangedFieldsFor(t, ctx, pool, siteUpdatedType)
	if !reflect.DeepEqual(fields, [][]string{{"default_warehouse_id"}, {"default_warehouse_id"}}) {
		t.Fatalf("changed_fields = %v", fields)
	}
}

func updatedChangedFieldsFor(t *testing.T, ctx context.Context, pool *pgxpool.Pool, eventType string) [][]string {
	t.Helper()
	var fields [][]string
	for _, event := range unitEvents(t, ctx, pool, eventType) {
		var names []string
		for _, name := range event["changed_fields"].([]any) {
			names = append(names, name.(string))
		}
		fields = append(fields, names)
	}
	return fields
}

func TestPostgres_MigrationAddsDefaultWarehouseColumnTwiceAndDeferredForeignKeyRejectsOtherEntities(t *testing.T) {
	admin, app := testkit.DBWithApp(t)
	ctx := principalContext(1)
	if err := dbx.Migrate(ctx, admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	seed(t, ctx, admin)
	script, err := migrations.FS.ReadFile("0027_site_default_warehouse.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, string(script)); err != nil {
		t.Fatalf("re-applying 0027: %v", err)
	}
	var columns int
	if err := admin.QueryRow(ctx, `select count(*) from information_schema.columns where table_schema = current_schema() and table_name = 'site' and column_name = 'default_warehouse_id'`).Scan(&columns); err != nil || columns != 1 {
		t.Fatalf("default_warehouse_id columns = %d, %v", columns, err)
	}
	s := store.New(app)
	a, b := mustCreate(t, s, ctx, newEntity("US01")), mustCreate(t, s, ctx, newEntity("US02"))
	siteA := mustCreateSite(t, s, ctx, newSite(a.ID, "S1"))
	foreign := mustCreateSite(t, s, ctx, siteWithDefault(b.ID, "S2", "WHB"))
	err = dbx.WithTenantTx(ctx, app, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `update site set default_warehouse_id = $2 where tenant_id = $3 and id = $1`, siteA.ID, *foreign.DefaultWarehouseID, testkit.TenantID(1))
		return err
	})
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23503" {
		t.Fatalf("pointing at another legal entity's warehouse error = %v; want a foreign key violation at commit", err)
	}
}

func TestPostgres_SiteDefaultWarehouseIsInvisibleAcrossTenantsAndScopes(t *testing.T) {
	s, ctx, _ := lifecycleStore(t)
	a, b := mustCreate(t, s, ctx, newEntity("US01")), mustCreate(t, s, ctx, newEntity("US02"))
	siteB := mustCreateSite(t, s, ctx, siteWithDefault(b.ID, "S2", "WHB"))
	siteA := mustCreateSite(t, s, ctx, siteWithDefault(a.ID, "S1", "WHA"))
	id := *siteB.DefaultWarehouseID
	scoped := scopedTo(a.ID)
	if _, err := s.UpdateSite(scoped, siteA.ID, 1, store.SitePatch{DefaultWarehouseID: &id}); problemCode(t, err) != store.CodeSiteDefaultWarehouseInvalid {
		t.Fatalf("scoped caller setting another entity's warehouse error = %v", err)
	}
	if _, err := s.UpdateSite(scoped, siteB.ID, 1, store.SitePatch{DefaultWarehouseID: &id}); problemCode(t, err) != store.CodeSiteNotFound {
		t.Fatalf("scoped caller updating another entity's site error = %v", err)
	}
	if _, err := s.UpdateSite(principalContext(2), siteB.ID, 1, store.SitePatch{DefaultWarehouseID: &id}); problemCode(t, err) != store.CodeSiteNotFound {
		t.Fatalf("tenant 2 updating tenant 1's site error = %v", err)
	}
	if got, _ := s.GetSite(scoped, siteA.ID); deref(got.DefaultWarehouseID) != *siteA.DefaultWarehouseID {
		t.Fatalf("scoped read = %+v", got)
	}
}
