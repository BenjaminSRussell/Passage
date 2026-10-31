package store

// Wave 46 proof for AC2 (revenue in the semantic layer equals the GL to the
// cent each day) and AC3 (a user sees only their entity's data).
//
// The semantic side is the shipped artifacts, executed on Postgres: the dbt
// mart SQL (data/dbt/models/mart/fin_journal_line.sql) over core_* current-state
// tables loaded from testdata/fx-gl-revenue-recon.json, the Cube revenue
// measure SQL from data/cube/model/fin_journal_line.yml, the query plan from
// domain.BuildPlan, and the filters added by the real data/cube/cube.js
// queryRewrite (run with node). The GL side is
// testdata/fx-gl-revenue-recon.expected.json, produced by
// testdata/gl_revenue_reference.py (python3 -I) from the ledger's own
// debit/credit turnover rule, independently of the analytics code.
//
// Not covered here: the 02:00 job that runs this check every day (FR-46.13),
// Trino and a live Cube runtime, and the core_* latest-state dedupe.

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/analytics/internal/domain"
)

const (
	proofFixture  = "../../testdata/fx-gl-revenue-recon.json"
	proofExpected = "../../testdata/fx-gl-revenue-recon.expected.json"
	proofDataDir  = "../../../../data/"
	leT2Other     = "33333333-0000-4000-8000-000000000003"
)

type glFixture struct {
	Ledgers []struct {
		TenantID      string `json:"tenant_id"`
		ID            string `json:"id"`
		LegalEntityID string `json:"legal_entity_id"`
		ChartCode     string `json:"chart_code"`
		Currency      string `json:"currency"`
	} `json:"ledgers"`
	Accounts []struct {
		TenantID      string `json:"tenant_id"`
		ID            string `json:"id"`
		ChartCode     string `json:"chart_code"`
		AccountNumber string `json:"account_number"`
		AccountType   string `json:"account_type"`
	} `json:"accounts"`
	CategoryMap []glCategory `json:"category_map"`
	Entries     []struct {
		TenantID   string  `json:"tenant_id"`
		ID         string  `json:"id"`
		LedgerID   string  `json:"ledger_id"`
		Posting    string  `json:"posting_date"`
		PeriodKey  string  `json:"period_key"`
		ReversalOf *string `json:"reversal_of_entry_id"`
	} `json:"entries"`
	Lines []glLine `json:"lines"`
}

type glLine struct {
	TenantID      string  `json:"tenant_id"`
	ID            string  `json:"id"`
	EntryID       string  `json:"entry_id"`
	LegalEntityID string  `json:"legal_entity_id"`
	MainAccountID string  `json:"main_account_id"`
	DebitCredit   string  `json:"debit_credit"`
	AmountAcct    string  `json:"amount_acct"`
	Negative      bool    `json:"negative_posting"`
	ReversalOf    *string `json:"reversal_of_line_id"`
}

type glCategory struct {
	TenantID        string `json:"tenant_id"`
	ChartCode       string `json:"chart_code"`
	AccountNumber   string `json:"account_number"`
	AccountCategory string `json:"account_category"`
}

type glDay struct {
	TenantID      string `json:"tenantId"`
	LegalEntityID string `json:"legalEntityId"`
	Date          string `json:"date"`
	Currency      string `json:"currency"`
	GLRevenue     string `json:"glRevenue"`
}

func readJSON(t *testing.T, path string, v any) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, v); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
}

func requireNode(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not on PATH; the proof runs the real cube.js queryRewrite")
	}
}

