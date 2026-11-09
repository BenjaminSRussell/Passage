package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

const (
	tenantA = "00000000-0000-0000-0000-00000000000a"
	tenantB = "00000000-0000-0000-0000-00000000000b"
)

type pair struct {
	schema      string
	scratch     *pgx.Conn
	liveAdmin   *pgx.Conn
	liveApp     *pgx.Conn
	scratchOnly func(sql string)
	liveOnly    func(sql string)
}

func connectEnv(t *testing.T, env string) *pgx.Conn {
	t.Helper()
	url := os.Getenv(env)
	if url == "" {
		t.Skipf("%s not set; skipping database test", env)
	}
	conn, err := pgx.Connect(context.Background(), url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close(context.Background()) })
	return conn
}

func exec(t *testing.T, conn *pgx.Conn, sql string) {
	t.Helper()
	if _, err := conn.Exec(context.Background(), sql); err != nil {
		t.Fatalf("%v\n%s", err, sql)
	}
}

func newPair(t *testing.T, ddl string) *pair {
	t.Helper()
	raw := make([]byte, 6)
	if _, err := rand.Read(raw); err != nil {
		t.Fatal(err)
	}
	p := &pair{
		schema:    "tr_" + hex.EncodeToString(raw),
		scratch:   connectEnv(t, "PASSAGE_TEST_RESTORE_DATABASE_URL"),
		liveAdmin: connectEnv(t, "PASSAGE_TEST_DATABASE_URL"),
		liveApp:   connectEnv(t, "PASSAGE_TEST_APP_DATABASE_URL"),
	}
	p.scratchOnly = func(sql string) { exec(t, p.scratch, strings.ReplaceAll(sql, "S.", p.schema+".")) }
	p.liveOnly = func(sql string) { exec(t, p.liveAdmin, strings.ReplaceAll(sql, "S.", p.schema+".")) }
	for _, c := range []*pgx.Conn{p.scratch, p.liveAdmin} {
		exec(t, c, "create schema "+p.schema)
		exec(t, c, strings.ReplaceAll(ddl, "S.", p.schema+"."))
	}
	exec(t, p.liveAdmin, "grant usage on schema "+p.schema+" to passage_app")
	t.Cleanup(func() {
		for _, c := range []*pgx.Conn{p.scratch, p.liveAdmin} {
			_, _ = c.Exec(context.Background(), "drop schema "+p.schema+" cascade")
		}
	})
	return p
}

func rlsTable(table string) string {
	return `alter table S.` + table + ` enable row level security;
alter table S.` + table + ` force row level security;
create policy tenant_access on S.` + table + ` using (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  with check (tenant_id = current_setting('passage.tenant_id', true)::uuid);
grant select, insert, update on S.` + table + ` to passage_app;`
}

const ledgerDDL = `
create table S.account (tenant_id uuid not null, id uuid primary key, name text not null, meta jsonb, balance numeric(12,2), tags text[], note text);
create table S.entry (tenant_id uuid not null, id uuid primary key, account_id uuid not null references S.account(id), amount numeric(12,2) not null);
create table S.shared_rate (id int primary key, rate numeric);
`

func id(n int) string {
	return "10000000-0000-0000-0000-" + strings.Repeat("0", 11) + string(rune('0'+n))
}

func seedScenario(t *testing.T, p *pair) {
	t.Helper()
	exec(t, p.liveAdmin, strings.ReplaceAll(rlsTable("account")+rlsTable("entry"), "S.", p.schema+"."))
	p.scratchOnly(`
insert into S.account values ('` + tenantA + `','` + id(1) + `','old name','{"k":[1,2]}',10.50,'{a,"b c"}',null);
insert into S.account values ('` + tenantA + `','` + id(2) + `','deleted later',null,0,null,'n');
insert into S.account values ('` + tenantB + `','` + id(3) + `','b at T',null,1,null,null);
insert into S.account values ('` + tenantB + `','` + id(5) + `','b only at T',null,1,null,null);
insert into S.entry values ('` + tenantA + `','` + id(1) + `','` + id(1) + `',5.25);
insert into S.entry values ('` + tenantA + `','` + id(2) + `','` + id(2) + `',7);
insert into S.shared_rate values (1, 1.5);`)
	p.liveOnly(`
insert into S.account values ('` + tenantA + `','` + id(1) + `','changed after T','{"k":[9]}',99,null,null);
insert into S.account values ('` + tenantA + `','` + id(4) + `','created after T',null,0,null,null);
insert into S.account values ('` + tenantB + `','` + id(3) + `','b live',null,2,null,null);
insert into S.entry values ('` + tenantA + `','` + id(1) + `','` + id(1) + `',1);
insert into S.shared_rate values (1, 9);`)
}

