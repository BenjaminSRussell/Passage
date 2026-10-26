package domain

import (
	"errors"
	"testing"

	"github.com/shopspring/decimal"
	"pgregory.net/rapid"
)

// biweekly BR-43.1 worker: gross 4,000, 401(k) 240, Colorado, 2020+ W-4 Single
// with no Step 2 to 4 entries.
func workerBR431() Input {
	return Input{
		RunType: RunRegular, Periods: 26, W4: W4{FilingStatus: FilingSingle}, State: "CO",
		Earnings: []Earning{{Code: "REGULAR", Amount: dec("4000")}},
		PreTax:   []Deduction{{Code: "401K", Amount: dec("240")}},
		Match:    &Match{Code: "401K", Percent: dec("0.5"), CapRate: dec("0.06")},
	}
}

func lineAmounts(st Statement) map[string]decimal.Decimal {
	m := map[string]decimal.Decimal{}
	for _, l := range st.Lines {
		m[l.Code] = l.Amount
	}
	return m
}

// TestCalculate_BR431WorkedExample checks the BR-43.1 worker under Pub 15-T.
// The PRD's federal figure (577.43, net 2,711.13) skips Worksheet 1A line 1g,
// the 8,600 adjustment every 2020+ W-4 without the Step 2 box gets; Pub 15-T
// gives 504.66 and net 2,783.90. Every other BR-43.1 figure is unchanged. The
// PRD arithmetic is reproduced below for a pre-2020 W-4 with no allowances.
// See NOT-IMPLEMENTED.md (PRD discrepancy row).
func TestCalculate_BR431WorkedExample(t *testing.T) {
	st, err := Calculate(US2025(), workerBR431())
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"FED_WH": "504.66", "SS_EE": "248.00", "MED_EE": "58.00", "STATE_CO_WH": "165.44",
		"401K": "240", "MATCH_401K": "120.00", "FUTA": "24.00", "SS_ER": "248.00", "MED_ER": "58.00",
	}
	got := lineAmounts(st)
	for code, w := range want {
		if !got[code].Equal(dec(w)) {
			t.Errorf("%s = %s, want %s", code, got[code], w)
		}
	}
	if !st.Net.Equal(dec("2783.90")) {
		t.Errorf("net = %s, want 2783.90", st.Net)
	}
	if !st.FedTaxableWages.Equal(dec("3760")) || !st.FICAWages.Equal(dec("4000")) {
		t.Errorf("taxable %s fica %s", st.FedTaxableWages, st.FICAWages)
	}
	// Employer cost: 330.00 employer taxes plus 120.00 match.
	if !st.EmployerCost.Equal(dec("450.00")) {
		t.Errorf("employer cost = %s, want 450.00", st.EmployerCost)
	}
	for _, l := range st.Lines {
		if l.RuleVersion != "US-FED-2025.1" {
			t.Errorf("%s has rule version %q", l.Code, l.RuleVersion)
		}
	}
}

func TestCalculate_BR431PRDArithmeticOnPreTwentyTwentyW4(t *testing.T) {
	in := workerBR431()
	in.W4 = W4{FilingStatus: FilingSingle, Legacy: true}
	st, err := Calculate(US2025(), in)
	if err != nil {
		t.Fatal(err)
	}
	// AC-43.1 figures: federal 577.43, SS 248.00, Medicare 58.00, CO 165.44, net 2,711.13.
	got := lineAmounts(st)
	for code, w := range map[string]string{"FED_WH": "577.43", "SS_EE": "248.00", "MED_EE": "58.00", "STATE_CO_WH": "165.44"} {
		if !got[code].Equal(dec(w)) {
			t.Errorf("%s = %s, want %s", code, got[code], w)
		}
	}
	if !st.Net.Equal(dec("2711.13")) {
		t.Errorf("net = %s, want 2711.13", st.Net)
	}
}

func TestCalculate_Section125ReducesFICAWages401kDoesNot(t *testing.T) {
	in := workerBR431()
	in.PreTax = append(in.PreTax, Deduction{Code: "MED125", Amount: dec("100"), ReducesFICA: true})
	st, err := Calculate(US2025(), in)
	if err != nil {
		t.Fatal(err)
	}
	if !st.FedTaxableWages.Equal(dec("3660")) || !st.FICAWages.Equal(dec("3900")) {
		t.Fatalf("fed wages %s, FICA wages %s; want 3660 and 3900", st.FedTaxableWages, st.FICAWages)
	}
	got := lineAmounts(st)
	// 6.2% and 1.45% of 3,900; FUTA 0.6% of 3,900; CO 4.40% of 3,660.
	for code, w := range map[string]string{"SS_EE": "241.80", "MED_EE": "56.55", "FUTA": "23.40", "MED_ER": "56.55", "STATE_CO_WH": "161.04"} {
		if !got[code].Equal(dec(w)) {
			t.Errorf("%s = %s, want %s", code, got[code], w)
		}
	}
}

