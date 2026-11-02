package store_test

import (
	"context"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/migration/internal/domain"
	"github.com/BenjaminSRussell/passage/services/migration/internal/store"
	"github.com/BenjaminSRussell/passage/services/migration/migrations"
)

func custBinding(profile, entity string) store.EntityBinding {
	return store.EntityBinding{ConnectionProfileID: profile, Entity: entity, Status: domain.BindingActive, KeyFields: []string{"id"},
		Source: map[string]any{"schema": "dbo", "table": "CustTable"}, CursorField: "upd", CursorType: domain.CursorTimestamp}
}

// exerciseBindings runs against Postgres (under the app role) and Mem so the fake keeps matching the contract.
func exerciseBindings(t *testing.T, s store.Store, one, two context.Context, tenantAware bool) {
	profile, _, err := s.SaveConnectionProfile(one, sqlProfile("Acme ERP"), "")
	if err != nil {
		t.Fatal(err)
	}
	saved, created, err := s.SaveEntityBinding(one, custBinding(profile.ID, "customer"), "b1")
	if err != nil || !created || saved.ID == "" || saved.Version != 1 || saved.Status != "active" || saved.Source["table"] != "CustTable" || saved.KeyFields[0] != "id" || saved.CursorType != "timestamp" {
		t.Fatalf("save = %+v %v %v", saved, created, err)
	}
	if again, created, err := s.SaveEntityBinding(one, custBinding(profile.ID, "customer"), "b1"); err != nil || created || again.ID != saved.ID {
		t.Fatalf("same key must replay: %+v %v %v", again, created, err)
	}
	_, _, err = s.SaveEntityBinding(one, custBinding(profile.ID, "vendor"), "b1")
	problemStatus(t, err, 422, "idempotency_key_reuse")
	_, _, err = s.SaveEntityBinding(one, custBinding(profile.ID, "customer"), "b2")
	problemStatus(t, err, 409, store.CodeBindingExists)
	_, _, err = s.SaveEntityBinding(one, custBinding(ids.New(), "vendor"), "")
	problemStatus(t, err, 422, store.CodeConnectionUnknown)
	withSpec := custBinding(profile.ID, "vendor")
	withSpec.MappingSpecID = ids.New()
	_, _, err = s.SaveEntityBinding(one, withSpec, "")
	problemStatus(t, err, 422, "MIG-SPEC-UNKNOWN")
	if _, _, err := s.SaveEntityBinding(one, custBinding(profile.ID, "item"), ""); err != nil {
		t.Fatal(err)
	}

	if got, err := s.GetEntityBinding(one, saved.ID); err != nil || got.Entity != "customer" || got.ConnectionProfileID != profile.ID {
		t.Fatalf("get = %+v %v", got, err)
	}
	if _, err := s.GetEntityBinding(one, "not-a-uuid"); err == nil {
		t.Fatal("a bad id must be not found")
	}
	page, err := s.ListEntityBindings(one, store.BindingFilter{Limit: 1})
	if err != nil || len(page) != 1 || page[0].Entity != "customer" {
		t.Fatalf("first page = %+v %v", page, err)
	}
	next, err := s.ListEntityBindings(one, store.BindingFilter{Limit: 5, After: &store.BindingCursor{Entity: page[0].Entity, ID: page[0].ID}})
	if err != nil || len(next) != 1 || next[0].Entity != "item" {
		t.Fatalf("second page = %+v %v", next, err)
	}
	if only, _ := s.ListEntityBindings(one, store.BindingFilter{Entity: "item"}); len(only) != 1 {
		t.Fatalf("entity filter = %+v", only)
	}
	if only, _ := s.ListEntityBindings(one, store.BindingFilter{ConnectionProfileID: profile.ID, Status: "disabled"}); len(only) != 0 {
		t.Fatalf("status filter = %+v", only)
	}
	if none, err := s.ListEntityBindings(one, store.BindingFilter{ConnectionProfileID: "nope"}); err != nil || len(none) != 0 {
		t.Fatalf("bad profile filter = %+v %v", none, err)
	}

	up, err := s.UpdateEntityBinding(one, saved.ID, 1, func(b *store.EntityBinding) error {
		b.Source = map[string]any{"query": "select id, upd from t where upd > :watermark"}
		b.KeyFields = []string{"id", "company"}
		b.Status = domain.BindingDisabled
		return nil
	})
	if err != nil || up.Version != 2 || up.Status != "disabled" || up.Source["query"] == nil || up.Source["table"] != nil || len(up.KeyFields) != 2 || up.Entity != "customer" {
		t.Fatalf("update = %+v %v", up, err)
	}
	_, err = s.UpdateEntityBinding(one, saved.ID, 1, func(*store.EntityBinding) error { return nil })
	problemStatus(t, err, 409, store.CodeVersionConflict)
	_, err = s.UpdateEntityBinding(one, saved.ID, 2, func(b *store.EntityBinding) error { b.MappingSpecID = ids.New(); return nil })
	problemStatus(t, err, 422, "MIG-SPEC-UNKNOWN")

	if tenantAware {
		if _, err := s.GetEntityBinding(two, saved.ID); err == nil {
			t.Fatal("another tenant read the binding")
		}
		if list, _ := s.ListEntityBindings(two, store.BindingFilter{}); len(list) != 0 {
			t.Fatalf("another tenant lists %d bindings", len(list))
		}
		if _, _, err := s.SaveEntityBinding(two, custBinding(profile.ID, "customer"), ""); err == nil {
			t.Fatal("another tenant bound to this tenant's profile")
		}
	}
}

func TestMem_BindingContract(t *testing.T) {
	exerciseBindings(t, store.NewMem(), principalContext(1), principalContext(2), false)
}

func TestPostgres_EntityBindingsUnderRLS(t *testing.T) {
	admin, pool := testkit.DBWithApp(t) // the app role proves the 0009 grants are enough for the store
	drop := func() { _, _ = admin.Exec(context.Background(), "drop schema if exists migration cascade") }
	drop()
	t.Cleanup(drop)
	if err := dbx.Migrate(context.Background(), admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	exerciseBindings(t, store.New(pool), principalContext(1), principalContext(2), true)

	// A binding with a mapping spec reads it back, and the app role cannot delete (bindings are disabled, not removed).
	s := store.New(pool)
	one := principalContext(1)
	spec, _, err := s.SaveMappingSpec(one, sampleSpec(1, "c1"), "")
	if err != nil {
		t.Fatal(err)
	}
	profile, err := s.GetConnectionProfile(one, mustID(t, s, one, "Acme ERP"))
	if err != nil {
		t.Fatal(err)
	}
	b := custBinding(profile.ID, "party")
	b.MappingSpecID = spec.ID
	saved, _, err := s.SaveEntityBinding(one, b, "")
	if err != nil || saved.MappingSpecID != spec.ID {
		t.Fatalf("binding with spec: %+v %v", saved, err)
	}
	if _, err := pool.Exec(context.Background(), `delete from migration.entity_binding`); err == nil {
		t.Fatal("the app role can delete bindings")
	}
}
