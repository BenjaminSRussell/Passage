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

func roleConn(t *testing.T, p *pair, attrs string) *pgx.Conn {
	t.Helper()
	raw := make([]byte, 5)
	if _, err := rand.Read(raw); err != nil {
		t.Fatal(err)
	}
	name := "tr_r_" + hex.EncodeToString(raw)
	exec(t, p.liveAdmin, "create role "+name+" login password 'x' "+attrs)
	exec(t, p.liveAdmin, "grant usage on schema "+p.schema+" to "+name)
	exec(t, p.liveAdmin, "grant select, insert, update on all tables in schema "+p.schema+" to "+name)
	cfg, err := pgx.ParseConfig(os.Getenv("PASSAGE_TEST_APP_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	cfg.User, cfg.Password = name, "x"
	conn, err := pgx.ConnectConfig(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = conn.Close(context.Background())
		_, _ = p.liveAdmin.Exec(context.Background(), "drop owned by "+name)
		_, _ = p.liveAdmin.Exec(context.Background(), "drop role "+name)
	})
	return conn
}

const shapedDDL = `
create table S.program_position (tenant_id uuid not null, id uuid primary key, parent_id uuid, name text,
  unique (tenant_id, id), foreign key (tenant_id, parent_id) references S.program_position (tenant_id, id));
create table S.seq_line (tenant_id uuid not null, id bigint generated always as identity primary key, note text);
create table S.part (tenant_id uuid not null, id int primary key, v text) partition by range (id);
create table S.part_1 partition of S.part for values from (0) to (100);
`

func TestRestoreTenantOrdersSelfReferencesAndKeepsIdentityValues(t *testing.T) {
	p := newPair(t, shapedDDL)
	exec(t, p.liveAdmin, strings.ReplaceAll(rlsTable("program_position")+rlsTable("seq_line")+rlsTable("part"), "S.", p.schema+"."))
	p.scratchOnly(`alter table S.program_position drop constraint program_position_tenant_id_parent_id_fkey`)
	p.scratchOnly(`
insert into S.program_position values ('` + tenantA + `','` + id(3) + `','` + id(2) + `','leaf');
insert into S.program_position values ('` + tenantA + `','` + id(2) + `','` + id(1) + `','middle');
insert into S.program_position values ('` + tenantA + `','` + id(1) + `',null,'root');
insert into S.seq_line overriding system value values ('` + tenantA + `', 5, 'five');
insert into S.seq_line overriding system value values ('` + tenantA + `', 6, 'six');
insert into S.part values ('` + tenantA + `', 1, 'x');`)
	res, err := RestoreTenant(context.Background(), p.scratch, p.liveApp, p.schema, tenantA, true)
	if err != nil {
		t.Fatal(err)
	}
	got := byTable(res)
	if got["program_position"].Inserted != 3 || got["seq_line"].Inserted != 2 {
		t.Fatalf("results = %+v", res.Tables)
	}
	if _, partition := got["part_1"]; partition {
		t.Fatal("a partition was copied as its own table")
	}
	if n := count(t, p, `select count(*) from S.seq_line where id in (5, 6)`); n != 2 {
		t.Fatalf("identity values were not preserved: %d", n)
	}
	if n := count(t, p, `select count(*) from S.part_1`); n != 1 {
		t.Fatal("partitioned parent row did not route to its partition")
	}
}

func TestRestoreTenantRefusesRolesAndTablesThatRelyOnNothingButTheCopy(t *testing.T) {
	p := newPair(t, ledgerDDL)
	seedScenario(t, p)
	ctx := context.Background()
	if _, err := RestoreTenant(ctx, p.scratch, p.liveAdmin, p.schema, tenantA, true); err == nil || !strings.Contains(err.Error(), "BYPASSRLS") {
		t.Fatalf("superuser live role error = %v", err)
	}
	if _, err := RestoreTenant(ctx, p.scratch, roleConn(t, p, "bypassrls"), p.schema, tenantA, true); err == nil || !strings.Contains(err.Error(), "BYPASSRLS") {
		t.Fatalf("bypassrls live role error = %v", err)
	}
	owner := roleConn(t, p, "")
	exec(t, p.liveAdmin, "alter table "+p.schema+".account owner to "+owner.Config().User)
	exec(t, p.liveAdmin, "alter table "+p.schema+".account no force row level security")
	if _, err := RestoreTenant(ctx, p.scratch, owner, p.schema, tenantA, true); err == nil || !strings.Contains(err.Error(), "FORCE") {
		t.Fatalf("owner without FORCE error = %v", err)
	}
	exec(t, p.liveAdmin, "alter table "+p.schema+".entry disable row level security")
	if _, err := RestoreTenant(ctx, p.scratch, p.liveApp, p.schema, tenantA, true); err == nil || !strings.Contains(err.Error(), "row-level security") {
		t.Fatalf("table without RLS error = %v", err)
	}
	if n := count(t, p, `select count(*) from S.account where name = 'old name'`); n != 0 {
		t.Fatal("a refused restore wrote rows")
	}
}

func TestRestoreTenantAbortsOnCrossTenantPrimaryKeyCollision(t *testing.T) {
	for name, extra := range map[string]string{
		"strict RLS":           "",
		"visible to all reads": "create policy read_all on S.account for select using (true); create policy upd_all on S.account for update using (true);",
	} {
		t.Run(name, func(t *testing.T) {
			p := newPair(t, ledgerDDL)
			exec(t, p.liveAdmin, strings.ReplaceAll(rlsTable("account")+rlsTable("entry")+extra, "S.", p.schema+"."))
			p.scratchOnly(`insert into S.account values ('` + tenantA + `','` + id(1) + `','mine',null,0,null,null)`)
			p.liveOnly(`insert into S.account values ('` + tenantB + `','` + id(1) + `','theirs',null,0,null,null)`)
			_, err := RestoreTenant(context.Background(), p.scratch, p.liveApp, p.schema, tenantA, true)
			if err == nil {
				t.Fatal("collision with another tenant's primary key was accepted")
			}
			if n := count(t, p, `select count(*) from S.account where name = 'theirs' and tenant_id = '`+tenantB+`'`); n != 1 {
				t.Fatal("the other tenant's row was changed")
			}
			if name == "visible to all reads" && !strings.Contains(err.Error(), "another tenant") {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestRestoreTenantUsesTheColumnsBothDatabasesShareAndAbortsOnUniqueCollisions(t *testing.T) {
	p := newPair(t, `create table S.item (tenant_id uuid not null, id int primary key, code text unique, only_both text);`)
	exec(t, p.liveAdmin, strings.ReplaceAll(rlsTable("item"), "S.", p.schema+"."))
	p.liveOnly(`alter table S.item add column live_extra text default 'kept'`)
	p.scratchOnly(`alter table S.item add column scratch_extra text`)
	p.scratchOnly(`insert into S.item values ('` + tenantA + `', 1, 'c1', 'restored', 's')`)
	p.liveOnly(`insert into S.item (tenant_id, id, code, only_both) values ('` + tenantA + `', 1, 'c1', 'changed')`)
	if _, err := RestoreTenant(context.Background(), p.scratch, p.liveApp, p.schema, tenantA, true); err != nil {
		t.Fatal(err)
	}
	if n := count(t, p, `select count(*) from S.item where only_both = 'restored' and live_extra = 'kept'`); n != 1 {
		t.Fatal("shared column not restored or live-only column lost")
	}
	p.scratchOnly(`insert into S.item values ('` + tenantA + `', 2, 'dup', 'x', 's')`)
	p.liveOnly(`insert into S.item (tenant_id, id, code) values ('` + tenantA + `', 3, 'dup')`)
	if _, err := RestoreTenant(context.Background(), p.scratch, p.liveApp, p.schema, tenantA, true); err == nil {
		t.Fatal("a non-primary-key unique collision was accepted")
	}
}
