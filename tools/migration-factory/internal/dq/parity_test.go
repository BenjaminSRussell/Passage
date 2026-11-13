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

func TestOpenItemsSuiteFlagsWhatTheGoValidatorQuarantines(t *testing.T) {
	rec := func(id, ledger, party, account, currency string) domain.Record {
		return domain.Record{LegacyID: id, Ledger: ledger, Party: party, Account: account, Currency: currency, Amount: dec("10")}
	}
	records := []domain.Record{rec("OI-0", "", "C1", "1200", "USD"), rec("OI-0", "AP", "V1", "2100", "USD")} // the same id on the two sides is fine
	defects := []struct {
		check string
		r     domain.Record
	}{
		{"legacy_id_present", rec("", "", "C1", "1200", "USD")},
		{"legacy_id_unique_per_side", rec("OI-0", "AR", "C1", "1200", "USD")},
		{"party_in_master", rec("OI-3", "", "NOPE", "1200", "USD")},
		{"currency_iso4217", rec("OI-4", "", "C1", "1200", "ZZZ")},
		{"control_account_required", rec("OI-5", "AP", "V1", " ", "USD")},
	}
	var ds []defect
	for _, d := range defects {
		records = append(records, d.r)
	}
	rows := dq.OpenItemRows(records)
	for i, d := range defects {
		ds = append(ds, defect{check: d.check, row: rows[2+i]})
	}
	suite, err := dq.Load("open_items")
	if err != nil {
		t.Fatal(err)
	}
	rep, err := suite.Run(rows, map[string]map[string]bool{"parties": {"C1": true, "V1": true}, "iso4217": isoSet()})
	if err != nil {
		t.Fatal(err)
	}
	_, quarantined := domain.Validate(records, map[string]struct{}{"C1": {}, "V1": {}})
	assertRowParity(t, "open_items", rows, rep, ds, len(quarantined), suite, map[int]bool{2: true, 3: true, 4: true, 5: true, 6: true})
}

func TestOpenOrdersSuiteFlagsWhatTheGoValidatorQuarantines(t *testing.T) {
	line := func(id, typ, order, ln, item, party string) domain.OpenOrderLine {
		return domain.OpenOrderLine{LegacyID: id, OrderType: typ, OrderNumber: order, LineNumber: ln, Item: item, Party: party, Unit: "EA", Currency: "USD",
			OrderedQty: dec("10"), DeliveredQty: dec("2"), UnitPrice: dec("5"), DeliveryDate: "2026-12-01"}
	}
	with := func(o domain.OpenOrderLine, f func(*domain.OpenOrderLine)) domain.OpenOrderLine { f(&o); return o }
	lines := []domain.OpenOrderLine{line("L-0", "SALES", "SO1", "1", "BOLT", "C1"), line("L-0", "PURCHASE", "PO1", "1", "BOLT", "V1")}
	defects := []struct {
		check string
		o     domain.OpenOrderLine
	}{
		{"legacy_id_present", line("", "SALES", "SO2", "1", "BOLT", "C1")},
		{"order_type_known", line("L-3", "RETURN", "RO1", "1", "BOLT", "C1")},
		{"legacy_id_unique_per_order_type", line("L-0", "SALES", "SO3", "1", "BOLT", "C1")},
		{"order_and_line_number_required", line("L-5", "SALES", "", "1", "BOLT", "C1")},
		{"order_line_unique", line("L-6", "SALES", "SO1", "1", "BOLT", "C1")},
		{"item_in_master", line("L-7", "SALES", "SO4", "1", "NOPE", "C1")},
		{"party_in_master", line("L-8", "PURCHASE", "PO2", "1", "BOLT", "C1")},
		{"unit_required", with(line("L-9", "SALES", "SO5", "1", "BOLT", "C1"), func(o *domain.OpenOrderLine) { o.Unit = "" })},
		{"ordered_quantity_positive", with(line("L-10", "SALES", "SO6", "1", "BOLT", "C1"), func(o *domain.OpenOrderLine) { o.OrderedQty, o.DeliveredQty = dec("0"), dec("0") })},
		{"delivered_quantity_not_negative", with(line("L-11", "SALES", "SO7", "1", "BOLT", "C1"), func(o *domain.OpenOrderLine) { o.DeliveredQty = dec("-1") })},
		{"line_still_open", with(line("L-12", "SALES", "SO8", "1", "BOLT", "C1"), func(o *domain.OpenOrderLine) { o.DeliveredQty = dec("10") })},
		{"unit_price_not_negative", with(line("L-13", "SALES", "SO9", "1", "BOLT", "C1"), func(o *domain.OpenOrderLine) { o.UnitPrice = dec("-1") })},
		{"delivery_date_iso", with(line("L-14", "SALES", "SO10", "1", "BOLT", "C1"), func(o *domain.OpenOrderLine) { o.DeliveryDate = "2026-02-30" })},
		{"currency_iso4217", with(line("L-15", "SALES", "SO11", "1", "BOLT", "C1"), func(o *domain.OpenOrderLine) { o.Currency = "ZZZ" })},
	}
	for _, d := range defects {
		lines = append(lines, d.o)
	}
	rows := dq.OpenOrderRows(lines)
	var ds []defect
	failed := map[int]bool{}
	for i, d := range defects {
		ds = append(ds, defect{check: d.check, row: rows[2+i]})
		failed[2+i] = true
	}
	suite, err := dq.Load("open_orders")
	if err != nil {
		t.Fatal(err)
	}
	rep, err := suite.Run(rows, map[string]map[string]bool{"items": {"BOLT": true}, "customers": {"C1": true}, "vendors": {"V1": true}, "iso4217": isoSet()})
	if err != nil {
		t.Fatal(err)
	}
	_, quarantined := domain.ValidateOpenOrders(lines, domain.OrderMasters{Items: map[string]struct{}{"BOLT": {}}, Customers: map[string]struct{}{"C1": {}}, Vendors: map[string]struct{}{"V1": {}}})
	assertRowParity(t, "open_orders", rows, rep, ds, len(quarantined), suite, failed)
}

