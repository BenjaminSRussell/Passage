package rlscheck

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"text/tabwriter"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/testkit"
)

const appRole = "passage_app"

const (
	problemNotForced   = "rls-not-enabled-and-forced"
	problemNoTenantID  = "no-tenant-id-column"
	problemNoTenantPol = "no-tenant-policy"
	problemNoEntityPol = "no-legal-entity-policy"
	problemExtraPolicy = "extra-permissive-policy"
	problemNullEntity  = "null-admitting-legal-entity-policy"
)

type tableFacts struct {
	Name         string
	Enabled      bool
	Forced       bool
	HasTenantID  bool
	TenantPolicy bool
	HasEntity    bool
	EntityPolicy bool
	ExtraPolicy  bool
	NullEntity   bool
}

type violation struct {
	Service string
	Table   string
	Problem string
}

func (v violation) key() string { return v.Service + "." + v.Table + ":" + v.Problem }

const tenantColumn = `(^|[^._[:alnum:]])tenant_id([^._[:alnum:]]|$)`

const tenantExpr = tenantColumn + `[^=]*= *[A-Za-z_(]*current_setting\(.passage\.tenant_id.`

const tableFactsQuery = `
select c.relname, c.relrowsecurity, c.relforcerowsecurity,
  exists (select 1 from pg_attribute a where a.attrelid = c.oid and a.attname = 'tenant_id' and not a.attisdropped),
  exists (select 1 from pg_policies p where p.schemaname = n.nspname and p.tablename = c.relname and p.permissive = 'PERMISSIVE'
          and p.cmd in ('ALL', 'SELECT') and (p.roles = '{public}' or 'passage_app' = any (p.roles))
          and p.qual ~ '` + tenantExpr + `'
          and (p.with_check is null or p.with_check ~ '` + tenantExpr + `')),
  exists (select 1 from pg_attribute a where a.attrelid = c.oid and a.attname = 'legal_entity_id' and not a.attisdropped),
  exists (select 1 from pg_policies p where p.schemaname = n.nspname and p.tablename = c.relname and p.permissive = 'RESTRICTIVE'
          and p.cmd = 'ALL' and (p.roles = '{public}' or 'passage_app' = any (p.roles))
          and p.qual like '%legal_entity_id%' and p.qual like '%passage.legal_entity_scopes%'
          and (p.with_check is null or p.with_check like '%passage.legal_entity_scopes%')),
  exists (select 1 from pg_policies p where p.schemaname = n.nspname and p.tablename = c.relname and p.permissive = 'PERMISSIVE'
          and (p.roles = '{public}' or 'passage_app' = any (p.roles))
          and (coalesce(p.qual, '') || ' ' || coalesce(p.with_check, '')) !~ '` + tenantColumn + `'),
  exists (select 1 from pg_policies p where p.schemaname = n.nspname and p.tablename = c.relname and p.permissive = 'RESTRICTIVE'
          and p.qual ~* 'legal_entity_id[[:space:])]+is[[:space:]]+null')
from pg_class c
join pg_namespace n on n.oid = c.relnamespace
where n.nspname = $1 and c.relkind in ('r', 'p') and not c.relispartition and c.relname <> 'schema_migrations'
order by c.relname`

const userSchemasQuery = `select nspname from pg_namespace where nspname not like 'pg\_%' and nspname <> 'information_schema'`

func migrationDirs(t *testing.T) map[string]string {
	t.Helper()
	dirs := map[string]string{}
	for _, glob := range []string{"../../services/*/migrations", "../migration-factory/migrations"} {
		matches, err := filepath.Glob(glob)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range matches {
			dirs[filepath.Base(filepath.Dir(m))] = m
		}
	}
	if len(dirs) == 0 {
		t.Fatal("no service migrations found")
	}
	return dirs
}

func userSchemas(t *testing.T, ctx context.Context, pool *pgxpool.Pool) map[string]bool {
	t.Helper()
	rows, err := pool.Query(ctx, userSchemasQuery)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatal(err)
		}
		out[n] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func schemaViolations(t *testing.T, service, dir string) []violation {
	t.Helper()
	pool := testkit.DB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	var current string
	if err := pool.QueryRow(ctx, "select current_schema()").Scan(&current); err != nil {
		t.Fatal(err)
	}
	before := userSchemas(t, ctx, pool)
	var extra []string
	t.Cleanup(func() {
		for _, s := range extra {
			_, _ = pool.Exec(context.Background(), "drop schema if exists "+pgx.Identifier{s}.Sanitize()+" cascade")
		}
	})
	if err := dbx.Migrate(ctx, pool, os.DirFS(dir)); err != nil {
		t.Fatalf("%s: migrate: %v", service, err)
	}
	for s := range userSchemas(t, ctx, pool) {
		if !before[s] {
			extra = append(extra, s)
		}
	}
	sort.Strings(extra)
	var out []violation
	tables := 0
	for _, schema := range append([]string{current}, extra...) {
		n, vs := collect(t, ctx, pool, service, schema)
		tables += n
		out = append(out, vs...)
	}
	if tables == 0 {
		t.Fatalf("%s: migrations yielded no tables to check", service)
	}
	return out
}

