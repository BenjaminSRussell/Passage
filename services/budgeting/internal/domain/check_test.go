package domain

import (
	"testing"

	"github.com/shopspring/decimal"
)

func d(s string) decimal.Decimal { return decimal.RequireFromString(s) }

func TestCheck_Thresholds(t *testing.T) {
	// AC-38.1 / AC-38.2 balance: budget 100,000, actuals 60,000, encumbrances 30,000 => available 10,000.
	bal := Balance{Budget: d("100000"), Actuals: d("60000"), Encumbrances: d("30000")}
	tests := []struct {
		name     string
		warn     string
		block    string
		amount   string
		prior    string
		override bool
		want     Result
		reserve  string
	}{
		{"AC-38.1 over block is fail", "0", "5", "12000", "0", false, Fail, "0"},
		{"AC-38.2 between warn and block is warn", "0", "5", "10400", "0", false, Warn, "10400"},
		{"exactly available passes at zero warn", "0", "5", "10000", "0", false, Pass, "10000"},
		{"override turns fail into warn", "0", "5", "12000", "0", true, Warn, "12000"},
		{"warn tolerance passes", "5", "10", "10400", "0", false, Pass, "10400"},
		{"prior reservation is relieved before the check", "0", "5", "12000", "2000", false, Pass, "10000"},
		{"revision lowering an amount never fails", "0", "0", "3000", "12000", false, Pass, "-9000"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := Check(bal, Thresholds{WarnPct: d(tc.warn), BlockPct: d(tc.block)},
				CheckInput{Amount: d(tc.amount), Prior: d(tc.prior), Override: tc.override})
			if got.Result != tc.want {
				t.Fatalf("result = %s, want %s (trace %v)", got.Result, tc.want, got.Trace)
			}
			if !got.Reserve.Equal(d(tc.reserve)) {
				t.Fatalf("reserve = %s, want %s", got.Reserve, tc.reserve)
			}
		})
	}
}

func TestCheck_TraceShowsEveryStep(t *testing.T) {
	bal := Balance{Budget: d("100000"), Actuals: d("60000"), Encumbrances: d("30000")}
	got := Check(bal, Thresholds{WarnPct: d("0"), BlockPct: d("5")}, CheckInput{Amount: d("12000"), Override: true})
	want := []string{"relieve-prior", "available", "threshold:fail", "override"}
	for i, w := range want {
		if i >= len(got.Trace) || got.Trace[i] != w {
			t.Fatalf("trace = %v, want prefix %v", got.Trace, want)
		}
	}
}

func TestCheck_ZeroAvailableFailsAnyIncrease(t *testing.T) {
	bal := Balance{Budget: d("1000"), Actuals: d("1000")}
	got := Check(bal, Thresholds{WarnPct: d("0"), BlockPct: d("50")}, CheckInput{Amount: d("1")})
	if got.Result != Fail {
		t.Fatalf("result = %s, want fail", got.Result)
	}
	if got := Check(bal, Thresholds{}, CheckInput{Amount: d("0")}); got.Result != Pass {
		t.Fatalf("zero amount on zero funds = %s, want pass", got.Result)
	}
}

func TestBalance_AvailableFormula(t *testing.T) {
	b := Balance{Budget: d("100"), Actuals: d("10"), Encumbrances: d("20"), PreEncumbrances: d("5")}
	if got := b.Available(); !got.Equal(d("65")) {
		t.Fatalf("available = %s, want 65", got)
	}
	b.Reservations = d("15")
	if got := b.Available(); !got.Equal(d("50")) {
		t.Fatalf("available with 15 earmarked = %s, want 50", got)
	}
}

func TestNetChange_RevisionNeverDropsBelowRelief(t *testing.T) {
	tests := []struct{ amount, prior, relieved, want string }{
		{"600", "500", "300", "100"},  // raise a partly invoiced PO line
		{"400", "500", "300", "-100"}, // lower it: release only the difference
		{"200", "500", "300", "-200"}, // lower below invoiced: release the open 200 only
		{"500", "0", "0", "500"},      // first check of the line
	}
	for _, tc := range tests {
		if got := NetChange(d(tc.amount), d(tc.prior), d(tc.relieved)); !got.Equal(d(tc.want)) {
			t.Errorf("NetChange(%s, %s, %s) = %s, want %s", tc.amount, tc.prior, tc.relieved, got, tc.want)
		}
	}
}
