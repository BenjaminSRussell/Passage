package store_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/migration/internal/domain"
	"github.com/BenjaminSRussell/passage/services/migration/internal/store"
	"github.com/BenjaminSRussell/passage/services/migration/migrations"
)

const projectID = "00000000-0000-7000-8000-0000000000aa"

func samplePackage() store.ConfigPackage {
	return store.ConfigPackage{
		Version: "1.2.0", SchemaVersion: 1, Signature: []byte{1, 2, 3},
		Manifest: domain.Manifest{Files: []domain.ManifestFile{{Name: "uom.json", SHA256: "abc", DependsOn: []string{}}}},
	}
}

// exercise runs against both the Postgres store and Mem so the fake keeps matching the contract.
func exercise(t *testing.T, s store.Store, one, two context.Context, tenantAware bool) {
	saved, created, err := s.SaveConfigPackage(one, samplePackage(), "key-1")
	if err != nil || !created || saved.ID == "" || saved.Version != "1.2.0" || len(saved.Applied) != 0 {
		t.Fatalf("save = %+v, %v, %v", saved, created, err)
	}
	again, created, err := s.SaveConfigPackage(one, samplePackage(), "key-1")
	if err != nil || created || again.ID != saved.ID {
		t.Fatalf("same key must return the stored package: %+v %v %v", again, created, err)
	}
	other := samplePackage()
	other.Signature = []byte{9}
	if _, _, err := s.SaveConfigPackage(one, other, "key-1"); err == nil {
		t.Fatal("key reuse with another package must fail")
	} else if p := new(httpx.Problem); !errors.As(err, &p) || p.Status != 422 {
		t.Fatalf("key reuse = %v", err)
	}
	for i, counts := range []store.Counts{{Create: 3, Update: 1, Unchanged: 0}, {Unchanged: 4}} {
		if _, err := s.AppendPackageApplied(one, saved.ID, store.PackageApplied{AppliedAt: time.Now().UTC(), Key: "k", DiffCounts: counts}); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}
	got, err := s.GetConfigPackage(one, saved.ID)
	if err != nil || len(got.Applied) != 2 || got.Applied[0].DiffCounts.Create != 3 || got.Applied[1].DiffCounts.Unchanged != 4 || string(got.Signature) != "\x01\x02\x03" ||
		len(got.Manifest.Files) != 1 || got.Manifest.Files[0].SHA256 != "abc" {
		t.Fatalf("round trip = %+v, %v", got, err)
	}
	if byKey, ok, err := s.ConfigPackageByKey(one, "key-1"); err != nil || !ok || byKey.ID != saved.ID {
		t.Fatalf("by key = %+v %v %v", byKey, ok, err)
	}
	if list, err := s.ListConfigPackages(one, 0); err != nil || len(list) != 1 {
		t.Fatalf("list = %+v %v", list, err)
	}
	rules := []domain.KeyRewrite{{Field: domain.KeyField, From: "USMF", To: "CAMF"}, {Field: "dataAreaId", From: "USMF", To: "CAMF"}}
	if err := s.SaveRewriteRules(one, projectID, rules); err != nil {
		t.Fatal(err)
	}
	if got, err := s.RewriteRules(one, projectID); err != nil || len(got) != 2 || got[0] != rules[0] || got[1] != rules[1] {
		t.Fatalf("rules = %+v %v", got, err)
	}
	if _, err := s.GetConfigPackage(one, "not-a-uuid"); err == nil {
		t.Fatal("bad id must be not found")
	}
	if !tenantAware { // Mem keeps one tenant's state
		return
	}
	if _, err := s.GetConfigPackage(two, saved.ID); err == nil {
		t.Fatal("another tenant must not read the package")
	}
	if _, err := s.AppendPackageApplied(two, saved.ID, store.PackageApplied{}); err == nil {
		t.Fatal("another tenant must not append to the package")
	}
	if list, _ := s.ListConfigPackages(two, 0); len(list) != 0 {
		t.Fatalf("tenant 2 lists %d packages", len(list))
	}
	if got, err := s.RewriteRules(two, projectID); err != nil || len(got) != 0 {
		t.Fatalf("tenant 2 reads %d rules, %v", len(got), err)
	}
	if _, ok, _ := s.ConfigPackageByKey(two, "key-1"); ok {
		t.Fatal("another tenant must not find the key")
	}
}

func TestPostgres_ConfigPackageRoundTripAppendedAppliedAndRewriteRulesUnderRLS(t *testing.T) {
	admin, pool := testkit.DBWithApp(t) // the app role proves the 0003 grants are enough for the store
	drop := func() { _, _ = admin.Exec(context.Background(), "drop schema if exists migration cascade") }
	drop()
	t.Cleanup(drop)
	if err := dbx.Migrate(context.Background(), admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	exercise(t, store.New(pool), principalContext(1), principalContext(2), true)
}

func TestMem_ConfigPackageContract(t *testing.T) {
	exercise(t, store.NewMem(), principalContext(1), principalContext(2), false)
}