func collect(t *testing.T, ctx context.Context, pool *pgxpool.Pool, service, schema string) (int, []violation) {
	t.Helper()
	rows, err := pool.Query(ctx, tableFactsQuery, schema)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []violation
	tables := 0
	for rows.Next() {
		var f tableFacts
		if err := rows.Scan(&f.Name, &f.Enabled, &f.Forced, &f.HasTenantID, &f.TenantPolicy, &f.HasEntity, &f.EntityPolicy, &f.ExtraPolicy, &f.NullEntity); err != nil {
			t.Fatal(err)
		}
		tables++
		out = append(out, judge(service, f)...)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return tables, out
}

func judge(service string, f tableFacts) []violation {
	var out []violation
	add := func(problem string) { out = append(out, violation{service, f.Name, problem}) }
	if !f.Enabled || !f.Forced {
		add(problemNotForced)
	}
	if !f.HasTenantID {
		add(problemNoTenantID)
	} else if !f.TenantPolicy {
		add(problemNoTenantPol)
	}
	if f.HasEntity && !f.EntityPolicy {
		add(problemNoEntityPol)
	}
	if f.ExtraPolicy {
		add(problemExtraPolicy)
	}
	if f.NullEntity {
		add(problemNullEntity)
	}
	return out
}

func TestJudge(t *testing.T) {
	clean := tableFacts{Name: "x", Enabled: true, Forced: true, HasTenantID: true, TenantPolicy: true, HasEntity: true, EntityPolicy: true}
	if got := judge("s", clean); len(got) != 0 {
		t.Fatalf("clean table flagged: %v", got)
	}
	bad := tableFacts{Name: "x", HasEntity: true}
	if got := judge("s", bad); len(got) != 3 {
		t.Fatalf("got %d violations, want 3: %v", len(got), got)
	}
}

func TestAppRoleHasNoBypassRLS(t *testing.T) {
	pool := testkit.DB(t)
	var super, bypass bool
	err := pool.QueryRow(context.Background(), `select rolsuper, rolbypassrls from pg_roles where rolname = $1`, appRole).Scan(&super, &bypass)
	if err != nil {
		t.Fatalf("role %s: %v", appRole, err)
	}
	if super || bypass {
		t.Fatalf("%s superuser=%v bypassrls=%v; RLS would not bind the application role", appRole, super, bypass)
	}
}

func TestEveryTableEnforcesRLS(t *testing.T) {
	testkit.DB(t)
	dirs := migrationDirs(t)
	services := make([]string, 0, len(dirs))
	for s := range dirs {
		services = append(services, s)
	}
	sort.Strings(services)

	var all []violation
	failed := map[string]bool{}
	for _, s := range services {
		ok := t.Run(s, func(t *testing.T) {
			all = append(all, schemaViolations(t, s, dirs[s])...)
		})
		failed[s] = !ok
	}

	var unexpected, allowed []violation
	seen := map[string]bool{}
	for _, v := range all {
		seen[v.key()] = true
		if _, ok := allowlist[v.key()]; ok {
			allowed = append(allowed, v)
		} else {
			unexpected = append(unexpected, v)
		}
	}
	var stale []string
	for k := range allowlist {
		if !seen[k] && !failed[strings.SplitN(k, ".", 2)[0]] {
			stale = append(stale, k)
		}
	}
	sort.Strings(stale)

	if len(allowed) > 0 {
		t.Logf("allowlisted (see allowlist.go):\n%s", table(allowed))
	}
	if len(unexpected) > 0 {
		t.Errorf("RLS coverage violations (%d):\n%s", len(unexpected), table(unexpected))
	}
	if len(stale) > 0 {
		t.Errorf("allowlist entries that no longer violate; remove them: %s", strings.Join(stale, ", "))
	}
}

func table(vs []violation) string {
	var b strings.Builder
	w := tabwriter.NewWriter(&b, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(w, "SERVICE\tTABLE\tPROBLEM")
	for _, v := range vs {
		_, _ = fmt.Fprintf(w, "%s\t%s\t%s\n", v.Service, v.Table, v.Problem)
	}
	_ = w.Flush()
	return b.String()
}

func TestFixtureViolationsArePinned(t *testing.T) {
	testkit.DB(t)
	got := schemaViolations(t, "fixture", "testdata/fixture")
	var keys []string
	for _, v := range got {
		keys = append(keys, v.key())
	}
	sort.Strings(keys)
	want := []string{
		"fixture.bad_column:" + problemExtraPolicy,
		"fixture.bad_column:" + problemNoTenantPol,
		"fixture.extra_true:" + problemExtraPolicy,
		"fixture.insert_true:" + problemExtraPolicy,
		"fixture.null_admitting:" + problemNullEntity,
		"fixture.select_restrictive:" + problemNoEntityPol,
	}
	if strings.Join(keys, "\n") != strings.Join(want, "\n") {
		t.Fatalf("fixture violations:\n%s\nwant:\n%s", strings.Join(keys, "\n"), strings.Join(want, "\n"))
	}
}
