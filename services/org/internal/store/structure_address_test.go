package store_test

import (
	"context"
	"reflect"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/org/internal/store"
)

const (
	legalEntityUpdatedType = "dev.passage.org.legalentity.updated.v1"
	siteUpdatedTypeName    = "dev.passage.org.site.updated.v1"
	locationUpdatedType    = "dev.passage.org.location.updated.v1"
)

type pointerOwner struct {
	kind, id, updatedType, field string
	read                         func(context.Context) (version int, pointer *string, err error)
	point                        func(ctx context.Context, version int, addressID string) error
}

func structureOwners(w warehouseWorld, locationID string) []pointerOwner {
	return []pointerOwner{
		{"legal_entity", w.usmf.ID, legalEntityUpdatedType, "primaryAddressId",
			func(ctx context.Context) (int, *string, error) {
				e, err := w.s.GetLegalEntity(ctx, w.usmf.ID)
				return e.Version, e.PrimaryAddressID, err
			},
			func(ctx context.Context, version int, addressID string) error {
				_, err := w.s.UpdateLegalEntity(ctx, w.usmf.ID, version, store.LegalEntityPatch{PrimaryAddressID: &addressID})
				return err
			}},
		{"site", w.s1.ID, siteUpdatedTypeName, "addressId",
			func(ctx context.Context) (int, *string, error) {
				s, err := w.s.GetSite(ctx, w.s1.ID)
				return s.Version, s.AddressID, err
			},
			func(ctx context.Context, version int, addressID string) error {
				_, err := w.s.UpdateSite(ctx, w.s1.ID, version, store.SitePatch{AddressID: &addressID})
				return err
			}},
		{"warehouse", w.wh1.ID, warehouseUpdatedType, "addressId",
			func(ctx context.Context) (int, *string, error) {
				h, err := w.s.GetWarehouse(ctx, w.wh1.ID)
				return h.Version, h.AddressID, err
			},
			func(ctx context.Context, version int, addressID string) error {
				_, err := w.s.UpdateWarehouse(ctx, w.wh1.ID, version, store.WarehousePatch{AddressID: &addressID})
				return err
			}},
		{"location", locationID, locationUpdatedType, "addressId",
			func(ctx context.Context) (int, *string, error) {
				l, err := w.s.GetLocation(ctx, locationID)
				return l.Version, l.AddressID, err
			},
			func(ctx context.Context, version int, addressID string) error {
				_, err := w.s.UpdateLocation(ctx, locationID, version, store.LocationPatch{AddressID: &addressID})
				return err
			}},
	}
}

