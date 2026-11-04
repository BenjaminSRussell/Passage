package domain

import (
	"testing"

	"github.com/shopspring/decimal"
)

func TestValidStatementPeriod_FollowsTheAgreementPeriod(t *testing.T) {
	month, quarter, year := rbAgreement(BasisNetAmount), rbAgreement(BasisNetAmount), rbAgreement(BasisNetAmount)
	quarter.Period, year.Period = PeriodQuarter, PeriodYear
	cases := []struct {
		a    RebateAgreement
		key  string
		fail bool
	}{
		{month, "2026-03", false}, {month, "2026-13", true}, {month, "2026-Q1", true}, {month, "", true},
		{quarter, "2026-Q4", false}, {quarter, "2026-Q5", true}, {quarter, "2026-03", true},
		{year, "2026", false}, {year, "26", true}, {year, "2026-03", true},
	}
	for _, tc := range cases {
		err := ValidStatementPeriod(tc.a, tc.key)
		if (err != nil) != tc.fail {
			t.Errorf("%s agreement, period %q: error = %v, want failure %v", tc.a.Period, tc.key, err, tc.fail)
		}
		if err != nil && (err.Code != CodeValidation || err.Fields["period"] == "") {
			t.Errorf("period %q: error = %+v, want a COM-0001 field error on period", tc.key, err)
		}
	}
}

func TestBuildRebateStatement_PercentTiersFooterAndReconciliation(t *testing.T) {
	a := rbAgreement(BasisNetAmount, Tier{From: decimal.Zero, RatePct: dec("1")}, Tier{From: dec("5000"), RatePct: dec("2.5")})
	st := BuildRebateStatement(a, "2026-03", StatementInput{
		Period:  PeriodState{Value: dec("6000"), Accrued: dec("150")},
		Settled: dec("30"), LineCount: 2, Lines: []StatementLine{{InvoiceLineID: "L1"}, {InvoiceLineID: "L2"}},
		RowsAccrued: dec("150"), RowsBasisValue: dec("6000")})
	if st.Tier == nil || !st.Tier.RatePct.Equal(dec("2.5")) {
		t.Fatalf("tier = %+v, want the 2.5%% tier", st.Tier)
	}
	if !st.Target.Equal(dec("150")) || !st.Open.Equal(dec("120")) || !st.Reconciled || st.Truncated {
		t.Fatalf("target %s open %s reconciled %v truncated %v; want 150, 120, true, false", st.Target, st.Open, st.Reconciled, st.Truncated)
	}
}

func TestBuildRebateStatement_DriftIsNotReconciledAndTruncationIsFlagged(t *testing.T) {
	a := rbAgreement(BasisNetAmount, Tier{From: decimal.Zero, RatePct: dec("2")})
	st := BuildRebateStatement(a, "2026-03", StatementInput{Period: PeriodState{Value: dec("1000"), Accrued: dec("19.99")}, LineCount: 6000,
		Lines: make([]StatementLine, 5000), RowsAccrued: dec("19.99"), RowsBasisValue: dec("1000")})
	if st.Reconciled || !st.Target.Equal(dec("20")) {
		t.Fatalf("accrued 19.99 against a target of %s must not reconcile", st.Target)
	}
	if !st.Truncated {
		t.Fatal("6000 rows with 5000 listed must be flagged truncated")
	}
}

func TestBuildRebateStatement_QuantityBasisUsesTheQuantityTier(t *testing.T) {
	a := rbAgreement(BasisQuantity, Tier{From: decimal.Zero, AmountPerUnit: dec("0.10")}, Tier{From: dec("1000"), AmountPerUnit: dec("0.15")})
	st := BuildRebateStatement(a, "2026-03", StatementInput{Period: PeriodState{Value: dec("99999"), Qty: dec("1200"), Accrued: dec("180")},
		RowsAccrued: dec("180"), RowsBasisValue: dec("99999"), RowsBasisQty: dec("1200")})
	if st.Tier == nil || !st.Tier.AmountPerUnit.Equal(dec("0.15")) || !st.Target.Equal(dec("180")) || !st.Reconciled {
		t.Fatalf("tier %+v target %s reconciled %v; want the 0.15 per unit tier on quantity 1200 = 180", st.Tier, st.Target, st.Reconciled)
	}
}

