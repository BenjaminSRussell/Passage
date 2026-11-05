package testkit

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BenjaminSRussell/passage/platform/ids"
)

const insufficientPrivilege = "42501"

// ServiceTokenReplayTable is exempt from the FORCE check: RLS is enabled but not
// forced so its SECURITY DEFINER purge can delete every tenant's expired ids
// (allowlisted in tools/rlscheck).
const ServiceTokenReplayTable = "service_token_replay"

// AssertTenantRLS fails the test for every table in the current schema with a
// tenant_id column that lacks ENABLE and FORCE row-level security, a permissive
// tenant policy with USING and WITH CHECK, or (when it carries legal_entity_id)
// a restrictive legal-entity policy.
func AssertTenantRLS(t *testing.T, admin *pgxpool.Pool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	rows, err := admin.Query(ctx, `
select c.relname, c.relrowsecurity, c.relforcerowsecurity,
  exists (select 1 from pg_policies p where p.schemaname = n.nspname and p.tablename = c.relname and p.permissive = 'PERMISSIVE'
          and p.qual like '%passage.tenant_id%' and p.with_check like '%passage.tenant_id%'),
  exists (select 1 from pg_attribute a where a.attrelid = c.oid and a.attname = 'legal_entity_id' and not a.attisdropped),
  exists (select 1 from pg_policies p where p.schemaname = n.nspname and p.tablename = c.relname and p.permissive = 'RESTRICTIVE'
          and p.qual like '%passage.legal_entity_scopes%')
from pg_class c
join pg_namespace n on n.oid = c.relnamespace
where n.nspname = current_schema() and c.relkind in ('r', 'p')
  and exists (select 1 from pg_attribute a where a.attrelid = c.oid and a.attname = 'tenant_id' and not a.attisdropped)
order by c.relname`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	tables := 0
	for rows.Next() {
		var name string
		var enabled, forced, tenantPolicy, hasEntity, entityPolicy bool
		if err := rows.Scan(&name, &enabled, &forced, &tenantPolicy, &hasEntity, &entityPolicy); err != nil {
			t.Fatal(err)
		}
		tables++
		if !enabled || (!forced && name != ServiceTokenReplayTable) {
			t.Errorf("%s: row-level security enabled=%v forced=%v", name, enabled, forced)
		}
		if !tenantPolicy {
			t.Errorf("%s: no permissive tenant policy with USING and WITH CHECK", name)
		}
		if hasEntity && !entityPolicy {
			t.Errorf("%s: carries legal_entity_id but no restrictive legal-entity policy", name)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if tables == 0 {
		t.Fatal("no tenant tables found; migrations not applied")
	}
}

// RLSCase is one table's cross-tenant probe. Insert takes the arguments
// returned by Args, after the optional Before statement that creates a parent
// row; Args receives the owning tenant and legal entity.
type RLSCase struct {
	Table       string
	Before      string
	Insert      string
	Args        func(tenantID, legalEntityID string) []any
	LegalEntity bool
	NoUpdate    bool
	Delete      bool
}

func (c RLSCase) insert(tx pgx.Tx, args []any) error {
	if c.Before != "" {
		if _, err := tx.Exec(context.Background(), c.Before, args...); err != nil {
			return err
		}
	}
	_, err := tx.Exec(context.Background(), c.Insert, args...)
	return err
}

// OutboxCase probes the outbox table every service carries; the application
// role may only insert and read it.
func OutboxCase() RLSCase {
	return RLSCase{
		Table:    "outbox",
		Insert:   `insert into outbox (id, tenant_id, aggregate_type, aggregate_id, type, payload) values ($1, $2, 'probe', $3, 'probe.created', '{}')`,
		Args:     func(tenantID, _ string) []any { return []any{ids.New(), tenantID, ids.New()} },
		NoUpdate: true,
	}
}

// IdempotencyCase probes the idempotency_keys table every command service carries.
func IdempotencyCase() RLSCase {
	return RLSCase{
		Table:    "idempotency_keys",
		Insert:   `insert into idempotency_keys (tenant_id, key, request_hash, response_status) values ($1, $2, 'hash', 200)`,
		Args:     func(tenantID, _ string) []any { return []any{tenantID, ids.New()} },
		NoUpdate: true,
	}
}

// CrossTenant runs each case as the application role: tenant A inserts a row,
// then tenant B cannot read it, update it, delete it, or insert a row owned by tenant A.
// For LegalEntity cases, a same-tenant principal scoped to another legal entity
// is held to the same checks.
func CrossTenant(t *testing.T, app *pgxpool.Pool, cases []RLSCase) {
	t.Helper()
	tenantA, tenantB := ids.New(), ids.New()
	entityA, entityB := ids.New(), ids.New()
	for _, c := range cases {
		t.Run(c.Table, func(t *testing.T) {
			args := c.Args(tenantA, entityA)
			if err := inScope(app, tenantA, "*", func(tx pgx.Tx) error {
				return c.insert(tx, args)
			}); err != nil {
				t.Fatalf("tenant A insert: %v", err)
			}
			if got := visibleRows(t, app, c.Table, tenantA, "*"); got != 1 {
				t.Fatalf("tenant A sees %d rows, want 1", got)
			}
			if !c.NoUpdate {
				if got := updatedRows(t, app, c.Table, tenantA, "*"); got != 1 {
					t.Errorf("tenant A updated %d rows, want 1", got)
				}
			}
			if got := visibleRows(t, app, c.Table, tenantB, "*"); got != 0 {
				t.Errorf("tenant B reads %d rows of tenant A", got)
			}
			if !c.NoUpdate {
				if got := updatedRows(t, app, c.Table, tenantB, "*"); got != 0 {
					t.Errorf("tenant B updated %d rows of tenant A", got)
				}
			}
			err := inScope(app, tenantB, "*", func(tx pgx.Tx) error {
				return c.insert(tx, c.Args(tenantA, entityA))
			})
			if !isRLSViolation(err) {
				t.Errorf("tenant B insert of tenant A row: %v, want row-level security violation", err)
			}
			if !c.LegalEntity {
				return
			}
			if got := visibleRows(t, app, c.Table, tenantA, entityA); got != 1 {
				t.Errorf("own legal entity sees %d rows, want 1", got)
			}
			if got := visibleRows(t, app, c.Table, tenantA, entityB); got != 0 {
				t.Errorf("other legal entity reads %d rows", got)
			}
			if !c.NoUpdate {
				if got := updatedRows(t, app, c.Table, tenantA, entityB); got != 0 {
					t.Errorf("other legal entity updated %d rows", got)
				}
			}
			err = inScope(app, tenantA, entityB, func(tx pgx.Tx) error {
				return c.insert(tx, c.Args(tenantA, entityA))
			})
			if !isRLSViolation(err) {
				t.Errorf("other legal entity insert: %v, want row-level security violation", err)
			}
		})
	}
}

func inScope(app *pgxpool.Pool, tenantID, scopes string, fn func(tx pgx.Tx) error) error {
	ctx := context.Background()
	tx, err := app.Begin(ctx)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `select set_config('passage.tenant_id', $1, true), set_config('passage.legal_entity_scopes', $2, true)`, tenantID, scopes); err != nil {
		_ = tx.Rollback(ctx)
		return err
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback(ctx)
		return err
	}
	return tx.Commit(ctx)
}

func visibleRows(t *testing.T, app *pgxpool.Pool, table, tenantID, scopes string) int {
	t.Helper()
	var n int
	err := inScope(app, tenantID, scopes, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(), fmt.Sprintf("select count(*) from %s", table)).Scan(&n)
	})
	if err != nil {
		t.Fatalf("select %s as app role: %v", table, err)
	}
	return n
}

func updatedRows(t *testing.T, app *pgxpool.Pool, table, tenantID, scopes string) int {
	t.Helper()
	var n int64
	err := inScope(app, tenantID, scopes, func(tx pgx.Tx) error {
		tag, err := tx.Exec(context.Background(), fmt.Sprintf("update %[1]s set tenant_id = tenant_id", table))
		n = tag.RowsAffected()
		return err
	})
	if err != nil {
		t.Fatalf("update %s as app role: %v", table, err)
	}
	return int(n)
}

func isRLSViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == insufficientPrivilege && strings.Contains(pgErr.Message, "row-level security")
}
