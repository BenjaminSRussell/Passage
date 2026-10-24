package registry_test

import (
	"testing"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/metadata/internal/domain"
	"github.com/BenjaminSRussell/passage/services/metadata/internal/registry"
	"github.com/BenjaminSRussell/passage/services/metadata/migrations"
)

func TestPostgresRegistryPublishesExtensionAndRecords(t *testing.T) {
	pool := testkit.DB(t)
	if err := dbx.Migrate(testkit.Ctx(testkit.TenantID(1)), pool, migrations.FS); err != nil {
		t.Fatal(err)
	}
	store := registry.NewPostgres(pool)
	ctx := testkit.Ctx(testkit.TenantID(1))
	if _, err := store.Publish(ctx, testkit.TenantID(1), baseEntity()); err != nil {
		t.Fatal(err)
	}
	extension := domain.Extension{
		Name: "customer.vendor-note", Entity: "Vendor",
		Fields: []domain.Field{{Name: "note", Type: "text"}},
	}
	version, err := store.Extend(ctx, testkit.TenantID(1), extension)
	if err != nil {
		t.Fatal(err)
	}
	if version.Number != 2 {
		t.Fatalf("version = %d", version.Number)
	}
	record, err := store.CreateRecord(ctx, testkit.TenantID(1), "Vendor", map[string]any{"code": "V-1", "note": "Preferred"})
	if err != nil {
		t.Fatal(err)
	}
	if record["note"] != "Preferred" {
		t.Fatalf("record = %#v", record)
	}
	rows, err := store.ListRecords(ctx, testkit.TenantID(1), "Vendor", 100)
	if err != nil || len(rows) != 1 {
		t.Fatalf("rows=%#v err=%v", rows, err)
	}
}
