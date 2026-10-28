package store

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/treasury/migrations"
)

func TestOutbox_InsertOnlyAndTenantScoped(t *testing.T) {
	admin, app := testkit.DBWithApp(t)
	ctx := context.Background()
	if err := dbx.Migrate(ctx, admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	const insert = `insert into outbox(id, tenant_id, aggregate_type, aggregate_id, type, payload) values($1, $2, 'statement', 'a', 't', '{}')`
	if _, err := admin.Exec(ctx, insert, "o-other", testkit.TenantID(2)); err != nil {
		t.Fatal(err)
	}
	mine := testkit.Ctx(testkit.TenantID(1))
	rollback := errors.New("rollback")
	_ = dbx.WithTenantTx(mine, app, func(tx pgx.Tx) error {
		if _, err := tx.Exec(mine, insert, "o-mine", testkit.TenantID(1)); err != nil {
			t.Errorf("own-tenant outbox insert: %v", err)
		}
		return rollback
	})
	_ = dbx.WithTenantTx(mine, app, func(tx pgx.Tx) error {
		if _, err := tx.Exec(mine, insert, "o-cross", testkit.TenantID(2)); err == nil {
			t.Error("cross-tenant outbox insert must violate the policy")
		}
		return rollback
	})
	_ = dbx.WithTenantTx(mine, app, func(tx pgx.Tx) error {
		var n int
		if err := tx.QueryRow(mine, `select count(*) from outbox`).Scan(&n); err == nil {
			t.Error("passage_app must not be able to read the outbox")
		}
		return rollback
	})
}
