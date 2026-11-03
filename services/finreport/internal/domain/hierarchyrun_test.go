package domain

import (
	"strings"
	"testing"
)

func acctHierarchy() Hierarchy {
	return Hierarchy{Code: "ACCT-CAT", Name: "Account groups", Subject: SubjectAccount, Strict: false, Nodes: []HierarchyNode{
		{Code: "ASSETS", Label: "Assets"},
		{Code: "CASH", Label: "Cash", Parent: "ASSETS", Rules: []Rule{{Values: []string{"10*"}}}},
		{Code: "RECV", Label: "Receivables", Parent: "ASSETS", Rules: []Rule{{From: "1200", To: "1299"}}},
		{Code: "EQUITY", Label: "Equity", Rules: []Rule{{Values: []string{"30*"}}}},
		{Code: "EMPTY", Label: "Nothing here", Rules: []Rule{{Values: []string{"99*"}}}},
	}}
}

func runBalanceSheet(t *testing.T, def Definition, bals []Balance) Result {
	t.Helper()
	res, err := Evaluate(Input{Definition: def, LedgerID: "L1", CurrencyType: "rpt", Calendar: calendarMonths(t, 3), At: 1, Balances: bals})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range res.Cells {
		if err := c.CheckDrill(); err != nil {
			t.Fatalf("cell %s/%s: %v", c.Row, c.Column, err)
		}
	}
	return res
}

func hierBase() Definition {
	return Definition{Code: "TB-TEST", Name: "Trial balance", Kind: KindTrialBalance,
		Columns: []Column{{Code: "C1", Label: "Closing", PeriodExpr: "P"}}}
}

