package registry_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/events"
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

type outboxRow struct {
	AggregateType string
	AggregateID   string
	Event         events.Event
	Data          registry.EntityPublishedData
}

func outboxRows(ctx context.Context, t *testing.T, app *pgxpool.Pool, tenantID string) []outboxRow {
	t.Helper()
	var rows []outboxRow
	err := dbx.WithTenantTx(ctx, app, func(tx pgx.Tx) error {
		result, err := tx.Query(ctx, `select aggregate_type, aggregate_id, payload from outbox
			where tenant_id=$1 and type=$2 order by (payload->'data'->>'version')::int`, tenantID, registry.EntityPublished)
		if err != nil {
			return err
		}
		defer result.Close()
		for result.Next() {
			var row outboxRow
			var payload []byte
			if err := result.Scan(&row.AggregateType, &row.AggregateID, &payload); err != nil {
				return err
			}
			if err := json.Unmarshal(payload, &row.Event); err != nil {
				return err
			}
			if err := json.Unmarshal(row.Event.Data, &row.Data); err != nil {
				return err
			}
			rows = append(rows, row)
		}
		return result.Err()
	})
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

func TestOutboxReadWithoutTenantReturnsNoRows(t *testing.T) {
	admin, app := testkit.DBWithApp(t)
	tenantID := testkit.TenantID(3)
	ctx := testkit.Ctx(tenantID)
	if err := dbx.Migrate(ctx, admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.NewPostgres(app).Publish(ctx, tenantID, baseEntity()); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := app.QueryRow(context.Background(), `select count(*) from outbox`).Scan(&n); err != nil {
		t.Fatalf("select without tenant: %v", err)
	}
	if n != 0 {
		t.Fatalf("rows visible without tenant = %d", n)
	}
}

func TestPostgresPublishAndExtendWriteOutboxEvents(t *testing.T) {
	admin, app := testkit.DBWithApp(t)
	tenantID := testkit.TenantID(2)
	ctx := testkit.Ctx(tenantID)
	if err := dbx.Migrate(ctx, admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	store := registry.NewPostgres(app)
	if _, err := store.Publish(ctx, tenantID, baseEntity()); err != nil {
		t.Fatal(err)
	}
	rows := outboxRows(ctx, t, app, tenantID)
	if len(rows) != 1 {
		t.Fatalf("rows after publish = %d", len(rows))
	}
	first := rows[0]
	wantFirst := registry.EntityPublishedData{
		Entity: "Vendor", Domain: "vendor", Version: 1,
		DiffSummary: registry.DiffSummary{Added: 2},
	}
	if first.Data != wantFirst || first.AggregateType != "entity" || first.AggregateID != "Vendor" ||
		first.Event.TenantID != tenantID || first.Event.SpecVersion != "1.0" || first.Event.Source != "metadata" ||
		first.Event.Type != registry.EntityPublished || first.Event.Subject != "Vendor" {
		t.Fatalf("first row = %#v", first)
	}

	extension := domain.Extension{Name: "customer.vendor-note", Entity: "Vendor", Fields: []domain.Field{{Name: "note", Type: "text"}}}
	if _, err := store.Extend(ctx, tenantID, extension); err != nil {
		t.Fatal(err)
	}
	rows = outboxRows(ctx, t, app, tenantID)
	if len(rows) != 2 {
		t.Fatalf("rows after extend = %d", len(rows))
	}
	wantSecond := registry.EntityPublishedData{
		Entity: "Vendor", Domain: "vendor", Version: 2,
		DiffSummary: registry.DiffSummary{Added: 1},
	}
	if rows[1].Data != wantSecond || rows[1].AggregateID != "Vendor" || rows[1].Event.ID == first.Event.ID {
		t.Fatalf("second row = %#v", rows[1])
	}

	if _, err := store.Extend(ctx, tenantID, extension); err == nil {
		t.Fatal("duplicate extension name must fail")
	}
	if got := len(outboxRows(ctx, t, app, tenantID)); got != 2 {
		t.Fatalf("failed extend left %d rows, want 2", got)
	}
}
