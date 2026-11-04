package store_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/services/migration/internal/store"
)

func exerciseSettings(t *testing.T, s store.Store, one, two context.Context, tenantAware bool) {
	got, err := s.GetTenantSettings(one)
	if err != nil || got.RecurringMinIntervalMin != 15 || got.Version != 0 || got.UpdatedAt != nil {
		t.Fatalf("defaults = %+v %v", got, err)
	}
	saved, err := s.PutTenantSettings(one, 0, 90)
	if err != nil || saved.RecurringMinIntervalMin != 90 || saved.Version != 1 || saved.UpdatedAt == nil || saved.UpdatedBy == "" {
		t.Fatalf("create = %+v %v", saved, err)
	}
	// Version 0 is only for a tenant without a row; version 1 again would overwrite a newer write.
	_, err = s.PutTenantSettings(one, 0, 30)
	problemStatus(t, err, 409, store.CodeVersionConflict)
	next, err := s.PutTenantSettings(one, 1, 1440)
	if err != nil || next.Version != 2 || next.RecurringMinIntervalMin != 1440 {
		t.Fatalf("update = %+v %v", next, err)
	}
	_, err = s.PutTenantSettings(one, 1, 60)
	problemStatus(t, err, 409, store.CodeVersionConflict)
	if cur, _ := s.GetTenantSettings(one); cur.RecurringMinIntervalMin != 1440 || cur.Version != 2 {
		t.Fatalf("stale write changed the row: %+v", cur)
	}
	if tenantAware {
		if other, err := s.GetTenantSettings(two); err != nil || other.Version != 0 || other.RecurringMinIntervalMin != 15 {
			t.Fatalf("tenant 2 sees tenant 1's settings: %+v %v", other, err)
		}
		_, err = s.PutTenantSettings(two, 1, 30)
		problemStatus(t, err, 409, store.CodeVersionConflict)
		if _, err := s.PutTenantSettings(two, 0, 30); err != nil {
			t.Fatalf("tenant 2 create: %v", err)
		}
	}
}

func TestMem_SettingsContract(t *testing.T) {
	exerciseSettings(t, store.NewMem(), adminContext(1), adminContext(2), false)
}

func TestPostgres_SettingsUnderRLS(t *testing.T) {
	_, pool := outboundDB(t) // the app role proves the 0020 grants are enough
	exerciseSettings(t, store.New(pool), adminContext(1), adminContext(2), true)

	var visible int
	err := dbx.WithTenantTx(principalContext(3), pool, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(), `select count(*) from migration.tenant_setting`).Scan(&visible)
	})
	if err != nil || visible != 0 {
		t.Fatalf("tenant 3 sees %d settings rows: %v", visible, err)
	}
}

func exerciseSpecScope(t *testing.T, s store.Store, one, two context.Context, tenantAware bool) {
	pa, _, err := s.SaveProject(one, store.Project{Name: "AX A", Type: "migrate", SourceSystem: "ax2012", Status: "active"}, "")
	if err != nil {
		t.Fatal(err)
	}
	pb, _, err := s.SaveProject(one, store.Project{Name: "AX B", Type: "migrate", SourceSystem: "ax2012", Status: "active"}, "")
	if err != nil {
		t.Fatal(err)
	}
	in := func(project, checksum string) store.MappingSpec {
		sp := sampleSpec(1, checksum)
		sp.ProjectID, sp.NaturalKey = project, []string{"account"}
		sp.Spec = json.RawMessage(`{"entity":"customer"}`)
		return sp
	}
	wide, created, err := s.SaveMappingSpec(one, sampleSpec(1, "c1"), "")
	if err != nil || !created || wide.ProjectID != "" || wide.NaturalKey == nil || len(wide.NaturalKey) != 0 {
		t.Fatalf("tenant-wide = %+v %v %v", wide, created, err)
	}
	a, created, err := s.SaveMappingSpec(one, in(pa.ID, "ca"), "ka")
	if err != nil || !created || a.ID == wide.ID || a.ProjectID != pa.ID || len(a.NaturalKey) != 1 || a.NaturalKey[0] != "account" {
		t.Fatalf("project A = %+v %v %v", a, created, err)
	}
	b, created, err := s.SaveMappingSpec(one, in(pb.ID, "ca"), "")
	if err != nil || !created || b.ID == a.ID {
		t.Fatalf("the same entity and version in project B = %+v %v %v", b, created, err)
	}
	if again, created, err := s.SaveMappingSpec(one, in(pa.ID, "ca"), ""); err != nil || created || again.ID != a.ID {
		t.Fatalf("replay in a scope = %+v %v %v", again, created, err)
	}
	_, _, err = s.SaveMappingSpec(one, in(pa.ID, "different"), "")
	problemStatus(t, err, 409, store.CodeSpecVersionUsed)
	// A key used for project A cannot replay the same content for project B.
	_, _, err = s.SaveMappingSpec(one, in(pb.ID, "ca"), "ka")
	problemStatus(t, err, 422, store.CodeSpecKeyReuse)

	ids := func(scope store.SpecScope, limit int, after *store.SpecCursor) []string {
		list, err := s.ListMappingSpecsIn(one, scope, "", after, limit)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, x := range list {
			out = append(out, x.ID)
		}
		return out
	}
	if got := ids(store.SpecScope{ProjectID: pa.ID}, 10, nil); len(got) != 1 || got[0] != a.ID {
		t.Fatalf("project A = %v", got)
	}
	if got := ids(store.SpecScope{TenantWide: true}, 10, nil); len(got) != 1 || got[0] != wide.ID {
		t.Fatalf("tenant-wide = %v", got)
	}
	if got := ids(store.SpecScope{ProjectID: "not-a-uuid"}, 10, nil); len(got) != 0 {
		t.Fatalf("malformed project = %v", got)
	}
	all := ids(store.SpecScope{}, 10, nil)
	if len(all) != 3 {
		t.Fatalf("all = %v", all)
	}
	// Keyset paging walks specs that share entity and version, one per page, without a gap or a repeat.
	var walked []string
	var cursor *store.SpecCursor
	for range 4 {
		list, err := s.ListMappingSpecsIn(one, store.SpecScope{}, "", cursor, 1)
		if err != nil || len(list) > 1 {
			t.Fatalf("page = %v %v", list, err)
		}
		if len(list) == 0 {
			break
		}
		walked = append(walked, list[0].ID)
		cursor = &store.SpecCursor{Entity: list[0].Entity, Version: list[0].Version, ID: list[0].ID}
	}
	if len(walked) != 3 || walked[0] != all[0] || walked[1] != all[1] || walked[2] != all[2] {
		t.Fatalf("walked %v, want %v", walked, all)
	}
	if tenantAware {
		if l, _ := s.ListMappingSpecsIn(two, store.SpecScope{ProjectID: pa.ID}, "", nil, 10); len(l) != 0 {
			t.Fatalf("tenant 2 lists tenant 1's specs: %v", l)
		}
		// A project of another tenant is not a valid scope for a spec.
		if _, _, err := s.SaveMappingSpec(two, in(pa.ID, "cx"), ""); err == nil {
			t.Fatal("tenant 2 published a spec into tenant 1's project")
		}
	}
}

func TestMem_SpecScopeContract(t *testing.T) {
	exerciseSpecScope(t, store.NewMem(), adminContext(1), adminContext(2), false)
}

func TestPostgres_SpecScopeUnderRLS(t *testing.T) {
	_, pool := outboundDB(t) // proves 0021 (column, composite foreign key, scoped uniqueness) under the app role
	exerciseSpecScope(t, store.New(pool), adminContext(1), adminContext(2), true)
}
