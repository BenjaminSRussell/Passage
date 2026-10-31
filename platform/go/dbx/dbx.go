// Package dbx connects to Postgres, applies a service's SQL migrations, scopes
// transactions to a tenant for row-level security, and provides the DDL
// templates every service reuses.
package dbx

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BenjaminSRussell/passage/platform/tenant"
)

// Connect opens a pool. url is a libpq/pgx connection string.
func Connect(ctx context.Context, url string) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, err
	}
	cfg.MaxConns = 20
	return pgxpool.NewWithConfig(ctx, cfg)
}

// Migrate applies every *.sql file in migrations (sorted by name) that has
// not been recorded in schema_migrations. Each file runs in its own transaction.
func Migrate(ctx context.Context, pool *pgxpool.Pool, migrations fs.FS) error {
	if _, err := pool.Exec(ctx, `create table if not exists schema_migrations (name text primary key, applied_at timestamptz not null default now())`); err != nil {
		return err
	}
	entries, err := fs.ReadDir(migrations, ".")
	if err != nil {
		return err
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".sql") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	for _, name := range names {
		var exists bool
		if err := pool.QueryRow(ctx, `select exists(select 1 from schema_migrations where name=$1)`, name).Scan(&exists); err != nil {
			return err
		}
		if exists {
			continue
		}
		body, err := fs.ReadFile(migrations, name)
		if err != nil {
			return err
		}
		tx, err := pool.Begin(ctx)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, string(body)); err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("migration %s: %w", name, err)
		}
		if _, err := tx.Exec(ctx, `insert into schema_migrations(name) values($1)`, name); err != nil {
			_ = tx.Rollback(ctx)
			return err
		}
		if err := tx.Commit(ctx); err != nil {
			return err
		}
	}
	return nil
}

// LegalEntityScopes derives passage.legal_entity_scopes from the principal and
// fails closed: its UUID legal entities when set (any other entry is dropped,
// so '*' can never come from LegalEntityID), '*' only for a principal with no
// legal entity that holds tenant.DutyAllScope, otherwise the empty list.
func LegalEntityScopes(p tenant.Principal) string {
	if p.LegalEntityID != "" {
		return strings.Join(tenant.LegalEntityScopeList(p), ",")
	}
	if p.HasDuty(tenant.DutyAllScope) {
		return "*"
	}
	return ""
}

// WithTenantTx begins a transaction with passage.tenant_id and passage.legal_entity_scopes set from the context
// principal so RLS policies apply, runs fn, and commits or rolls back.
func WithTenantTx(ctx context.Context, pool *pgxpool.Pool, fn func(tx pgx.Tx) error) error {
	p, ok := tenant.FromContext(ctx)
	if !ok {
		return fmt.Errorf("dbx: no principal in context")
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `select set_config('passage.tenant_id', $1, true), set_config('passage.legal_entity_scopes', $2, true)`, p.TenantID, LegalEntityScopes(p)); err != nil {
		_ = tx.Rollback(ctx)
		return err
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback(ctx)
		return err
	}
	return tx.Commit(ctx)
}

var ErrNotTenantWide = errors.New("dbx: a tenant-wide transaction needs a principal with no legal-entity claim, the tenant.administer duty and the named duty")

// TenantWide reports whether p may run a transaction spanning every legal entity of its tenant:
// no legal-entity claim AND tenant.administer AND the named duty.
func TenantWide(p tenant.Principal, duty string) bool {
	return tenant.IsTenantAdmin(p) && p.HasDuty(duty)
}

// WithTenantWideAdminTx runs fn with passage.legal_entity_scopes set to '*' for p's tenant, or
// returns ErrNotTenantWide when p fails TenantWide(p, duty).
func WithTenantWideAdminTx(ctx context.Context, pool *pgxpool.Pool, p tenant.Principal, duty string, fn func(tx pgx.Tx) error) error {
	if !TenantWide(p, duty) {
		return ErrNotTenantWide
	}
	return pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `select set_config('passage.tenant_id', $1, true), set_config('passage.legal_entity_scopes', '*', true)`, p.TenantID); err != nil {
			return err
		}
		return fn(tx)
	})
}

// RLSPolicy returns the DDL that enables tenant row-level security on table:
// one PERMISSIVE policy (without it Postgres denies every row). Include it in
// the migration that creates the table. The GUC is passage.tenant_id, set per
// transaction by WithTenantTx.
func RLSPolicy(table string) string {
	return fmt.Sprintf(`alter table %[1]s enable row level security;
alter table %[1]s force row level security;
create policy %[1]s_tenant_access on %[1]s as permissive
  using (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  with check (tenant_id = current_setting('passage.tenant_id', true)::uuid);`, table)
}

// LegalEntityScopePolicy returns the additional RESTRICTIVE policy for tables
// that carry legal_entity_id: rows are visible only inside the caller's
// legal-entity scopes (passage.legal_entity_scopes, a comma list, or '*' for
// a principal holding tenant.DutyAllScope; an empty list matches no rows).
func LegalEntityScopePolicy(table string) string {
	return fmt.Sprintf(`create policy %[1]s_legal_entity_scope on %[1]s as restrictive
  using (current_setting('passage.legal_entity_scopes', true) = '*'
         or legal_entity_id::text = any(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));`, table)
}

// IdempotencyDDL is the table every command-handling service creates to make
// Idempotency-Key requests replay-safe for 24 hours.
const IdempotencyDDL = `create table if not exists idempotency_keys (
  tenant_id uuid not null,
  key text not null,
  request_hash text not null,
  response_status int not null,
  response_body jsonb,
  created_at timestamptz not null default now(),
  primary key (tenant_id, key)
);`

// OutboxDDL is the transactional outbox table Debezium relays to Kafka.
const OutboxDDL = `create table if not exists outbox (
  id text primary key,
  tenant_id uuid not null,
  aggregate_type text not null,
  aggregate_id text not null,
  type text not null,
  payload jsonb not null,
  created_at timestamptz not null default now()
);`

// ServiceTokenReplayDDL is the table that lets every replica of a service
// refuse a service token another replica already accepted
// (tenant.ShareReplayCache). Copy it into the migration of each service that
// verifies service tokens.
var ServiceTokenReplayDDL = `create table service_token_replay (
  tenant_id uuid not null,
  jti text not null,
  expires_at timestamptz not null,
  primary key (tenant_id, jti)
);
create index service_token_replay_expires_idx on service_token_replay (tenant_id, expires_at);
` + RLSPolicy("service_token_replay") + `
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE format('GRANT USAGE ON SCHEMA %I TO passage_app', current_schema());
    EXECUTE 'GRANT SELECT, INSERT, DELETE ON service_token_replay TO passage_app';
  END IF;
END
$$;
`
