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
	"github.com/BenjaminSRussell/passage/services/org/internal/store"
)

const (
	warehouseCreatedType = "dev.passage.org.warehouse.created.v1"
	warehouseUpdatedType = "dev.passage.org.warehouse.updated.v1"
)

func newWarehouse(siteID, code string) store.Warehouse {
	return store.Warehouse{SiteID: siteID, Code: code, Name: "Warehouse " + code}
}

func mustCreateWarehouse(t *testing.T, s *store.Postgres, ctx context.Context, in store.Warehouse) store.Warehouse {
	t.Helper()
	created, err := s.CreateWarehouse(ctx, in, "")
	if err != nil {
		t.Fatal(err)
	}
	return created
}

type warehouseWorld struct {
	s           *store.Postgres
	pool        *pgxpool.Pool
	ctx         context.Context
	usmf, demf  store.LegalEntity
	s1, s1b, s2 store.Site
	wh1, wh1De  store.Warehouse
	poolExec    func(context.Context, string, ...any) error
	unitEvents  func(string) []map[string]any
}

func newWarehouseWorld(t *testing.T) warehouseWorld {
	t.Helper()
	s, ctx, pool := lifecycleStore(t)
	w := warehouseWorld{s: s, ctx: ctx, pool: pool}
	w.usmf, w.demf = mustCreate(t, s, ctx, newEntity("USMF")), mustCreate(t, s, ctx, newEntity("DEMF"))
	w.s1, w.s1b = mustCreateSite(t, s, ctx, newSite(w.usmf.ID, "S1")), mustCreateSite(t, s, ctx, newSite(w.usmf.ID, "S1B"))
	w.s2 = mustCreateSite(t, s, ctx, newSite(w.demf.ID, "S2"))
	w.wh1 = mustCreateWarehouse(t, s, ctx, newWarehouse(w.s1.ID, "WH1"))
	w.wh1De = mustCreateWarehouse(t, s, ctx, newWarehouse(w.s2.ID, "WH1"))
	w.poolExec = func(ctx context.Context, sql string, args ...any) error {
		return dbx.WithTenantTx(ctx, pool, func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, sql, args...)
			return err
		})
	}
	w.unitEvents = func(eventType string) []map[string]any { return unitEvents(t, ctx, pool, eventType) }
	return w
}

func TestPostgres_WarehouseCreateWritesOneEventAndReplayWritesNone(t *testing.T) {
	w := newWarehouseWorld(t)
	first, err := w.s.CreateWarehouse(w.ctx, newWarehouse(w.s1.ID, "WH2"), "key-1")
	if err != nil {
		t.Fatal(err)
	}
	if first.LegalEntityID != w.usmf.ID || first.Type != "standard" || first.TimeZone != w.s1.TimeZone || first.Version != 1 {
		t.Fatalf("created = %+v; want the site's legal entity and time zone, type standard", first)
	}
	replay, err := w.s.CreateWarehouse(w.ctx, newWarehouse(w.s1.ID, "WH2"), "key-1")
	if err != nil || replay.ID != first.ID {
		t.Fatalf("replay = %+v, %v; want the original warehouse", replay, err)
	}
	changed := newWarehouse(w.s1.ID, "WH2")
	changed.Name = "Other"
	if _, err := w.s.CreateWarehouse(w.ctx, changed, "key-1"); problemCode(t, err) != store.CodeIdempotencyReuse {
		t.Fatalf("replay with a different name error = %v", err)
	}
	transit := newWarehouse(w.s1.ID, "WH2")
	transit.Type = "transit"
	if _, err := w.s.CreateWarehouse(w.ctx, transit, "key-1"); problemCode(t, err) != store.CodeIdempotencyReuse {
		t.Fatalf("replay with a different supplied type error = %v", err)
	}
	var created []map[string]any
	for _, evt := range w.unitEvents(warehouseCreatedType) {
		if evt["id"] == first.ID {
			created = append(created, evt)
		}
	}
	if len(created) != 1 || created[0]["legal_entity_id"] != w.usmf.ID || created[0]["parent_id"] != w.s1.ID || created[0]["type"] != "standard" {
		t.Fatalf("created events = %v; want exactly one for the warehouse", created)
	}
	if value, present := created[0]["address_id"]; !present || value != nil {
		t.Fatalf("address_id = %v, present %v; want present and null", value, present)
	}
	if got := len(w.unitEvents(warehouseCreatedType)); got != 3 {
		t.Fatalf("created events = %d; want 3 (WH1, WH1 in DEMF, WH2)", got)
	}
}