func (w warehouseWorld) mustAddress(t *testing.T, ownerType, ownerID string) store.Address {
	t.Helper()
	a, err := w.s.CreateAddress(w.ctx, newAddress(ownerType, ownerID, business(true)), "")
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func (w warehouseWorld) rejectsPointer(t *testing.T, o pointerOwner, ctx context.Context, addressID string) {
	t.Helper()
	versionBefore, pointerBefore, err := o.read(w.ctx)
	if err != nil {
		t.Fatal(err)
	}
	eventsBefore := len(w.unitEvents(o.updatedType))
	err = o.point(ctx, versionBefore, addressID)
	if err == nil {
		t.Fatalf("%s pointing at %s was accepted", o.kind, addressID)
	}
	if problemCode(t, err) != store.CodeAddressNotOwned {
		t.Fatalf("%s pointing at %s error = %v; want %s", o.kind, addressID, err, store.CodeAddressNotOwned)
	}
	if _, named := problemErrors(err)[o.field]; !named {
		t.Fatalf("%s errors = %v; want field %s", o.kind, problemErrors(err), o.field)
	}
	versionAfter, pointerAfter, _ := o.read(w.ctx)
	if versionAfter != versionBefore || !reflect.DeepEqual(pointerAfter, pointerBefore) || len(w.unitEvents(o.updatedType)) != eventsBefore {
		t.Fatalf("%s rejection changed version %d->%d, pointer %v->%v or the outbox", o.kind, versionBefore, versionAfter, pointerBefore, pointerAfter)
	}
}

func TestPostgres_StructureAddressSetReadClearAndFollowRevisions(t *testing.T) {
	w := newWarehouseWorld(t)
	locationID := mustLocation(t, w)
	var addressIDs []string
	for _, o := range structureOwners(w, locationID) {
		address := w.mustAddress(t, o.kind, o.id)
		addressIDs = append(addressIDs, address.ID)
		eventsBefore := len(w.unitEvents(o.updatedType))
		if err := o.point(w.ctx, 1, address.ID); err != nil {
			t.Fatalf("%s: %v", o.kind, err)
		}
		version, pointer, err := o.read(w.ctx)
		if err != nil || version != 2 || pointer == nil || *pointer != address.ID {
			t.Fatalf("%s read = %d, %v, %v; want version 2 pointing at %s", o.kind, version, pointer, err, address.ID)
		}
		events := w.unitEvents(o.updatedType)
		wantField := "address_id"
		if o.kind == "legal_entity" {
			wantField = "primary_address_id"
		}
		if len(events) != eventsBefore+1 || !reflect.DeepEqual(events[eventsBefore]["changed_fields"], []any{wantField}) {
			t.Fatalf("%s events = %v; want exactly one more with changed_fields [%s]", o.kind, events, wantField)
		}
		if err := o.point(w.ctx, 2, address.ID); err != nil {
			t.Fatalf("%s repeat: %v", o.kind, err)
		}
		if _, _, err := o.read(w.ctx); err != nil || len(w.unitEvents(o.updatedType)) != eventsBefore+1 {
			t.Fatalf("%s repeating the same pointer wrote an event: %v", o.kind, err)
		}
	}
	page, err := w.s.ListLegalEntities(w.ctx, store.LegalEntityFilter{}, "", 10)
	if err != nil {
		t.Fatalf("list = %+v, %v", page, err)
	}
	listed := map[string]*string{}
	for _, e := range page.Items {
		listed[e.ID] = e.PrimaryAddressID
	}
	if listed[w.usmf.ID] == nil || *listed[w.usmf.ID] != addressIDs[0] || listed[w.demf.ID] != nil {
		t.Fatalf("listed pointers = %v; want usmf set and demf null", listed)
	}

	current, err := w.s.GetAddress(w.ctx, addressIDs[2])
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.s.ReviseAddress(w.ctx, current.ID, current.VersionID, revisionOf(current, store.AddressPatch{City: ptr("Oakland")}, "")); err != nil {
		t.Fatal(err)
	}
	warehouse, err := w.s.GetWarehouse(w.ctx, w.wh1.ID)
	if err != nil || warehouse.AddressID == nil || *warehouse.AddressID != current.ID || warehouse.Version != 2 {
		t.Fatalf("warehouse after revision = %+v, %v; want the same addressId and version", warehouse, err)
	}
	if resolved, err := w.s.GetAddress(w.ctx, *warehouse.AddressID); err != nil || resolved.City != "Oakland" {
		t.Fatalf("resolved = %+v, %v; want the new city", resolved, err)
	}
	if err := structureOwners(w, locationID)[2].point(w.ctx, 2, current.ID); err != nil {
		t.Fatalf("address_id is accepted after a revision: %v", err)
	}

	eventsBefore := len(w.unitEvents(siteUpdatedTypeName))
	cleared, err := w.s.UpdateSite(w.ctx, w.s1.ID, 2, store.SitePatch{AddressID: ptr("")})
	if err != nil || cleared.AddressID != nil || cleared.Version != 3 {
		t.Fatalf("cleared = %+v, %v; want a null addressId at version 3", cleared, err)
	}
	events := w.unitEvents(siteUpdatedTypeName)
	if len(events) != eventsBefore+1 || !reflect.DeepEqual(events[eventsBefore]["changed_fields"], []any{"address_id"}) {
		t.Fatalf("clear events = %v; want one more with changed_fields [address_id]", events)
	}

	var orphans int
	if err := dbx.WithTenantTx(w.ctx, w.pool, func(tx pgx.Tx) error {
		return tx.QueryRow(w.ctx, `select count(*) from (
			select 'legal_entity' as owner_type, id as owner_id, primary_address_id as address_id from legal_entity
			union all select 'site', id, address_id from site
			union all select 'warehouse', id, address_id from warehouse
			union all select 'location', id, address_id from location) p
			where p.address_id is not null and (select count(*) from postal_address a
				where a.address_id = p.address_id and a.owner_type = p.owner_type and a.owner_id = p.owner_id and a.valid_to is null) <> 1`).Scan(&orphans)
	}); err != nil || orphans != 0 {
		t.Fatalf("orphan pointers = %d, %v; want every pointer to resolve to exactly one current owned address", orphans, err)
	}
}

func TestPostgres_StructureAddressRejections(t *testing.T) {
	w := newWarehouseWorld(t)
	locationID := mustLocation(t, w)
	siteAddress := w.mustAddress(t, "site", w.s1.ID)
	otherSiteAddress := w.mustAddress(t, "site", w.s1b.ID)
	warehouseAddress := w.mustAddress(t, "warehouse", w.wh1.ID)
	for _, o := range structureOwners(w, locationID) {
		w.rejectsPointer(t, o, w.ctx, ids.New())
		w.rejectsPointer(t, o, w.ctx, otherSiteAddress.ID)
	}
	owners := structureOwners(w, locationID)
	w.rejectsPointer(t, owners[2], w.ctx, siteAddress.ID)
	w.rejectsPointer(t, owners[1], w.ctx, warehouseAddress.ID)

	revised, err := w.s.ReviseAddress(w.ctx, warehouseAddress.ID, warehouseAddress.VersionID, revisionOf(warehouseAddress, store.AddressPatch{City: ptr("Oakland")}, ""))
	if err != nil {
		t.Fatal(err)
	}
	latest, err := w.s.ReviseAddress(w.ctx, revised.ID, revised.VersionID, revisionOf(revised, store.AddressPatch{City: ptr("Berkeley")}, ""))
	if err != nil || latest.VersionID == revised.VersionID {
		t.Fatalf("second revision = %+v, %v", latest, err)
	}
	w.rejectsPointer(t, owners[2], w.ctx, revised.VersionID)
	if err := owners[2].point(w.ctx, 1, warehouseAddress.ID); err != nil {
		t.Fatalf("stable address_id after a revision: %v", err)
	}

	demfAddress := w.mustAddress(t, "legal_entity", w.demf.ID)
	w.rejectsPointer(t, owners[0], scopedTo(w.usmf.ID), demfAddress.ID)
}

func TestPostgres_UpdateLocationVersionedPatchAndIsolation(t *testing.T) {
	w := newWarehouseWorld(t)
	locationID := mustLocation(t, w)
	rowsBefore := len(w.unitEvents(locationUpdatedType))
	patched, err := w.s.UpdateLocation(w.ctx, locationID, 1, store.LocationPatch{Name: ptr("Renamed"), Type: ptr("receiving"), Status: ptr("inactive")})
	if err != nil || patched.Version != 2 || patched.Name != "Renamed" || patched.Type != "receiving" || patched.Status != "inactive" {
		t.Fatalf("patched = %+v, %v", patched, err)
	}
	events := w.unitEvents(locationUpdatedType)
	if len(events) != rowsBefore+1 || !reflect.DeepEqual(events[rowsBefore]["changed_fields"], []any{"name", "status", "type"}) || events[rowsBefore]["version"] != float64(2) {
		t.Fatalf("events = %v; want one with changed_fields [name status type] at version 2", events)
	}
	if _, err := w.s.UpdateLocation(w.ctx, locationID, 1, store.LocationPatch{Name: ptr("Stale")}); problemCode(t, err) != store.CodeVersionConflict {
		t.Fatalf("stale version error = %v", err)
	}
	same, err := w.s.UpdateLocation(w.ctx, locationID, 2, store.LocationPatch{Name: ptr("Renamed")})
	if err != nil || same.Version != 2 || len(w.unitEvents(locationUpdatedType)) != rowsBefore+1 {
		t.Fatalf("no-op = %+v, %v; want version 2 and no new event", same, err)
	}

	foreign := mustCreateLocation(t, w, newLocation(w.wh1De.ID, "LDE"))
	if _, err := w.s.UpdateLocation(scopedTo(w.usmf.ID), foreign.ID, 1, store.LocationPatch{Name: ptr("x")}); problemCode(t, err) != store.CodeLocationNotFound {
		t.Fatalf("out-of-scope error = %v", err)
	}
	if _, err := w.s.UpdateLocation(principalContext(2), locationID, 2, store.LocationPatch{Name: ptr("x")}); problemCode(t, err) != store.CodeLocationNotFound {
		t.Fatalf("other tenant error = %v", err)
	}
	if len(w.unitEvents(locationUpdatedType)) != rowsBefore+1 {
		t.Fatalf("a refused update wrote an event")
	}
}
