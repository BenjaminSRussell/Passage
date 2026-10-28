package store

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/billing/migrations"
)

func TestOutbox_InsertOnlyAndTenantScoped(t *testing.T) {
	admin, app := testkit.DBWithApp(t)
	ctx := context.Background()
	if err := dbx.Migrate(ctx, admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	const insert = `insert into outbox(id, tenant_id, aggregate_type, aggregate_id, type, payload) values($1, $2, 'invoice', 'a', 't', '{}')`
	if _, err := admin.Exec(ctx, insert, "o-other", testkit.TenantID(2)); err != nil {
		t.Fatal(err)
	}
	mine := ctxAllEntities(testkit.TenantID(1))
	rollback := errors.New("rollback")
	err := dbx.WithTenantTx(mine, app, func(tx pgx.Tx) error {
		if _, err := tx.Exec(mine, insert, "o-mine", testkit.TenantID(1)); err != nil {
			t.Errorf("own-tenant outbox insert: %v", err)
		}
		return rollback
	})
	if !errors.Is(err, rollback) {
		t.Fatalf("own-tenant tx error = %v, want rollback", err)
	}
	err = dbx.WithTenantTx(mine, app, func(tx pgx.Tx) error {
		_, err := tx.Exec(mine, insert, "o-cross", testkit.TenantID(2))
		return err
	})
	requireInsufficientPrivilege(t, err, "cross-tenant outbox insert must violate the policy")
	err = dbx.WithTenantTx(mine, app, func(tx pgx.Tx) error {
		var n int
		return tx.QueryRow(mine, `select count(*) from outbox`).Scan(&n)
	})
	requireInsufficientPrivilege(t, err, "passage_app must not be able to read the outbox")
}

func requireInsufficientPrivilege(t *testing.T, err error, msg string) {
	t.Helper()
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "42501" {
		t.Fatalf("%s: got %v, want SQLSTATE 42501", msg, err)
	}
}
