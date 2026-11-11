package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParseMasksValidates(t *testing.T) {
	good := `{"salt":"per-clone-secret","columns":{"account.name":"hash","*.email":"email","*.phone":"digits","account.note":"null","x.y":"redact"}}`
	if m, err := ParseMasks([]byte(good)); err != nil || len(m.Columns) != 5 {
		t.Fatalf("%v %v", m, err)
	}
	for name, bad := range map[string]string{
		"no salt":      `{"columns":{"a.b":"hash"}}`,
		"short salt":   `{"salt":"x","columns":{"a.b":"hash"}}`,
		"no columns":   `{"salt":"per-clone-secret","columns":{}}`,
		"bad key":      `{"salt":"per-clone-secret","columns":{"ab":"hash"}}`,
		"bad rule":     `{"salt":"per-clone-secret","columns":{"a.b":"shuffle"}}`,
		"unknown prop": `{"salt":"per-clone-secret","columns":{"a.b":"hash"},"extra":1}`,
		"not json":     `salt`,
	} {
		if _, err := ParseMasks([]byte(bad)); err == nil {
			t.Errorf("%s must be refused", name)
		}
	}
}

func TestMaskRules(t *testing.T) {
	m := &Masks{Salt: "per-clone-secret"}
	if a, b := m.mask("hash", "t.c", "Alice Example"), m.mask("hash", "t.c", "Alice Example"); a != b || len(a.(string)) != len("Alice Example") || a == "Alice Example" {
		t.Fatalf("hash %v %v", a, b)
	}
	if m.mask("hash", "t.c", "Alice") == m.mask("hash", "t.c", "Bob") {
		t.Fatal("different inputs must differ")
	}
	if got := m.mask("email", "t.c", "alice@corp.com").(string); !strings.HasSuffix(got, "@example.invalid") || strings.Contains(got, "alice") {
		t.Fatalf("email %q", got)
	}
	d := m.mask("digits", "t.c", "+49 (30) 1234-5678").(string)
	if len(d) != 18 || d[0] != '+' || d[4] != '(' || d == "+49 (30) 1234-5678" {
		t.Fatalf("digits %q", d)
	}
	if m.mask("redact", "t.c", "x") != "REDACTED" || m.mask("null", "t.c", "x") != nil {
		t.Fatal("redact/null")
	}
	// another salt gives other values: a clone cannot be matched against another clone
	if (&Masks{Salt: "another-secret"}).mask("hash", "t.c", "Alice Example") == m.mask("hash", "t.c", "Alice Example") {
		t.Fatal("the salt must change the output")
	}
}

func TestMaskApplyRefusesKeysAndNonText(t *testing.T) {
	tb := table{Name: "account", Key: []string{"id"}, Columns: []column{{Name: "tenant_id"}, {Name: "id"}, {Name: "name"}, {Name: "balance"}}}
	m, err := ParseMasks([]byte(`{"salt":"per-clone-secret","columns":{"account.name":"hash","*.id":"hash"}}`))
	if err != nil {
		t.Fatal(err)
	}
	rows, err := m.Apply(tb, [][]any{{"t", "id-1", "Alice", 1}})
	if err != nil || rows[0][2] == "Alice" || rows[0][1] != "id-1" {
		t.Fatalf("a *.column rule must skip a key: %v %v", rows, err)
	}
	if _, err := (&Masks{Salt: "per-clone-secret", Columns: map[string]string{"account.id": "hash"}}).Apply(tb, [][]any{{"t", "id-1", "A", 1}}); err == nil {
		t.Fatal("masking a key column explicitly must be refused")
	}
	if _, err := (&Masks{Salt: "per-clone-secret", Columns: map[string]string{"account.balance": "hash"}}).Apply(tb, [][]any{{"t", "id-1", "A", 1}}); err == nil || !strings.Contains(err.Error(), "text only") {
		t.Fatalf("a non-text value must stop the clone: %v", err)
	}
	if rows, err := (&Masks{Salt: "per-clone-secret", Columns: map[string]string{"account.name": "hash"}}).Apply(tb, [][]any{{"t", "id-1", nil, 1}}); err != nil || rows[0][2] != nil {
		t.Fatalf("NULL stays NULL: %v %v", rows, err)
	}
}

func TestMaskedCloneCopiesTheTenantWithMaskedColumnsOnly(t *testing.T) {
	p := newPair(t, ledgerDDL)
	exec(t, p.liveAdmin, strings.ReplaceAll(rlsTable("account")+rlsTable("entry"), "S.", p.schema+"."))
	p.scratchOnly(`
insert into S.account values ('` + tenantA + `','` + id(1) + `','Alice Example','{"k":[1]}',10.50,'{a}','call +49 30 1234');
insert into S.account values ('` + tenantB + `','` + id(3) + `','Other Tenant',null,1,null,null);
insert into S.entry values ('` + tenantA + `','` + id(1) + `','` + id(1) + `',5.25);`)
	masks, err := ParseMasks([]byte(`{"salt":"per-clone-secret","columns":{"account.name":"hash","account.note":"digits"}}`))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	res, err := CloneTenantMasked(ctx, p.scratch, p.liveApp, p.schema, tenantA, false, masks)
	if err != nil || count(t, p, "select count(*) from S.account") != 0 {
		t.Fatalf("a dry run writes nothing: %v", err)
	}
	if byTable(res)["account"].Inserted != 1 {
		t.Fatalf("dry run counts %+v", res)
	}
	if _, err := CloneTenantMasked(ctx, p.scratch, p.liveApp, p.schema, tenantA, true, masks); err != nil {
		t.Fatal(err)
	}
	var name, note string
	var amount string
	if err := p.liveAdmin.QueryRow(ctx, strings.ReplaceAll(`select name, note from S.account where tenant_id = $1`, "S.", p.schema+"."), tenantA).Scan(&name, &note); err != nil {
		t.Fatal(err)
	}
	if name == "Alice Example" || len(name) != len("Alice Example") || note == "call +49 30 1234" || !strings.HasPrefix(note, "call +") {
		t.Fatalf("masked values: name=%q note=%q", name, note)
	}
	if err := p.liveAdmin.QueryRow(ctx, strings.ReplaceAll(`select amount::text from S.entry where tenant_id = $1`, "S.", p.schema+"."), tenantA).Scan(&amount); err != nil || amount != "5.25" {
		t.Fatalf("unmasked columns copy as they are: %q %v", amount, err)
	}
	if n := count(t, p, "select count(*) from S.account where tenant_id = '"+tenantB+"'"); n != 0 {
		t.Fatalf("another tenant's rows were cloned: %d", n)
	}
	if _, err := CloneTenantMasked(ctx, p.scratch, p.liveApp, p.schema, tenantA, true, nil); err == nil {
		t.Fatal("a clone without masks must be refused")
	}
}

