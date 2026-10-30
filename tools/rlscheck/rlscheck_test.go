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

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/testkit"
)

const appRole = "passage_app"

type tableFacts struct {
	Name         string
	Enabled      bool
	Forced       bool
	HasTenantID  bool
	TenantPolicy bool
	HasEntity    bool
	EntityPolicy bool
}

type violation struct {
	Service string
	Table   string
	Problem string
}

func (v violation) key() string { return v.Service + "." + v.Table }

const tableFactsQuery = `
select c.relname, c.relrowsecurity, c.relforcerowsecurity,
  exists (select 1 from pg_attribute a where a.attrelid = c.oid and a.attname = 'tenant_id' and not a.attisdropped),
  exists (select 1 from pg_policies p where p.schemaname = n.nspname and p.tablename = c.relname and p.permissive = 'PERMISSIVE'
          and p.qual like '%passage.tenant_id%' and p.with_check like '%passage.tenant_id%'),
  exists (select 1 from pg_attribute a where a.attrelid = c.oid and a.attname = 'legal_entity_id' and not a.attisdropped),
  exists (select 1 from pg_policies p where p.schemaname = n.nspname and p.tablename = c.relname and p.permissive = 'RESTRICTIVE'
          and p.qual like '%passage.legal_entity_scopes%')
from pg_class c
join pg_namespace n on n.oid = c.relnamespace
where n.nspname = current_schema() and c.relkind in ('r', 'p') and not c.relispartition and c.relname <> 'schema_migrations'
order by c.relname`

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

func schemaViolations(t *testing.T, service, dir string) []violation {
	t.Helper()
	pool := testkit.DB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if err := dbx.Migrate(ctx, pool, os.DirFS(dir)); err != nil {
		t.Fatalf("%s: migrate: %v", service, err)
	}
	return collect(t, ctx, pool, service)
}

func collect(t *testing.T, ctx context.Context, pool *pgxpool.Pool, service string) []violation {
	t.Helper()
	rows, err := pool.Query(ctx, tableFactsQuery)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []violation
	for rows.Next() {
		var f tableFacts
		if err := rows.Scan(&f.Name, &f.Enabled, &f.Forced, &f.HasTenantID, &f.TenantPolicy, &f.HasEntity, &f.EntityPolicy); err != nil {
			t.Fatal(err)
		}
		out = append(out, judge(service, f)...)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func judge(service string, f tableFacts) []violation {
	var out []violation
	add := func(problem string) { out = append(out, violation{service, f.Name, problem}) }
	if !f.Enabled || !f.Forced {
		add(fmt.Sprintf("RLS enabled=%v forced=%v", f.Enabled, f.Forced))
	}
	if !f.HasTenantID {
		add("no tenant_id column")
	} else if !f.TenantPolicy {
		add("no permissive tenant policy with USING and WITH CHECK")
	}
	if f.HasEntity && !f.EntityPolicy {
		add("legal_entity_id without restrictive legal-entity policy")
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
	for _, s := range services {
		t.Run(s, func(t *testing.T) {
			all = append(all, schemaViolations(t, s, dirs[s])...)
		})
	}

	var unexpected []violation
	seen := map[string]bool{}
	for _, v := range all {
		seen[v.key()] = true
		if _, ok := allowlist[v.key()]; !ok {
			unexpected = append(unexpected, v)
		}
	}
	var stale []string
	for k := range allowlist {
		if !seen[k] {
			stale = append(stale, k)
		}
	}
	sort.Strings(stale)

	if len(all) > len(unexpected) {
		t.Logf("%d allowlisted violations remain (see allowlist.go)", len(all)-len(unexpected))
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
	fmt.Fprintln(w, "SERVICE\tTABLE\tPROBLEM")
	for _, v := range vs {
		fmt.Fprintf(w, "%s\t%s\t%s\n", v.Service, v.Table, v.Problem)
	}
	_ = w.Flush()
	return b.String()
}