func TestWIPProdSuiteFlagsWhatTheGoValidatorQuarantines(t *testing.T) {
	line := func(id, order, item, currency, material string) domain.WIPProdLine {
		return domain.WIPProdLine{LegacyID: id, ProductionOrder: order, Item: item, Currency: currency, Material: dec(material), Labor: dec("10"), Overhead: dec("5")}
	}
	lines := []domain.WIPProdLine{line("W-0", "PO-1", "PUMP", "USD", "100")}
	defects := []struct {
		check string
		w     domain.WIPProdLine
	}{
		{"legacy_id_present", line("", "PO-2", "PUMP", "USD", "1")},
		{"legacy_id_unique", line("W-0", "PO-3", "PUMP", "USD", "1")},
		{"production_order_unique", line("W-4", "PO-1", "PUMP", "USD", "1")},
		{"currency_iso4217", line("W-5", "PO-5", "PUMP", "ZZZ", "1")},
		{"measures_at_most_6_places", line("W-6", "PO-6", "PUMP", "USD", "1.1234567")},
		{"production_order_required", line("W-7", "", "PUMP", "USD", "1")},
		{"item_in_master", line("W-8", "PO-8", "NOPE", "USD", "1")},
		{"components_not_negative", line("W-9", "PO-9", "PUMP", "USD", "-1")},
	}
	for _, d := range defects {
		lines = append(lines, d.w)
	}
	rows := dq.WIPProdRows(lines)
	var ds []defect
	failed := map[int]bool{}
	for i, d := range defects {
		ds = append(ds, defect{check: d.check, row: rows[1+i]})
		failed[1+i] = true
	}
	suite, err := dq.Load("wip_prod")
	if err != nil {
		t.Fatal(err)
	}
	rep, err := suite.Run(rows, map[string]map[string]bool{"items": {"PUMP": true}, "iso4217": isoSet()})
	if err != nil {
		t.Fatal(err)
	}
	_, quarantined := domain.ValidateWIPProd(lines, map[string]struct{}{"PUMP": {}})
	assertRowParity(t, "wip_prod", rows, rep, ds, len(quarantined), suite, failed)
}

