package store_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/audit/internal/domain"
	"github.com/BenjaminSRussell/passage/services/audit/internal/store"
	"github.com/BenjaminSRussell/passage/services/audit/migrations"
)

func TestGrants_AppRoleAppendsButCannotRewrite(t *testing.T) {
	admin, app := testkit.DBWithApp(t)
	ctx := tenant.With(context.Background(), principal(1))
	if err := dbx.Migrate(ctx, admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	s := store.New(app)
	if _, err := s.CreateRule(ctx, store.AlertRule{Entity: "party.vendor", Field: "name", Kind: domain.AlertChange}); err != nil {
		t.Fatal(err)
	}
	change := domain.Change{Entity: "party.vendor", RecordID: "v-1", Field: "name", OldValue: "Acme", NewValue: "Acme Ltd",
		UserID: "user-1", TraceID: "trace-abc", OccurredAt: time.Now().UTC()}
	if err := s.Apply(ctx, change); err != nil {
		t.Fatalf("apply as passage_app needs the audit_entry sequence: %v", err)
	}
	if alerts, err := s.ListAlerts(ctx); err != nil || len(alerts) != 1 {
		t.Fatalf("alerts = %+v, %v", alerts, err)
	}
	for _, stmt := range []string{
		`update audit_entry set new_value = 'tamper'`,
		`delete from audit_entry`,
		`update alert_inbox set message = message`,
		`update outbox set type = type`,
		`select count(*) from outbox`,
	} {
		err := dbx.WithTenantTx(ctx, app, func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, stmt)
			return err
		})
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "42501" {
			t.Errorf("%q = %v; want insufficient_privilege 42501", stmt, err)
		}
	}
}