// buildLake loads the fixture into core_* tables in the test schema and builds
// the mart with the dbt model's own SQL. Identifiers are text, as in the CDC
// landing tables and the varchar seed.
func buildLake(t *testing.T, pool *pgxpool.Pool, fx glFixture) {
	t.Helper()
	ctx := context.Background()
	ddl := `
create table core_ledger (tenant_id text, id text, legal_entity_id text, code text, chart_code text, currency text);
create table core_journal_entry (tenant_id text, id text, legal_entity_id text, ledger_id text, posting_date date,
  period_key text, reversal_of_entry_id text);
create table core_main_account (tenant_id text, id text, chart_code text, account_number text, name text, account_type text);
create table core_journal_line (tenant_id text, id text, legal_entity_id text, entry_id text, main_account_id text,
  debit_credit char(1), amount_acct numeric(20,6), negative_posting boolean, reversal_of_line_id text, late boolean not null default false);
create table account_category_map (tenant_id varchar, chart_code varchar, account_number varchar, account_category varchar);`
	if _, err := pool.Exec(ctx, ddl); err != nil {
		t.Fatal(err)
	}
	run := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	for _, l := range fx.Ledgers {
		run(`insert into core_ledger values ($1,$2,$3,'L',$4,$5)`, l.TenantID, l.ID, l.LegalEntityID, l.ChartCode, l.Currency)
	}
	for _, a := range fx.Accounts {
		run(`insert into core_main_account values ($1,$2,$3,$4,$4,$5)`, a.TenantID, a.ID, a.ChartCode, a.AccountNumber, a.AccountType)
	}
	for _, m := range fx.CategoryMap {
		run(`insert into account_category_map values ($1,$2,$3,$4)`, m.TenantID, m.ChartCode, m.AccountNumber, m.AccountCategory)
	}
	for _, e := range fx.Entries {
		run(`insert into core_journal_entry values ($1,$2,null,$3,$4::date,$5,$6)`, e.TenantID, e.ID, e.LedgerID, e.Posting, e.PeriodKey, e.ReversalOf)
	}
	for _, l := range fx.Lines {
		run(`insert into core_journal_line values ($1,$2,$3,$4,$5,$6,$7::numeric,$8,$9,false)`,
			l.TenantID, l.ID, l.LegalEntityID, l.EntryID, l.MainAccountID, l.DebitCredit, l.AmountAcct, l.Negative, l.ReversalOf)
	}
	run(`create table mart_fin_journal_line as ` + renderMartSQL(t))
}

var dbtRef = regexp.MustCompile(`\{\{\s*ref\('(\w+)'\)\s*\}\}`)

// renderMartSQL renders the dbt mart model: each {{ ref('x') }} becomes table x.
// Any other Jinja would mean the model changed shape, so it fails the test.
func renderMartSQL(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(proofDataDir + "dbt/models/mart/fin_journal_line.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := dbtRef.ReplaceAllString(string(raw), "$1")
	if strings.Contains(sql, "{{") || strings.Contains(sql, "{%") {
		t.Fatal("fin_journal_line.sql has Jinja other than ref(); extend renderMartSQL")
	}
	return sql
}

// cubeMeasure reads one measure of the Cube model: its type, its SQL and its
// filter SQL, with {CUBE} bound to alias m.
func cubeMeasure(t *testing.T, name string) (typ, expr, filter string) {
	t.Helper()
	f, err := os.Open(proofDataDir + "cube/model/fin_journal_line.yml")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	section, in := "", false
	item := regexp.MustCompile(`^\s{6}- name: (\w+)\s*$`)
	field := regexp.MustCompile(`^\s{8}(sql|type): (.+)$`)
	filterSQL := regexp.MustCompile(`^\s{10}- sql: (.+)$`)
	unquote := func(s string) string {
		if u, err := strconv.Unquote(strings.TrimSpace(s)); err == nil {
			return u
		}
		return strings.TrimSpace(s)
	}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if strings.TrimSpace(line) == "measures:" || strings.TrimSpace(line) == "dimensions:" {
			section = strings.TrimSpace(line)
		}
		if m := item.FindStringSubmatch(line); m != nil {
			in = section == "measures:" && m[1] == name
			continue
		}
		if !in {
			continue
		}
		if m := field.FindStringSubmatch(line); m != nil {
			if m[1] == "sql" {
				expr = unquote(m[2])
			} else {
				typ = unquote(m[2])
			}
		}
		if m := filterSQL.FindStringSubmatch(line); m != nil {
			filter = unquote(m[1])
		}
	}
	if expr == "" || typ == "" {
		t.Fatalf("Cube measure %s not found", name)
	}
	bind := func(s string) string { return strings.ReplaceAll(s, "{CUBE}", "m") }
	return typ, bind(expr), bind(filter)
}

