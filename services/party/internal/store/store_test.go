package store_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/party/internal/store"
	"github.com/BenjaminSRussell/passage/services/party/migrations"
)

const (
	legalEntityA = "00000000-0000-0000-0000-0000000000a1"
	legalEntityB = "00000000-0000-0000-0000-0000000000b1"
	rlsViolation = "42501"
	uniqueError  = "23505"
)

func principalContext(tenantNumber int, legalEntityID string) context.Context {
	return tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(tenantNumber), LegalEntityID: legalEntityID, Duties: testkit.ScopeDuties(legalEntityID)})
}

func migratedApp(t *testing.T) *pgxpool.Pool {
	t.Helper()
	admin, app := testkit.DBWithApp(t)
	ctx := principalContext(1, "")
	if err := dbx.Migrate(ctx, admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	return app
}

func insertParty(ctx context.Context, app *pgxpool.Pool, tenantID, number string) (string, error) {
	id := ids.New()
	err := dbx.WithTenantTx(ctx, app, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `insert into party (tenant_id, id, party_number, party_type, name, search_name, org_legal_name)
			values ($1, $2, $3, 'organization', 'Acme', 'acme', 'Acme')`, tenantID, id, number)
		return err
	})
	return id, err
}

func insertCustomerRole(ctx context.Context, app *pgxpool.Pool, tenantID, partyID, legalEntityID, account string) error {
	return dbx.WithTenantTx(ctx, app, func(tx pgx.Tx) error {
		profileID, termID := ids.New(), ids.New()
		if _, err := tx.Exec(ctx, `insert into posting_profile (tenant_id, id, legal_entity_id, code, name) values ($1, $2, $3, $4, 'Default')`, tenantID, profileID, legalEntityID, "PP-"+account); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `insert into payment_term (tenant_id, id, code, name, net_days) values ($1, $2, $3, 'Net 30', 30)`, tenantID, termID, "T-"+account); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `insert into customer_role (tenant_id, id, party_id, legal_entity_id, account_number, customer_group_id, payment_term_id, posting_profile_id, currency_code)
			values ($1, $2, $3, $4, $5, $6, $7, $8, 'USD')`, tenantID, ids.New(), partyID, legalEntityID, account, ids.New(), termID, profileID)
		return err
	})
}

func insertPaymentTerm(ctx context.Context, app *pgxpool.Pool, tenantID, code string) error {
	return dbx.WithTenantTx(ctx, app, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `insert into payment_term (tenant_id, id, code, name, net_days) values ($1, $2, $3, 'Net 30', 30)`, tenantID, ids.New(), code)
		return err
	})
}

func insertBareCustomerRole(ctx context.Context, app *pgxpool.Pool, tenantID, partyID, legalEntityID, account string) error {
	return dbx.WithTenantTx(ctx, app, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `insert into customer_role (tenant_id, id, party_id, legal_entity_id, account_number, customer_group_id, payment_term_id, posting_profile_id, currency_code)
			values ($1, $2, $3, $4, $5, $6, $7, $8, 'USD')`, tenantID, ids.New(), partyID, legalEntityID, account, ids.New(), ids.New(), ids.New())
		return err
	})
}

func count(ctx context.Context, t *testing.T, app *pgxpool.Pool, table string) int {
	t.Helper()
	var n int
	err := dbx.WithTenantTx(ctx, app, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, "select count(*) from "+table).Scan(&n)
	})
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func sqlState(err error) string {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code
	}
	return ""
}

func TestMigrate_AppliesTwice(t *testing.T) {
	pool := testkit.DB(t)
	ctx := principalContext(1, "")
	for i := 0; i < 2; i++ {
		if err := dbx.Migrate(ctx, pool, migrations.FS); err != nil {
			t.Fatalf("migrate run %d: %v", i+1, err)
		}
	}
	var applied int
	if err := pool.QueryRow(ctx, "select count(*) from schema_migrations").Scan(&applied); err != nil || applied != 5 {
		t.Fatalf("schema_migrations rows = %d, %v; want 5", applied, err)
	}
}

func TestMigrate_PolicyBlockIsRerunnable(t *testing.T) {
	pool := testkit.DB(t)
	ctx := principalContext(1, "")
	if err := dbx.Migrate(ctx, pool, migrations.FS); err != nil {
		t.Fatal(err)
	}
	body, err := migrations.FS.ReadFile("0001_init.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, string(body)); err != nil {
		t.Fatalf("0001_init.sql must be safe to re-run: %v", err)
	}
}

func TestPing(t *testing.T) {
	pool := testkit.DB(t)
	s := store.New(pool)
	if err := s.Ping(context.Background()); err != nil {
		t.Fatal(err)
	}
	pool.Close()
	if err := s.Ping(context.Background()); err == nil {
		t.Fatal("ping on a closed pool must fail")
	}
}