func TestRunCloneNeedsMasksAndDistinctDatabases(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "masks.json")
	if err := os.WriteFile(good, []byte(`{"salt":"per-clone-secret","columns":{"a.b":"hash"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	same := func(string) string { return "postgres://x" }
	var out bytes.Buffer
	if err := run(context.Background(), []string{"clone", "--tenant", tenantA}, &out, same, time.Now()); err == nil || !strings.Contains(err.Error(), "--masks") {
		t.Fatalf("err %v", err)
	}
	if err := run(context.Background(), []string{"clone", "--tenant", tenantA, "--masks", good}, &out, same, time.Now()); err == nil || !strings.Contains(err.Error(), "same database") {
		t.Fatalf("err %v", err)
	}
}

func TestRestoreDrillPassesAndFailsOnItsCriteria(t *testing.T) {
	p := newPair(t, ledgerDDL)
	seedScenario(t, p)
	ctx := context.Background()
	tick := time.Date(2026, 11, 11, 10, 0, 0, 0, time.UTC)
	clock := func() time.Time { tick = tick.Add(90 * time.Second); return tick }
	before := count(t, p, "select count(*) from S.account")

	rec := RunDrill(ctx, p.scratch, p.liveApp, DrillOptions{Tenant: tenantA, Schema: p.schema, TargetTime: "2026-11-11T09:00:00Z", RTO: time.Hour, MinRows: 3}, clock)
	if rec.Status != "PASS" || rec.RowsRestored != 4 || rec.TablesChecked < 2 || rec.DurationSeconds != 90 || len(rec.Failures) != 0 {
		t.Fatalf("pass: %+v", rec)
	}
	if rec.WouldUpdate == 0 || rec.LiveOnly == 0 {
		t.Fatalf("the dry run must show what differs: %+v", rec)
	}
	if count(t, p, "select count(*) from S.account") != before {
		t.Fatal("a drill must not write")
	}

	slow := RunDrill(ctx, p.scratch, p.liveApp, DrillOptions{Tenant: tenantA, Schema: p.schema, RTO: time.Minute}, clock)
	if slow.Status != "FAIL" || !strings.Contains(strings.Join(slow.Failures, ";"), "over the RTO") {
		t.Fatalf("rto: %+v", slow)
	}
	empty := RunDrill(ctx, p.scratch, p.liveApp, DrillOptions{Tenant: "00000000-0000-0000-0000-0000000000ff", Schema: p.schema, MinRows: 1}, clock)
	if empty.Status != "FAIL" || !strings.Contains(strings.Join(empty.Failures, ";"), "rows restored") {
		t.Fatalf("a drill that restored nothing proves nothing: %+v", empty)
	}
	bad := RunDrill(ctx, p.scratch, p.liveApp, DrillOptions{Tenant: "not-a-uuid", Schema: p.schema}, clock)
	if bad.Status != "FAIL" || !strings.Contains(strings.Join(bad.Failures, ";"), "restore failed") {
		t.Fatalf("a failed restore fails the drill: %+v", bad)
	}
}

func TestRunDrillWritesTheRecordAndFailsTheCommand(t *testing.T) {
	p := newPair(t, ledgerDDL)
	seedScenario(t, p)
	env := map[string]string{
		scratchURLEnv: os.Getenv("PASSAGE_TEST_RESTORE_DATABASE_URL"),
		liveURLEnv:    os.Getenv("PASSAGE_TEST_APP_DATABASE_URL"),
	}
	getenv := func(k string) string { return env[k] }
	file := filepath.Join(t.TempDir(), "drill.json")
	var out bytes.Buffer
	if err := run(context.Background(), []string{"drill", "--tenant", tenantA, "--schema", p.schema, "--target-time", "2026-11-11T09:00:00Z", "--record", file}, &out, getenv, time.Now()); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	b, err := os.ReadFile(file)
	if err != nil || !strings.Contains(string(b), `"status": "PASS"`) || !strings.Contains(out.String(), `"rowsRestored": 4`) {
		t.Fatalf("record %s err %v", b, err)
	}
	out.Reset()
	err = run(context.Background(), []string{"drill", "--tenant", "00000000-0000-0000-0000-0000000000ff", "--schema", p.schema}, &out, getenv, time.Now())
	if err == nil || !strings.Contains(out.String(), `"status": "FAIL"`) {
		t.Fatalf("a failing drill exits non-zero and still prints the record: %v\n%s", err, out.String())
	}
}
