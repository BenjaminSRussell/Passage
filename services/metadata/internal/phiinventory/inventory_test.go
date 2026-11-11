// Package phiinventory keeps docs/ops/phi-field-inventory.md honest (#606): every column the review lists must exist
// in the service's migrations, so a rename or a dropped column cannot leave the inventory describing a column that is gone.
package phiinventory_test

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

const (
	inventoryPath = "../../../../docs/ops/phi-field-inventory.md"
	servicesDir   = "../../.."
)

type row struct {
	service, table, column, decision, protection, action string
	line                                                 int
}

// parseInventory reads the rows of the inventory table: | service | `table.column` | decision | protection | action |.
func parseInventory(md string) ([]row, []string) {
	var rows []row
	var problems []string
	for i, line := range strings.Split(md, "\n") {
		if !strings.HasPrefix(line, "| ") {
			continue
		}
		cells := strings.Split(strings.Trim(strings.TrimSpace(line), "|"), "|")
		for j := range cells {
			cells[j] = strings.TrimSpace(cells[j])
		}
		if len(cells) != 5 || cells[0] == "Service" {
			continue
		}
		tableColumn := strings.Trim(cells[1], "`")
		table, column, ok := strings.Cut(tableColumn, ".")
		if !ok || table == "" || column == "" {
			problems = append(problems, fmt.Sprintf("line %d: %q is not table.column", i+1, cells[1]))
			continue
		}
		rows = append(rows, row{cells[0], table, column, cells[2], cells[3], cells[4], i + 1})
	}
	return rows, problems
}

var (
	createTable = regexp.MustCompile(`(?is)create\s+table(?:\s+if\s+not\s+exists)?\s+(\w+)\s*\(`)
	alterTable  = regexp.MustCompile(`(?is)alter\s+table\s+(?:only\s+)?(?:if\s+exists\s+)?(\w+)\s+([^;]*);`)
	addColumn   = regexp.MustCompile(`(?is)add\s+column(?:\s+if\s+not\s+exists)?\s+(\w+)\s+(\w+)`)
	lineComment = regexp.MustCompile(`--[^\n]*`)
	firstWord   = regexp.MustCompile(`^\s*(\w+)\s+(\w+)`)
	constraint  = map[string]bool{"primary": true, "unique": true, "foreign": true, "check": true, "constraint": true, "exclude": true, "like": true}
)

// splitTop splits a column list on the commas outside parentheses and quotes.
func splitTop(body string) []string {
	var parts []string
	depth, start, quoted := 0, 0, false
	for i, r := range body {
		switch {
		case r == '\'':
			quoted = !quoted
		case quoted:
		case r == '(':
			depth++
		case r == ')':
			depth--
		case r == ',' && depth == 0:
			parts = append(parts, body[start:i])
			start = i + 1
		}
	}
	return append(parts, body[start:])
}

// parseColumns returns table to column to lower-case type for the SQL of one service.
func parseColumns(sql string, into map[string]map[string]string) {
	sql = lineComment.ReplaceAllString(sql, "")
	add := func(table, column, kind string) {
		if into[table] == nil {
			into[table] = map[string]string{}
		}
		into[table][column] = strings.ToLower(kind)
	}
	for _, m := range createTable.FindAllStringSubmatchIndex(sql, -1) {
		table := sql[m[2]:m[3]]
		depth, quoted, end := 1, false, m[1]
		for ; end < len(sql) && depth > 0; end++ {
			switch c := sql[end]; {
			case c == '\'':
				quoted = !quoted
			case quoted:
			case c == '(':
				depth++
			case c == ')':
				depth--
			}
		}
		for _, def := range splitTop(sql[m[1] : end-1]) {
			if w := firstWord.FindStringSubmatch(def); w != nil && !constraint[strings.ToLower(w[1])] {
				add(table, w[1], w[2])
			}
		}
	}
	for _, m := range alterTable.FindAllStringSubmatch(sql, -1) {
		for _, c := range addColumn.FindAllStringSubmatch(m[2], -1) {
			add(m[1], c[1], c[2])
		}
	}
}

func serviceColumns(t *testing.T, service string) map[string]map[string]string {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(servicesDir, service, "migrations", "*.sql"))
	if err != nil || len(files) == 0 {
		t.Fatalf("service %q has no migrations under %s", service, servicesDir)
	}
	slices.Sort(files)
	columns := map[string]map[string]string{}
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		parseColumns(string(data), columns)
	}
	return columns
}

