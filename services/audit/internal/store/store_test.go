package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/audit/internal/domain"
	"github.com/BenjaminSRussell/passage/services/audit/internal/store"
	"github.com/BenjaminSRussell/passage/services/audit/migrations"
)

func principal(n int) tenant.Principal {
	return tenant.Principal{TenantID: testkit.TenantID(n), UserID: "user-1"}
}

func migrated(t *testing.T) (*store.Postgres, context.Context) {
	t.Helper()
	pool := testkit.DB(t)
	ctx := tenant.With(context.Background(), principal(1))
	if err := dbx.Migrate(ctx, pool, migrations.FS); err != nil {
		t.Fatal(err)
	}
	return store.New(pool), ctx
}

func TestApplyAppearsWithinTwoSecondsWithTrace(t *testing.T) {
	s, ctx := migrated(t)
	change := domain.Change{
		Entity: "party.vendor", RecordID: "v-1", Field: "name",
		OldValue: "Acme", NewValue: "Acme Ltd", UserID: "user-1", TraceID: "trace-abc",
		OccurredAt: time.Now().UTC(),
	}
	start := time.Now()
	if err := s.Apply(ctx, change); err != nil {
		t.Fatal(err)
	}
	got, err := s.Query(ctx, "party.vendor", "v-1")
	if err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("audit visible after %s", elapsed)
	}
	if len(got) != 1 {
		t.Fatalf("got %+v", got)
	}
	rec := got[0]
	if rec.UserID != "user-1" || rec.OldValue != "Acme" || rec.NewValue != "Acme Ltd" || rec.TraceID != "trace-abc" || rec.OccurredAt.IsZero() {
		t.Fatalf("record %+v", rec)
	}
	other := tenant.With(context.Background(), principal(2))
	hidden, err := s.Query(other, "party.vendor", "v-1")
	if err != nil || len(hidden) != 0 {
		t.Fatalf("tenant 2 saw %v %v", hidden, err)
	}
	if err := s.Ping(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestAlertInboxFromChange(t *testing.T) {
	s, ctx := migrated(t)
	if _, err := s.CreateRule(ctx, store.AlertRule{Entity: "party.vendor", Field: "name", Kind: domain.AlertChange}); err != nil {
		t.Fatal(err)
	}
	change := domain.Change{
		Entity: "party.vendor", RecordID: "v-1", Field: "name",
		OldValue: "Acme", NewValue: "Acme Ltd", UserID: "user-1", TraceID: "trace-abc",
		OccurredAt: time.Now().UTC(),
	}
	if err := s.Apply(ctx, change); err != nil {
		t.Fatal(err)
	}
	alerts, err := s.ListAlerts(ctx)
	if err != nil || len(alerts) != 1 || alerts[0].RecordID != "v-1" {
		t.Fatalf("alerts %+v %v", alerts, err)
	}
}

func TestMigrationsApplyWithRLSAndImmutability(t *testing.T) {
	admin, pool := testkit.DBWithApp(t)
	ctx := tenant.With(context.Background(), principal(1))
	if err := dbx.Migrate(ctx, admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	var schema string
	if err := admin.QueryRow(ctx, "select current_schema()").Scan(&schema); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, "grant usage on schema "+schema+" to passage_app"); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, "grant select, insert, update, delete on all tables in schema "+schema+" to passage_app"); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, "grant usage, select on all sequences in schema "+schema+" to passage_app"); err != nil {
		t.Fatal(err)
	}
	var currentUser string
	var roleSuper, roleBypass bool
	metadataErr := pool.QueryRow(ctx, `select current_user, r.rolsuper, r.rolbypassrls from pg_roles r where r.rolname = current_user`).Scan(&currentUser, &roleSuper, &roleBypass)
	if metadataErr != nil || currentUser != "passage_app" || roleSuper || roleBypass {
		t.Fatalf("RLS test must use non-bypass passage_app role: user=%q super=%v bypass=%v err=%v", currentUser, roleSuper, roleBypass, metadataErr)
	}
	for _, table := range []string{"audit_entry", "alert_rule", "alert_inbox", "outbox"} {
		var enabled, forced bool
		err := pool.QueryRow(ctx, `select relrowsecurity, relforcerowsecurity from pg_class where relname = $1 and relnamespace = current_schema()::regnamespace`, table).Scan(&enabled, &forced)
		if err != nil || !enabled || !forced {
			t.Errorf("%s: rls enabled=%v forced=%v err=%v", table, enabled, forced, err)
		}
	}
	app := store.New(pool)
	change := domain.Change{
		Entity: "party.vendor", RecordID: "v-1", Field: "name",
		OldValue: "Acme", NewValue: "Acme Ltd", UserID: "user-1", TraceID: "trace-abc",
		OccurredAt: time.Now().UTC(),
	}
	if err := app.Apply(ctx, change); err != nil {
		t.Fatal(err)
	}
	if err := updateBlocked(ctx, pool); err == nil {
		t.Fatal("audit_entry must reject updates")
	}
}

func updateBlocked(ctx context.Context, pool *pgxpool.Pool) error {
	return dbx.WithTenantTx(ctx, pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `update audit_entry set new_value='tamper'`)
		return err
	})
}