type cubeQuery struct {
	Measures       []string        `json:"measures"`
	Dimensions     []string        `json:"dimensions"`
	TimeDimensions []cubeTimeDim   `json:"timeDimensions"`
	Filters        []domain.Filter `json:"filters"`
}

type cubeTimeDim struct {
	Dimension string   `json:"dimension"`
	DateRange []string `json:"dateRange"`
}

type securityContext struct {
	TenantID       string   `json:"tenantId"`
	LegalEntityIDs []string `json:"legalEntityIds,omitempty"`
	Unrestricted   bool     `json:"unrestricted,omitempty"`
}

// rewrite runs data/cube/cube.js queryRewrite on q. A thrown error comes back
// as its message.
func rewrite(t *testing.T, q cubeQuery, sc securityContext) (cubeQuery, string) {
	t.Helper()
	cubeJS, err := filepath.Abs(proofDataDir + "cube/cube.js")
	if err != nil {
		t.Fatal(err)
	}
	in, _ := json.Marshal(map[string]any{"query": q, "securityContext": sc})
	script := `const { queryRewrite } = require(process.argv[1]); let s = "";
process.stdin.on("data", (d) => (s += d)).on("end", () => {
  const { query, securityContext } = JSON.parse(s);
  try { process.stdout.write(JSON.stringify({ query: queryRewrite(query, { securityContext }) })); }
  catch (e) { process.stdout.write(JSON.stringify({ error: e.message })); }
});`
	cmd := exec.Command("node", "-e", script, cubeJS)
	cmd.Stdin = bytes.NewReader(in)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("node queryRewrite: %v", err)
	}
	var res struct {
		Query cubeQuery `json:"query"`
		Error string    `json:"error"`
	}
	if err := json.Unmarshal(out, &res); err != nil {
		t.Fatalf("queryRewrite output %q: %v", out, err)
	}
	return res.Query, res.Error
}

// cubeColumns are the fin_journal_line dimensions a filter may name.
var cubeColumns = []string{"tenant_id", "legal_entity_id", "account_code", "account_category", "currency", "fiscal_period"}

