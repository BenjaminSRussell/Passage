package domain

import (
	"errors"
	"strings"
	"testing"

	"github.com/shopspring/decimal"
	"pgregory.net/rapid"
)

func codeOf(err error) string {
	var de *Error
	if errors.As(err, &de) {
		return de.Code
	}
	return ""
}

func cfInput(t *testing.T, def Definition, bal []Balance) (Input, Result) {
	t.Helper()
	in := Input{Definition: def, LedgerID: "L1", CurrencyType: "rpt", Calendar: calendarMonths(t, 2), At: 1, Balances: bal}
	res, err := Evaluate(in)
	if err != nil {
		t.Fatal(err)
	}
	return in, res
}

// TestCashFlow_DemoLedgerReconciles is AC-15.2 on the demo ledger: in period 2
// net income 100 plus the payable increase 80 equals the cash increase 180; the
// year to date also carries the opening 1,000 of cash paid in as equity.
func TestCashFlow_DemoLedgerReconciles(t *testing.T) {
	def := standardByCode(t, CodeCFStd)
	in, res := cfInput(t, def, demoBalances(false))
	want := map[[2]string]int64{
		{"NI", "P"}: 100, {"CF_WC", "P"}: 80, {"CF_OP", "P"}: 180, {"CF_FIN", "P"}: 0, {"CF_NET", "P"}: 180, {"CASH", "P"}: 180, {"CF_CHK", "P"}: 0,
		{"CF_FIN", "YTD"}: 1000, {"CF_NET", "YTD"}: 1180, {"CASH", "YTD"}: 1180, {"CF_CHK", "YTD"}: 0,
	}
	for k, v := range want {
		if c := cellAt(t, res, k[0], k[1]); c.Blank || !c.Value.Equal(d(v)) {
			t.Errorf("%s/%s = %+v, want %d", k[0], k[1], c, v)
		}
	}
	if err := CheckCashFlow(in, res); err != nil {
		t.Fatalf("demo ledger failed CF-STD: %v", err)
	}
}

// Closing entries move the year's result into equity; they are not cash flows
// and must not show in financing (BR-15.5).
func TestCashFlow_ClosingEntriesAreNotCashFlows(t *testing.T) {
	bal := append(demoBalances(false),
		Balance{LedgerID: "L1", Period: 1, Account: "400100", Dims: map[string]string{}, CurrencyType: "rpt", Net: d(300), ClosingNet: d(300)},
		Balance{LedgerID: "L1", Period: 1, Account: "500100", Dims: map[string]string{}, CurrencyType: "rpt", Net: d(-120), ClosingNet: d(-120)},
		Balance{LedgerID: "L1", Period: 1, Account: "600100", Dims: map[string]string{}, CurrencyType: "rpt", Net: d(-80), ClosingNet: d(-80)},
		Balance{LedgerID: "L1", Period: 1, Account: "300900", Dims: map[string]string{}, CurrencyType: "rpt", Net: d(-100), ClosingNet: d(-100)})
	in, res := cfInput(t, standardByCode(t, CodeCFStd), bal)
	if got := cellAt(t, res, "NI", "P").Value; !got.Equal(d(100)) {
		t.Fatalf("net income = %s, want 100 (closing entries excluded)", got)
	}
	if got := cellAt(t, res, "CF_FIN", "P").Value; !got.IsZero() {
		t.Fatalf("financing = %s, want 0: the closing transfer is not a cash flow", got)
	}
	if err := CheckCashFlow(in, res); err != nil {
		t.Fatal(err)
	}
}

