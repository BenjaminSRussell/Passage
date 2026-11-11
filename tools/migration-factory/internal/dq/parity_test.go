package dq_test

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/tools/migration-factory/internal/domain"
	"github.com/BenjaminSRussell/passage/tools/migration-factory/internal/dq"
)

// A defect is one row that breaks exactly one check of the Go validator; the parity test proves the suite names the same
// row and the same check, and the coverage is the share of the Go validator's checks the suite catches (FR-49.11).
type defect struct {
	check string
	row   dq.Row
	// partner is a clean row the defect repeats (a duplicate), appended before the defect.
	partner dq.Row
}

func dec(s string) decimal.Decimal { return decimal.RequireFromString(s) }

func isoSet() map[string]bool {
	out := map[string]bool{}
	for _, c := range []string{"USD", "EUR", "JPY", "GBP", "XXX", "usd", "US", ""} {
		if domain.ValidCurrency(c) {
			out[c] = true
		}
	}
	return out
}

// onhandRow and the helpers build a clean extract row; the Go line is built from the same row so both see the same data.
func onhandRow(id, item, site, serial, qty, value, currency string) dq.Row {
	return dq.Row{"legacyId": id, "item": item, "site": site, "warehouse": "W1", "location": "A", "batch": "", "serial": serial, "unit": "EA", "currency": currency, "quantity": qty, "value": value}
}

func onhandLine(r dq.Row) domain.OnhandLine {
	return domain.OnhandLine{LegacyID: r["legacyId"], Item: r["item"], Site: r["site"], Warehouse: r["warehouse"], Location: r["location"], Batch: r["batch"], Serial: r["serial"],
		Unit: r["unit"], Currency: r["currency"], Quantity: dec(r["quantity"]), Value: dec(r["value"])}
}

func TestOnhandSuiteFlagsWhatTheGoValidatorQuarantines(t *testing.T) {
	clean := onhandRow("OH-0", "BOLT", "S1", "", "10", "5", "USD")
	defects := []defect{
		{check: "legacy_id_present", row: onhandRow("", "BOLT", "S2", "", "1", "1", "USD")},
		{check: "legacy_id_unique", row: onhandRow("OH-0", "BOLT", "S3", "", "1", "1", "USD")},
		{check: "natural_key_unique", row: onhandRow("OH-2", "BOLT", "S1", "", "1", "1", "USD")},
		{check: "serial_unique_per_item", partner: onhandRow("OH-3", "PUMP", "S1", "SN1", "1", "9", "USD"), row: onhandRow("OH-4", "PUMP", "S2", "SN1", "1", "9", "USD")},
		{check: "item_in_master", row: onhandRow("OH-5", "NOPE", "S1", "", "1", "1", "USD")},
		{check: "site_required", row: onhandRow("OH-6", "BOLT", "", "", "1", "1", "USD")},
		{check: "warehouse_required", row: func() dq.Row {
			r := onhandRow("OH-12", "BOLT", "S9", "", "1", "1", "USD")
			r["warehouse"] = ""
			return r
		}()},
		{check: "unit_required", row: func() dq.Row { r := onhandRow("OH-7", "BOLT", "S4", "", "1", "1", "USD"); r["unit"] = ""; return r }()},
		{check: "quantity_positive", row: onhandRow("OH-8", "BOLT", "S5", "", "0", "1", "USD")},
		{check: "value_not_negative", row: onhandRow("OH-9", "BOLT", "S6", "", "1", "-1", "USD")},
		{check: "serial_quantity_one", row: onhandRow("OH-10", "PUMP", "S7", "SN9", "2", "9", "USD")},
		{check: "currency_iso4217", row: onhandRow("OH-11", "BOLT", "S8", "", "1", "1", "ZZZ")},
	}
	rows := []dq.Row{clean}
	for _, d := range defects {
		if d.partner != nil {
			rows = append(rows, d.partner)
		}
		rows = append(rows, d.row)
	}
	suite, err := dq.Load("onhand")
	if err != nil {
		t.Fatal(err)
	}
	rep, err := suite.Run(rows, map[string]map[string]bool{"items": {"BOLT": true, "PUMP": true}, "iso4217": isoSet()})
	if err != nil {
		t.Fatal(err)
	}
	lines := make([]domain.OnhandLine, len(rows))
	for i, r := range rows {
		lines[i] = onhandLine(r)
	}
	_, quarantined := domain.ValidateOnhand(lines, map[string]struct{}{"BOLT": {}, "PUMP": {}})
	assertParity(t, "onhand", rows, rep, defects, quarantined, suite)
}