func TestPayrollYTDSuiteFlagsWhatTheGoValidatorQuarantines(t *testing.T) {
	line := func(id, worker, year, currency, gross, tax, ded, net string) domain.PayrollYTDLine {
		return domain.PayrollYTDLine{LegacyID: id, Worker: worker, TaxYear: year, Currency: currency, Gross: dec(gross), Tax: dec(tax), Deductions: dec(ded), Net: dec(net)}
	}
	lines := []domain.PayrollYTDLine{line("P-0", "W1", "2026", "USD", "1000", "200", "50", "750")}
	defects := []struct {
		check string
		p     domain.PayrollYTDLine
	}{
		{"legacy_id_present", line("", "W2", "2026", "USD", "10", "0", "0", "10")},
		{"legacy_id_unique", line("P-0", "W3", "2026", "USD", "10", "0", "0", "10")},
		{"worker_tax_year_unique", line("P-3", "W1", "2026", "USD", "10", "0", "0", "10")},
		{"currency_iso4217", line("P-4", "W4", "2026", "ZZZ", "10", "0", "0", "10")},
		{"worker_in_master", line("P-5", "NOPE", "2026", "USD", "10", "0", "0", "10")},
		{"tax_year_yyyy", line("P-6", "W6", "26", "USD", "10", "0", "0", "10")},
		{"amounts_not_negative", line("P-7", "W7", "2026", "USD", "10", "-1", "0", "11")},
		{"amounts_in_minor_units", line("P-8", "W8", "2026", "JPY", "10.5", "0", "0", "10.5")},
		{"net_is_gross_less_tax_and_deductions", line("P-9", "W9", "2026", "USD", "10", "1", "0", "10")},
	}
	for _, d := range defects {
		lines = append(lines, d.p)
	}
	rows := dq.PayrollYTDRows(lines)
	var ds []defect
	failed := map[int]bool{}
	for i, d := range defects {
		ds = append(ds, defect{check: d.check, row: rows[1+i]})
		failed[1+i] = true
	}
	suite, err := dq.Load("payroll_ytd")
	if err != nil {
		t.Fatal(err)
	}
	workers := map[string]bool{}
	master := map[string]struct{}{}
	for _, w := range []string{"W1", "W2", "W3", "W4", "W6", "W7", "W8", "W9"} {
		workers[w] = true
		master[w] = struct{}{}
	}
	rep, err := suite.Run(rows, map[string]map[string]bool{"workers": workers, "iso4217": isoSet()})
	if err != nil {
		t.Fatal(err)
	}
	_, quarantined := domain.ValidatePayrollYTD(lines, master)
	assertRowParity(t, "payroll_ytd", rows, rep, ds, len(quarantined), suite, failed)
}

// assertRowParity is assertParity for the packs whose fixtures say which rows the validator quarantines: the suite must flag
// exactly those rows, the validator must quarantine as many, each defect must be caught by its own check, every expectation
// must be exercised and coverage must be at least 95 %.
func assertRowParity(t *testing.T, pack string, rows []dq.Row, rep dq.Report, defects []defect, quarantined int, suite dq.Suite, want map[int]bool) {
	t.Helper()
	if quarantined != len(want) {
		t.Fatalf("%s: the Go validator quarantines %d rows, the fixtures expect %d", pack, quarantined, len(want))
	}
	got := map[int]bool{}
	for _, i := range rep.Failed() {
		got[i] = true
	}
	if fmt.Sprint(sortedKeys(got)) != fmt.Sprint(sortedKeys(want)) {
		t.Fatalf("%s: the suite flags rows %v, the validator quarantines rows %v", pack, sortedKeys(got), sortedKeys(want))
	}
	index := map[string]int{}
	for i, r := range rows {
		index[fmt.Sprintf("%p", r)] = i
	}
	caught := 0
	exercised := map[string]bool{}
	for _, d := range defects {
		exercised[d.check] = true
		i := index[fmt.Sprintf("%p", d.row)]
		for _, res := range rep.Results {
			if res.Expectation.Check == d.check && slices.Contains(res.Unexpected, i) {
				caught++
				break
			}
		}
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
	if got := dq.Packs(); !slices.Equal(got, []string{"bank_balances", "gl_balances", "onhand", "open_items", "open_orders", "payroll_ytd", "wip_prod"}) {
		t.Fatalf("packs %v", got)
	}
}