// Opening periods (period number 0) carry brought-forward balances. They are
// not movements of the year, so the year-to-date cash flow leaves them out.
func TestCashFlow_YearToDateLeavesOutTheOpeningPeriod(t *testing.T) {
	cal, err := NewCalendar([]Period{{Year: 2026, No: 0, Quarter: 1}, {Year: 2026, No: 1, Quarter: 1}})
	if err != nil {
		t.Fatal(err)
	}
	bal := []Balance{
		{LedgerID: "L1", Period: 0, Account: "100100", CurrencyType: "acct", Net: d(500)},
		{LedgerID: "L1", Period: 0, Account: "300100", CurrencyType: "acct", Net: d(-500)},
		{LedgerID: "L1", Period: 1, Account: "100100", CurrencyType: "acct", Net: d(40)},
		{LedgerID: "L1", Period: 1, Account: "400100", CurrencyType: "acct", Net: d(-40)},
	}
	in := Input{Definition: standardByCode(t, CodeCFStd), LedgerID: "L1", CurrencyType: "acct", Calendar: cal, At: 1, Balances: bal}
	res, err := Evaluate(in)
	if err != nil {
		t.Fatal(err)
	}
	if got := cellAt(t, res, "CASH", "YTD").Value; !got.Equal(d(40)) {
		t.Fatalf("YTD change in cash = %s, want 40 (opening balance left out)", got)
	}
	if err := CheckCashFlow(in, res); err != nil {
		t.Fatal(err)
	}
}

func TestCashFlow_UnreconciledFailsWithREPORT0008(t *testing.T) {
	// Cash moves with no other entry, so the ledger does not balance.
	bal := append(demoBalances(false), Balance{LedgerID: "L1", Period: 1, Account: "100100", Dims: map[string]string{}, CurrencyType: "rpt", Net: d(50)})
	in, res := cfInput(t, standardByCode(t, CodeCFStd), bal)
	err := CheckCashFlow(in, res)
	if codeOf(err) != CodeCashFlow || !strings.Contains(err.Error(), "column P") {
		t.Fatalf("err = %v, want %s naming column P", err, CodeCashFlow)
	}
}

func TestCashFlow_UntaggedAccountFailsWithREPORT0009(t *testing.T) {
	def := standardByCode(t, CodeCFStd)
	for i := range def.Rows {
		if def.Rows[i].Code == "INV_OTH" {
			def.Rows[i].CashFlowTag = "" // 160000-199999 no longer classified
		}
	}
	bal := append(demoBalances(false),
		Balance{LedgerID: "L1", Period: 1, Account: "180100", Dims: map[string]string{}, CurrencyType: "rpt", Net: d(50)},
		Balance{LedgerID: "L1", Period: 1, Account: "100100", Dims: map[string]string{}, CurrencyType: "rpt", Net: d(-50)})
	in, res := cfInput(t, def, bal)
	err := CheckCashFlow(in, res)
	if codeOf(err) != CodeUnclassified || !strings.Contains(err.Error(), "180100") {
		t.Fatalf("err = %v, want %s naming account 180100", err, CodeUnclassified)
	}
}

// A tagged row with an explicit account list classifies only the accounts it
// lists, not every balance-sheet account.
func TestCashFlow_TaggedAccountListCoversOnlyItsAccounts(t *testing.T) {
	def := standardByCode(t, CodeCFStd)
	for i := range def.Rows {
		if def.Rows[i].Code == "INV_OTH" {
			def.Rows[i].AccountFrom, def.Rows[i].AccountTo, def.Rows[i].Accounts = "", "", []string{"160000"}
		}
	}
	bal := append(demoBalances(false),
		Balance{LedgerID: "L1", Period: 1, Account: "180100", Dims: map[string]string{}, CurrencyType: "rpt", Net: d(50)},
		Balance{LedgerID: "L1", Period: 1, Account: "100100", Dims: map[string]string{}, CurrencyType: "rpt", Net: d(-50)})
	in, res := cfInput(t, def, bal)
	if err := CheckCashFlow(in, res); codeOf(err) != CodeUnclassified || !strings.Contains(err.Error(), "180100") {
		t.Fatalf("err = %v, want %s naming account 180100", err, CodeUnclassified)
	}
}

func TestCashFlow_OtherKindsAndUntaggedDefinitionsPass(t *testing.T) {
	bal := append(demoBalances(false), Balance{LedgerID: "L1", Period: 1, Account: "100100", Dims: map[string]string{}, CurrencyType: "rpt", Net: d(50)})
	in, res := cfInput(t, standardByCode(t, CodeBSStd), bal)
	if err := CheckCashFlow(in, res); err != nil {
		t.Fatalf("a balance sheet is not a cash flow: %v", err)
	}
	custom := Definition{Code: "CF-OWN", Name: "Own cash view", Kind: KindCashFlow,
		Rows:    []Row{{Code: "CASH", Kind: RowAccountRange, Label: "Cash", AccountFrom: "100000", AccountTo: "109999", NormalBalance: "debit"}},
		Columns: []Column{{Code: "P", Label: "Period", PeriodExpr: "P"}}}
	in, res = cfInput(t, custom, bal)
	if err := CheckCashFlow(in, res); err != nil {
		t.Fatalf("a cash flow definition without tags is not reconciled: %v", err)
	}
}