func bankRow(id, account, currency, balance string) dq.Row {
	return dq.Row{"legacyId": id, "bankAccount": account, "currency": currency, "balance": balance}
}

func TestBankBalancesSuiteFlagsWhatTheGoValidatorQuarantines(t *testing.T) {
	clean := bankRow("BK-0", "OPER", "USD", "10.50")
	defects := []defect{
		{check: "legacy_id_present", row: bankRow("", "PAY", "USD", "1")},
		{check: "legacy_id_unique", row: bankRow("BK-0", "PAY", "USD", "1")},
		{check: "bank_account_unique", row: bankRow("BK-2", "OPER", "USD", "1")},
		{check: "bank_account_in_master", row: bankRow("BK-3", "GHOST", "USD", "1")},
		{check: "currency_iso4217", row: bankRow("BK-4", "SAV", "ZZZ", "1")},
		{check: "balance_minor_units", row: bankRow("BK-5", "YEN", "JPY", "100.5")},
	}
	rows := []dq.Row{clean}
	for _, d := range defects {
		rows = append(rows, d.row)
	}
	suite, err := dq.Load("bank_balances")
	if err != nil {
		t.Fatal(err)
	}
	rep, err := suite.Run(rows, map[string]map[string]bool{"bankAccounts": {"OPER": true, "PAY": true, "SAV": true, "YEN": true}, "iso4217": isoSet()})
	if err != nil {
		t.Fatal(err)
	}
	lines := make([]domain.BankBalanceLine, len(rows))
	for i, r := range rows {
		lines[i] = domain.BankBalanceLine{LegacyID: r["legacyId"], BankAccount: r["bankAccount"], Currency: r["currency"], Balance: dec(r["balance"])}
	}
	_, quarantined := domain.ValidateBankBalances(lines, map[string]struct{}{"OPER": {}, "PAY": {}, "SAV": {}, "YEN": {}})
	assertParity(t, "bank_balances", rows, rep, defects, quarantined, suite)
}

func glRow(id, account, dims, currency string) dq.Row {
	return dq.Row{"legacyId": id, "account": account, "dimensions": dims, "currency": currency}
}

func TestGLBalancesSuiteFlagsWhatTheGoValidatorQuarantines(t *testing.T) {
	clean := glRow("GL-0", "1000", "D1", "USD")
	defects := []defect{
		{check: "legacy_id_present", row: glRow("", "1100", "D1", "USD")},
		{check: "legacy_id_unique", row: glRow("GL-0", "1200", "D1", "USD")},
		{check: "account_dimension_currency_unique", row: glRow("GL-3", "1000", "D1", "USD")},
		{check: "account_in_chart", row: glRow("GL-4", "9999", "D1", "USD")},
		{check: "currency_iso4217", row: glRow("GL-5", "1300", "D1", "ZZZ")},
	}
	rows := []dq.Row{clean}
	for _, d := range defects {
		rows = append(rows, d.row)
	}
	suite, err := dq.Load("gl_balances")
	if err != nil {
		t.Fatal(err)
	}
	rep, err := suite.Run(rows, map[string]map[string]bool{"accounts": {"1000": true, "1100": true, "1200": true, "1300": true}, "iso4217": isoSet()})
	if err != nil {
		t.Fatal(err)
	}
	lines := make([]domain.GLBalance, len(rows))
	for i, r := range rows {
		lines[i] = domain.GLBalance{LegacyID: r["legacyId"], Account: r["account"], Dimensions: r["dimensions"], Currency: r["currency"], Balance: decimal.Zero}
	}
	_, quarantined := domain.ValidateGL(lines, map[string]struct{}{"1000": {}, "1100": {}, "1200": {}, "1300": {}})
	assertParity(t, "gl_balances", rows, rep, defects, quarantined, suite)
}

