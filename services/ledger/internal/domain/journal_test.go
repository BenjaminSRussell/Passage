package domain

import (
	"bytes"
	"testing"
	"time"

	"github.com/shopspring/decimal"
)

var today = time.Date(2026, 3, 15, 0, 0, 0, 0, time.UTC)

func dec(s string) decimal.Decimal { return decimal.RequireFromString(s) }

func testRules() Rules {
	return Rules{Currency: "USD", DimensionAttributes: []string{"DEPT", "CC"}, FutureDays: 366}
}

func testAccounts() map[string]Account {
	closed := time.Date(2025, 12, 31, 0, 0, 0, 0, time.UTC)
	return map[string]Account{
		"1000": {ID: "a-1000", Number: "1000", Postable: true, ReconType: "none"},
		"2000": {ID: "a-2000", Number: "2000", Postable: true, ReconType: "none"},
		"3000": {ID: "a-3000", Number: "3000", Postable: false, ReconType: "none"},
		"4000": {ID: "a-4000", Number: "4000", Postable: true, SuspendedFrom: &closed, ReconType: "none"},
		"5000": {ID: "a-5000", Number: "5000", Postable: true, ReconType: "customer"},
	}
}

func line(acct, side, amount string) LineInput {
	return LineInput{Account: acct, Side: side, Amount: dec(amount), Currency: "USD"}
}

func codes(res Result) []string {
	out := make([]string, 0, len(res.Violations))
	for _, v := range res.Violations {
		out = append(out, v.Code)
	}
	return out
}

func hasCode(res Result, code string) bool {
	for _, v := range res.Violations {
		if v.Code == code {
			return true
		}
	}
	return false
}

