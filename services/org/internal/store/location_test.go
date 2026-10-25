package store_test

import (
	"errors"
	"reflect"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/BenjaminSRussell/passage/services/org/internal/store"
)

const locationCreatedType = "dev.passage.org.location.created.v1"

func newLocation(warehouseID, code string) store.Location {
	return store.Location{WarehouseID: warehouseID, Code: code, Name: "Location " + code}
}

func mustCreateLocation(t *testing.T, w warehouseWorld, in store.Location) store.Location {
	t.Helper()
	created, err := w.s.CreateLocation(w.ctx, in, "")
	if err != nil {
		t.Fatal(err)
	}
	return created
}

func TestPostgres_LocationCreateWritesOneEventAndReplayWritesNone(t *testing.T) {
	w := newWarehouseWorld(t)
	first, err := w.s.CreateLocation(w.ctx, newLocation(w.wh1.ID, "L1"), "key-1")
	if err != nil {
		t.Fatal(err)
	}
	if first.LegalEntityID != w.usmf.ID || first.Type != "storage" || first.Status != "active" || first.Version != 1 {
		t.Fatalf("created = %+v; want the warehouse's legal entity, type storage", first)
	}
	replay, err := w.s.CreateLocation(w.ctx, newLocation(w.wh1.ID, "L1"), "key-1")
	if err != nil || replay.ID != first.ID {
		t.Fatalf("replay = %+v, %v; want the original location", replay, err)
	}
	renamed := newLocation(w.wh1.ID, "L1")
	renamed.Name = "Other"
	if _, err := w.s.CreateLocation(w.ctx, renamed, "key-1"); problemCode(t, err) != store.CodeIdempotencyReuse {
		t.Fatalf("replay with a different name error = %v", err)
	}
	receiving := newLocation(w.wh1.ID, "L1")
	receiving.Type = "receiving"
	if _, err := w.s.CreateLocation(w.ctx, receiving, "key-1"); problemCode(t, err) != store.CodeIdempotencyReuse {
		t.Fatalf("replay with a different supplied type error = %v", err)
	}
	if _, err := w.s.CreateLocation(w.ctx, newLocation("00000000-0000-0000-0000-0000000000ff", "L2"), ""); problemCode(t, err) != store.CodeLocationWarehouseInvalid {
		t.Fatalf("create under an unknown warehouse error = %v", err)
	}
	events := w.unitEvents(locationCreatedType)
	if len(events) != 1 || events[0]["id"] != first.ID || events[0]["legal_entity_id"] != w.usmf.ID || events[0]["parent_id"] != w.wh1.ID || events[0]["type"] != "storage" {
		t.Fatalf("created events = %v; want exactly one for the location", events)
	}
	if value, present := events[0]["address_id"]; !present || value != nil {
		t.Fatalf("address_id = %v, present %v; want present and null", value, present)
	}
}

func TestPostgres_LocationCodeRulesAndWarehouseValidity(t *testing.T) {
	w := newWarehouseWorld(t)
	wh2 := mustCreateWarehouse(t, w.s, w.ctx, newWarehouse(w.s1.ID, "WH2"))
	l1 := mustCreateLocation(t, w, newLocation(w.wh1.ID, "L1"))
	mustCreateLocation(t, w, newLocation(wh2.ID, "L1"))
	if _, err := w.s.CreateLocation(w.ctx, newLocation(w.wh1.ID, "L1"), ""); problemCode(t, err) != store.CodeLocationCodeDuplicate {
		t.Fatalf("duplicate code error = %v", err)
	}
	inactive := "inactive"
	if _, err := w.s.UpdateWarehouse(w.ctx, wh2.ID, 1, store.WarehousePatch{Status: &inactive}); err != nil {
		t.Fatal(err)
	}
	if _, err := w.s.CreateLocation(w.ctx, newLocation(wh2.ID, "L2"), ""); problemCode(t, err) != store.CodeLocationWarehouseInvalid {
		t.Fatalf("inactive warehouse error = %v", err)
	}
	err := w.poolExec(principalContext(1), `update location set warehouse_id = $1 where id = $2`, w.wh1De.ID, l1.ID)
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23503" {
		t.Fatalf("direct cross-legal-entity update error = %v; want SQLSTATE 23503", err)
	}
	if got := len(w.unitEvents(locationCreatedType)); got != 2 {
		t.Fatalf("created events = %d; want 2 (rejected creates write none)", got)
	}
}

