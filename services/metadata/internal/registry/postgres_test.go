package registry_test

import (
	"testing"

	"github.com/jackc/pgx/v5"

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

func TestPostgresPublishWritesOutboxEvent(t *testing.T) {
	pool := testkit.DB(t)
	if err := dbx.Migrate(testkit.Ctx(testkit.TenantID(2)), pool, migrations.FS); err != nil {
		t.Fatal(err)
	}
	store := registry.NewPostgres(pool)
	ctx := testkit.Ctx(testkit.TenantID(2))
	entity := baseEntity()
	if _, err := store.Publish(ctx, testkit.TenantID(2), entity); err != nil {
		t.Fatal(err)
	}
	var count int
	var version string
	err := dbx.WithTenantTx(ctx, pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `select count(*), coalesce(max(payload->'data'->>'version'), '')
			from outbox where tenant_id=$1 and type=$2 and aggregate_id=$3`,
			testkit.TenantID(2), registry.EntityPublished, entity.Name).Scan(&count, &version)
	})
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 || version != "1" {
		t.Fatalf("count=%d version=%q", count, version)
	}
}