func TestValidateEntry_Table(t *testing.T) {
	pd := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	openRules := testRules()
	closedRules := testRules()
	firstOpen := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	closedRules.FirstOpenDate = &firstOpen
	manualRules := testRules()
	manualRules.Manual = true
	pastDate := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	farFuture := today.AddDate(2, 0, 0)

	tests := []struct {
		name     string
		rules    Rules
		date     time.Time
		lines    []LineInput
		wantCode string // empty means valid
		wantLine int    // -1 for entry-level
	}{
		{"balanced two lines", openRules, pd, []LineInput{line("1000", "D", "100.00"), line("2000", "C", "100.00")}, "", -1},
		{"unbalanced by one cent", openRules, pd, []LineInput{line("1000", "D", "100.00"), line("2000", "C", "99.99")}, "LEDGER_UNBALANCED", -1},
		{"no lines", openRules, pd, nil, "LEDGER_LINE_EMPTY", -1},
		{"unknown account", openRules, pd, []LineInput{line("9999", "D", "1"), line("2000", "C", "1")}, "LEDGER_ACCOUNT_NOT_FOUND", 0},
		{"total account not postable", openRules, pd, []LineInput{line("3000", "D", "1"), line("2000", "C", "1")}, "LEDGER_ACCOUNT_NOT_POSTABLE", 0},
		{"suspended account", openRules, pd, []LineInput{line("4000", "D", "1"), line("2000", "C", "1")}, "LEDGER_ACCOUNT_SUSPENDED", 0},
		{"recon account refused on a manual journal", manualRules, pd, []LineInput{line("2000", "C", "1"), line("5000", "D", "1")}, "LEDGER_RECON_ACCOUNT_MANUAL", 1},
		{"recon account accepted from a subledger", openRules, pd, []LineInput{line("5000", "D", "1"), line("2000", "C", "1")}, "", -1},
		{"blank dimension value means no value", openRules, pd, []LineInput{{Account: "1000", Side: "D", Amount: dec("1"), Currency: "USD", Dimensions: map[string]string{"DEPT": ""}}, line("2000", "C", "1")}, "", -1},
		{"excess decimals", openRules, pd, []LineInput{line("1000", "D", "1.001"), line("2000", "C", "1.001")}, "LEDGER_AMOUNT_SCALE", 0},
		{"zero line without text", openRules, pd, []LineInput{line("1000", "D", "0"), line("2000", "C", "0")}, "LEDGER_LINE_EMPTY", 0},
		{"currency mismatch", openRules, pd, []LineInput{{Account: "1000", Side: "D", Amount: dec("1"), Currency: "EUR"}, line("2000", "C", "1")}, "LEDGER_CURRENCY_MISMATCH", 0},
		{"unknown dimension attribute", openRules, pd, []LineInput{{Account: "1000", Side: "D", Amount: dec("1"), Currency: "USD", Dimensions: map[string]string{"PROJ": "P1"}}, line("2000", "C", "1")}, "LEDGER_DIMENSION_INVALID", 0},
		{"invalid dimension value", openRules, pd, []LineInput{{Account: "1000", Side: "D", Amount: dec("1"), Currency: "USD", Dimensions: map[string]string{"DEPT": "bad value!"}}, line("2000", "C", "1")}, "LEDGER_DIMENSION_INVALID", 0},
		{"valid dimensions", openRules, pd, []LineInput{{Account: "1000", Side: "D", Amount: dec("1"), Currency: "USD", Dimensions: map[string]string{"DEPT": "D100", "CC": "C200"}}, line("2000", "C", "1")}, "", -1},
		{"closed period before first open date", closedRules, pastDate.AddDate(0, -2, 0), []LineInput{line("1000", "D", "1"), line("2000", "C", "1")}, "LEDGER_PERIOD_CLOSED", -1},
		{"date too far ahead", openRules, farFuture, []LineInput{line("1000", "D", "1"), line("2000", "C", "1")}, "LEDGER_DATE_INVALID", -1},
		{"statistical zero line with quantity and unit", openRules, pd, []LineInput{{Account: "1000", Side: "D", Amount: dec("0"), Currency: "USD", Quantity: dec("12.5"), Unit: "KG"}, {Account: "2000", Side: "C", Amount: dec("0"), Currency: "USD", Quantity: dec("12.5"), Unit: "KG"}}, "", -1},
		{"quantity without unit", openRules, pd, []LineInput{{Account: "1000", Side: "D", Amount: dec("1"), Currency: "USD", Quantity: dec("2")}, line("2000", "C", "1")}, CodeQuantityInvalid, 0},
		{"unit without quantity", openRules, pd, []LineInput{{Account: "1000", Side: "D", Amount: dec("1"), Currency: "USD", Unit: "KG"}, line("2000", "C", "1")}, CodeQuantityInvalid, 0},
		{"bad unit code", openRules, pd, []LineInput{{Account: "1000", Side: "D", Amount: dec("1"), Currency: "USD", Quantity: dec("2"), Unit: "kg"}, line("2000", "C", "1")}, CodeQuantityInvalid, 0},
		{"negative quantity", openRules, pd, []LineInput{{Account: "1000", Side: "D", Amount: dec("1"), Currency: "USD", Quantity: dec("-2"), Unit: "KG"}, line("2000", "C", "1")}, CodeQuantityInvalid, 0},
		{"quantity with seven decimals", openRules, pd, []LineInput{{Account: "1000", Side: "D", Amount: dec("1"), Currency: "USD", Quantity: dec("2.0000001"), Unit: "KG"}, line("2000", "C", "1")}, CodeQuantityInvalid, 0},
		{"quantity too large", openRules, pd, []LineInput{{Account: "1000", Side: "D", Amount: dec("1"), Currency: "USD", Quantity: dec("1000000000000"), Unit: "KG"}, line("2000", "C", "1")}, CodeQuantityInvalid, 0},
		{"text-only zero line is allowed", openRules, pd, []LineInput{{Account: "1000", Side: "D", Amount: dec("0"), Currency: "USD", Text: "memo"}, {Account: "2000", Side: "C", Amount: dec("0"), Currency: "USD", Text: "memo"}}, "", -1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			res := ValidateEntry(tc.rules, testAccounts(), tc.date, today, tc.lines)
			if tc.wantCode == "" {
				if !res.Valid() {
					t.Fatalf("want valid, got %v", codes(res))
				}
				return
			}
			if !hasCode(res, tc.wantCode) {
				t.Fatalf("want %s, got %v", tc.wantCode, codes(res))
			}
			matched := false
			for _, v := range res.Violations {
				if v.Code != tc.wantCode {
					continue
				}
				if tc.wantLine < 0 && v.LineIndex == nil {
					matched = true
				}
				if tc.wantLine >= 0 && v.LineIndex != nil && *v.LineIndex == tc.wantLine {
					matched = true
				}
			}
			if !matched {
				t.Fatalf("want %s at line_index %d, got %+v", tc.wantCode, tc.wantLine, res.Violations)
			}
		})
	}
}

