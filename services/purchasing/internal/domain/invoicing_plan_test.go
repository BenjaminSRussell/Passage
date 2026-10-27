package domain

import (
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"pgregory.net/rapid"
)

// ymd builds a plan day from year, month and day of month.
func ymd(y int, m time.Month, dd int) Day {
	return Day{time.Date(y, m, dd, 0, 0, 0, 0, time.UTC)}
}

func dptr(s string) *decimal.Decimal { v := d(s); return &v }

func amounts(rows []PlanRow) []string {
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = r.Amount.StringFixed(2)
	}
	return out
}

func TestGeneratePeriodic_AC17_15(t *testing.T) {
	spec := PeriodicSpec{Start: ymd(2027, 1, 1), Frequency: FreqMonthly, Periods: 12, BillingDay: 1, AmountPerPeriod: d("1000.00")}
	rows, err := GeneratePeriodic(spec, d("12000.00"), 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 12 {
		t.Fatalf("rows = %d, want 12", len(rows))
	}
	for i, r := range rows {
		want := ymd(2027, time.Month(i+1), 1)
		if !r.DueDate.Equal(want.Time) || !r.Amount.Equal(d("1000")) || r.Status != PlanRowOpen || r.Seq != i+1 {
			t.Fatalf("row %d = %+v, want 1000.00 due %s OPEN", i+1, r, want)
		}
	}
	if !PlanTotal(rows).Equal(d("12000")) {
		t.Fatalf("Σ = %s", PlanTotal(rows))
	}
}

func TestGeneratePeriodic_Rules(t *testing.T) {
	base := PeriodicSpec{Start: ymd(2027, 1, 15), Frequency: FreqMonthly, Periods: 4, AmountPerPeriod: d("250")}
	tests := []struct {
		name   string
		mutate func(*PeriodicSpec)
		line   string
		code   string
	}{
		{"periods × amount ≠ line amount is PRC-IP-001", func(*PeriodicSpec) {}, "1001", CodeInvoicingPlan},
		{"weekly is not a frequency", func(s *PeriodicSpec) { s.Frequency = "WEEKLY" }, "1000", CodeValidation},
		{"zero periods", func(s *PeriodicSpec) { s.Periods = 0 }, "1000", CodeValidation},
		{"more than 120 periods", func(s *PeriodicSpec) { s.Periods = 121 }, "1000", CodeValidation},
		{"billing day 32", func(s *PeriodicSpec) { s.BillingDay = 32 }, "1000", CodeValidation},
		{"no start", func(s *PeriodicSpec) { s.Start = Day{} }, "1000", CodeValidation},
		{"zero amount", func(s *PeriodicSpec) { s.AmountPerPeriod = d("0") }, "0", CodeValidation},
		{"amount finer than the currency", func(s *PeriodicSpec) { s.AmountPerPeriod = d("250.001") }, "1000.004", CodeValidation},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			spec := base
			tc.mutate(&spec)
			if _, err := GeneratePeriodic(spec, d(tc.line), 2); codeOf(err) != tc.code {
				t.Fatalf("code = %q, want %q (%v)", codeOf(err), tc.code, err)
			}
		})
	}
}

func TestGeneratePeriodic_BillingDayDefaultsAndClamps(t *testing.T) {
	rows, err := GeneratePeriodic(PeriodicSpec{Start: ymd(2027, 1, 15), Frequency: FreqMonthly, Periods: 2, AmountPerPeriod: d("5")}, d("10"), 2)
	if err != nil {
		t.Fatal(err)
	}
	if !rows[0].DueDate.Equal(ymd(2027, 1, 15).Time) || !rows[1].DueDate.Equal(ymd(2027, 2, 15).Time) {
		t.Fatalf("billing day should default to the start day: %v, %v", rows[0].DueDate, rows[1].DueDate)
	}
	rows, err = GeneratePeriodic(PeriodicSpec{Start: ymd(2027, 11, 1), Frequency: FreqQuarterly, Periods: 4, BillingDay: 31, AmountPerPeriod: d("250")}, d("1000"), 2)
	if err != nil {
		t.Fatal(err)
	}
	want := []Day{ymd(2027, 11, 30), ymd(2028, 2, 29), ymd(2028, 5, 31), ymd(2028, 8, 31)}
	for i, w := range want {
		if !rows[i].DueDate.Equal(w.Time) {
			t.Fatalf("row %d due %s, want %s", i+1, rows[i].DueDate, w)
		}
	}
}