// assertParity checks that the suite and the Go validator flag the same rows, that each defect is caught by its own
// check, that every expectation of the suite is exercised, and that coverage is at least 95 %.
func assertParity(t *testing.T, pack string, rows []dq.Row, rep dq.Report, defects []defect, quarantined []domain.Quarantined, suite dq.Suite) {
	t.Helper()
	goFailed := map[int]bool{}
	// Map the Go quarantine back to row indexes: a row is quarantined when its legacy id and position match; duplicates of a
	// legacy id are told apart by order (the first occurrence is the one accepted).
	seen := map[string]int{}
	quarantineCount := map[string]int{}
	for _, q := range quarantined {
		quarantineCount[q.Record.LegacyID]++
	}
	for i, r := range rows {
		id := r["legacyId"]
		seen[id]++
		if quarantineCount[id] == 0 {
			continue
		}
		// blank ids and later duplicates are the quarantined ones; a unique id with a defect is quarantined too
		if id == "" || seen[id] > 1 || quarantineCount[id] > 0 && !firstOfDuplicated(rows, i) {
			goFailed[i] = true
		}
	}
	dqFailed := map[int]bool{}
	for _, i := range rep.Failed() {
		dqFailed[i] = true
	}
	if fmt.Sprint(sortedKeys(goFailed)) != fmt.Sprint(sortedKeys(dqFailed)) {
		t.Fatalf("%s: the Go validator quarantines rows %v, the suite flags rows %v", pack, sortedKeys(goFailed), sortedKeys(dqFailed))
	}
	// each defect row is flagged by its own check
	index := map[string]int{}
	for i, r := range rows {
		index[fmt.Sprintf("%p", r)] = i
	}
	caught := 0
	for _, d := range defects {
		i := index[fmt.Sprintf("%p", d.row)]
		found := false
		for _, res := range rep.Results {
			if res.Expectation.Check == d.check && slices.Contains(res.Unexpected, i) {
				found = true
			}
		}
		if !found {
			t.Errorf("%s: the check %s did not flag row %d", pack, d.check, i)
			continue
		}
		caught++
	}
	exercised := map[string]bool{}
	for _, d := range defects {
		exercised[d.check] = true
	}
	for _, e := range suite.Expectations {
		if !exercised[e.Check] {
			t.Errorf("%s: expectation %s has no defect fixture", pack, e.Check)
		}
	}
	if cov := float64(caught) / float64(len(defects)); cov < 0.95 {
		t.Errorf("%s: coverage %.0f%% is under 95%%", pack, cov*100)
	} else {
		t.Logf("%s: %d of %d validator checks covered (%.0f%%)", pack, caught, len(defects), cov*100)
	}
}

// firstOfDuplicated reports whether row i is the first row holding its legacy id while a later row repeats it.
func firstOfDuplicated(rows []dq.Row, i int) bool {
	id := rows[i]["legacyId"]
	for j := 0; j < i; j++ {
		if rows[j]["legacyId"] == id {
			return false
		}
	}
	for j := i + 1; j < len(rows); j++ {
		if rows[j]["legacyId"] == id {
			return true
		}
	}
	return false
}

func sortedKeys(m map[int]bool) []int {
	out := make([]int, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

func TestRunnerExpectations(t *testing.T) {
	rows := []dq.Row{{"a": "1", "c": "USD"}, {"a": "1", "c": "JPY"}, {"a": "", "c": "usd"}}
	sets := map[string]map[string]bool{"cur": {"USD": true, "JPY": true}}
	run := func(e dq.Expectation) []int {
		rep, err := dq.Suite{Pack: "x", Expectations: []dq.Expectation{e}}.Run(rows, sets)
		if err != nil {
			t.Fatal(err)
		}
		return rep.Results[0].Unexpected
	}
	if got := run(dq.Expectation{Type: dq.NotNull, Column: "a"}); !slices.Equal(got, []int{2}) {
		t.Fatalf("not null %v", got)
	}
	if got := run(dq.Expectation{Type: dq.Unique, Column: "a"}); !slices.Equal(got, []int{1}) {
		t.Fatalf("unique %v", got)
	}
	if got := run(dq.Expectation{Type: dq.InSet, Column: "c", Set: "cur"}); !slices.Equal(got, []int{2}) {
		t.Fatalf("in set %v", got)
	}
	if got := run(dq.Expectation{Type: dq.MatchRegex, Column: "c", Regex: "^[A-Z]{3}$"}); !slices.Equal(got, []int{2}) {
		t.Fatalf("regex %v", got)
	}
	if _, err := (dq.Suite{Expectations: []dq.Expectation{{Type: dq.InSet, Column: "c", Set: "missing"}}}).Run(rows, sets); err == nil || !strings.Contains(err.Error(), "missing") {
		t.Fatalf("an unknown reference set: %v", err)
	}
	if _, err := dq.Load("nope"); err == nil {
		t.Fatal("a pack without a suite")
	}
	if got := dq.Packs(); !slices.Equal(got, []string{"bank_balances", "gl_balances", "onhand"}) {
		t.Fatalf("packs %v", got)
	}
}