func TestValidateEntry_ReportsEveryViolation(t *testing.T) {
	lines := []LineInput{
		line("9999", "D", "1.001"),
		{Account: "3000", Side: "C", Amount: dec("2"), Currency: "USD", Dimensions: map[string]string{"NOPE": "x"}},
	}
	res := ValidateEntry(testRules(), testAccounts(), time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC), today, lines)
	for _, want := range []string{"LEDGER_ACCOUNT_NOT_FOUND", "LEDGER_AMOUNT_SCALE", "LEDGER_ACCOUNT_NOT_POSTABLE", "LEDGER_DIMENSION_INVALID", "LEDGER_UNBALANCED"} {
		if !hasCode(res, want) {
			t.Errorf("missing %s in %v", want, codes(res))
		}
	}
}

func TestValidateEntry_LineLimit(t *testing.T) {
	lines := make([]LineInput, MaxLinesPerEntry+1)
	res := ValidateEntry(testRules(), testAccounts(), today, today, lines)
	if !hasCode(res, "LEDGER_LINE_LIMIT") {
		t.Fatalf("want LEDGER_LINE_LIMIT, got %v", codes(res))
	}
}

func TestCombinationID_StableAndOrdered(t *testing.T) {
	attrs := []string{"DEPT", "CC"}
	a, nameA := CombinationID(attrs, "STD", "1010", map[string]string{"DEPT": "DEPT01", "CC": "CC200"})
	b, _ := CombinationID(attrs, "STD", "1010", map[string]string{"CC": "CC200", "DEPT": "DEPT01"})
	if !bytes.Equal(a, b) {
		t.Fatal("map order must not change the id")
	}
	if len(a) != 32 {
		t.Fatalf("want 32-byte id, got %d", len(a))
	}
	if nameA != "1010-DEPT01-CC200" {
		t.Fatalf("display name %q", nameA)
	}
	c, _ := CombinationID([]string{"CC", "DEPT"}, "STD", "1010", map[string]string{"DEPT": "DEPT01", "CC": "CC200"})
	if bytes.Equal(a, c) {
		t.Fatal("attribute order is part of the tuple and must change the id")
	}
	d, nameD := CombinationID(attrs, "STD", "1010", nil)
	if bytes.Equal(a, d) || nameD != "1010" {
		t.Fatalf("blank dimensions must hash differently: name %q", nameD)
	}
	e, _ := CombinationID(attrs, "OTHER", "1010", map[string]string{"DEPT": "DEPT01", "CC": "CC200"})
	if bytes.Equal(a, e) {
		t.Fatal("the chart is part of the tuple and must change the id")
	}
}

func TestAggregateBalances_SortedAndSummed(t *testing.T) {
	acctA := Account{ID: "acct-b"}
	acctB := Account{ID: "acct-a"}
	id1, _ := CombinationID(nil, "STD", "x", nil)
	lines := []ValidLine{
		{Account: acctA, Side: SideDebit, Amount: dec("10"), CombinationID: id1},
		{Account: acctA, Side: SideCredit, Amount: dec("4"), CombinationID: id1},
		{Account: acctB, Side: SideDebit, Amount: dec("1"), CombinationID: id1},
	}
	deltas := AggregateBalances("2026-03", lines)
	if len(deltas) != 2 {
		t.Fatalf("want 2 deltas, got %d", len(deltas))
	}
	if deltas[0].MainAccountID != "acct-a" {
		t.Fatalf("deltas must be sorted by key, first was %s", deltas[0].MainAccountID)
	}
	for _, d := range deltas {
		if d.MainAccountID == "acct-b" && (!d.Debit.Equal(dec("10")) || !d.Credit.Equal(dec("4")) || d.LineCount != 2) {
			t.Fatalf("bad sums: %+v", d)
		}
	}
}