func count(t *testing.T, p *pair, sql string) int {
	t.Helper()
	var n int
	if err := p.liveAdmin.QueryRow(context.Background(), strings.ReplaceAll(sql, "S.", p.schema+".")).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func byTable(r Result) map[string]TableResult {
	m := map[string]TableResult{}
	for _, tr := range r.Tables {
		m[tr.Table] = tr
	}
	return m
}

func TestRestoreTenantCopiesOnlyThatTenantsRows(t *testing.T) {
	p := newPair(t, ledgerDDL)
	seedScenario(t, p)
	ctx := context.Background()

	dry, err := RestoreTenant(ctx, p.scratch, p.liveApp, p.schema, tenantA, false)
	if err != nil {
		t.Fatal(err)
	}
	if dry.Applied {
		t.Fatal("dry run reported applied")
	}
	if got := count(t, p, `select count(*) from S.account where name = 'old name'`); got != 0 {
		t.Fatalf("dry run changed the live database: %d rows restored", got)
	}
	if got := count(t, p, `select count(*) from S.account where id = '`+id(2)+`'`); got != 0 {
		t.Fatal("dry run inserted a row")
	}

	applied, err := RestoreTenant(ctx, p.scratch, p.liveApp, p.schema, tenantA, true)
	if err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]TableResult{
		"account": {Table: "account", Source: 2, Inserted: 1, Updated: 1, Unchanged: 0, LiveOnly: 1},
		"entry":   {Table: "entry", Source: 2, Inserted: 1, Updated: 1, Unchanged: 0, LiveOnly: 0},
	} {
		if got := byTable(applied)[name]; got != want {
			t.Errorf("applied %s = %+v, want %+v", name, got, want)
		}
		if got := byTable(dry)[name]; got != want {
			t.Errorf("dry run %s = %+v, want the same counts as apply %+v", name, got, want)
		}
	}
	if len(applied.Tables) != 2 {
		t.Fatalf("copied %d tables, want only the 2 with tenant_id", len(applied.Tables))
	}

	var name, meta, tags string
	var balance string
	if err := p.liveAdmin.QueryRow(ctx, `select name, meta::text, balance::text, tags::text from `+p.schema+`.account where id = $1`, id(1)).Scan(&name, &meta, &balance, &tags); err != nil {
		t.Fatal(err)
	}
	if name != "old name" || meta != `{"k": [1, 2]}` || balance != "10.50" || tags != `{a,"b c"}` {
		t.Fatalf("restored row = %q %q %q %q", name, meta, balance, tags)
	}
	if got := count(t, p, `select count(*) from S.account where id = '`+id(4)+`' and name = 'created after T'`); got != 1 {
		t.Fatal("a row created after the target time was removed")
	}
	if got := count(t, p, `select count(*) from S.account where id = '`+id(2)+`' and note = 'n'`); got != 1 {
		t.Fatal("deleted row was not restored")
	}
	if got := count(t, p, `select count(*) from S.account where tenant_id = '`+tenantB+`' and id = '`+id(3)+`' and name = 'b live'`); got != 1 {
		t.Fatal("another tenant's live row was overwritten")
	}
	if got := count(t, p, `select count(*) from S.account where id = '`+id(5)+`'`); got != 0 {
		t.Fatal("another tenant's scratch row was copied")
	}
	if got := count(t, p, `select count(*) from S.shared_rate where rate = 9`); got != 1 {
		t.Fatal("a table without tenant_id was touched")
	}
}

