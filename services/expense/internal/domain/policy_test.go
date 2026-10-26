package domain

import (
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"pgregory.net/rapid"
)

var (
	txn    = time.Date(2026, time.March, 10, 0, 0, 0, 0, time.UTC)
	submit = txn.AddDate(0, 0, 5)
)

func amt(s string) decimal.Decimal { return decimal.RequireFromString(s) }

func outcomes(rs []Result) map[string]Outcome {
	m := map[string]Outcome{}
	for _, r := range rs {
		m[r.RuleID] = r.Outcome
	}
	return m
}

func TestEvaluateDefaultRules(t *testing.T) {
	rules := DefaultRules()
	tests := []struct {
		name       string
		line       Line
		ctx        Context
		wantRules  map[string]Outcome
		wantSubmit bool
	}{
		{"receipt missing at 25.00", Line{Category: "TRAVEL", Amount: amt("25.00"), Merchant: "Air"}, Context{submit}, map[string]Outcome{"EXP-RCPT-025": OutcomeRequireReceipt}, false},
		{"receipt present at 25.00", Line{Category: "TRAVEL", Amount: amt("25.00"), HasReceipt: true}, Context{submit}, map[string]Outcome{}, true},
		{"24.99 needs no receipt", Line{Category: "TRAVEL", Amount: amt("24.99")}, Context{submit}, map[string]Outcome{}, true},
		{"alcohol is blocked", Line{Category: "ALCOHOL", Amount: amt("12.00"), HasReceipt: true}, Context{submit}, map[string]Outcome{"EXP-ALC-001": OutcomeBlock}, false},
		{"60 days is still in time", Line{Category: "TRAVEL", Amount: amt("5"), HasReceipt: true, TxnDate: txn}, Context{txn.AddDate(0, 0, 60)}, map[string]Outcome{}, true},
		{"61 days is blocked", Line{Category: "TRAVEL", Amount: amt("5"), HasReceipt: true, TxnDate: txn}, Context{txn.AddDate(0, 0, 61)}, map[string]Outcome{"EXP-SUB-060": OutcomeBlock}, false},
		{"meal over 75 without attendees", Line{Category: "MEALS", Amount: amt("80"), HasReceipt: true}, Context{submit}, map[string]Outcome{"EXP-MEAL-075": OutcomeRequireJustification}, false},
		{"meal over 75 with attendees", Line{Category: "MEALS", Amount: amt("80"), HasReceipt: true, Attendees: 3}, Context{submit}, map[string]Outcome{}, true},
		{"meal exactly 75 is not over", Line{Category: "MEALS", Amount: amt("75"), HasReceipt: true}, Context{submit}, map[string]Outcome{}, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tc.line.ID = "L1"
			if tc.line.TxnDate.IsZero() {
				tc.line.TxnDate = txn
			}
			got := Evaluate(rules, tc.line, tc.ctx)
			m := outcomes(got)
			if len(m) != len(tc.wantRules) {
				t.Fatalf("rules %v, want %v", m, tc.wantRules)
			}
			for id, o := range tc.wantRules {
				if m[id] != o {
					t.Errorf("%s = %s, want %s", id, m[id], o)
				}
			}
			if Submittable(got) != tc.wantSubmit {
				t.Errorf("submittable = %v, want %v", Submittable(got), tc.wantSubmit)
			}
		})
	}
}

func TestEvaluateReportLooksAcrossLines(t *testing.T) {
	rules := DefaultRules()
	nextDay := txn.AddDate(0, 0, 1)
	lines := []Line{
		// Two meals on one day: 40 + 45 = 85 > 75 a day without attendees.
		{ID: "M1", Category: "MEALS", Amount: amt("40"), HasReceipt: true, TxnDate: txn, Merchant: "Cafe"},
		{ID: "M2", Category: "MEALS", Amount: amt("45"), HasReceipt: true, TxnDate: txn, Merchant: "Bistro"},
		// A meal with attendees is not selected and does not count toward the day.
		{ID: "M3", Category: "MEALS", Amount: amt("200"), HasReceipt: true, TxnDate: nextDay, Attendees: 4, Merchant: "Grill"},
		// 70 alone the next day stays under the limit.
		{ID: "M4", Category: "MEALS", Amount: amt("70"), HasReceipt: true, TxnDate: nextDay, Merchant: "Diner"},
		// Same merchant, amount and date as T1: the repeat is blocked, the first is not.
		{ID: "T1", Category: "TRAVEL", Amount: amt("20"), TxnDate: txn, Merchant: "Taxi Co"},
		{ID: "T2", Category: "TRAVEL", Amount: amt("20"), TxnDate: txn, Merchant: " taxi co "},
	}
	got := map[string]Result{}
	for _, r := range EvaluateReport(rules, lines, Context{submit}) {
		got[r.LineID+"/"+r.RuleID] = r
	}
	want := map[string]string{"M1/EXP-MEAL-075": CodeBlock, "M2/EXP-MEAL-075": CodeBlock, "T2/EXP-DUP-001": CodeBlock}
	if len(got) != len(want) {
		t.Fatalf("results %v, want %v", got, want)
	}
	for k, code := range want {
		if r, ok := got[k]; !ok || r.Code != code || r.Satisfied {
			t.Errorf("%s = %+v, want unsatisfied %s", k, r, code)
		}
	}
}

