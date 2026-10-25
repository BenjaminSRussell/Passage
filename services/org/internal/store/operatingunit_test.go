package store_test

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/org/internal/store"
)

const (
	createdType = "dev.passage.org.operatingunit.created.v1"
	updatedType = "dev.passage.org.operatingunit.updated.v1"
)

func unit(unitType, code string) store.OperatingUnit {
	return store.OperatingUnit{Type: unitType, Code: code, Name: "Unit " + code}
}

func unitEvents(t *testing.T, ctx context.Context, pool *pgxpool.Pool, eventType string) []map[string]any {
	t.Helper()
	var data []map[string]any
	for _, row := range outboxRows(t, ctx, pool) {
		if row.Type != eventType {
			continue
		}
		var evt struct{ Data map[string]any }
		if err := json.Unmarshal(row.Payload, &evt); err != nil {
			t.Fatal(err)
		}
		data = append(data, evt.Data)
	}
	return data
}

func mustCreateUnit(t *testing.T, s *store.Postgres, ctx context.Context, in store.OperatingUnit) store.OperatingUnit {
	t.Helper()
	created, err := s.CreateOperatingUnit(ctx, in, "")
	if err != nil {
		t.Fatal(err)
	}
	return created
}

func TestPostgres_CreateOperatingUnitWritesOneEventAndReplayWritesNone(t *testing.T) {
	s, ctx, pool := lifecycleStore(t)
	first, err := s.CreateOperatingUnit(ctx, unit("department", "D100"), "key-1")
	if err != nil {
		t.Fatal(err)
	}
	replay, err := s.CreateOperatingUnit(ctx, unit("department", "D100"), "key-1")
	if err != nil || replay.ID != first.ID {
		t.Fatalf("replay = %+v, %v; want the original unit", replay, err)
	}
	changed := unit("department", "D100")
	changed.Name = "Other"
	if _, err := s.CreateOperatingUnit(ctx, changed, "key-1"); problemCode(t, err) != store.CodeIdempotencyReuse {
		t.Fatalf("replay with another name error = %v; want %s", err, store.CodeIdempotencyReuse)
	}
	want := []map[string]any{{"id": first.ID, "type": "department", "code": "D100", "name": "Unit D100"}}
	if got := unitEvents(t, ctx, pool, createdType); !reflect.DeepEqual(got, want) {
		t.Fatalf("created events = %v; want exactly %v", got, want)
	}
}

func TestPostgres_OperatingUnitCodeIsUniquePerType(t *testing.T) {
	s, ctx, _ := lifecycleStore(t)
	mustCreateUnit(t, s, ctx, unit("department", "D100"))
	mustCreateUnit(t, s, ctx, unit("cost_center", "D100"))
	if _, err := s.CreateOperatingUnit(ctx, unit("department", "D100"), ""); problemCode(t, err) != store.CodeOUCodeDuplicate {
		t.Fatalf("duplicate error = %v; want %s", err, store.CodeOUCodeDuplicate)
	}
}

func TestPostgres_OperatingUnitsAreTenantIsolated(t *testing.T) {
	s, ctx, _ := lifecycleStore(t)
	created := mustCreateUnit(t, s, ctx, unit("department", "D100"))
	other := principalContext(2)
	if _, err := s.GetOperatingUnit(other, created.ID); problemCode(t, err) != store.CodeOUNotFound {
		t.Fatalf("tenant 2 get error = %v; want %s", err, store.CodeOUNotFound)
	}
	if page, err := s.ListOperatingUnits(other, store.OperatingUnitFilter{}, store.OperatingUnitKey{}, store.MaxPageSize); err != nil || len(page.Items) != 0 {
		t.Fatalf("tenant 2 list = %+v, %v; want empty", page, err)
	}
}

