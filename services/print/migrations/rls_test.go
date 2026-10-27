package migrations

import (
	"io/fs"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// createTable captures the name and body of each CREATE TABLE in the embedded migrations.
var createTable = regexp.MustCompile(`(?is)CREATE TABLE IF NOT EXISTS (\w+)\s*\((.*?)\n\);`)

// TestEveryTableHasTenantRowLevelSecurity is the static half of the tenant isolation
// check (CONVENTIONS, DoD 4): every table carries tenant_id, enables and forces row-level
// security, and has a tenant_access policy keyed on passage.tenant_id for reads and
// writes. The outbox is included: the Debezium relay reads it with a BYPASSRLS role. The
// live isolation test is internal/store, which runs when PASSAGE_TEST_DATABASE_URL is set.
func TestEveryTableHasTenantRowLevelSecurity(t *testing.T) {
	files, err := fs.Glob(FS, "*.sql")
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(files)
	var all strings.Builder
	tables := map[string]string{}
	for _, f := range files {
		raw, err := fs.ReadFile(FS, f)
		if err != nil {
			t.Fatal(err)
		}
		all.Write(raw)
		all.WriteString("\n")
		for _, m := range createTable.FindAllStringSubmatch(string(raw), -1) {
			tables[m[1]] = m[2]
		}
	}
	sql := strings.Join(strings.Fields(all.String()), " ")
	const tenantClause = "tenant_id = current_setting('passage.tenant_id', true)::uuid"

	for _, name := range []string{"print_device", "print_job", "print_routing_rule", "outbox"} {
		if _, ok := tables[name]; !ok {
			t.Errorf("table %s is not created by the migrations", name)
		}
	}
	for name, body := range tables {
		if !strings.Contains(strings.Join(strings.Fields(body), " "), "tenant_id uuid NOT NULL") {
			t.Errorf("table %s has no tenant_id uuid NOT NULL column", name)
		}
		for _, want := range []string{
			"ALTER TABLE " + name + " ENABLE ROW LEVEL SECURITY;",
			"ALTER TABLE " + name + " FORCE ROW LEVEL SECURITY;",
			"CREATE POLICY " + name + "_tenant_access ON " + name + " AS PERMISSIVE USING (" + tenantClause + ") WITH CHECK (" + tenantClause + ");",
		} {
			if !strings.Contains(sql, want) {
				t.Errorf("table %s: migrations lack %q", name, want)
			}
		}
	}
}