func TestRestoreTenantIsIdempotent(t *testing.T) {
	p := newPair(t, ledgerDDL)
	seedScenario(t, p)
	ctx := context.Background()
	if _, err := RestoreTenant(ctx, p.scratch, p.liveApp, p.schema, tenantA, true); err != nil {
		t.Fatal(err)
	}
	again, err := RestoreTenant(ctx, p.scratch, p.liveApp, p.schema, tenantA, true)
	if err != nil {
		t.Fatal(err)
	}
	for _, tr := range again.Tables {
		if tr.Inserted != 0 || tr.Updated != 0 || tr.Unchanged != tr.Source {
			t.Errorf("second run %+v changed rows", tr)
		}
	}
}

func TestRestoreTenantRollsBackEverythingOnFailure(t *testing.T) {
	p := newPair(t, ledgerDDL)
	seedScenario(t, p)
	p.scratchOnly(`alter table S.entry drop constraint entry_account_id_fkey`)
	p.scratchOnly(`insert into S.entry values ('` + tenantA + `','` + id(3) + `','` + id(9) + `',1)`)
	if _, err := RestoreTenant(context.Background(), p.scratch, p.liveApp, p.schema, tenantA, true); err == nil {
		t.Fatal("expected the foreign key violation to fail the restore")
	}
	if got := count(t, p, `select count(*) from S.account where name = 'old name'`); got != 0 {
		t.Fatal("parent rows were committed although a later table failed")
	}
}

func TestRestoreTenantRejectsUnsafeInput(t *testing.T) {
	p := newPair(t, ledgerDDL)
	for _, bad := range []string{"", "not-a-uuid", tenantA + "' or '1'='1", strings.ToUpper(tenantA)} {
		if _, err := RestoreTenant(context.Background(), p.scratch, p.liveApp, p.schema, bad, true); err == nil {
			t.Errorf("tenant %q was accepted", bad)
		}
	}
	if _, err := RestoreTenant(context.Background(), p.scratch, p.liveApp, p.schema+"_missing", tenantA, true); err == nil {
		t.Error("a schema without tenant tables was accepted")
	}
}

func TestRestoreTenantRefusesTablesItCannotOrderOrMatch(t *testing.T) {
	cycle := newPair(t, `
create table S.a (tenant_id uuid not null, id int primary key, b_id int);
create table S.b (tenant_id uuid not null, id int primary key, a_id int references S.a(id));
alter table S.a add foreign key (b_id) references S.b(id);`)
	if _, err := RestoreTenant(context.Background(), cycle.scratch, cycle.liveApp, cycle.schema, tenantA, false); err == nil || !strings.Contains(err.Error(), "cycle") {
		t.Fatalf("cycle error = %v", err)
	}
	noKey := newPair(t, `create table S.loose (tenant_id uuid not null, v int);`)
	if _, err := RestoreTenant(context.Background(), noKey.scratch, noKey.liveApp, noKey.schema, tenantA, false); err == nil || !strings.Contains(err.Error(), "primary key") {
		t.Fatalf("missing key error = %v", err)
	}
}

func TestRunCopyDryRunPrintsCountsAndNeedsEnvironment(t *testing.T) {
	p := newPair(t, ledgerDDL)
	seedScenario(t, p)
	env := map[string]string{
		scratchURLEnv: os.Getenv("PASSAGE_TEST_RESTORE_DATABASE_URL"),
		liveURLEnv:    os.Getenv("PASSAGE_TEST_APP_DATABASE_URL"),
	}
	var out bytes.Buffer
	err := run(context.Background(), []string{"copy", "--tenant", tenantA, "--schema", p.schema}, &out, func(k string) string { return env[k] }, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "DRY RUN") || !strings.Contains(out.String(), "inserted=1 updated=1") {
		t.Fatalf("output = %q", out.String())
	}
	if got := count(t, p, `select count(*) from S.account where name = 'old name'`); got != 0 {
		t.Fatal("the default run applied changes")
	}
	err = run(context.Background(), []string{"copy", "--tenant", tenantA}, &out, func(string) string { return "" }, time.Now())
	if err == nil || !strings.Contains(err.Error(), scratchURLEnv) {
		t.Fatalf("missing environment error = %v", err)
	}
}