func TestOutcomeCodes(t *testing.T) {
	for o, want := range map[Outcome]string{OutcomeBlock: "EXP-POL-001", OutcomeWarn: "EXP-POL-002",
		OutcomeRequireReceipt: "EXP-REC-001", OutcomeRequireJustification: "EXP-POL-001"} {
		if got := outcomeCode(o); got != want {
			t.Errorf("%s -> %s, want %s", o, got, want)
		}
	}
}

func TestLatestEffectiveRuleVersionWins(t *testing.T) {
	jan := civil(2026, time.January, 1)
	jun := civil(2026, time.June, 1)
	rules := []Rule{
		{ID: "EXP-TAXI", Version: 1, EffectiveFrom: jan, Categories: []string{"TAXI"}, Predicate: PredAmountOver, Amount: amt("50"), Outcome: OutcomeWarn, Message: "v1"},
		{ID: "EXP-TAXI", Version: 2, EffectiveFrom: jun, Categories: []string{"TAXI"}, Predicate: PredAmountOver, Amount: amt("80"), Outcome: OutcomeBlock, Message: "v2"},
	}
	tests := []struct {
		name    string
		date    time.Time
		amount  string
		version int
	}{
		{"before v2 is effective v1 applies", civil(2026, time.March, 1), "60", 1},
		{"after v2 is effective v1 no longer applies", civil(2026, time.July, 1), "60", 0},
		{"after v2 is effective v2 applies", civil(2026, time.July, 1), "90", 2},
		{"before any version nothing applies", civil(2025, time.December, 31), "90", 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := Evaluate(rules, Line{ID: "L", Category: "TAXI", Amount: amt(tc.amount), TxnDate: tc.date}, Context{tc.date})
			if tc.version == 0 {
				if len(got) != 0 {
					t.Fatalf("got %+v, want none", got)
				}
				return
			}
			if len(got) != 1 || got[0].Version != tc.version {
				t.Fatalf("got %+v, want version %d", got, tc.version)
			}
		})
	}
}

func TestJustificationSatisfiesRequireJustification(t *testing.T) {
	line := Line{ID: "L1", Category: "MEALS", Amount: amt("80"), HasReceipt: true, TxnDate: txn, Justification: "  "}
	if Submittable(Evaluate(DefaultRules(), line, Context{submit})) {
		t.Fatal("blank justification must not satisfy the rule")
	}
	line.Justification = "client dinner, paid by company card holder"
	if !Submittable(Evaluate(DefaultRules(), line, Context{submit})) {
		t.Fatal("justified line should be submittable")
	}
}

func TestReceiptRuleNeverFlagsSmallLinesOrReceiptedLines(t *testing.T) {
	rules := DefaultRules()
	rapid.Check(t, func(t *rapid.T) {
		cents := rapid.Int64Range(0, 1_000_000).Draw(t, "cents")
		receipt := rapid.Bool().Draw(t, "receipt")
		line := Line{ID: "L", Category: "TRAVEL", TxnDate: txn, HasReceipt: receipt,
			Amount: decimal.New(cents, -2)}
		for _, r := range Evaluate(rules, line, Context{submit}) {
			if r.RuleID == "EXP-RCPT-025" && (receipt || line.Amount.LessThan(amt("25"))) {
				t.Fatalf("false receipt flag for %s receipt=%v", line.Amount, receipt)
			}
		}
	})
}

func TestSumLinesInvariant(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		n := rapid.IntRange(0, 8).Draw(t, "n")
		lines := make([]Line, n)
		for i := range lines {
			lines[i] = Line{Amount: decimal.New(rapid.Int64Range(0, 100_000).Draw(t, "cents"), -2)}
			if rapid.Bool().Draw(t, "card") {
				lines[i].PaidBy = PaidByCompanyCard
			}
		}
		got := SumLines(lines)
		if !got.Total.Equal(got.Reimbursable.Add(got.CompanyPaid)) {
			t.Fatalf("total %s != reimbursable %s + company %s", got.Total, got.Reimbursable, got.CompanyPaid)
		}
	})
}