func TestCalculate_EmployerMedicareSkipsAdditionalTax(t *testing.T) {
	in := workerBR431()
	in.YTD = YTD{FICAWages: dec("199000"), FUTAWages: dec("7000")}
	st, err := Calculate(US2025(), in)
	if err != nil {
		t.Fatal(err)
	}
	got := lineAmounts(st)
	// Employee: 58.00 + 0.9% × 3,000 over 200,000; employer: 1.45% only. SS past the base.
	if !got["MED_EE"].Equal(dec("85.00")) || !got["MED_ER"].Equal(dec("58.00")) || !got["FUTA"].IsZero() {
		t.Fatalf("MED_EE %s MED_ER %s FUTA %s", got["MED_EE"], got["MED_ER"], got["FUTA"])
	}
	if !st.EmployerCost.Equal(dec("178.00")) { // SS_ER 0 (past the base) + MED_ER 58 + match 120
		t.Fatalf("employer cost = %s, want 178.00", st.EmployerCost)
	}
}

func TestCalculate_PreTaxAboveGrossRejected(t *testing.T) {
	in := workerBR431()
	in.PreTax = []Deduction{{Code: "401K", Amount: dec("4000.01")}}
	_, err := Calculate(US2025(), in)
	var de *Error
	if !errors.As(err, &de) || de.Code != CodeInvalidInput {
		t.Fatalf("want %s, got %v", CodeInvalidInput, err)
	}
}

func TestCalculate_NoArrearsPolicyCreatesNoArrears(t *testing.T) {
	in := workerBR431()
	in.PostTax = []Deduction{{Code: "UNION", Amount: dec("5000"), Policy: PolicyNoArrears, RecoveryLimitPct: dec("1")}}
	st, err := Calculate(US2025(), in)
	if err != nil {
		t.Fatal(err)
	}
	if !lineAmounts(st)["UNION"].IsZero() || len(st.Arrears) != 0 {
		t.Fatalf("NO_ARREARS took %s with arrears %+v", lineAmounts(st)["UNION"], st.Arrears)
	}
	if !st.Net.Equal(dec("2783.90")) {
		t.Fatalf("net = %s", st.Net)
	}
}

func TestAllocatePostTax_PartialArrearsAC4311(t *testing.T) {
	// AC-43.11: net before medical 150.00, PARTIAL medical 200.00 → take 150.00,
	// arrears 50.00; the next period takes 200.00 + 50.00 = 250.00.
	medical := Deduction{Code: "MEDICAL", Amount: dec("200"), Priority: 5, Policy: PolicyPartial, RecoveryLimitPct: dec("1")}
	var first Statement
	if taken := allocatePostTax(&first, US2025(), []Deduction{medical}, dec("150")); !taken.Equal(dec("150")) {
		t.Fatalf("first period took %s, want 150", taken)
	}
	if len(first.Arrears) != 1 || !first.Arrears[0].Created.Equal(dec("50")) || !first.Arrears[0].Balance.Equal(dec("50")) {
		t.Fatalf("first period arrears = %+v", first.Arrears)
	}
	medical.ArrearsBalance = first.Arrears[0].Balance
	var next Statement
	if taken := allocatePostTax(&next, US2025(), []Deduction{medical}, dec("2000")); !taken.Equal(dec("250")) {
		t.Fatalf("next period took %s, want 250", taken)
	}
	if len(next.Arrears) != 1 || !next.Arrears[0].Recovered.Equal(dec("50")) || !next.Arrears[0].Balance.IsZero() {
		t.Fatalf("next period arrears = %+v", next.Arrears)
	}
}