func TestPostgres_WarehouseCodeRulesAndSiteValidity(t *testing.T) {
	w := newWarehouseWorld(t)
	if w.wh1.Code != "WH1" || w.wh1De.Code != "WH1" {
		t.Fatalf("same code under two legal entities = %+v, %+v", w.wh1, w.wh1De)
	}
	if _, err := w.s.CreateWarehouse(w.ctx, newWarehouse(w.s1b.ID, "WH1"), ""); problemCode(t, err) != store.CodeWarehouseCodeDuplicate {
		t.Fatalf("duplicate code error = %v", err)
	}
	if _, err := w.s.CreateWarehouse(w.ctx, newWarehouse("00000000-0000-0000-0000-0000000000ff", "WH3"), ""); problemCode(t, err) != store.CodeWarehouseSiteInvalid {
		t.Fatalf("unknown site error = %v", err)
	}
	inactive := "inactive"
	if _, err := w.s.UpdateSite(w.ctx, w.s1b.ID, 1, store.SitePatch{Status: &inactive}); err != nil {
		t.Fatal(err)
	}
	if _, err := w.s.CreateWarehouse(w.ctx, newWarehouse(w.s1b.ID, "WH3"), ""); problemCode(t, err) != store.CodeWarehouseSiteInvalid {
		t.Fatalf("inactive site error = %v", err)
	}
}

func TestPostgres_WarehouseCannotChangeLegalEntityAcrossSites(t *testing.T) {
	w := newWarehouseWorld(t)
	moved := w.s2.ID
	if _, err := w.s.UpdateWarehouse(w.ctx, w.wh1.ID, 1, store.WarehousePatch{SiteID: &moved}); problemCode(t, err) != store.CodeWarehouseLegalEntityMismatch {
		t.Fatalf("move to another legal entity's site error = %v", err)
	}
	if kept, err := w.s.GetWarehouse(w.ctx, w.wh1.ID); err != nil || kept.SiteID != w.s1.ID || kept.Version != 1 {
		t.Fatalf("warehouse after rejected move = %+v, %v; want S1 at version 1", kept, err)
	}
	err := w.poolExec(w.ctx, `update warehouse set site_id = $1 where id = $2`, w.s2.ID, w.wh1.ID)
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23503" {
		t.Fatalf("direct cross-legal-entity update error = %v; want SQLSTATE 23503", err)
	}
	same := w.s1b.ID
	got, err := w.s.UpdateWarehouse(w.ctx, w.wh1.ID, 1, store.WarehousePatch{SiteID: &same})
	if err != nil || got.SiteID != w.s1b.ID || got.Version != 2 {
		t.Fatalf("move within the legal entity = %+v, %v", got, err)
	}
	events := w.unitEvents(warehouseUpdatedType)
	if len(events) != 1 || !reflect.DeepEqual(events[0]["changed_fields"], []any{"site_id"}) {
		t.Fatalf("updated events = %v; want exactly the move", events)
	}
	inactive := "inactive"
	if _, err := w.s.UpdateSite(w.ctx, w.s1.ID, 1, store.SitePatch{Status: &inactive}); err != nil {
		t.Fatal(err)
	}
	back := w.s1.ID
	if _, err := w.s.UpdateWarehouse(w.ctx, w.wh1.ID, 2, store.WarehousePatch{SiteID: &back}); problemCode(t, err) != store.CodeWarehouseSiteInvalid {
		t.Fatalf("move to an inactive site error = %v", err)
	}
}

