package migrations_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/metadata/migrations"
)

func TestUsageDailyLegalEntityScope(t *testing.T) {
	admin, app := testkit.DBWithApp(t)
	ctx := context.Background()
	if err := dbx.Migrate(ctx, admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	tenantA, tenantB := testkit.TenantID(1), testkit.TenantID(2)
	leA, leB := ids.New(), ids.New()

	as := func(tenantID, scopes, sql string, args ...any) (int64, error) {
		tx, err := app.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback(ctx) }()
		if _, err := tx.Exec(ctx, `select set_config('passage.tenant_id', $1, true), set_config('passage.legal_entity_scopes', $2, true)`, tenantID, scopes); err != nil {
			t.Fatal(err)
		}
		tag, err := tx.Exec(ctx, sql, args...)
		if err != nil {
			return 0, err
		}
		return tag.RowsAffected(), tx.Commit(ctx)
	}
	insert := `insert into metadata_usage_daily (tenant_id, day, legal_entity_id, role) values ($1, '2026-10-24', $2, 'buyer')`
	for _, seed := range []struct{ tenantID, le string }{{tenantA, leA}, {tenantA, leB}, {tenantA, ""}, {tenantB, leA}} {
		if _, err := as(seed.tenantID, "*", insert, seed.tenantID, seed.le); err != nil {
			t.Fatalf("seed %v: %v", seed, err)
		}
	}
	count := func(tenantID, scopes string) int {
		var n int
		tx, err := app.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback(ctx) }()
		if _, err := tx.Exec(ctx, `select set_config('passage.tenant_id', $1, true), set_config('passage.legal_entity_scopes', $2, true)`, tenantID, scopes); err != nil {
			t.Fatal(err)
		}
		if err := tx.QueryRow(ctx, `select count(*) from metadata_usage_daily`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	for _, c := range []struct {
		name, tenantID, scopes string
		want                   int
	}{
		{"all-scope sees tenant-level and every entity", tenantA, "*", 3},
		{"scoped caller sees only its entity", tenantA, leA, 1},
		{"scoped caller never sees tenant-level rows", tenantA, leB, 1},
		{"unrelated entity sees nothing", tenantA, ids.New(), 0},
		{"empty scope sees nothing", tenantA, "", 0},
		{"other tenant isolated", tenantB, "*", 1},
		{"other tenant cannot reach first tenant's entity", tenantB, leB, 0},
	} {
		if got := count(c.tenantID, c.scopes); got != c.want {
			t.Errorf("%s: %d rows, want %d", c.name, got, c.want)
		}
	}

	if n, err := as(tenantA, leB, `update metadata_usage_daily set api_requests = 1`); err != nil || n != 1 {
		t.Errorf("scoped update touched %d rows err=%v, want 1", n, err)
	}
	if n, err := as(tenantA, leB, `delete from metadata_usage_daily`); err != nil || n != 1 {
		t.Errorf("scoped delete removed %d rows err=%v, want 1", n, err)
	}
	if _, err := as(tenantA, leB, insert, tenantA, leA); !rlsViolation(err) {
		t.Errorf("insert for another legal entity: %v, want row-level security violation", err)
	}
	if _, err := as(tenantA, leB, insert, tenantA, ""); !rlsViolation(err) {
		t.Errorf("scoped insert of tenant-level row: %v, want row-level security violation", err)
	}
	if _, err := as(tenantA, "*", insert, tenantA, "not-a-uuid"); err == nil {
		t.Error("insert with a non-uuid legal entity succeeded")
	}
	if _, err := as(tenantB, "*", insert, tenantA, leA); !rlsViolation(err) {
		t.Errorf("cross-tenant insert: %v, want row-level security violation", err)
	}
}

func rlsViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "42501" && strings.Contains(pgErr.Message, "row-level security")
}
