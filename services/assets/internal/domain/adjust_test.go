package domain

import (
	"errors"
	"testing"

	"github.com/shopspring/decimal"
	"pgregory.net/rapid"
)

func ErrorCode(err error) string {
	var e *Error
	if errors.As(err, &e) {
		return e.Code
	}
	return ""
}

func TestAdjust_Table(t *testing.T) {
	base := BookBalance{Cost: d("10000"), Accumulated: d("4000"), Currency: "USD"}
	impaired := BookBalance{Cost: d("10000"), Accumulated: d("4500"), Impairment: d("500"), Currency: "USD"}
	cases := []struct {
		name    string
		kind    AdjustmentType
		b       BookBalance
		amount  string
		code    string
		applied string
		limited bool
	}{
		{name: "acquisition adds cost", kind: AdjustmentAcquisition, b: base, amount: "2000", applied: "2000"},
		{name: "credit note lowers cost", kind: AdjustmentAcquisition, b: base, amount: "-1000", applied: "-1000"},
		{name: "credit note below accumulated", kind: AdjustmentAcquisition, b: base, amount: "-6000.01", code: CodeInvalidAdjustment},
		{name: "zero rejected", kind: AdjustmentAcquisition, b: base, amount: "0", code: CodeInvalidAdjustment},
		{name: "sub-cent rejected", kind: AdjustmentAcquisition, b: base, amount: "1.001", code: CodeInvalidAdjustment},
		{name: "yen has no minor unit", kind: AdjustmentAcquisition, b: BookBalance{Cost: d("100000"), Currency: "JPY"}, amount: "0.5", code: CodeInvalidAdjustment},
		{name: "depreciation adj within bounds", kind: AdjustmentDepreciation, b: base, amount: "-1000", applied: "-1000"},
		{name: "depreciation adj below zero", kind: AdjustmentDepreciation, b: base, amount: "-4000.01", code: CodeInvalidAdjustment},
		{name: "depreciation adj above cost", kind: AdjustmentDepreciation, b: base, amount: "6000.01", code: CodeInvalidAdjustment},
		{name: "depreciation adj under the impairment", kind: AdjustmentDepreciation, b: impaired, amount: "-4000.01", code: CodeInvalidAdjustment},
		{name: "revaluation up", kind: AdjustmentRevaluation, b: base, amount: "2500", applied: "2500"},
		{name: "revaluation down to NBV zero", kind: AdjustmentRevaluation, b: base, amount: "-6000", applied: "-6000"},
		{name: "revaluation below accumulated", kind: AdjustmentRevaluation, b: base, amount: "-6000.01", code: CodeInvalidAdjustment},
		{name: "impairment write-down", kind: AdjustmentImpairment, b: base, amount: "1000", applied: "1000"},
		{name: "impairment past NBV", kind: AdjustmentImpairment, b: base, amount: "6000.01", code: CodeInvalidAdjustment},
		{name: "reversal within impairment", kind: AdjustmentImpairment, b: impaired, amount: "-300", applied: "-300"},
		{name: "reversal capped at impairment", kind: AdjustmentImpairment, b: impaired, amount: "-900", applied: "-500", limited: true},
		{name: "reversal with no impairment", kind: AdjustmentImpairment, b: base, amount: "-100", code: CodeInvalidAdjustment},
		{name: "unknown type", kind: "Transfer", b: base, amount: "1", code: CodeInvalidAdjustment},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := Adjust(c.kind, c.b, d(c.amount))
			if c.code != "" {
				if ErrorCode(err) != c.code {
					t.Fatalf("err = %v, want code %s", err, c.code)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error %v", err)
			}
			if !got.Applied.Equal(d(c.applied)) || got.Limited != c.limited {
				t.Fatalf("applied = %s limited = %v, want %s %v", got.Applied, got.Limited, c.applied, c.limited)
			}
		})
	}
}

// An impaired asset depreciates less afterwards, so a reversal is capped by the
// carrying amount the asset would have had without the impairment (FR-37.9),
// not by the whole recorded impairment.
func TestAdjust_ReversalCappedByUnimpairedCarryingAmount(t *testing.T) {
	// Cost 12,000; impaired by 3,000 after 2,000 of depreciation; then 1,500
	// more depreciation at the lower rate where the schedule would have charged
	// 2,000. Without the impairment accumulated would be 4,000, so the reversal
	// can lift NBV from 5,500 to at most 8,000: 2,500, not 3,000.
	b := BookBalance{Cost: d("12000"), Accumulated: d("6500"), Impairment: d("3000"), Scheduled: d("4000"), Currency: "USD"}
	got, err := Adjust(AdjustmentImpairment, b, d("-3000"))
	if err != nil {
		t.Fatal(err)
	}
	if !got.Applied.Equal(d("-2500")) || !got.Limited {
		t.Fatalf("applied %s limited %v, want -2500 limited", got.Applied, got.Limited)
	}
	if !got.AccumulatedDelta.Equal(d("-2500")) || !got.ImpairmentDelta.Equal(d("-2500")) || !got.CostDelta.IsZero() {
		t.Fatalf("deltas %+v", got)
	}
}

func TestReversalCap_NeverNegative(t *testing.T) {
	b := BookBalance{Cost: d("100"), Accumulated: d("50"), Impairment: d("10"), Scheduled: d("60"), Currency: "USD"}
	if c := ReversalCap(b); !c.IsZero() {
		t.Fatalf("cap = %s, want 0", c)
	}
}

// Property: no sequence of adjustments breaks 0 ≤ accumulated ≤ cost, and the
// recorded impairment never goes negative.
func TestAdjust_KeepsBalancesConsistent(t *testing.T) {
	kinds := []AdjustmentType{AdjustmentAcquisition, AdjustmentDepreciation, AdjustmentRevaluation, AdjustmentImpairment}
	rapid.Check(t, func(rt *rapid.T) {
		b := BookBalance{Cost: decimal.NewFromInt(rapid.Int64Range(1000, 1_000_000).Draw(rt, "cost")), Currency: "USD"}
		for i, steps := 0, rapid.IntRange(1, 25).Draw(rt, "steps"); i < steps; i++ {
			kind := kinds[rapid.IntRange(0, len(kinds)-1).Draw(rt, "kind")]
			amt := decimal.New(rapid.Int64Range(-500_000, 500_000).Draw(rt, "cents"), -2)
			adj, err := Adjust(kind, b, amt)
			if err != nil {
				continue
			}
			if kind == AdjustmentImpairment && adj.Applied.IsNegative() && adj.Applied.Neg().GreaterThan(b.Impairment) {
				rt.Fatalf("reversal %s exceeds impairment %s", adj.Applied, b.Impairment)
			}
			b.Cost = b.Cost.Add(adj.CostDelta)
			b.Accumulated = b.Accumulated.Add(adj.AccumulatedDelta)
			b.Impairment = b.Impairment.Add(adj.ImpairmentDelta)
			if b.Accumulated.IsNegative() || b.Accumulated.GreaterThan(b.Cost) || b.Impairment.IsNegative() || b.Impairment.GreaterThan(b.Accumulated) {
				rt.Fatalf("invariant broken after %s %s: %+v", kind, amt, b)
			}
		}
	})
}

func TestRequireOpenBook(t *testing.T) {
	if err := RequireOpenBook("Open"); err != nil {
		t.Fatalf("open: %v", err)
	}
	if ErrorCode(RequireOpenBook("Sold")) != CodeAssetState {
		t.Fatal("sold book accepted")
	}
}