// problemsWith checks rows against the columns of their service.
func problemsWith(rows []row, columns map[string]map[string]map[string]string) []string {
	var problems []string
	seen := map[string]bool{}
	for _, r := range rows {
		at := fmt.Sprintf("line %d (%s %s.%s)", r.line, r.service, r.table, r.column)
		if !slices.Contains([]string{"phi", "not phi", "free text"}, strings.Trim(r.decision, "`")) {
			problems = append(problems, at+": decision must be phi, not phi or free text, got "+r.decision)
		}
		if r.protection == "" || r.action == "" {
			problems = append(problems, at+": protection and action are required")
		}
		key := r.service + "/" + r.table + "." + r.column
		if seen[key] {
			problems = append(problems, at+": listed twice")
		}
		seen[key] = true
		service, ok := columns[r.service]
		if !ok {
			problems = append(problems, at+": unknown service")
			continue
		}
		kind, ok := service[r.table][r.column]
		if !ok {
			problems = append(problems, at+": the column does not exist in the service's migrations")
			continue
		}
		if strings.HasPrefix(r.protection, "Sealed") && kind != "bytea" {
			problems = append(problems, at+": the row says it is sealed but the column type is "+kind+", not bytea")
		}
	}
	return problems
}

func TestInventoryMatchesMigrations(t *testing.T) {
	data, err := os.ReadFile(inventoryPath)
	if err != nil {
		t.Fatal(err)
	}
	rows, problems := parseInventory(string(data))
	services := []string{"hr", "payroll", "expense", "fieldservice"}
	columns := map[string]map[string]map[string]string{}
	for _, s := range services {
		columns[s] = serviceColumns(t, s)
	}
	problems = append(problems, problemsWith(rows, columns)...)
	for _, s := range services {
		if !slices.ContainsFunc(rows, func(r row) bool { return r.service == s }) {
			problems = append(problems, "no rows for service "+s+": every service in scope needs a reviewed entry")
		}
	}
	if !slices.ContainsFunc(rows, func(r row) bool { return strings.Trim(r.decision, "`") == "phi" }) {
		problems = append(problems, "the inventory lists no phi column")
	}
	if len(rows) < 40 {
		problems = append(problems, fmt.Sprintf("only %d rows parsed: the table format changed?", len(rows)))
	}
	if len(problems) > 0 {
		t.Fatalf("docs/ops/phi-field-inventory.md is out of step with the migrations:\n%s", strings.Join(problems, "\n"))
	}
}

func TestInventoryFlagsAColumnThatIsGone(t *testing.T) {
	md := "| Service | Table.column | Decision | Protected today | Action |\n|---|---|---|---|---|\n" +
		"| hr | `benefit_enrollment.option_code` | phi | RLS | tag |\n" +
		"| hr | `benefit_enrollment.renamed_away` | phi | RLS | tag |\n" +
		"| hr | `hr_incident.details_sealed` | phi | Sealed (fieldcrypt) | none |\n" +
		"| hr | `hr_incident.incident_type` | phi | Sealed (fieldcrypt) | none |\n" +
		"| hr | `nowhere.col` | maybe | RLS | tag |\n" +
		"| nowhere | `t.c` | phi | RLS | tag |\n"
	rows, parse := parseInventory(md)
	if len(parse) != 0 || len(rows) != 6 {
		t.Fatalf("parsed %d rows, problems %v", len(rows), parse)
	}
	columns := map[string]map[string]map[string]string{}
	hr := map[string]map[string]string{}
	parseColumns("CREATE TABLE benefit_enrollment (tenant_id uuid NOT NULL, option_code text NOT NULL CHECK (option_code ~ '^(a|b)$'), PRIMARY KEY (tenant_id, option_code));"+
		"create table hr_incident (tenant_id uuid not null, incident_type text, details_sealed bytea not null);"+
		"-- add column ignored_in_comment text\n"+
		"ALTER TABLE benefit_enrollment ADD COLUMN IF NOT EXISTS later_col int, ADD COLUMN other_col text;", hr)
	columns["hr"] = hr
	if hr["benefit_enrollment"]["later_col"] != "int" || hr["benefit_enrollment"]["other_col"] != "text" || hr["benefit_enrollment"]["option_code"] != "text" {
		t.Fatalf("parsed columns: %v", hr)
	}
	if _, junk := hr["benefit_enrollment"]["PRIMARY"]; junk {
		t.Fatal("a constraint is not a column")
	}
	got := strings.Join(problemsWith(rows, columns), "\n")
	for _, want := range []string{"renamed_away): the column does not exist", "incident_type): the row says it is sealed but the column type is text", "decision must be phi", "unknown service"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing problem %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "option_code") || strings.Contains(got, "details_sealed") {
		t.Errorf("valid rows must not be flagged:\n%s", got)
	}
}