func TestGeneratePartial(t *testing.T) {
	tests := []struct {
		name string
		ms   []Milestone
		line string
		want []string
		code string
	}{
		{"percent 30/70", []Milestone{{Date: ymd(2027, 2, 1), Percent: dptr("30")}, {Date: ymd(2027, 3, 1), Percent: dptr("70")}}, "1000", []string{"300.00", "700.00"}, ""},
		{"thirds share the cent by largest remainder", []Milestone{
			{Date: ymd(2027, 2, 1), Percent: dptr("33.333333")}, {Date: ymd(2027, 3, 1), Percent: dptr("33.333333")}, {Date: ymd(2027, 4, 1), Percent: dptr("33.333334")},
		}, "100", []string{"33.33", "33.33", "33.34"}, ""},
		{"percents not 100 is PRC-IP-001", []Milestone{{Date: ymd(2027, 2, 1), Percent: dptr("30")}, {Date: ymd(2027, 3, 1), Percent: dptr("60")}}, "1000", nil, CodeInvoicingPlan},
		{"percent above 100", []Milestone{{Date: ymd(2027, 2, 1), Percent: dptr("101")}}, "1000", nil, CodeValidation},
		{"percent with 7 decimals", []Milestone{{Date: ymd(2027, 2, 1), Percent: dptr("99.9999999")}, {Date: ymd(2027, 2, 1), Percent: dptr("0.0000001")}}, "1000", nil, CodeValidation},
		{"a percent that rounds to nothing", []Milestone{{Date: ymd(2027, 2, 1), Percent: dptr("99.9")}, {Date: ymd(2027, 3, 1), Percent: dptr("0.1")}}, "1.00", nil, CodeInvoicingPlan},
		{"amounts equal the line", []Milestone{{Date: ymd(2027, 2, 1), Amount: dptr("400")}, {Date: ymd(2027, 3, 1), Amount: dptr("600")}}, "1000", []string{"400.00", "600.00"}, ""},
		{"amounts miss the line is PRC-IP-001", []Milestone{{Date: ymd(2027, 2, 1), Amount: dptr("400")}}, "1000", nil, CodeInvoicingPlan},
		{"negative amount", []Milestone{{Date: ymd(2027, 2, 1), Amount: dptr("-1")}, {Date: ymd(2027, 2, 1), Amount: dptr("1001")}}, "1000", nil, CodeValidation},
		{"percent and amount mixed", []Milestone{{Date: ymd(2027, 2, 1), Percent: dptr("50")}, {Date: ymd(2027, 3, 1), Amount: dptr("500")}}, "1000", nil, CodeValidation},
		{"both on one milestone", []Milestone{{Date: ymd(2027, 2, 1), Percent: dptr("100"), Amount: dptr("1000")}}, "1000", nil, CodeValidation},
		{"dates decrease", []Milestone{{Date: ymd(2027, 3, 1), Percent: dptr("50")}, {Date: ymd(2027, 2, 1), Percent: dptr("50")}}, "1000", nil, CodeValidation},
		{"no date", []Milestone{{Percent: dptr("100")}}, "1000", nil, CodeValidation},
		{"no milestones", nil, "1000", nil, CodeValidation},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rows, err := GeneratePartial(tc.ms, d(tc.line), 2)
			if codeOf(err) != tc.code {
				t.Fatalf("code = %q, want %q (%v)", codeOf(err), tc.code, err)
			}
			if tc.code != "" {
				return
			}
			if got := amounts(rows); len(got) != len(tc.want) || got[0] != tc.want[0] || got[len(got)-1] != tc.want[len(tc.want)-1] {
				t.Fatalf("amounts = %v, want %v", got, tc.want)
			}
			if !PlanTotal(rows).Equal(d(tc.line)) {
				t.Fatalf("Σ = %s, want %s", PlanTotal(rows), tc.line)
			}
		})
	}
}