func TestPostgres_LocationIsolationAndScope(t *testing.T) {
	w := newWarehouseWorld(t)
	l1 := mustCreateLocation(t, w, newLocation(w.wh1.ID, "L1"))
	l1De := mustCreateLocation(t, w, newLocation(w.wh1De.ID, "L1"))
	other := principalContext(2)
	if _, err := w.s.GetLocation(other, l1.ID); problemCode(t, err) != store.CodeLocationNotFound {
		t.Fatalf("tenant 2 get error = %v", err)
	}
	if page, err := w.s.ListLocations(other, store.LocationFilter{}, store.SiteKey{}, store.MaxPageSize); err != nil || len(page.Items) != 0 {
		t.Fatalf("tenant 2 list = %+v, %v; want empty", page, err)
	}
	scoped := scopedTo(w.usmf.ID)
	page, err := w.s.ListLocations(scoped, store.LocationFilter{}, store.SiteKey{}, store.MaxPageSize)
	if err != nil || len(page.Items) != 1 || page.Items[0].ID != l1.ID {
		t.Fatalf("scoped list = %+v, %v; want only USMF's location", page, err)
	}
	if _, err := w.s.GetLocation(scoped, l1De.ID); problemCode(t, err) != store.CodeLocationNotFound {
		t.Fatalf("get of DEMF's location error = %v", err)
	}
	if _, err := w.s.CreateLocation(scoped, newLocation(w.wh1De.ID, "L9"), ""); problemCode(t, err) != store.CodeLocationWarehouseInvalid {
		t.Fatalf("create under DEMF's warehouse error = %v", err)
	}
}

func TestPostgres_ListLocationsFiltersAndPages(t *testing.T) {
	w := newWarehouseWorld(t)
	l1 := mustCreateLocation(t, w, newLocation(w.wh1.ID, "L1"))
	dock := newLocation(w.wh1.ID, "L2")
	dock.Type = "receiving"
	l2 := mustCreateLocation(t, w, dock)
	l1De := mustCreateLocation(t, w, newLocation(w.wh1De.ID, "L1"))
	for name, tc := range map[string]struct {
		filter store.LocationFilter
		want   []string
	}{
		"warehouse":    {store.LocationFilter{WarehouseID: w.wh1.ID}, []string{l1.ID, l2.ID}},
		"type":         {store.LocationFilter{Type: "receiving"}, []string{l2.ID}},
		"legal entity": {store.LocationFilter{LegalEntityID: w.demf.ID}, []string{l1De.ID}},
	} {
		page, err := w.s.ListLocations(w.ctx, tc.filter, store.SiteKey{}, store.MaxPageSize)
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
		page, err := w.s.ListLocations(w.ctx, store.LocationFilter{Status: "active"}, after, 1)
		if err != nil || len(page.Items) != 1 {
			t.Fatalf("page %d = %+v, %v", pages, page, err)
		}
		seen[page.Items[0].ID] = true
		if page.Next == nil {
			break
		}
		after = *page.Next
	}
	if len(seen) != 3 || !seen[l1.ID] || !seen[l1De.ID] {
		t.Fatalf("walk saw %v; want all three locations once, including both L1", seen)
	}
}

func TestPostgres_SiteDeactivationRefusedWhileWarehousesActive(t *testing.T) {
	w := newWarehouseWorld(t)
	inactive := "inactive"
	_, err := w.s.UpdateSite(w.ctx, w.s1.ID, 1, store.SitePatch{Status: &inactive})
	if problemCode(t, err) != store.CodeSiteInUse {
		t.Fatalf("deactivation with an active warehouse error = %v", err)
	}
	if kept, err := w.s.GetSite(w.ctx, w.s1.ID); err != nil || kept.Version != 1 || kept.Status != "active" {
		t.Fatalf("site after refusal = %+v, %v; want version 1, active", kept, err)
	}
	if got := len(w.unitEvents(siteUpdatedType)); got != 0 {
		t.Fatalf("refusal left %d site.updated events; want 0", got)
	}
	name := "Renamed"
	if renamed, err := w.s.UpdateSite(w.ctx, w.s1.ID, 1, store.SitePatch{Name: &name}); err != nil || renamed.Version != 2 {
		t.Fatalf("name-only patch = %+v, %v; want version 2", renamed, err)
	}
	if _, err := w.s.UpdateWarehouse(w.ctx, w.wh1.ID, 1, store.WarehousePatch{Status: &inactive}); err != nil {
		t.Fatal(err)
	}
	if _, err := w.s.UpdateSite(w.ctx, w.s1.ID, 2, store.SitePatch{Status: &inactive}); err != nil {
		t.Fatal(err)
	}
	events := w.unitEvents(siteUpdatedType)
	if len(events) != 2 || !reflect.DeepEqual(events[1]["changed_fields"], []any{"status"}) {
		t.Fatalf("site.updated events = %v; want the rename then the deactivation", events)
	}
	if deactivated, err := w.s.UpdateSite(w.ctx, w.s1b.ID, 1, store.SitePatch{Status: &inactive}); err != nil || deactivated.Status != "inactive" {
		t.Fatalf("deactivation of a site without warehouses = %+v, %v", deactivated, err)
	}
}