func TestExpandHierarchy_AccountsRollUpAndTieToTheLedger(t *testing.T) {
	bals := []Balance{
		{LedgerID: "L1", Period: 0, Account: "1000", Combination: "x", CurrencyType: "rpt", Net: d(400)},
		{LedgerID: "L1", Period: 1, Account: "1210", Combination: "x", CurrencyType: "rpt", Net: d(250)},
		{LedgerID: "L1", Period: 1, Account: "3000", Combination: "x", CurrencyType: "rpt", Net: d(-600)},
		{LedgerID: "L1", Period: 1, Account: "7777", Combination: "x", CurrencyType: "rpt", Net: d(-50)},
	}
	out, err := ExpandHierarchy(hierBase(), acctHierarchy(), Membership{Assigned: map[string]string{"1000": "CASH"}},
		HierarchyObserved{Members: []string{"7777", "3000", "1210", "1000"}}, HierarchyRunOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var codes []string
	for _, r := range out.Definition.Rows {
		codes = append(codes, r.Code)
	}
	if got, want := strings.Join(codes, ","), "CASH,RECV,ASSETS,EQUITY,UNASSIGNED,TOTAL"; got != want {
		t.Fatalf("rows = %s, want %s (empty node left out, unassigned last, grand total closes)", got, want)
	}
	if got := strings.Join(out.Unassigned, ","); got != "7777" {
		t.Fatalf("unassigned = %s, want 7777", got)
	}
	res := runBalanceSheet(t, out.Definition, bals)
	for row, want := range map[string]int64{"CASH": 400, "RECV": 250, "ASSETS": 650, "EQUITY": -600, "UNASSIGNED": -50, "TOTAL": 0} {
		if got := cellAt(t, res, row, "C1").Value; !got.Equal(d(want)) {
			t.Errorf("%s = %s, want %d", row, got, want)
		}
	}
	if ind := out.Definition.Rows[0].Format.Indent; ind != 1 {
		t.Errorf("leaf indent = %d, want 1", ind)
	}
}

func TestExpandHierarchy_StoredMembershipWinsOverRules(t *testing.T) {
	h := Hierarchy{Code: "BYATTR", Name: "x", Subject: SubjectAccount, Nodes: []HierarchyNode{
		{Code: "A", Label: "A", Rules: []Rule{{Attribute: "kind", Values: []string{"a"}}}},
		{Code: "B", Label: "B", Rules: []Rule{{Values: []string{"2*"}}}},
	}}
	// 2000 matches B by key, but the publish placed it in A through its attribute.
	out, err := ExpandHierarchy(hierBase(), h, Membership{Assigned: map[string]string{"2000": "A"}},
		HierarchyObserved{Members: []string{"2000", "2100"}}, HierarchyRunOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got := out.Definition.Rows[0]; got.Code != "A" || len(got.Accounts) != 1 || got.Accounts[0] != "2000" {
		t.Fatalf("row A = %+v, want account 2000", got)
	}
	if got := out.Definition.Rows[1]; got.Code != "B" || got.Accounts[0] != "2100" {
		t.Fatalf("row B = %+v, want account 2100 placed by its key", got)
	}
}

func TestExpandHierarchy_OverlapOfNewMembersIsRefused(t *testing.T) {
	h := Hierarchy{Code: "OVER", Name: "x", Subject: SubjectAccount, Nodes: []HierarchyNode{
		{Code: "A", Label: "A", Rules: []Rule{{Values: []string{"1*"}}}},
		{Code: "B", Label: "B", Rules: []Rule{{Values: []string{"12*"}}}},
	}}
	_, err := ExpandHierarchy(hierBase(), h, Membership{}, HierarchyObserved{Members: []string{"1200"}}, HierarchyRunOptions{})
	if e, ok := err.(*Error); !ok || e.Code != CodeHierOverlap {
		t.Fatalf("err = %v, want %s", err, CodeHierOverlap)
	}
}

func TestExpandHierarchy_DimensionValuesIncludeBlankInUnassigned(t *testing.T) {
	h := Hierarchy{Code: "REGIONS", Name: "Regions", Subject: SubjectDimension, DimensionAttribute: "cost_center", Nodes: []HierarchyNode{
		{Code: "EAST", Label: "East", Rules: []Rule{{Values: []string{"E*"}}}},
		{Code: "WEST", Label: "West", Rules: []Rule{{Values: []string{"W*"}}}},
	}}
	bals := []Balance{
		{LedgerID: "L1", Period: 1, Account: "6000", Combination: "E1", Dims: map[string]string{"cost_center": "E1"}, CurrencyType: "rpt", Net: d(70)},
		{LedgerID: "L1", Period: 1, Account: "6000", Combination: "W9", Dims: map[string]string{"cost_center": "W9"}, CurrencyType: "rpt", Net: d(30)},
		{LedgerID: "L1", Period: 1, Account: "6000", Combination: "", CurrencyType: "rpt", Net: d(5)},
		{LedgerID: "L1", Period: 1, Account: "6000", Combination: "N1", Dims: map[string]string{"cost_center": "N1"}, CurrencyType: "rpt", Net: d(1)},
		{LedgerID: "L1", Period: 1, Account: "1000", Combination: "E1", Dims: map[string]string{"cost_center": "E1"}, CurrencyType: "rpt", Net: d(1000)},
	}
	base := hierBase()
	base.Kind = KindIncomeStatement
	out, err := ExpandHierarchy(base, h, Membership{}, HierarchyObserved{Members: []string{"E1", "W9", "N1"}, HasBlank: true},
		HierarchyRunOptions{AccountFrom: "6000", AccountTo: "6999"})
	if err != nil {
		t.Fatal(err)
	}
	res := runBalanceSheet(t, out.Definition, bals)
	for row, want := range map[string]int64{"EAST": 70, "WEST": 30, "UNASSIGNED": 6, "TOTAL": 106} {
		if got := cellAt(t, res, row, "C1").Value; !got.Equal(d(want)) {
			t.Errorf("%s = %s, want %d (account range keeps the balance sheet account out)", row, got, want)
		}
	}
	if c := HierarchyRunCheck(h, 3, out.Unassigned, true); c == nil || c.Code != CodeHierarchyRunUnassigned || !strings.Contains(c.Detail, "N1") {
		t.Fatalf("check = %+v, want REPORT-0034 naming N1", c)
	}
}

func TestExpandHierarchy_Refusals(t *testing.T) {
	cash := hierBase()
	cash.Kind = KindCashFlow
	cases := map[string]struct {
		base Definition
		h    Hierarchy
		seen HierarchyObserved
		opts HierarchyRunOptions
	}{
		"cash flow":          {cash, acctHierarchy(), HierarchyObserved{Members: []string{"1000"}}, HierarchyRunOptions{}},
		"category subject":   {hierBase(), Hierarchy{Code: "CAT", Name: "x", Subject: SubjectCategory, Nodes: acctHierarchy().Nodes}, HierarchyObserved{Members: []string{"1000"}}, HierarchyRunOptions{}},
		"no members":         {hierBase(), acctHierarchy(), HierarchyObserved{}, HierarchyRunOptions{}},
		"bad normal":         {hierBase(), acctHierarchy(), HierarchyObserved{Members: []string{"1000"}}, HierarchyRunOptions{NormalBalance: "left"}},
		"half account range": {hierBase(), Hierarchy{Code: "D", Name: "x", Subject: SubjectDimension, DimensionAttribute: "cc", Nodes: acctHierarchy().Nodes}, HierarchyObserved{Members: []string{"1000"}}, HierarchyRunOptions{AccountFrom: "1"}},
		"range on accounts":  {hierBase(), acctHierarchy(), HierarchyObserved{Members: []string{"1000"}}, HierarchyRunOptions{AccountFrom: "1", AccountTo: "2"}},
	}
	for name, c := range cases {
		_, err := ExpandHierarchy(c.base, c.h, Membership{}, c.seen, c.opts)
		if e, ok := err.(*Error); !ok || e.Code != CodeHierarchyRun {
			t.Errorf("%s: err = %v, want %s", name, err, CodeHierarchyRun)
		}
	}
}

func TestEvaluate_AccountListRowAndValidation(t *testing.T) {
	def := hierBase()
	def.Rows = []Row{{Code: "R", Kind: RowAccountRange, Label: "Two accounts", Accounts: []string{"1000", "3000"}, NormalBalance: "debit"}}
	bals := []Balance{
		{LedgerID: "L1", Period: 1, Account: "1000", Combination: "x", CurrencyType: "rpt", Net: d(5)},
		{LedgerID: "L1", Period: 1, Account: "2000", Combination: "x", CurrencyType: "rpt", Net: d(7)},
		{LedgerID: "L1", Period: 1, Account: "3000", Combination: "x", CurrencyType: "rpt", Net: d(11)},
	}
	if got := cellAt(t, runBalanceSheet(t, def, bals), "R", "C1").Value; !got.Equal(d(16)) {
		t.Fatalf("list row = %s, want 16", got)
	}
	for name, mutate := range map[string]func(*Row){
		"empty":       func(r *Row) { r.Accounts = nil },
		"empty entry": func(r *Row) { r.Accounts = []string{"1000", ""} },
		"half range":  func(r *Row) { r.AccountFrom = "1000" },
		"reversed":    func(r *Row) { r.AccountFrom, r.AccountTo = "9", "1" },
	} {
		bad := def
		bad.Rows = []Row{def.Rows[0]}
		mutate(&bad.Rows[0])
		if len(bad.Validate()) == 0 {
			t.Errorf("%s: want a validation issue", name)
		}
	}
}

// Members placed from the stored membership and by the rules end up in one
// sorted list, so the expanded definition stored with the run is the same for
// the same inputs.
func TestExpandHierarchy_LeafAccountsAreSorted(t *testing.T) {
	members := []string{"1000", "1010", "1020", "1030", "1040", "1050"}
	for i := 0; i < 20; i++ {
		out, err := ExpandHierarchy(hierBase(), acctHierarchy(), Membership{Assigned: map[string]string{"1050": "CASH"}},
			HierarchyObserved{Members: members}, HierarchyRunOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if got := strings.Join(out.Definition.Rows[0].Accounts, ","); got != strings.Join(members, ",") {
			t.Fatalf("CASH accounts = %s, want %s", got, strings.Join(members, ","))
		}
	}
}