// runRevenue executes a rewritten revenue query the way Cube compiles a sum
// measure with a filter: SUM(CASE WHEN filter THEN sql END), grouped by the
// legal entity, over the posting-date range, with every rewritten filter as an
// equals (IN) predicate. A nil value is Cube's NULL: no matching line.
func runRevenue(t *testing.T, pool *pgxpool.Pool, q cubeQuery) map[string]*decimal.Decimal {
	t.Helper()
	typ, expr, filter := cubeMeasure(t, "revenue")
	if typ != "sum" || filter == "" {
		t.Fatalf("revenue measure is %s with filter %q; runRevenue compiles a filtered sum", typ, filter)
	}
	if !slices.Equal(q.Measures, []string{"fin_journal_line.revenue"}) ||
		!slices.Equal(q.Dimensions, []string{"fin_journal_line.legal_entity_id"}) || len(q.TimeDimensions) != 1 {
		t.Fatalf("unexpected query shape %+v", q)
	}
	td := q.TimeDimensions[0]
	args := []any{td.DateRange[0], td.DateRange[1]}
	where := []string{"m.posting_date between $1::date and $2::date"}
	for _, f := range q.Filters {
		col, ok := strings.CutPrefix(f.Member, "fin_journal_line.")
		if !ok || f.Operator != "equals" || !slices.Contains(cubeColumns, col) {
			t.Fatalf("filter %+v is not an equals filter on a fin_journal_line dimension", f)
		}
		args = append(args, f.Values)
		where = append(where, fmt.Sprintf("m.%s::text = any($%d::text[])", col, len(args)))
	}
	sql := fmt.Sprintf(`select m.legal_entity_id::text, sum(case when (%s) then (%s) end)::text
from mart_fin_journal_line m where %s group by 1 order by 1`, filter, expr, strings.Join(where, " and "))
	rows, err := pool.Query(context.Background(), sql, args...)
	if err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
	defer rows.Close()
	out := map[string]*decimal.Decimal{}
	for rows.Next() {
		var le string
		var v *string
		if err := rows.Scan(&le, &v); err != nil {
			t.Fatal(err)
		}
		if v == nil {
			out[le] = nil
			continue
		}
		d := decimal.RequireFromString(*v)
		out[le] = &d
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// scopedRevenue plans a revenue-by-legal-entity query for principal p over
// [from, to], passes it through cube.js with the security context the scope
// maps to, and runs it. NO_SCOPE plans return reason and no rows.
func scopedRevenue(t *testing.T, pool *pgxpool.Pool, p tenant.Principal, from, to string) (map[string]*decimal.Decimal, string) {
	t.Helper()
	f, _ := time.Parse(time.DateOnly, from)
	e, _ := time.Parse(time.DateOnly, to)
	scope := domain.ScopeFromPrincipal(p)
	plan, perr := domain.BuildPlan(domain.Query{Measures: []string{"revenue"}, Dimensions: []string{"legal_entity_id"}, From: f, To: e},
		domain.GovernedMetrics(), scope)
	if perr != nil {
		t.Fatal(perr)
	}
	q := cubeQuery{
		Measures:       []string{plan.Model + ".revenue"},
		Dimensions:     []string{plan.Model + ".legal_entity_id"},
		TimeDimensions: []cubeTimeDim{{Dimension: plan.Model + ".posting_date", DateRange: []string{plan.From, plan.To}}},
		Filters:        plan.Filters,
	}
	sc := securityContext{TenantID: p.TenantID, LegalEntityIDs: scope.LegalEntityIDs, Unrestricted: scope.Unrestricted}
	rewritten, rerr := rewrite(t, q, sc)
	if plan.Reason == domain.ReasonNoScope {
		if !strings.Contains(rerr, "ANA-0102") {
			t.Fatalf("NO_SCOPE plan: cube.js returned %q, want ANA-0102", rerr)
		}
		return nil, plan.Reason
	}
	if rerr != "" {
		t.Fatalf("queryRewrite: %s", rerr)
	}
	if !slices.ContainsFunc(rewritten.Filters, func(f domain.Filter) bool {
		return f.Member == "fin_journal_line.tenant_id" && slices.Equal(f.Values, []string{p.TenantID})
	}) {
		t.Fatalf("rewritten query has no tenant filter: %+v", rewritten.Filters)
	}
	return runRevenue(t, pool, rewritten), plan.Reason
}

// jobPrincipal is the reconciliation job for one tenant: no legal entity, the
// all-scope duty (whole tenant).
func jobPrincipal(tenantID string) tenant.Principal {
	return tenant.Principal{TenantID: tenantID, UserID: "recon-job", Duties: testkit.ScopeDuties("")}
}

// martRevenue applies the Go catalog formula (domain.Revenue) to the mart rows
// of one tenant, entity and day.
func martRevenue(t *testing.T, pool *pgxpool.Pool, tenantID, le, date string) decimal.Decimal {
	t.Helper()
	rows, err := pool.Query(context.Background(), `select account_category, status, amount_reporting::text from mart_fin_journal_line
where tenant_id = $1 and legal_entity_id = $2 and posting_date = $3::date`, tenantID, le, date)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var lines []domain.JournalLine
	for rows.Next() {
		var l domain.JournalLine
		var amt string
		if err := rows.Scan(&l.AccountCategory, &l.Status, &amt); err != nil {
			t.Fatal(err)
		}
		l.Amount = decimal.RequireFromString(amt)
		lines = append(lines, l)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return domain.Revenue(lines)
}

// dailyCheck runs the semantic side for every (tenant, day) the GL reference
// lists and returns the finalized reconciliation per (tenant, entity, day).
func dailyCheck(t *testing.T, pool *pgxpool.Pool, gl []glDay) (map[string]domain.Reconciliation, int) {
	t.Helper()
	out := map[string]domain.Reconciliation{}
	nullDays := 0
	byDay := map[[2]string][]glDay{}
	for _, g := range gl {
		k := [2]string{g.TenantID, g.Date}
		byDay[k] = append(byDay[k], g)
	}
	for k, days := range byDay {
		semantic, _ := scopedRevenue(t, pool, jobPrincipal(k[0]), k[1], k[1])
		if len(semantic) != len(days) {
			t.Errorf("tenant %s on %s: semantic layer returned entities %v, GL has %d", k[0], k[1], keys(semantic), len(days))
		}
		for _, g := range days {
			v, ok := semantic[g.LegalEntityID]
			if !ok {
				t.Errorf("%s/%s/%s: no semantic row", g.TenantID, g.LegalEntityID, g.Date)
				continue
			}
			value := decimal.Zero
			if v == nil {
				nullDays++ // Cube's SUM over no revenue line is NULL; the check reads it as zero.
			} else {
				value = *v
			}
			if formula := martRevenue(t, pool, g.TenantID, g.LegalEntityID, g.Date); !formula.Equal(value) {
				t.Errorf("%s/%s: Cube measure %s, Go formula %s", g.LegalEntityID, g.Date, value, formula)
			}
			r, err := domain.FinalizeReconciliation(domain.Reconciliation{MetricID: "revenue", LegalEntityID: g.LegalEntityID, Date: g.Date,
				Currency: g.Currency, SemanticValue: value, SourceValue: decimal.RequireFromString(g.GLRevenue), Tolerance: domain.DefaultTolerance()})
			if err != nil {
				t.Fatal(err)
			}
			out[g.TenantID+"/"+g.LegalEntityID+"/"+g.Date] = r
		}
	}
	return out, nullDays
}

func keys(m map[string]*decimal.Decimal) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

// The committed GL reference must be what the independent script produces from
// the committed fixture.
func TestRevenueReconciliation_ReferenceMatchesFixture(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not on PATH")
	}
	out, err := exec.Command("python3", "-I", "../../testdata/gl_revenue_reference.py", proofFixture).Output()
	if err != nil {
		t.Fatalf("gl_revenue_reference.py: %v", err)
	}
	want, err := os.ReadFile(proofExpected)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(out, want) {
		t.Fatalf("%s is stale; regenerate it with gl_revenue_reference.py", proofExpected)
	}
}

func TestRevenueReconciliation_SemanticLayerEqualsGLToTheCent(t *testing.T) {
	requireNode(t)
	pool := migrated(t)
	var fx glFixture
	var gl []glDay
	readJSON(t, proofFixture, &fx)
	readJSON(t, proofExpected, &gl)
	buildLake(t, pool, fx)

	// Hand-derived anchors, independent of the reference script:
	// USMF 10-29: credit 1000.00 - sales-return debit 120.25 = 879.75.
	// USMF 10-30: 0.004 + 0.004 + 333.333333 - storno 50.00 = 283.341333.
	// USMF 11-02: reversal of the 10-29 invoice, -1000.00 (10-29 is not re-dated).
	// USMF 11-03: expense only, 0. DEMF 10-30: -0.01 + storno 200.00 = 199.99.
	// Tenant 2 reuses the USMF id: 7777.77, never added to tenant 1.
	anchors := map[string]string{
		testkit.TenantID(1) + "/" + leUSMF + "/2026-10-29": "879.75",
		testkit.TenantID(1) + "/" + leUSMF + "/2026-10-30": "283.341333",
		testkit.TenantID(1) + "/" + leUSMF + "/2026-11-02": "-1000",
		testkit.TenantID(1) + "/" + leUSMF + "/2026-11-03": "0",
		testkit.TenantID(1) + "/" + leDEMF + "/2026-10-30": "199.99",
		testkit.TenantID(2) + "/" + leUSMF + "/2026-10-29": "7777.77",
	}
	seen := 0
	for _, g := range gl {
		if want, ok := anchors[g.TenantID+"/"+g.LegalEntityID+"/"+g.Date]; ok {
			seen++
			if !decimal.RequireFromString(g.GLRevenue).Equal(decimal.RequireFromString(want)) {
				t.Fatalf("reference GL %s/%s = %s, hand-derived %s", g.LegalEntityID, g.Date, g.GLRevenue, want)
			}
		}
	}
	if seen != len(anchors) || len(gl) != 9 {
		t.Fatalf("reference has %d days and %d of %d anchors; regenerate it with gl_revenue_reference.py", len(gl), seen, len(anchors))
	}

	results, nullDays := dailyCheck(t, pool, gl)
	if nullDays != 1 {
		t.Errorf("days with no revenue line = %d, want 1 (USMF 2026-11-03)", nullDays)
	}
	st := New(pool)
	for _, g := range gl {
		r := results[g.TenantID+"/"+g.LegalEntityID+"/"+g.Date]
		if r.Status != domain.ReconOK || !r.Delta.IsZero() {
			t.Errorf("%s/%s/%s: semantic %s vs GL %s: delta %s status %s, want exact match",
				g.TenantID, g.LegalEntityID, g.Date, r.SemanticValue, r.SourceValue, r.Delta, r.Status)
		}
		// The store records the day for the tenant and derives the same outcome.
		ctx := tenant.With(context.Background(), jobPrincipal(g.TenantID))
		rec, _, err := st.RecordReconciliation(ctx, r)
		if err != nil || rec.Status != domain.ReconOK || !rec.Delta.IsZero() {
			t.Errorf("record %s/%s: %+v %v", g.LegalEntityID, g.Date, rec, err)
		}
	}
	rows, err := st.ListReconciliations(tenant.With(context.Background(), jobPrincipal(testkit.TenantID(1))), "2026-10-30", "revenue")
	if err != nil || len(rows) != 2 {
		t.Fatalf("tenant 1 on 2026-10-30: %+v %v", rows, err)
	}
}

// The check must catch a one-cent divergence and a misclassified account, and
// flag only the affected days.
func TestRevenueReconciliation_FlagsOneCentDriftAndMisclassifiedAccount(t *testing.T) {
	requireNode(t)
	var gl []glDay
	readJSON(t, proofExpected, &gl)
	t1 := testkit.TenantID(1)
	tests := []struct {
		name   string
		mutate func(*glFixture)
		failed map[string]string // key -> expected delta (semantic - GL)
	}{
		{
			name: "CDC lost the one-cent revenue debit",
			mutate: func(fx *glFixture) {
				fx.Lines = slices.DeleteFunc(fx.Lines, func(l glLine) bool { return l.ID == "dddd0000-0000-4000-8000-000000000026" })
			},
			failed: map[string]string{t1 + "/" + leDEMF + "/2026-10-30": "0.01"},
		},
		{
			name: "revenue account 401200 mapped to COGS",
			mutate: func(fx *glFixture) {
				fx.CategoryMap = append(fx.CategoryMap, glCategory{TenantID: t1, ChartCode: "STD", AccountNumber: "401200", AccountCategory: "COGS"})
			},
			// USMF 10-30 semantic keeps only the -50.00 storno; DEMF 10-30 only the -0.01.
			failed: map[string]string{
				t1 + "/" + leUSMF + "/2026-10-30": "-333.341333",
				t1 + "/" + leDEMF + "/2026-10-30": "-200.00",
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			pool := migrated(t)
			var fx glFixture
			readJSON(t, proofFixture, &fx)
			tc.mutate(&fx)
			buildLake(t, pool, fx)
			results, _ := dailyCheck(t, pool, gl)
			if len(results) != len(gl) {
				t.Fatalf("checked %d days, want %d", len(results), len(gl))
			}
			for k, r := range results {
				want, bad := tc.failed[k]
				switch {
				case bad && (r.Status != domain.ReconFailed || !r.Delta.Equal(decimal.RequireFromString(want))):
					t.Errorf("%s: delta %s status %s, want FAILED with delta %s", k, r.Delta, r.Status, want)
				case !bad && (r.Status != domain.ReconOK || !r.Delta.IsZero()):
					t.Errorf("%s: delta %s status %s, want OK", k, r.Delta, r.Status)
				}
			}
		})
	}
}

// AC3 at the data layer: the revenue query a dashboard tile issues, planned for
// the caller and rewritten by cube.js, returns only the caller's entities of
// the caller's tenant, even where another tenant reuses the legal-entity id.
func TestRevenueQuery_ScopedUserSeesOnlyOwnEntityAndTenant(t *testing.T) {
	requireNode(t)
	pool := migrated(t)
	var fx glFixture
	readJSON(t, proofFixture, &fx)
	buildLake(t, pool, fx)
	t1, t2 := testkit.TenantID(1), testkit.TenantID(2)
	scoped := func(tenantID, le string) tenant.Principal {
		return tenant.Principal{TenantID: tenantID, LegalEntityID: le, UserID: "viewer", Duties: map[string]struct{}{}}
	}
	// Hand-derived October-November totals: USMF 879.75 + 283.341333 +
	// 12345678901.23 - 1000.00 + 0 = 12345679064.321333; DEMF 500.00 + 199.99.
	const usmfT1, demfT1 = "12345679064.321333", "699.99"
	tests := []struct {
		name string
		p    tenant.Principal
		want map[string]string
	}{
		{"DEMF user in tenant 1", scoped(t1, leDEMF), map[string]string{leDEMF: demfT1}},
		{"USMF user in tenant 1", scoped(t1, leUSMF), map[string]string{leUSMF: usmfT1}},
		{"USMF user in tenant 2 (same entity id)", scoped(t2, leUSMF), map[string]string{leUSMF: "7777.77"}},
		{"whole-tenant user in tenant 1", jobPrincipal(t1), map[string]string{leUSMF: usmfT1, leDEMF: demfT1}},
		{"whole-tenant user in tenant 2", jobPrincipal(t2), map[string]string{leUSMF: "7777.77", leT2Other: "1"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, reason := scopedRevenue(t, pool, tc.p, "2026-10-01", "2026-11-30")
			if reason != "" {
				t.Fatalf("reason %s", reason)
			}
			if !slices.Equal(keys(got), slices.Sorted(maps.Keys(tc.want))) {
				t.Fatalf("entities %v, want %v", keys(got), tc.want)
			}
			for le, want := range tc.want {
				if got[le] == nil || !got[le].Equal(decimal.RequireFromString(want)) {
					t.Errorf("%s revenue %v, want %s", le, got[le], want)
				}
			}
		})
	}
	t.Run("user with no entity and no all-scope duty gets NO_SCOPE", func(t *testing.T) {
		got, reason := scopedRevenue(t, pool, scoped(t1, ""), "2026-10-01", "2026-11-30")
		if reason != domain.ReasonNoScope || got != nil {
			t.Fatalf("got %v reason %q, want NO_SCOPE and no rows", got, reason)
		}
	})
}