func TestPostgres_WarehouseIsolationAndScope(t *testing.T) {
	w := newWarehouseWorld(t)
	other := principalContext(2)
	if _, err := w.s.GetWarehouse(other, w.wh1.ID); problemCode(t, err) != store.CodeWarehouseNotFound {
		t.Fatalf("tenant 2 get error = %v", err)
	}
	if page, err := w.s.ListWarehouses(other, store.WarehouseFilter{}, store.SiteKey{}, store.MaxPageSize); err != nil || len(page.Items) != 0 {
		t.Fatalf("tenant 2 list = %+v, %v; want empty", page, err)
	}
	scoped := scopedTo(w.usmf.ID)
	page, err := w.s.ListWarehouses(scoped, store.WarehouseFilter{}, store.SiteKey{}, store.MaxPageSize)
	if err != nil || len(page.Items) != 1 || page.Items[0].ID != w.wh1.ID {
		t.Fatalf("scoped list = %+v, %v; want only USMF's warehouse", page, err)
	}
	if _, err := w.s.GetWarehouse(scoped, w.wh1De.ID); problemCode(t, err) != store.CodeWarehouseNotFound {
		t.Fatalf("get of DEMF's warehouse error = %v", err)
	}
	if _, err := w.s.CreateWarehouse(scoped, newWarehouse(w.s2.ID, "WH9"), ""); problemCode(t, err) != store.CodeWarehouseSiteInvalid {
		t.Fatalf("create under DEMF's site error = %v", err)
	}
}

func TestPostgres_ListWarehousesFiltersAndPages(t *testing.T) {
	w := newWarehouseWorld(t)
	transit := newWarehouse(w.s1.ID, "WH2")
	transit.Type = "transit"
	transitWarehouse := mustCreateWarehouse(t, w.s, w.ctx, transit)
	for name, tc := range map[string]struct {
		filter store.WarehouseFilter
		want   []string
	}{
		"site":         {store.WarehouseFilter{SiteID: w.s1.ID}, []string{w.wh1.ID, transitWarehouse.ID}},
		"type":         {store.WarehouseFilter{Type: "transit"}, []string{transitWarehouse.ID}},
		"legal entity": {store.WarehouseFilter{LegalEntityID: w.demf.ID}, []string{w.wh1De.ID}},
	} {
		page, err := w.s.ListWarehouses(w.ctx, tc.filter, store.SiteKey{}, store.MaxPageSize)
		var got []string
		for _, item := range page.Items {
			got = append(got, item.ID)
		}
		if err != nil || len(got) != len(tc.want) {
			t.Fatalf("%s filter = %v, %v; want %v", name, got, err, tc.want)
		}
		for _, id := range tc.want {
			if !contains(got, id) {
				t.Fatalf("%s filter = %v; want %v", name, got, tc.want)
			}
		}
	}
	seen := map[string]bool{}
	var after store.SiteKey
	for pages := 0; pages < 4; pages++ {
		page, err := w.s.ListWarehouses(w.ctx, store.WarehouseFilter{Status: "active"}, after, 1)
		if err != nil || len(page.Items) != 1 {
			t.Fatalf("page %d = %+v, %v", pages, page, err)
		}
		seen[page.Items[0].ID] = true
		if page.Next == nil {
			break
		}
		after = *page.Next
	}
	if len(seen) != 3 || !seen[w.wh1.ID] || !seen[w.wh1De.ID] {
		t.Fatalf("walk saw %v; want all three warehouses once, including both WH1", seen)
	}
}

func contains(items []string, want string) bool {
	for _, item := range items {
		if item == want {
			return true
		}
	}
	return false
}