func TestBuildRebateStatement_NoActivityIsZero(t *testing.T) {
	a := rbAgreement(BasisNetAmount, Tier{From: dec("100"), RatePct: dec("1")})
	st := BuildRebateStatement(a, "2026-03", StatementInput{})
	if st.Tier != nil || !st.Accrued.IsZero() || !st.Open.IsZero() || !st.Reconciled {
		t.Fatalf("empty period = %+v; want no tier below the first threshold and zero totals", st)
	}
}

// The period row is written as the target, so it always matches it; the rows are the
// independent side. A period whose rows do not add up to its accrued total, or whose rows'
// basis does not add up to its cumulative basis, is not reconciled.
func TestBuildRebateStatement_RowsMustAgreeWithThePeriod(t *testing.T) {
	a := rbAgreement(BasisNetAmount, Tier{From: decimal.Zero, RatePct: dec("1")}, Tier{From: dec("5000"), RatePct: dec("2.5")})
	period := PeriodState{Value: dec("6000"), Accrued: dec("150")}
	if st := BuildRebateStatement(a, "2026-03", StatementInput{Period: period, RowsAccrued: dec("149.99"), RowsBasisValue: dec("6000")}); st.Reconciled {
		t.Fatal("rows summing to 149.99 against a 150.00 period must not reconcile")
	}
	if st := BuildRebateStatement(a, "2026-03", StatementInput{Period: period, RowsAccrued: dec("150"), RowsBasisValue: dec("5000")}); st.Reconciled {
		t.Fatal("rows whose basis sums to 5,000 against a 6,000 period must not reconcile")
	}
}

// BR-26.8: a credit drops the period back below the 5,000 tier; its negative row brings the
// rows to the 1% target on the net 4,000 (30 + 120 - 110 = 40).
func TestBuildRebateStatement_CreditBelowATierReconciles(t *testing.T) {
	a := rbAgreement(BasisNetAmount, Tier{From: decimal.Zero, RatePct: dec("1")}, Tier{From: dec("5000"), RatePct: dec("2.5")})
	prior := PeriodState{}
	var rows, basis decimal.Decimal
	for _, v := range []string{"3000", "3000", "-2000"} {
		var inc decimal.Decimal
		prior, inc = Accrue(a, prior, dec(v), decimal.Zero)
		rows, basis = rows.Add(inc), basis.Add(dec(v))
	}
	st := BuildRebateStatement(a, "2026-03", StatementInput{Period: prior, RowsAccrued: rows, RowsBasisValue: basis})
	if !st.Reconciled || !st.Target.Equal(dec("40")) || st.Tier == nil || !st.Tier.RatePct.Equal(dec("1")) {
		t.Fatalf("after a credit: target %s rows %s tier %+v reconciled %v; want 40 at 1%% and reconciled", st.Target, rows, st.Tier, st.Reconciled)
	}
}

func TestStatementLineSource(t *testing.T) {
	cases := []struct {
		line StatementLine
		want string
	}{
		{StatementLine{InvoiceLineID: "INV-1"}, SourceInvoice},
		{StatementLine{InvoiceLineID: "INV-1", Retroactive: true}, SourceReplayed},
		{StatementLine{InvoiceLineID: "retro:run-1:2026-03", Retroactive: true}, SourceRetroDelta},
		// Only the recompute writes the prefix on a retroactive row; a feed line id is never a delta.
		{StatementLine{InvoiceLineID: "retro:feed"}, SourceInvoice},
	}
	for _, tc := range cases {
		if got := tc.line.Source(); got != tc.want {
			t.Errorf("%+v source = %s, want %s", tc.line, got, tc.want)
		}
	}
}