func TestRLS_TenantIsolation(t *testing.T) {
	app := migratedApp(t)
	ctxA, ctxB := principalContext(1, ""), principalContext(2, "")
	partyA, err := insertParty(ctxA, app, testkit.TenantID(1), "P-1")
	if err != nil {
		t.Fatal(err)
	}
	if err := insertCustomerRole(ctxA, app, testkit.TenantID(1), partyA, legalEntityA, "CUST-A"); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"party", "customer_role", "payment_term", "posting_profile"} {
		if got := count(ctxA, t, app, table); got != 1 {
			t.Errorf("tenant A sees %d %s rows, want 1", got, table)
		}
		if got := count(ctxB, t, app, table); got != 0 {
			t.Errorf("tenant B sees %d %s rows, want 0", got, table)
		}
	}
	if _, err := insertParty(ctxB, app, testkit.TenantID(1), "P-2"); sqlState(err) != rlsViolation {
		t.Fatalf("tenant B writing a tenant A row: got %v, want SQLSTATE %s", err, rlsViolation)
	}
	if err := insertCustomerRole(ctxB, app, testkit.TenantID(1), partyA, legalEntityA, "CUST-B"); sqlState(err) != rlsViolation {
		t.Fatalf("tenant B writing a tenant A role: got %v, want SQLSTATE %s", err, rlsViolation)
	}
	if err := insertBareCustomerRole(ctxB, app, testkit.TenantID(1), partyA, legalEntityA, "CUST-B2"); sqlState(err) != rlsViolation {
		t.Fatalf("tenant B writing a tenant A customer_role row directly: got %v, want SQLSTATE %s", err, rlsViolation)
	}
	if err := insertPaymentTerm(ctxB, app, testkit.TenantID(1), "T-B"); sqlState(err) != rlsViolation {
		t.Fatalf("tenant B writing a tenant A payment_term: got %v, want SQLSTATE %s", err, rlsViolation)
	}
}

func TestRLS_LegalEntityScope(t *testing.T) {
	app := migratedApp(t)
	all := principalContext(1, "")
	partyID, err := insertParty(all, app, testkit.TenantID(1), "P-1")
	if err != nil {
		t.Fatal(err)
	}
	for legalEntityID, account := range map[string]string{legalEntityA: "CUST-A", legalEntityB: "CUST-B"} {
		if err := insertCustomerRole(all, app, testkit.TenantID(1), partyID, legalEntityID, account); err != nil {
			t.Fatal(err)
		}
	}
	scoped := principalContext(1, legalEntityA)
	if got := count(scoped, t, app, "customer_role"); got != 1 {
		t.Fatalf("scoped session sees %d roles, want 1", got)
	}
	if got := count(scoped, t, app, "posting_profile"); got != 1 {
		t.Fatalf("scoped session sees %d posting profiles, want 1", got)
	}
	if got := count(all, t, app, "customer_role"); got != 2 {
		t.Fatalf("unscoped session sees %d roles, want 2", got)
	}
	if err := insertCustomerRole(scoped, app, testkit.TenantID(1), partyID, legalEntityB, "CUST-C"); sqlState(err) != rlsViolation {
		t.Fatalf("scoped session writing another legal entity's role: got %v, want SQLSTATE %s", err, rlsViolation)
	}
	if err := insertBareCustomerRole(scoped, app, testkit.TenantID(1), partyID, legalEntityB, "CUST-D"); sqlState(err) != rlsViolation {
		t.Fatalf("scoped session writing another legal entity's customer_role row directly: got %v, want SQLSTATE %s", err, rlsViolation)
	}
}

func TestCustomerRole_UniquePerPartyAndLegalEntity(t *testing.T) {
	app := migratedApp(t)
	ctx := principalContext(1, "")
	partyID, err := insertParty(ctx, app, testkit.TenantID(1), "P-1")
	if err != nil {
		t.Fatal(err)
	}
	if err := insertCustomerRole(ctx, app, testkit.TenantID(1), partyID, legalEntityA, "CUST-1"); err != nil {
		t.Fatal(err)
	}
	if err := insertCustomerRole(ctx, app, testkit.TenantID(1), partyID, legalEntityA, "CUST-2"); sqlState(err) != uniqueError {
		t.Fatalf("second role for the same party and legal entity: got %v, want SQLSTATE %s", err, uniqueError)
	}
}

func TestMigrate_EveryTableForcesRowLevelSecurity(t *testing.T) {
	app := migratedApp(t)
	ctx := principalContext(1, "")
	var unprotected []string
	err := dbx.WithTenantTx(ctx, app, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select c.relname from pg_class c
			where c.relnamespace = current_schema()::regnamespace and c.relkind = 'r'
			and c.relname <> 'schema_migrations' and not (c.relrowsecurity and c.relforcerowsecurity)`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var name string
			if err := rows.Scan(&name); err != nil {
				return err
			}
			unprotected = append(unprotected, name)
		}
		return rows.Err()
	})
	if err != nil || len(unprotected) > 0 {
		t.Fatalf("tables without forced RLS: %v, %v", unprotected, err)
	}
}