func TestPostgres_UpdateWarehouseVersionsAndEmitsOneEvent(t *testing.T) {
	w := newWarehouseWorld(t)
	name := "Renamed"
	renamed, err := w.s.UpdateWarehouse(w.ctx, w.wh1.ID, 1, store.WarehousePatch{Name: &name})
	if err != nil || renamed.Version != 2 || renamed.Name != name {
		t.Fatalf("rename = %+v, %v", renamed, err)
	}
	events := w.unitEvents(warehouseUpdatedType)
	if len(events) != 1 || !reflect.DeepEqual(events[0]["changed_fields"], []any{"name"}) || events[0]["version"] != float64(2) {
		t.Fatalf("updated events = %v", events)
	}
	if _, err := w.s.UpdateWarehouse(w.ctx, w.wh1.ID, 1, store.WarehousePatch{Name: &name}); problemCode(t, err) != store.CodeVersionConflict {
		t.Fatalf("stale version error = %v", err)
	}
	same, err := w.s.UpdateWarehouse(w.ctx, w.wh1.ID, 2, store.WarehousePatch{Name: &name})
	if err != nil || same.Version != 2 {
		t.Fatalf("no-op = %+v, %v; want version 2", same, err)
	}
	if got := len(w.unitEvents(warehouseUpdatedType)); got != 1 {
		t.Fatalf("stale and no-op patches left %d updated events; want 1", got)
	}
	kind, status := "quarantine", "inactive"
	if _, err := w.s.UpdateWarehouse(w.ctx, w.wh1.ID, 2, store.WarehousePatch{Type: &kind, Status: &status}); err != nil {
		t.Fatal(err)
	}
	events = w.unitEvents(warehouseUpdatedType)
	if len(events) != 2 || !reflect.DeepEqual(events[1]["changed_fields"], []any{"status", "type"}) {
		t.Fatalf("updated events = %v", events)
	}
}

func TestPostgres_DefaultWarehouseCannotBeDeactivatedOrMovedUntilTheSiteReleasesIt(t *testing.T) {
	s, ctx, _ := lifecycleStore(t)
	a := mustCreate(t, s, ctx, newEntity("US01"))
	site := mustCreateSite(t, s, ctx, siteWithDefault(a.ID, "S1", "MAIN"))
	elsewhere := mustCreateSite(t, s, ctx, newSite(a.ID, "S2"))
	plain := mustCreateWarehouse(t, s, ctx, newWarehouse(site.ID, "PLAIN"))
	def := *site.DefaultWarehouseID
	inactive, moved := "inactive", elsewhere.ID
	for name, patch := range map[string]store.WarehousePatch{"deactivate": {Status: &inactive}, "move": {SiteID: &moved}} {
		if _, err := s.UpdateWarehouse(ctx, def, 1, patch); problemCode(t, err) != store.CodeWarehouseIsDefault {
			t.Fatalf("%s: error = %v; want ORG-WH-IS-DEFAULT", name, err)
		}
	}
	if got, _ := s.GetWarehouse(ctx, def); got.Status != "active" || got.SiteID != site.ID || got.Version != 1 {
		t.Fatalf("refusals changed the warehouse: %+v", got)
	}
	if got, err := s.UpdateWarehouse(ctx, plain.ID, 1, store.WarehousePatch{Status: &inactive}); err != nil || got.Status != "inactive" {
		t.Fatalf("non-default deactivate = %+v, %v", got, err)
	}
	same := "active"
	if got, err := s.UpdateWarehouse(ctx, def, 1, store.WarehousePatch{Status: &same, SiteID: &site.ID}); err != nil || got.Version != 1 {
		t.Fatalf("unchanged values on the default = %+v, %v; want a no-op", got, err)
	}
	empty := ""
	if _, err := s.UpdateSite(ctx, site.ID, 1, store.SitePatch{DefaultWarehouseID: &empty}); err != nil {
		t.Fatal(err)
	}
	if got, err := s.UpdateWarehouse(ctx, def, 1, store.WarehousePatch{Status: &inactive}); err != nil || got.Status != "inactive" {
		t.Fatalf("deactivate after the default was cleared = %+v, %v", got, err)
	}
}