func TestPostgres_DefaultLegalEntityMustBeActiveAndVisible(t *testing.T) {
	s, ctx, _ := lifecycleStore(t)
	a, b := mustCreate(t, s, ctx, newEntity("US01")), mustCreate(t, s, ctx, newEntity("US02"))
	withEntity := func(code, id string) store.OperatingUnit {
		in := unit("department", code)
		in.DefaultLegalEntityID = &id
		return in
	}
	if _, err := s.CreateOperatingUnit(ctx, withEntity("D1", testkit.TenantID(9)), ""); problemCode(t, err) != store.CodeOULegalEntityInvalid {
		t.Fatalf("unknown legal entity error = %v", err)
	}
	inactive := "inactive"
	if _, err := s.UpdateLegalEntity(ctx, b.ID, 1, store.LegalEntityPatch{Status: &inactive}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateOperatingUnit(ctx, withEntity("D2", b.ID), ""); problemCode(t, err) != store.CodeOULegalEntityInvalid {
		t.Fatalf("inactive legal entity error = %v", err)
	}
	scoped := tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(1), LegalEntityID: a.ID})
	c := mustCreate(t, s, ctx, newEntity("US03"))
	if _, err := s.CreateOperatingUnit(scoped, withEntity("D3", c.ID), ""); problemCode(t, err) != store.CodeOULegalEntityInvalid {
		t.Fatalf("out-of-scope legal entity error = %v", err)
	}
	created, err := s.CreateOperatingUnit(scoped, withEntity("D4", a.ID), "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpdateOperatingUnit(scoped, created.ID, 1, store.OperatingUnitPatch{DefaultLegalEntityID: &c.ID}); problemCode(t, err) != store.CodeOULegalEntityInvalid {
		t.Fatalf("update to out-of-scope legal entity error = %v", err)
	}
	page, err := s.ListOperatingUnits(scoped, store.OperatingUnitFilter{}, store.OperatingUnitKey{}, store.MaxPageSize)
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("scoped list = %+v, %v", page, err)
	}
	mustCreateUnit(t, s, ctx, unit("department", "D5"))
	if page, err = s.ListOperatingUnits(scoped, store.OperatingUnitFilter{}, store.OperatingUnitKey{}, store.MaxPageSize); err != nil || len(page.Items) != 2 {
		t.Fatalf("scoped caller must list every unit of the tenant: %+v, %v", page, err)
	}
}

func TestPostgres_ListOperatingUnitsFiltersAndPages(t *testing.T) {
	s, ctx, _ := lifecycleStore(t)
	a := mustCreate(t, s, ctx, newEntity("US01"))
	withEntity := unit("department", "D2")
	withEntity.DefaultLegalEntityID = &a.ID
	mustCreateUnit(t, s, ctx, unit("cost_center", "C1"))
	mustCreateUnit(t, s, ctx, unit("department", "D1"))
	mustCreateUnit(t, s, ctx, withEntity)
	byType, err := s.ListOperatingUnits(ctx, store.OperatingUnitFilter{Type: "cost_center"}, store.OperatingUnitKey{}, store.MaxPageSize)
	if err != nil || len(byType.Items) != 1 || byType.Items[0].Code != "C1" {
		t.Fatalf("type filter = %+v, %v", byType, err)
	}
	byEntity, err := s.ListOperatingUnits(ctx, store.OperatingUnitFilter{DefaultLegalEntityID: a.ID}, store.OperatingUnitKey{}, store.MaxPageSize)
	if err != nil || len(byEntity.Items) != 1 || byEntity.Items[0].Code != "D2" {
		t.Fatalf("legal entity filter = %+v, %v", byEntity, err)
	}
	first, err := s.ListOperatingUnits(ctx, store.OperatingUnitFilter{}, store.OperatingUnitKey{}, 2)
	if err != nil || len(first.Items) != 2 || first.Next == nil || *first.Next != (store.OperatingUnitKey{Type: "department", Code: "D1"}) {
		t.Fatalf("first page = %+v, %v", first, err)
	}
	second, err := s.ListOperatingUnits(ctx, store.OperatingUnitFilter{}, *first.Next, 2)
	if err != nil || len(second.Items) != 1 || second.Items[0].Code != "D2" || second.Next != nil {
		t.Fatalf("second page = %+v, %v", second, err)
	}
}

