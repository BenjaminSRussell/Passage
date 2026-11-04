package store

import (
	"context"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"

	"github.com/BenjaminSRussell/passage/services/finreport/internal/domain"
	"github.com/BenjaminSRussell/passage/services/finreport/internal/events"
	"github.com/BenjaminSRussell/passage/services/finreport/migrations"
)

func profitCenterTree() domain.Tree {
	return domain.Tree{Code: "PC-TREE", Name: "Profit centers", Attribute: "profit_center", UpdatedBy: "u1", Units: []domain.TreeUnit{
		{Code: "ALL", Name: "Group"},
		{Code: "EMEA", Name: "EMEA", Parent: "ALL"},
		{Code: "DE", Name: "Germany", Parent: "EMEA", Values: []string{"PC10", "PC11"}},
		{Code: "FR", Name: "France", Parent: "EMEA", Values: []string{"PC20"}},
		{Code: "US", Name: "United States", Parent: "ALL", Values: []string{"PC30"}},
	}}
}

// FR-15.4: a tree is created without If-Match and replaced with the current
// revision (428 when missing, 412 when stale); another tenant sees none.
func TestTree_SaveReplaceAndTenantIsolation(t *testing.T) {
	admin, app := testkit.DBWithApp(t)
	ctx := testkit.Ctx(testkit.TenantID(1))
	if err := dbx.Migrate(ctx, admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	st := New(app)
	tr := profitCenterTree()
	if _, rev, err := st.SaveTree(ctx, tr, nil); err != nil || rev != 1 {
		t.Fatalf("create: rev %d, %v", rev, err)
	}
	tr.Name = "Profit centers by region"
	if _, _, err := st.SaveTree(ctx, tr, nil); problemCode(err) != "precondition_required" {
		t.Fatalf("replace without If-Match = %v, want 428", err)
	}
	stale := 4
	if _, _, err := st.SaveTree(ctx, tr, &stale); problemCode(err) != CodeRevisionConflict {
		t.Fatalf("replace with a stale If-Match = %v, want %s", err, CodeRevisionConflict)
	}
	one := 1
	if _, rev, err := st.SaveTree(ctx, tr, &one); err != nil || rev != 2 {
		t.Fatalf("replace: rev %d, %v", rev, err)
	}
	got, rev, err := st.GetTree(ctx, "PC-TREE")
	if err != nil || rev != 2 || got.Name != "Profit centers by region" || len(got.Units) != 5 || got.Units[2].Values[1] != "PC11" || got.UpdatedBy != "u1" {
		t.Fatalf("get: %+v rev %d, %v", got, rev, err)
	}
	if _, _, err := st.GetTree(ctx, "MISSING"); problemCode(err) != domain.CodePartNotFound {
		t.Fatalf("missing tree = %v, want %s", err, domain.CodePartNotFound)
	}
	other := testkit.Ctx(testkit.TenantID(2))
	if list, err := st.ListTrees(other); err != nil || len(list) != 0 {
		t.Fatalf("other tenant lists %+v, %v; want nothing", list, err)
	}
	if list, err := st.ListTrees(ctx); err != nil || len(list) != 1 {
		t.Fatalf("list = %+v, %v", list, err)
	}
}

// seedDimBalanceRow adds a read-model row that carries dimensions, and moves the high-water mark.
func seedDimBalanceRow(t *testing.T, st *Postgres, ctx context.Context, ledger, account, combination, dims, net string, hwm int64) {
	t.Helper()
	err := dbx.WithTenantTx(ctx, st.pool, func(tx pgx.Tx) error {
		p, _ := tenant.FromContext(ctx)
		if _, err := tx.Exec(ctx, `insert into reporting_balance(tenant_id, ledger_id, period_year, period_no, quarter, account, combination, dims, currency_type, net, legal_entity_id)
			values($1,$2,2026,3,1,$3,$4,$5::jsonb,'acct',$6::numeric,nullif($7,'')::uuid)`, p.TenantID, ledger, account, combination, dims, net, p.LegalEntityID); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `insert into reporting_hwm(tenant_id, ledger_id, posting_sequence, legal_entity_id) values($1,$2,$3,nullif($4,'')::uuid)
			on conflict (tenant_id, ledger_id) do update set posting_sequence = excluded.posting_sequence`, p.TenantID, ledger, hwm, p.LegalEntityID)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

// FR-15.4: a run limited to a unit keeps the balances of the values the unit
// and the units below it list, so a parent is the roll-up of its children; the
// run records the tree revision, a whole-tree run warns about values outside
// the tree, and the cells still drill.
func TestCreateRun_LimitedToTreeUnits(t *testing.T) {
	st, ctx := migrated(t)
	if _, _, err := st.SaveTree(ctx, profitCenterTree(), nil); err != nil {
		t.Fatal(err)
	}
	def := storeFixtureDefinition("PL-TREE")
	def.Rows = def.Rows[:1]
	def.Rows[0].AccountFrom, def.Rows[0].AccountTo = "4000", "4999"
	if _, _, err := st.SaveDraft(ctx, def, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Publish(ctx, "PL-TREE", "tester"); err != nil {
		t.Fatal(err)
	}
	ledger := ids.New()
	for i, b := range []struct{ pc, net string }{{"PC10", "100"}, {"PC11", "20"}, {"PC20", "7"}, {"PC30", "3"}, {"PC99", "1000"}, {"", "5000"}} {
		combo := "pc" + string(rune('a'+i))
		dims := `{}`
		if b.pc != "" {
			dims = `{"profit_center":"` + b.pc + `"}`
		}
		seedDimBalanceRow(t, st, ctx, ledger, "4000", combo, dims, b.net, int64(i+1))
	}
	run := func(key string, tr *TreeRun) Run {
		t.Helper()
		r, err := st.CreateRun(ctx, RunSpec{Code: "PL-TREE", LedgerID: ledger, CurrencyType: "acct", PeriodYear: 2026, PeriodNo: 3,
			Calendar: storeFixtureCalendar(), RequestedBy: "tester", IdempotencyKey: key, RequestHash: key, Tree: tr})
		if err != nil {
			t.Fatalf("%s: %v", key, err)
		}
		return r
	}
	value := func(r Run) decimal.Decimal {
		t.Helper()
		cells, err := st.ListCells(ctx, r.ID)
		if err != nil || len(cells) == 0 {
			t.Fatalf("cells of %s: %v", r.ID, err)
		}
		for _, c := range cells {
			if err := c.CheckDrill(); err != nil {
				t.Fatalf("drill %s: %v", c.Row, err)
			}
		}
		return cells[0].Value
	}
	for _, c := range []struct {
		unit, want string
		checks     int
	}{{"DE", "120", 0}, {"FR", "7", 0}, {"EMEA", "127", 0}, {"ALL", "130", 0}, {"", "130", 1}} {
		r := run("u-"+c.unit, &TreeRun{Code: "PC-TREE", Unit: c.unit})
		if !value(r).Equal(decimal.RequireFromString(c.want)) {
			t.Errorf("unit %q = %s, want %s", c.unit, value(r), c.want)
		}
		if r.Tree == nil || r.Tree.Code != "PC-TREE" || r.Tree.Unit != c.unit || r.Tree.Revision != 1 {
			t.Errorf("unit %q run tree = %+v", c.unit, r.Tree)
		}
		if len(r.Checks) != c.checks {
			t.Errorf("unit %q checks = %+v, want %d", c.unit, r.Checks, c.checks)
		}
	}
	whole := run("u-", &TreeRun{Code: "PC-TREE"})
	if len(whole.Checks) != 1 || whole.Checks[0].Code != domain.CodeTreeOutside || !strings.Contains(whole.Checks[0].Detail, "PC99") {
		t.Fatalf("whole-tree checks = %+v, want REPORT-0041 naming PC99", whole.Checks)
	}
	stored, err := st.GetRun(ctx, whole.ID)
	if err != nil || stored.Tree == nil || stored.Tree.Revision != 1 || stored.Tree.Unit != "" {
		t.Fatalf("stored run = %+v, %v", stored, err)
	}
	// Without a tree the run still covers every balance and records none.
	plain := run("plain", nil)
	if plain.Tree != nil || !value(plain).Equal(decimal.RequireFromString("6130")) {
		t.Fatalf("plain run = %+v value %s", plain.Tree, value(plain))
	}
	// The key replays; the event names the tree.
	if again := run("u-DE", &TreeRun{Code: "PC-TREE", Unit: "DE"}); again.Tree == nil || again.Tree.Unit != "DE" {
		t.Fatalf("replay = %+v", again)
	}
	if n := countType(outboxTypes(t, ctx, st.pool), events.StatementGenerated); n != 6 {
		t.Fatalf("statement.generated events = %d, want 6", n)
	}
	// A later change of the tree does not alter the stored run's revision.
	tr := profitCenterTree()
	tr.Name = "Renamed"
	one := 1
	if _, _, err := st.SaveTree(ctx, tr, &one); err != nil {
		t.Fatal(err)
	}
	if r := run("u-after", &TreeRun{Code: "PC-TREE", Unit: "DE"}); r.Tree.Revision != 2 {
		t.Fatalf("run after the change used revision %d, want 2", r.Tree.Revision)
	}
	if got, _ := st.GetRun(ctx, whole.ID); got.Tree.Revision != 1 {
		t.Fatalf("stored run revision changed to %d", got.Tree.Revision)
	}
}

func TestCreateRun_TreeRefusals(t *testing.T) {
	st, ctx := migrated(t)
	if _, _, err := st.SaveTree(ctx, profitCenterTree(), nil); err != nil {
		t.Fatal(err)
	}
	def := storeFixtureDefinition("PL-REF")
	if _, _, err := st.SaveDraft(ctx, def, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Publish(ctx, "PL-REF", "tester"); err != nil {
		t.Fatal(err)
	}
	ledger := ids.New()
	seedDimBalanceRow(t, st, ctx, ledger, "1000", "c", `{"profit_center":"PC10"}`, "5", 1)
	for name, tr := range map[string]*TreeRun{"unknown tree": {Code: "NOPE"}, "unknown unit": {Code: "PC-TREE", Unit: "XX"}} {
		_, err := st.CreateRun(ctx, RunSpec{Code: "PL-REF", LedgerID: ledger, CurrencyType: "acct", PeriodYear: 2026, PeriodNo: 3,
			Calendar: storeFixtureCalendar(), RequestedBy: "tester", IdempotencyKey: name, RequestHash: name, Tree: tr})
		if problemCode(err) != domain.CodeTreeRun {
			t.Errorf("%s = %v, want %s", name, err, domain.CodeTreeRun)
		}
	}
	// Nothing was stored for a refused run.
	if runs, err := st.ListRuns(ctx, "", 10); err != nil || len(runs) != 0 {
		t.Fatalf("runs after refusals = %+v, %v", runs, err)
	}
}

// FR-15.4: a parent unit's calculated rows are recomputed over the rolled-up
// balances, never summed from the children (a margin is not the sum of the
// children's margins), and a whole-tree run over a hierarchy keeps the tree's
// REPORT-0041 warning next to the hierarchy's checks.
func TestCreateRun_TreeRollUpRecomputesCalculationsAndKeepsWarningsWithAHierarchy(t *testing.T) {
	st, ctx := migrated(t)
	if _, _, err := st.SaveTree(ctx, profitCenterTree(), nil); err != nil {
		t.Fatal(err)
	}
	def := storeFixtureDefinition("PL-MARGIN")
	def.Rows = []domain.Row{
		{Code: "REV", Kind: domain.RowAccountRange, Label: "Revenue", AccountFrom: "4000", AccountTo: "4999", NormalBalance: "debit"},
		{Code: "COST", Kind: domain.RowAccountRange, Label: "Cost", AccountFrom: "5000", AccountTo: "5999", NormalBalance: "debit"},
		{Code: "MARGIN", Kind: domain.RowCalculation, Label: "Cost ratio %", Expression: "COST/REV*100", NormalBalance: "debit"},
	}
	if _, _, err := st.SaveDraft(ctx, def, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Publish(ctx, "PL-MARGIN", "tester"); err != nil {
		t.Fatal(err)
	}
	ledger := ids.New()
	seq := int64(0)
	for _, b := range []struct{ pc, account, net string }{
		{"PC10", "4000", "100"}, {"PC10", "5000", "50"}, // DE: 50 %
		{"PC20", "4000", "300"}, {"PC20", "5000", "30"}, // FR: 10 %
		{"PC99", "4000", "1"}, // outside the tree
	} {
		seq++
		seedDimBalanceRow(t, st, ctx, ledger, b.account, "c"+b.pc+b.account, `{"profit_center":"`+b.pc+`"}`, b.net, seq)
	}
	run := func(key string, tr *TreeRun, h *HierarchyRun) Run {
		t.Helper()
		r, err := st.CreateRun(ctx, RunSpec{Code: "PL-MARGIN", LedgerID: ledger, CurrencyType: "acct", PeriodYear: 2026, PeriodNo: 3,
			Calendar: storeFixtureCalendar(), RequestedBy: "tester", IdempotencyKey: key, RequestHash: key, Tree: tr, Hierarchy: h})
		if err != nil {
			t.Fatalf("%s: %v", key, err)
		}
		return r
	}
	cell := func(r Run, row string) decimal.Decimal {
		t.Helper()
		cells, err := st.ListCells(ctx, r.ID)
		if err != nil {
			t.Fatal(err)
		}
		for _, c := range cells {
			if c.Row == row {
				return c.Value
			}
		}
		t.Fatalf("run %s has no row %s", r.ID, row)
		return decimal.Zero
	}
	for unit, want := range map[string]string{"DE": "50", "FR": "10", "EMEA": "20"} {
		if got := cell(run("m-"+unit, &TreeRun{Code: "PC-TREE", Unit: unit}, nil), "MARGIN"); !got.Equal(decimal.RequireFromString(want)) {
			t.Errorf("unit %s margin = %s, want %s (80 / 400 for EMEA, not 50 + 10)", unit, got, want)
		}
	}

	h := domain.Hierarchy{Code: "PL-GRP", Name: "P&L groups", Subject: domain.SubjectAccount, Nodes: []domain.HierarchyNode{
		{Code: "REVENUE", Label: "Revenue", Rules: []domain.Rule{{Values: []string{"4*"}}}},
		{Code: "COSTS", Label: "Costs", Rules: []domain.Rule{{Values: []string{"5*"}}}},
	}}
	if _, _, err := st.SaveHierarchy(ctx, h, nil, "owner"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.PublishHierarchy(ctx, "PL-GRP", "publisher", []domain.Member{{ID: "4000", Key: "4000"}, {ID: "5000", Key: "5000"}}); err != nil {
		t.Fatal(err)
	}
	both := run("m-hier", &TreeRun{Code: "PC-TREE"}, &HierarchyRun{Code: "PL-GRP"})
	if both.Hierarchy == nil || both.Tree == nil {
		t.Fatalf("run over tree and hierarchy = %+v", both)
	}
	found := false
	for _, c := range both.Checks {
		found = found || (c.Code == domain.CodeTreeOutside && strings.Contains(c.Detail, "PC99"))
	}
	if !found {
		t.Fatalf("checks = %+v, want REPORT-0041 naming PC99 kept next to the hierarchy's checks", both.Checks)
	}
}

// FR-15.4: every save keeps its revision, so a run shows the units it used
// after the tree changed; another tenant sees no history and a run that was
// not limited to a tree has none.
func TestTreeRevisions_KeepEachSaveAndShowTheRunsUnits(t *testing.T) {
	st, ctx := migrated(t)
	tr := profitCenterTree()
	if _, _, err := st.SaveTree(ctx, tr, nil); err != nil {
		t.Fatal(err)
	}
	def := storeFixtureDefinition("PL-REV")
	if _, _, err := st.SaveDraft(ctx, def, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Publish(ctx, "PL-REV", "tester"); err != nil {
		t.Fatal(err)
	}
	ledger := ids.New()
	seedDimBalanceRow(t, st, ctx, ledger, "1000", "c", `{"profit_center":"PC10"}`, "5", 1)
	spec := func(key string, tree *TreeRun) RunSpec {
		return RunSpec{Code: "PL-REV", LedgerID: ledger, CurrencyType: "acct", PeriodYear: 2026, PeriodNo: 3,
			Calendar: storeFixtureCalendar(), RequestedBy: "tester", IdempotencyKey: key, RequestHash: key, Tree: tree}
	}
	first, err := st.CreateRun(ctx, spec("first", &TreeRun{Code: "PC-TREE", Unit: "DE"}))
	if err != nil {
		t.Fatal(err)
	}
	// Replace the tree: DE now lists PC10 only and a new unit appears.
	tr.Name = "Renamed"
	tr.Units[2].Values = []string{"PC10"}
	tr.Units = append(tr.Units, domain.TreeUnit{Code: "APAC", Name: "Asia Pacific", Parent: "ALL", Values: []string{"PC40"}})
	one := 1
	if _, rev, err := st.SaveTree(ctx, tr, &one); err != nil || rev != 2 {
		t.Fatalf("replace: rev %d, %v", rev, err)
	}
	revs, err := st.ListTreeRevisions(ctx, "PC-TREE")
	if err != nil || len(revs) != 2 || revs[0].Revision != 2 || revs[1].Revision != 1 || revs[0].UpdatedBy != "u1" {
		t.Fatalf("revisions = %+v, %v", revs, err)
	}
	old, err := st.GetTreeRevision(ctx, "PC-TREE", 1)
	if err != nil || old.Name != "Profit centers" || len(old.Units) != 5 || len(old.Units[2].Values) != 2 {
		t.Fatalf("revision 1 = %+v, %v", old, err)
	}
	if _, err := st.GetTreeRevision(ctx, "PC-TREE", 9); problemCode(err) != domain.CodePartNotFound {
		t.Fatalf("missing revision = %v, want %s", err, domain.CodePartNotFound)
	}
	if _, err := st.ListTreeRevisions(ctx, "NOPE"); problemCode(err) != domain.CodePartNotFound {
		t.Fatalf("unknown tree = %v, want %s", err, domain.CodePartNotFound)
	}
	v, err := st.RunTree(ctx, first.ID)
	// The run's viewer sees DE as it was (two values), not the rest of the tree.
	if err != nil || v.Code != "PC-TREE" || v.Unit != "DE" || v.Revision != 1 || v.CurrentRevision != 2 || v.Tree.Name != "Profit centers" ||
		len(v.Tree.Units) != 1 || v.Tree.Units[0].Code != "DE" || len(v.Tree.Units[0].Values) != 2 {
		t.Fatalf("run tree = %+v, %v", v, err)
	}
	plain, err := st.CreateRun(ctx, spec("plain", nil))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.RunTree(ctx, plain.ID); problemCode(err) != domain.CodePartNotFound {
		t.Fatalf("run without a tree = %v, want %s", err, domain.CodePartNotFound)
	}
	if n := revisionRows(t, st, ctx); n != 2 {
		t.Fatalf("stored revision rows = %d, want 2", n)
	}
}

func revisionRows(t *testing.T, st *Postgres, ctx context.Context) int {
	t.Helper()
	var n int
	err := dbx.WithTenantTx(ctx, st.pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `select count(*) from report_tree_revision`).Scan(&n)
	})
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// The history is tenant data: another tenant, on the application role under
// row-level security, sees no revisions of a code it does not have.
func TestTreeRevisions_TenantIsolation(t *testing.T) {
	admin, app := testkit.DBWithApp(t)
	ctx := testkit.Ctx(testkit.TenantID(1))
	if err := dbx.Migrate(ctx, admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	st := New(app)
	if _, _, err := st.SaveTree(ctx, profitCenterTree(), nil); err != nil {
		t.Fatal(err)
	}
	if revs, err := st.ListTreeRevisions(ctx, "PC-TREE"); err != nil || len(revs) != 1 {
		t.Fatalf("own revisions = %+v, %v", revs, err)
	}
	other := testkit.Ctx(testkit.TenantID(2))
	if _, err := st.GetTreeRevision(other, "PC-TREE", 1); problemCode(err) != domain.CodePartNotFound {
		t.Fatalf("other tenant reads revision 1: %v", err)
	}
	if _, err := st.ListTreeRevisions(other, "PC-TREE"); problemCode(err) != domain.CodePartNotFound {
		t.Fatalf("other tenant lists revisions: %v", err)
	}
}

// A tree saved before migration 0016 whose backfill was skipped (the migration
// role did not bypass row-level security) has no stored revision: its current
// revision is read from the tree, and the next replace stores it before
// overwriting it, so the runs made on it keep their units.
func TestTreeRevisions_TreeSavedBeforeTheHistory(t *testing.T) {
	st, ctx := migrated(t)
	tr := profitCenterTree()
	if _, _, err := st.SaveTree(ctx, tr, nil); err != nil {
		t.Fatal(err)
	}
	if err := dbx.WithTenantTx(ctx, st.pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `delete from report_tree_revision`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	def := storeFixtureDefinition("PL-OLD")
	if _, _, err := st.SaveDraft(ctx, def, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Publish(ctx, "PL-OLD", "tester"); err != nil {
		t.Fatal(err)
	}
	ledger := ids.New()
	seedDimBalanceRow(t, st, ctx, ledger, "1000", "c", `{"profit_center":"PC20"}`, "5", 1)
	run, err := st.CreateRun(ctx, RunSpec{Code: "PL-OLD", LedgerID: ledger, CurrencyType: "acct", PeriodYear: 2026, PeriodNo: 3,
		Calendar: storeFixtureCalendar(), RequestedBy: "tester", IdempotencyKey: "old", RequestHash: "old", Tree: &TreeRun{Code: "PC-TREE", Unit: "EMEA"}})
	if err != nil {
		t.Fatal(err)
	}
	if revs, err := st.ListTreeRevisions(ctx, "PC-TREE"); err != nil || len(revs) != 1 || revs[0].Revision != 1 {
		t.Fatalf("revisions before a save = %+v, %v", revs, err)
	}
	if v, err := st.RunTree(ctx, run.ID); err != nil || v.Revision != 1 || len(v.Tree.Units) != 3 {
		t.Fatalf("run tree before a save = %+v, %v", v, err)
	}
	tr.Units = tr.Units[:4] // US goes
	one := 1
	if _, rev, err := st.SaveTree(ctx, tr, &one); err != nil || rev != 2 {
		t.Fatalf("replace: rev %d, %v", rev, err)
	}
	if n := revisionRows(t, st, ctx); n != 2 {
		t.Fatalf("stored revision rows = %d, want 2 (the overwritten one and the new one)", n)
	}
	if old, err := st.GetTreeRevision(ctx, "PC-TREE", 1); err != nil || len(old.Units) != 5 {
		t.Fatalf("revision 1 after the replace = %+v, %v", old, err)
	}
	if v, err := st.RunTree(ctx, run.ID); err != nil || v.Revision != 1 || v.CurrentRevision != 2 || len(v.Tree.Units) != 3 {
		t.Fatalf("run tree after the replace = %+v, %v", v, err)
	}
}

// FR-15.3 with FR-15.4 and FR-15.13: the tree unit, the hierarchy's rows and the
// column's dimension filter all apply to a balance (AND), none twice; each cell
// drills to exactly its balances and a CALC column reads the filtered column.
func TestCreateRun_ColumnFilterWithTreeUnitAndHierarchy(t *testing.T) {
	st, ctx := migrated(t)
	if _, _, err := st.SaveTree(ctx, profitCenterTree(), nil); err != nil {
		t.Fatal(err)
	}
	def := storeFixtureDefinition("PL-COLF")
	def.Rows = []domain.Row{{Code: "REV", Kind: domain.RowAccountRange, Label: "Revenue", AccountFrom: "4000", AccountTo: "5999", NormalBalance: "debit"}}
	def.Columns = []domain.Column{
		{Code: "ALL", Label: "All", PeriodExpr: "P", CurrencyType: "acct"},
		{Code: "PC", Label: "PC10 and PC30", PeriodExpr: "P", CurrencyType: "acct", DimensionFilter: map[string][]string{"profit_center": {"PC10", "PC30"}}},
		{Code: "BLANK", Label: "No profit center", PeriodExpr: "P", CurrencyType: "acct", DimensionFilter: map[string][]string{"profit_center": {""}}},
		{Code: "SHARE", Label: "Share", PeriodExpr: "CALC", Expression: "PC/ALL*100"},
	}
	if _, _, err := st.SaveDraft(ctx, def, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Publish(ctx, "PL-COLF", "tester"); err != nil {
		t.Fatal(err)
	}
	ledger := ids.New()
	for i, b := range []struct{ pc, account, net string }{
		{"PC10", "4000", "100"}, {"PC11", "4000", "20"}, {"PC20", "4000", "80"}, {"PC30", "4000", "7"}, {"", "4000", "1000"},
		{"PC10", "5000", "50"},
	} {
		dims := `{}`
		if b.pc != "" {
			dims = `{"profit_center":"` + b.pc + `"}`
		}
		seedDimBalanceRow(t, st, ctx, ledger, b.account, "c"+b.pc+b.account, dims, b.net, int64(i+1))
	}
	h := domain.Hierarchy{Code: "PL-GRP", Name: "P&L groups", Subject: domain.SubjectAccount, Nodes: []domain.HierarchyNode{
		{Code: "REVENUE", Label: "Revenue", Rules: []domain.Rule{{Values: []string{"4*"}}}},
		{Code: "COSTS", Label: "Costs", Rules: []domain.Rule{{Values: []string{"5*"}}}},
	}}
	if _, _, err := st.SaveHierarchy(ctx, h, nil, "owner"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.PublishHierarchy(ctx, "PL-GRP", "publisher", []domain.Member{{ID: "4000", Key: "4000"}, {ID: "5000", Key: "5000"}}); err != nil {
		t.Fatal(err)
	}
	cells := func(key string, tr *TreeRun, hr *HierarchyRun) map[string]string {
		t.Helper()
		r, err := st.CreateRun(ctx, RunSpec{Code: "PL-COLF", LedgerID: ledger, CurrencyType: "acct", PeriodYear: 2026, PeriodNo: 3,
			Calendar: storeFixtureCalendar(), RequestedBy: "tester", IdempotencyKey: key, RequestHash: key, Tree: tr, Hierarchy: hr})
		if err != nil {
			t.Fatalf("%s: %v", key, err)
		}
		list, err := st.ListCells(ctx, r.ID)
		if err != nil {
			t.Fatal(err)
		}
		out := map[string]string{}
		for _, c := range list {
			if err := c.CheckDrill(); err != nil {
				t.Fatalf("%s %s/%s: %v", key, c.Row, c.Column, err)
			}
			out[c.Row+"/"+c.Column] = c.Value.Round(2).String()
		}
		return out
	}
	// No tree: the column filter alone.
	if got := cells("plain", nil, nil); got["REV/ALL"] != "1257" || got["REV/PC"] != "157" || got["REV/BLANK"] != "1000" {
		t.Fatalf("plain = %v", got)
	}
	// EMEA covers PC10, PC11 and PC20: PC30 and the blank balance are left out
	// by the tree, whatever the column asks for.
	if got := cells("emea", &TreeRun{Code: "PC-TREE", Unit: "EMEA"}, nil); got["REV/ALL"] != "250" || got["REV/PC"] != "150" || got["REV/BLANK"] != "0" || got["REV/SHARE"] != "60" {
		t.Fatalf("EMEA = %v", got)
	}
	// The hierarchy replaces the rows; the tree and the column filter still apply.
	got := cells("emea-hier", &TreeRun{Code: "PC-TREE", Unit: "EMEA"}, &HierarchyRun{Code: "PL-GRP"})
	var revenue, costs string
	for k := range got {
		if strings.HasSuffix(k, "/PC") && strings.Contains(k, "REVENUE") {
			revenue = got[k]
		}
		if strings.HasSuffix(k, "/PC") && strings.Contains(k, "COSTS") {
			costs = got[k]
		}
	}
	if revenue != "100" || costs != "50" {
		t.Fatalf("hierarchy run over EMEA = %v, want REVENUE/PC 100 and COSTS/PC 50", got)
	}
}