// AC-17.15: the line changes from 12,000.00 to 13,200.00 before any invoice and
// all 12 rows become 1,100.00.
func TestReproportion_AC17_15(t *testing.T) {
	spec := PeriodicSpec{Start: ymd(2027, 1, 1), Frequency: FreqMonthly, Periods: 12, BillingDay: 1, AmountPerPeriod: d("1000.00")}
	rows, err := GeneratePeriodic(spec, d("12000.00"), 2)
	if err != nil {
		t.Fatal(err)
	}
	out, err := Reproportion(rows, d("13200.00"), 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 12 {
		t.Fatalf("rows = %d", len(out))
	}
	for i, r := range out {
		if !r.Amount.Equal(d("1100")) || !r.DueDate.Equal(rows[i].DueDate.Time) {
			t.Fatalf("row %d = %s due %s, want 1100.00 due %s", i+1, r.Amount, r.DueDate, rows[i].DueDate)
		}
	}
	if !rows[0].Amount.Equal(d("1000")) {
		t.Fatal("Reproportion modified its input")
	}
}

func TestReproportion_InvoicedRowsKeepTheirAmount(t *testing.T) {
	rows := []PlanRow{
		{Seq: 1, Amount: d("500"), Status: PlanRowInvoiced},
		{Seq: 2, Amount: d("300"), Status: PlanRowOpen},
		{Seq: 3, Amount: d("200"), Status: PlanRowOpen},
	}
	out, err := Reproportion(rows, d("1500"), 2)
	if err != nil {
		t.Fatal(err)
	}
	if got := amounts(out); got[0] != "500.00" || got[1] != "600.00" || got[2] != "400.00" {
		t.Fatalf("amounts = %v, want [500 600 400]", got)
	}
	// Down to exactly what is invoiced: the open rows drop out.
	out, err = Reproportion(rows, d("500"), 2)
	if err != nil || len(out) != 1 || out[0].Status != PlanRowInvoiced {
		t.Fatalf("down to invoiced = %+v, %v", out, err)
	}
	if _, err := Reproportion(rows, d("499.99"), 2); codeOf(err) != CodeInvoicingPlan {
		t.Fatalf("below invoiced: code %q", codeOf(err))
	}
	all := []PlanRow{{Seq: 1, Amount: d("100"), Status: PlanRowInvoiced}}
	if _, err := Reproportion(all, d("120"), 2); codeOf(err) != CodeInvoicingPlan {
		t.Fatalf("increase with no open row: code %q", codeOf(err))
	}
}

func TestReproportion_DropsRowsThatShareNothing(t *testing.T) {
	rows := make([]PlanRow, 7)
	for i := range rows {
		rows[i] = PlanRow{Seq: i + 1, Amount: d("10"), Status: PlanRowOpen}
	}
	out, err := Reproportion(rows, d("0.05"), 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 5 || !PlanTotal(out).Equal(d("0.05")) {
		t.Fatalf("out = %v", amounts(out))
	}
	for _, r := range out {
		if !r.Amount.IsPositive() {
			t.Fatalf("non-positive row %+v", r)
		}
	}
}

func TestCheckPlanTarget(t *testing.T) {
	for _, s := range []string{POStatusDraft, POStatusInReview, POStatusApproved, POStatusConfirmed} {
		if err := CheckPlanTarget(s, POLineOpen); err != nil {
			t.Fatalf("%s refused: %v", s, err)
		}
	}
	for _, s := range []string{POStatusCancelled, POStatusFinalized} {
		if codeOf(CheckPlanTarget(s, POLineOpen)) != CodePlanTarget {
			t.Fatalf("%s accepted", s)
		}
	}
	if codeOf(CheckPlanTarget(POStatusDraft, POLineCancelled)) != CodePlanTarget {
		t.Fatal("cancelled line accepted")
	}
}

// Property (BR-17.17): percent milestones summing to 100 give positive rows
// that sum to the line amount to the cent, and reproportioning to any new
// amount keeps that identity with no negative row.
func TestInvoicingPlan_SumIdentityProperty(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		n := rapid.IntRange(1, 12).Draw(t, "n")
		cuts := make([]int64, n)
		left := int64(1_000_000) // percent in 1/10,000ths
		for i := 0; i < n-1; i++ {
			cuts[i] = rapid.Int64Range(1, left-int64(n-1-i)).Draw(t, "cut")
			left -= cuts[i]
		}
		cuts[n-1] = left
		ms := make([]Milestone, n)
		for i, c := range cuts {
			p := decimal.New(c, -4)
			ms[i] = Milestone{Date: ymd(2027, time.Month(i%12+1), 1), Percent: &p}
		}
		line := decimal.New(rapid.Int64Range(100_000, 99_999_999).Draw(t, "cents"), -2)
		rows, err := GeneratePartial(ms, line, 2)
		if err != nil {
			t.Skip("a milestone rounds to nothing:", err)
		}
		if !PlanTotal(rows).Equal(line) {
			t.Fatalf("Σ rows %s ≠ line %s", PlanTotal(rows), line)
		}
		next := decimal.New(rapid.Int64Range(0, 99_999_999).Draw(t, "next"), -2)
		out, err := Reproportion(rows, next, 2)
		if err != nil {
			t.Fatalf("reproportion: %v", err)
		}
		if !PlanTotal(out).Equal(next) {
			t.Fatalf("Σ after reproportion %s ≠ %s", PlanTotal(out), next)
		}
		for _, r := range out {
			if !r.Amount.IsPositive() {
				t.Fatalf("row %d amount %s", r.Seq, r.Amount)
			}
		}
	})
}