func TestAggregateQuantities_PerUnitAndNegative(t *testing.T) {
	acct := Account{ID: "acct-a"}
	id1, _ := CombinationID(nil, "STD", "x", nil)
	lines := []ValidLine{
		{Account: acct, Side: SideDebit, Amount: dec("0"), Quantity: dec("5"), Unit: "KG", CombinationID: id1},
		{Account: acct, Side: SideCredit, Amount: dec("0"), Quantity: dec("2"), Unit: "KG", CombinationID: id1},
		{Account: acct, Side: SideDebit, Amount: dec("0"), Quantity: dec("3"), Unit: "PCS", CombinationID: id1},
		{Account: acct, Side: SideDebit, Amount: dec("9"), CombinationID: id1},
		{Account: acct, Side: SideDebit, Amount: dec("0"), Quantity: dec("1"), Unit: "KG", CombinationID: id1, Negative: true},
	}
	got := AggregateQuantities("2026-03", lines)
	if len(got) != 2 || got[0].Unit != "KG" || got[1].Unit != "PCS" {
		t.Fatalf("one delta per unit, sorted: %+v", got)
	}
	// A negative posting flips the side and negates the quantity (a storno of a credit line is
	// a negative debit line), so the negative 1 KG debit reduces the credit side: 2 - 1 = 1.
	if !got[0].Debit.Equal(dec("5")) || !got[0].Credit.Equal(dec("1")) || got[0].LineCount != 3 {
		t.Fatalf("KG: %+v", got[0])
	}
	if !got[1].Debit.Equal(dec("3")) || !got[1].Credit.IsZero() {
		t.Fatalf("PCS: %+v", got[1])
	}
}

func TestValidateEntry_BlankDimensionHashesLikeAbsent(t *testing.T) {
	pd := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	withBlank := ValidateEntry(testRules(), testAccounts(), pd, today, []LineInput{
		{Account: "1000", Side: "D", Amount: dec("1"), Currency: "USD", Dimensions: map[string]string{"DEPT": "", "CC": "C200"}}, line("2000", "C", "1")})
	without := ValidateEntry(testRules(), testAccounts(), pd, today, []LineInput{
		{Account: "1000", Side: "D", Amount: dec("1"), Currency: "USD", Dimensions: map[string]string{"CC": "C200"}}, line("2000", "C", "1")})
	if !withBlank.Valid() || !without.Valid() {
		t.Fatalf("both entries must be valid: %v / %v", codes(withBlank), codes(without))
	}
	if !bytes.Equal(withBlank.Lines[0].CombinationID, without.Lines[0].CombinationID) {
		t.Fatal("a blank value must intern to the same combination as an absent one")
	}
	if _, ok := withBlank.Lines[0].Dimensions["DEPT"]; ok {
		t.Fatalf("blank values must not be stored: %v", withBlank.Lines[0].Dimensions)
	}
}

func TestCheckReversalDate(t *testing.T) {
	orig := time.Date(2026, 3, 10, 0, 0, 0, 0, time.UTC)
	if v := CheckReversalDate(testRules(), orig, orig, today); len(v) != 0 {
		t.Fatalf("same-day reversal must pass, got %+v", v)
	}
	if v := CheckReversalDate(testRules(), orig, orig.AddDate(0, 0, 3), today); len(v) != 0 {
		t.Fatalf("later reversal must pass, got %+v", v)
	}
	v := CheckReversalDate(testRules(), orig, orig.AddDate(0, 0, -1), today)
	if len(v) != 1 || v[0].Code != "LEDGER_DATE_INVALID" || v[0].Field != "postingDate" {
		t.Fatalf("reversal before the original must be LEDGER_DATE_INVALID, got %+v", v)
	}
	firstOpen := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	closed := testRules()
	closed.FirstOpenDate = &firstOpen
	early := time.Date(2026, 1, 10, 0, 0, 0, 0, time.UTC)
	if v := CheckReversalDate(closed, early, early, today); len(v) != 1 || v[0].Code != "LEDGER_PERIOD_CLOSED" {
		t.Fatalf("reversal into a closed period must be LEDGER_PERIOD_CLOSED, got %+v", v)
	}
}

func TestIsManualSource(t *testing.T) {
	if !IsManualSource("ledger", "manual_journal") || IsManualSource("ap", "vendor_invoice") || IsManualSource("ap", "manual_journal") {
		t.Fatal("only ledger/manual_journal is a manual source")
	}
}

func TestReversalSide(t *testing.T) {
	if ReversalSide("D") != "C" || ReversalSide("C") != "D" {
		t.Fatal("reversal must flip the side")
	}
}

func TestPeriodKey(t *testing.T) {
	if got := PeriodKey(time.Date(2026, 3, 31, 0, 0, 0, 0, time.UTC)); got != "2026-03" {
		t.Fatalf("got %s", got)
	}
}
