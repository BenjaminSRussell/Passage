package domain

import "testing"

// demoBalances is a balanced two-period ledger L1 (AC-15.1, AC-15.13 shape).
// P0 opens 1000 cash against equity. P1 has revenue 300, cost of sales 120,
// administrative expense 80 and a payable of 80. With withUnassigned, an extra
// expense of 10 carries no FUNC_AREA, which PL-FUNC must flag.
func demoBalances(withUnassigned bool) []Balance {
	fa := func(v string) map[string]string { return map[string]string{funcAreaDimension: v} }
	bs := []Balance{
		{LedgerID: "L1", Period: 0, Account: "100100", Dims: map[string]string{}, CurrencyType: "rpt", Net: d(1000)},
		{LedgerID: "L1", Period: 0, Account: "300100", Dims: map[string]string{}, CurrencyType: "rpt", Net: d(-1000)},
		{LedgerID: "L1", Period: 1, Account: "100100", Dims: map[string]string{}, CurrencyType: "rpt", Net: d(180)},
		{LedgerID: "L1", Period: 1, Account: "400100", Dims: map[string]string{}, CurrencyType: "rpt", Net: d(-300)},
		{LedgerID: "L1", Period: 1, Account: "500100", Dims: fa(funcAreaCostOfSale), CurrencyType: "rpt", Net: d(120)},
		{LedgerID: "L1", Period: 1, Account: "600100", Dims: fa("ADMIN"), CurrencyType: "rpt", Net: d(80)},
		{LedgerID: "L1", Period: 1, Account: "200100", Dims: map[string]string{}, CurrencyType: "rpt", Net: d(-80)},
	}
	if withUnassigned {
		bs = append(bs,
			Balance{LedgerID: "L1", Period: 1, Account: "610100", Dims: map[string]string{}, CurrencyType: "rpt", Net: d(10)},
			Balance{LedgerID: "L1", Period: 1, Account: "200100", Dims: map[string]string{}, CurrencyType: "rpt", Net: d(-10)})
	}
	return bs
}

func standardByCode(t *testing.T, code string) Definition {
	t.Helper()
	for _, def := range StandardDefinitions() {
		if def.Code == code {
			return def
		}
	}
	t.Fatalf("no standard definition %s", code)
	return Definition{}
}

func evaluateStd(t *testing.T, code string, withUnassigned bool) Result {
	t.Helper()
	cal := calendarMonths(t, 2)
	res, err := Evaluate(Input{Definition: standardByCode(t, code), LedgerID: "L1", Calendar: cal, At: 1, CurrencyType: "rpt",
		Balances: demoBalances(withUnassigned)})
	if err != nil {
		t.Fatalf("%s: %v", code, err)
	}
	return res
}

func TestStandardDefinitions_ValidateAndCodes(t *testing.T) {
	want := map[string]bool{CodeTBStd: true, CodeBSStd: true, CodePLStd: true, CodePLFunc: true}
	for _, def := range StandardDefinitions() {
		if !want[def.Code] {
			t.Errorf("unexpected standard definition %s", def.Code)
		}
		delete(want, def.Code)
		if issues := def.Validate(); len(issues) > 0 {
			t.Errorf("%s: %+v", def.Code, issues)
		}
	}
	if len(want) > 0 {
		t.Errorf("missing standard definitions %v", want)
	}
}

func TestStatements_TieOutToTrialBalance(t *testing.T) {
	tb := evaluateStd(t, CodeTBStd, false)
	bs := evaluateStd(t, CodeBSStd, false)
	pl := evaluateStd(t, CodePLStd, false)

	if total := cellAt(t, tb, "TOTAL", "CLOSE"); total.Blank || !total.Value.IsZero() {
		t.Fatalf("trial balance total = %+v, want 0.00", total)
	}
	if got := cellAt(t, tb, "ASSETS", "OPEN").Value; !got.Equal(d(1000)) {
		t.Fatalf("opening assets = %s, want 1000 (P-1 is cumulative)", got)
	}
	if got := cellAt(t, tb, "ASSETS", "MOVE").Value; !got.Equal(d(180)) {
		t.Fatalf("asset movement = %s, want 180", got)
	}
	if chk := cellAt(t, bs, "CHK", "CUR"); chk.Blank || !chk.Value.IsZero() {
		t.Fatalf("balance sheet check = %+v, want 0.00 (AC-15.1)", chk)
	}
	// P&L net equals the trial balance net of P&L accounts, with the sign flipped
	// because the trial balance shows raw net (debit minus credit).
	tbPL := cellAt(t, tb, "REVENUE", "CLOSE").Value.Add(cellAt(t, tb, "EXPENSE", "CLOSE").Value)
	if res := cellAt(t, pl, "RES", "YTD").Value; !res.Equal(tbPL.Neg()) || !res.Equal(d(100)) {
		t.Fatalf("P&L result = %s, trial balance P&L net = %s; want 100", res, tbPL)
	}
	if err := cellAt(t, bs, "RESULT", "CUR").CheckDrill(); err != nil {
		t.Fatal(err)
	}
}

func TestPLFunc_EqualsPLStdWhenEveryLineHasFunctionalArea(t *testing.T) {
	std := cellAt(t, evaluateStd(t, CodePLStd, false), "RES", "YTD").Value
	fn := evaluateStd(t, CodePLFunc, false)
	if got := cellAt(t, fn, "RES", "YTD").Value; !got.Equal(std) {
		t.Fatalf("PL-FUNC result = %s, PL-STD result = %s; want equal (AC-15.13)", got, std)
	}
	if err := PLFuncCheck(fn); err != nil {
		t.Fatalf("clean ledger failed the check: %v", err)
	}
}

func TestPLFunc_CheckFlagsExpenseWithoutFunctionalArea(t *testing.T) {
	fn := evaluateStd(t, CodePLFunc, true)
	if err := PLFuncCheck(fn); err == nil || err.Code != CodePLFuncMissing {
		t.Fatalf("check = %v, want %s", err, CodePLFuncMissing)
	}
	if got := cellAt(t, fn, PLFuncCheckRow, "YTD").Value; !got.Equal(d(10)) {
		t.Fatalf("check row = %s, want 10", got)
	}
}