func TestPostgres_UpdateOperatingUnitVersionsAndEmitsOneEvent(t *testing.T) {
	s, ctx, pool := lifecycleStore(t)
	a := mustCreate(t, s, ctx, newEntity("US01"))
	created := mustCreateUnit(t, s, ctx, unit("cost_center", "C1"))
	name := "Renamed"
	updated, err := s.UpdateOperatingUnit(ctx, created.ID, 1, store.OperatingUnitPatch{Name: &name})
	if err != nil || updated.Version != 2 || updated.Name != name {
		t.Fatalf("name update = %+v, %v", updated, err)
	}
	want := []map[string]any{{"id": created.ID, "changed_fields": []any{"name"}, "version": float64(2)}}
	if got := unitEvents(t, ctx, pool, updatedType); !reflect.DeepEqual(got, want) {
		t.Fatalf("updated events = %v; want %v", got, want)
	}
	other := "Stale"
	if _, err := s.UpdateOperatingUnit(ctx, created.ID, 1, store.OperatingUnitPatch{Name: &other}); problemCode(t, err) != store.CodeVersionConflict {
		t.Fatalf("stale update error = %v", err)
	}
	if got, _ := s.GetOperatingUnit(ctx, created.ID); got.Name != name || got.Version != 2 {
		t.Fatalf("stale update changed the unit: %+v", got)
	}
	noop, err := s.UpdateOperatingUnit(ctx, created.ID, 2, store.OperatingUnitPatch{Name: &name})
	if err != nil || noop.Version != 2 {
		t.Fatalf("no-op update = %+v, %v", noop, err)
	}
	if got := unitEvents(t, ctx, pool, updatedType); len(got) != 1 {
		t.Fatalf("no-op or stale update wrote an event: %v", got)
	}
	withEntity, err := s.UpdateOperatingUnit(ctx, created.ID, 2, store.OperatingUnitPatch{DefaultLegalEntityID: &a.ID})
	if err != nil || withEntity.DefaultLegalEntityID == nil || *withEntity.DefaultLegalEntityID != a.ID {
		t.Fatalf("set default legal entity = %+v, %v", withEntity, err)
	}
	empty := ""
	cleared, err := s.UpdateOperatingUnit(ctx, created.ID, 3, store.OperatingUnitPatch{DefaultLegalEntityID: &empty})
	if err != nil || cleared.DefaultLegalEntityID != nil || cleared.Version != 4 {
		t.Fatalf("clear default legal entity = %+v, %v", cleared, err)
	}
}

func TestPostgres_ManagerPartyIsForCostCentersOnly(t *testing.T) {
	s, ctx, _ := lifecycleStore(t)
	department := mustCreateUnit(t, s, ctx, unit("department", "D1"))
	party := testkit.TenantID(7)
	if _, err := s.UpdateOperatingUnit(ctx, department.ID, 1, store.OperatingUnitPatch{ManagerPartyID: &party}); problemCode(t, err) != "COM-0001" {
		t.Fatalf("manager on department error = %v", err)
	}
	if _, err := s.UpdateOperatingUnit(ctx, testkit.TenantID(9), 1, store.OperatingUnitPatch{}); problemCode(t, err) != store.CodeOUNotFound {
		t.Fatalf("unknown id error = %v", err)
	}
	costCenter := unit("cost_center", "C1")
	costCenter.ManagerPartyID = &party
	if got := mustCreateUnit(t, s, ctx, costCenter); got.ManagerPartyID == nil || *got.ManagerPartyID != party {
		t.Fatalf("cost center manager = %+v", got)
	}
}