func TestCalculate_BonusOffCycle(t *testing.T) {
	in := Input{RunType: RunBonus, Periods: 26, W4: W4{FilingStatus: FilingSingle}, State: "CO",
		Earnings: []Earning{{Code: "BONUS", Amount: dec("5000")}}}
	st, err := Calculate(US2025(), in)
	if err != nil {
		t.Fatal(err)
	}
	// AC-43.12: federal 1,100.00, SS 310.00, Medicare 72.50, CO 220.00, net 3,297.50.
	got := lineAmounts(st)
	for code, w := range map[string]string{"FED_WH": "1100.00", "SS_EE": "310.00", "MED_EE": "72.50", "STATE_CO_WH": "220.00"} {
		if !got[code].Equal(dec(w)) {
			t.Errorf("%s = %s, want %s", code, got[code], w)
		}
	}
	if !st.Net.Equal(dec("3297.50")) {
		t.Errorf("net = %s, want 3297.50", st.Net)
	}
}

func TestCalculate_ArrearsRecoveryAddsToDeduction(t *testing.T) {
	rs := US2025()
	in := Input{RunType: RunBonus, Periods: 26, Earnings: []Earning{{Code: "BONUS", Amount: dec("10000")}},
		PostTax: []Deduction{{Code: "MEDICAL", Amount: dec("200"), Policy: PolicyPartial, ArrearsBalance: dec("50"), RecoveryLimitPct: dec("1")}}}
	st, err := Calculate(rs, in)
	if err != nil {
		t.Fatal(err)
	}
	if !lineAmounts(st)["MEDICAL"].Equal(dec("250")) {
		t.Fatalf("medical with recovery = %s, want 250", lineAmounts(st)["MEDICAL"])
	}
	if len(st.Arrears) != 1 || !st.Arrears[0].Recovered.Equal(dec("50")) || !st.Arrears[0].Balance.IsZero() {
		t.Fatalf("arrears = %+v", st.Arrears)
	}
}

func TestCalculate_UnknownStateFailsClosed(t *testing.T) {
	in := workerBR431()
	in.State = "ZZ"
	if _, err := Calculate(US2025(), in); err == nil {
		t.Fatal("want PAY-RULE-001 for a state without a rate")
	}
}

func TestStatementHashChangesWithAnyLine(t *testing.T) {
	st, err := Calculate(US2025(), workerBR431())
	if err != nil {
		t.Fatal(err)
	}
	h1, err := st.Hash()
	if err != nil {
		t.Fatal(err)
	}
	again, _ := st.Hash()
	if h1 != again {
		t.Fatal("hash is not deterministic")
	}
	st.Lines[0].Amount = st.Lines[0].Amount.Add(dec("0.01"))
	h2, _ := st.Hash()
	if h1 == h2 {
		t.Fatal("hash did not change after a line changed")
	}
}

func TestCalculate_NetIdentityProperty(t *testing.T) {
	rs := US2025()
	statuses := []string{FilingSingle, FilingMarriedFilingJointly, FilingHeadOfHousehold, FilingMarriedFilingSeparately}
	rapid.Check(t, func(t *rapid.T) {
		gross := decimal.New(rapid.Int64Range(0, 2_000_000).Draw(t, "grossCents"), -2)
		pre := decimal.New(rapid.Int64Range(0, gross.Shift(2).IntPart()/2).Draw(t, "preCents"), -2)
		in := Input{RunType: RunRegular, Periods: 26, State: "CO",
			W4:       W4{FilingStatus: statuses[rapid.IntRange(0, 3).Draw(t, "status")], MultipleJobs: rapid.Bool().Draw(t, "step2")},
			Earnings: []Earning{{Code: "REGULAR", Amount: gross}},
			PreTax:   []Deduction{{Code: "401K", Amount: pre}},
			PostTax:  []Deduction{{Code: "LOAN", Amount: decimal.New(rapid.Int64Range(0, 500_000).Draw(t, "loanCents"), -2), Policy: PolicyPartial, RecoveryLimitPct: dec("1")}},
		}
		st, err := Calculate(rs, in)
		if err != nil {
			t.Fatal(err)
		}
		sum := decimal.Zero
		for _, l := range st.Lines {
			switch l.Kind {
			case KindPreTax, KindTax, KindPostTax:
				sum = sum.Add(l.Amount)
			}
			if l.Amount.Exponent() < -2 && l.Kind != KindEarning {
				t.Fatalf("%s amount %s is not rounded to cents", l.Code, l.Amount)
			}
		}
		if !st.Net.Equal(st.Gross.Sub(sum)) {
			t.Fatalf("net %s != gross %s - deductions %s", st.Net, st.Gross, sum)
		}
		if st.Net.IsNegative() {
			t.Fatalf("net %s is negative for gross %s", st.Net, st.Gross)
		}
	})
}