// genBalancedLedger draws entries that each debit one account and credit
// another, some of them closing entries, so the movements of every period sum
// to zero with and without the closing entries.
func genBalancedLedger(t *rapid.T) []Balance {
	n := rapid.IntRange(0, 25).Draw(t, "entries")
	type key struct {
		period  int
		account string
	}
	nets, closing := map[key]decimal.Decimal{}, map[key]decimal.Decimal{}
	for i := 0; i < n; i++ {
		p := rapid.IntRange(0, 1).Draw(t, "period")
		dr := rapid.SampledFrom(cfAccounts).Draw(t, "debit")
		cr := rapid.SampledFrom(cfAccounts).Draw(t, "credit")
		if dr == cr {
			continue
		}
		amt := decimal.New(rapid.Int64Range(1, 100000).Draw(t, "cents"), -2)
		nets[key{p, dr}] = nets[key{p, dr}].Add(amt)
		nets[key{p, cr}] = nets[key{p, cr}].Sub(amt)
		if rapid.Bool().Draw(t, "closing") {
			closing[key{p, dr}] = closing[key{p, dr}].Add(amt)
			closing[key{p, cr}] = closing[key{p, cr}].Sub(amt)
		}
	}
	var out []Balance
	for k, v := range nets {
		out = append(out, Balance{LedgerID: "L1", Period: k.period, Account: k.account, Combination: k.account,
			Dims: map[string]string{}, CurrencyType: "rpt", Net: v, ClosingNet: closing[k]})
	}
	return out
}

var cfAccounts = []string{"100100", "110100", "150100", "155100", "160100", "200100",
	"220100", "230100", "240100", "300100", "400100", "500100", "610100"}

// TestCashFlow_PropertyReconcilesEveryBalancedLedger checks BR-15.5 and AC-15.2
// over random balanced ledgers: CF-STD reconciles to the cent in both columns,
// the check row is 0.00 and every balance-backed cell passes the drill check.
func TestCashFlow_PropertyReconcilesEveryBalancedLedger(t *testing.T) {
	def := standardByCode(t, CodeCFStd)
	cal := calendarMonths(t, 2)
	rapid.Check(t, func(rt *rapid.T) {
		in := Input{Definition: def, LedgerID: "L1", CurrencyType: "rpt", Calendar: cal, At: 1, Balances: genBalancedLedger(rt)}
		res, err := Evaluate(in)
		if err != nil {
			rt.Fatal(err)
		}
		if err := CheckCashFlow(in, res); err != nil {
			rt.Fatalf("balanced ledger failed: %v", err)
		}
		for _, c := range res.Cells {
			if c.Row == "CF_CHK" && (c.Blank || !c.Value.IsZero()) {
				rt.Fatalf("check row %s = %+v, want 0.00", c.Column, c)
			}
			if err := c.CheckDrill(); err != nil {
				rt.Fatalf("cell %s/%s: %v", c.Row, c.Column, err)
			}
		}
	})
}

func TestRunChecks_PLFuncWarnsWithoutFailing(t *testing.T) {
	fn := standardByCode(t, CodePLFunc)
	got := RunChecks(fn, evaluateStd(t, CodePLFunc, true))
	if len(got) != 1 || got[0].Code != CodePLFuncMissing || got[0].Severity != SeverityWarning || !strings.Contains(got[0].Detail, "10.00") {
		t.Fatalf("checks = %+v, want one REPORT-0015 warning for 10.00", got)
	}
	if clean := RunChecks(fn, evaluateStd(t, CodePLFunc, false)); clean == nil || len(clean) != 0 {
		t.Fatalf("clean PL-FUNC checks = %#v, want an empty list", clean)
	}
	if none := RunChecks(standardByCode(t, CodePLStd), evaluateStd(t, CodePLStd, true)); len(none) != 0 {
		t.Fatalf("PL-STD checks = %+v, want none", none)
	}
}
