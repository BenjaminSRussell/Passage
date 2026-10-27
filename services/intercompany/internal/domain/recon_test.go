package domain

import (
	"errors"
	"fmt"
	"testing"

	"github.com/shopspring/decimal"
)

func d(s string) decimal.Decimal { return decimal.RequireFromString(s) }

func TestClassify_Table(t *testing.T) {
	half := DefaultFXTolerancePct
	cases := []struct {
		name string
		in   ReconInput
		want ReconStatus
		tol  bool
		diff string
	}{
		{"equal sides match", ReconInput{AmountA: d("1000"), AmountB: d("1000")}, ReconMatched, false, "0"},
		{"one cent tolerated", ReconInput{AmountA: d("1000.01"), AmountB: d("1000")}, ReconMatched, false, "0.01"},
		{"two cents is a mismatch", ReconInput{AmountA: d("1000.02"), AmountB: d("1000")}, ReconMismatch, false, "0.02"},
		{"fx item within 0.5 percent matches", ReconInput{AmountA: d("1000"), AmountB: d("995.5"), FXApplies: true, FXTolerancePct: half}, ReconMatched, true, "4.5"},
		{"fx item at exactly 0.5 percent matches", ReconInput{AmountA: d("1000"), AmountB: d("995"), FXApplies: true, FXTolerancePct: half}, ReconMatched, true, "5"},
		{"fx item beyond 0.5 percent is a mismatch", ReconInput{AmountA: d("1000"), AmountB: d("994.99"), FXApplies: true, FXTolerancePct: half}, ReconMismatch, false, "5.01"},
		{"same-currency item ignores fx tolerance", ReconInput{AmountA: d("1000"), AmountB: d("995.5"), FXTolerancePct: half}, ReconMismatch, false, "4.5"},
		{"missing mirror is a mismatch", ReconInput{AmountA: d("250"), AmountB: d("0")}, ReconMismatch, false, "250"},
		{"B side booked in the next period is timing", ReconInput{AmountA: d("250"), AmountB: d("0"), NextPeriodB: d("250")}, ReconTiming, false, "250"},
		{"A side booked in the next period is timing", ReconInput{AmountA: d("0"), AmountB: d("80"), NextPeriodA: d("80")}, ReconTiming, false, "-80"},
		{"next-period counterpart with the wrong amount is a mismatch", ReconInput{AmountA: d("250"), AmountB: d("0"), NextPeriodB: d("200")}, ReconMismatch, false, "250"},
		{"fx timing item uses the tolerance", ReconInput{AmountA: d("1000"), AmountB: d("0"), NextPeriodB: d("996"), FXApplies: true, FXTolerancePct: half}, ReconTiming, true, "1000"},
		{"negative balances (credit due-from) compare by value", ReconInput{AmountA: d("-40"), AmountB: d("-40")}, ReconMatched, false, "0"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := Classify(c.in)
			if got.Status != c.want || got.ToleranceApplied != c.tol || !got.Difference.Equal(d(c.diff)) {
				t.Fatalf("got %+v; want status %s tolerance %v difference %s", got, c.want, c.tol, c.diff)
			}
		})
	}
}

// TestClassify_PlantedMismatches is ticket AC3 and AC-40.5: 50 planted
// defects (missing mirror, wrong amount, wrong period) are all flagged
// Mismatch, and none of 50 clean pairs, including timing items whose
// counterpart is in the next period, is flagged.
func TestClassify_PlantedMismatches(t *testing.T) {
	var planted []ReconInput
	for i := 0; i < 50; i++ {
		base := d(fmt.Sprintf("%d.00", 100+i))
		switch i % 3 {
		case 0: // missing mirror: B never booked
			planted = append(planted, ReconInput{AmountA: base})
		case 1: // wrong amount: B booked 5.00 more
			planted = append(planted, ReconInput{AmountA: base, AmountB: base.Add(d("5.00"))})
		default: // wrong period: B booked two periods later, which is neither this nor the next period
			planted = append(planted, ReconInput{AmountA: base, NextPeriodA: d("0"), NextPeriodB: d("0")})
		}
	}
	for i, in := range planted {
		if got := Classify(in); got.Status != ReconMismatch {
			t.Fatalf("planted %d (%+v) classified %s; want Mismatch", i, in, got.Status)
		}
	}
	for i := 0; i < 50; i++ {
		v := d(fmt.Sprintf("%d.37", 10+i))
		in := ReconInput{AmountA: v, AmountB: v}
		if i%5 == 0 {
			in = ReconInput{AmountA: v, NextPeriodB: v}
		}
		if got := Classify(in); got.Status == ReconMismatch {
			t.Fatalf("clean pair %d flagged as Mismatch", i)
		}
	}
}

func TestValidateFXTolerance(t *testing.T) {
	for _, ok := range []string{"0", "0.5", "5"} {
		if err := ValidateFXTolerance(d(ok)); err != nil {
			t.Fatalf("%s rejected: %v", ok, err)
		}
	}
	for _, bad := range []string{"-0.1", "5.01"} {
		var de *Error
		if err := ValidateFXTolerance(d(bad)); !errors.As(err, &de) || de.Code != CodeInvalid {
			t.Fatalf("%s: err = %v; want COM-0001", bad, err)
		}
	}
}
