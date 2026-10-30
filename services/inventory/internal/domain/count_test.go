package domain

import (
	"fmt"
	"testing"

	"github.com/shopspring/decimal"
	"pgregory.net/rapid"
)

func TestExceedsTolerance(t *testing.T) {
	five, hundred := d("5"), d("100")
	tests := []struct {
		name                     string
		variance, book, unitCost string
		want                     bool
	}{
		{"no variance", "0", "50", "4", false},
		{"8 percent over 5 percent tolerance (AC-16.6)", "4", "50", "1", true},
		{"exactly at tolerance is allowed", "2.5", "50", "1", false},
		{"negative variance uses its magnitude", "-4", "50", "1", true},
		{"value over tolerance", "-30", "1000", "4", true},
		{"value within tolerance", "-10", "1000", "4", false},
		{"variance against zero book is 100 percent", "1", "0", "1", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := ExceedsTolerance(d(tc.variance), d(tc.book), d(tc.unitCost), five, hundred); got != tc.want {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}

// Property: a zero variance never needs approval; a variance strictly above
// the percent tolerance of the book always does; one at or below it with no
// value never does.
func TestExceedsToleranceProperty(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		book := decimal.NewFromInt(rapid.Int64Range(1, 100000).Draw(t, "book"))
		tol := decimal.NewFromInt(rapid.Int64Range(0, 100).Draw(t, "tol"))
		limit := book.Mul(tol).Div(decimal.NewFromInt(100))
		huge := decimal.NewFromInt(1e12)
		if ExceedsTolerance(decimal.Zero, book, d("3"), tol, decimal.Zero) {
			t.Fatal("zero variance flagged")
		}
		over := limit.Add(decimal.New(1, -3))
		if !ExceedsTolerance(over, book, decimal.Zero, tol, huge) {
			t.Fatalf("variance %s above %s%% of %s not flagged", over, tol, book)
		}
		if limit.Sign() > 0 && ExceedsTolerance(limit.Neg(), book, decimal.Zero, tol, huge) {
			t.Fatalf("variance %s at the tolerance flagged", limit.Neg())
		}
	})
}

func TestValidatePlanAndCounted(t *testing.T) {
	good := CountPlan{Name: "Weekly", Currency: "USD", Unit: "EA", TolerancePct: d("5"), ToleranceValue: d("100")}
	if err := ValidatePlan(good); err != nil {
		t.Fatalf("valid plan rejected: %v", err)
	}
	bad := map[string]func(p *CountPlan){
		"no name":             func(p *CountPlan) { p.Name = "  " },
		"bad currency":        func(p *CountPlan) { p.Currency = "US" },
		"no unit":             func(p *CountPlan) { p.Unit = "" },
		"pct above 100":       func(p *CountPlan) { p.TolerancePct = d("101") },
		"negative value":      func(p *CountPlan) { p.ToleranceValue = d("-1") },
		"long warehouse":      func(p *CountPlan) { p.Warehouse = string(make([]byte, 61)) },
		"value too precise":   func(p *CountPlan) { p.ToleranceValue = d("0.0000001") },
		"percent too precise": func(p *CountPlan) { p.TolerancePct = d("0.0000001") },
	}
	for name, mut := range bad {
		p := good
		mut(&p)
		if domainCode(ValidatePlan(p)) != CodeInvalid {
			t.Errorf("%s: invalid plan accepted", name)
		}
	}
	if err := ValidateCounted(d("-1"), 6); domainCode(err) != CodeInvalid {
		t.Fatalf("negative count accepted: %v", err)
	}
	if err := ValidateCounted(d("0.1234567"), 6); domainCode(err) != CodeInvalid {
		t.Fatalf("count beyond unit precision accepted: %v", err)
	}
	if err := ValidateCounted(d("0"), 6); err != nil {
		t.Fatalf("a count of zero is valid: %v", err)
	}
	if !Variance(d("50"), d("46")).Equal(d("-4")) {
		t.Fatal("variance is counted minus book")
	}
	if err := RequireOpen(CountPosted); domainCode(err) != CodeCountSheetState {
		t.Fatalf("posted sheet accepted a change: %v", err)
	}
}

func TestFourEyesRefusesAnApproverWhoCounted(t *testing.T) {
	lines := []CountLine{{LineNo: 1, CountedBy: "alice"}, {LineNo: 2, CountedBy: "bob"}}
	if err := CheckFourEyes("carol", lines); err != nil {
		t.Fatalf("separate approver: %v", err)
	}
	for _, who := range []string{"bob", ""} {
		err := CheckFourEyes(who, lines)
		if e, ok := err.(*Error); !ok || e.Code != CodeCountFourEyes {
			t.Fatalf("approver %q: %v", who, err)
		}
	}
}

func TestOutstandingRecountsCountsFlaggedLines(t *testing.T) {
	if n := OutstandingRecounts([]CountLine{{RecountRequested: true}, {}, {RecountRequested: true}}); n != 2 {
		t.Fatalf("got %d", n)
	}
}

func TestSampleKeysIsRepeatablePerSheetAndRotatesAcrossSheets(t *testing.T) {
	keys := []string{"a", "b", "c", "d", "e", "f", "g", "h", "i", "j"}
	pct := decimal.NewFromInt(25)
	first, second := SampleKeys("sheet-1", keys, pct), SampleKeys("sheet-1", keys, pct)
	if len(first) != 3 {
		t.Fatalf("25%% of 10 rounds up to 3, got %d", len(first))
	}
	for k := range first {
		if !second[k] {
			t.Fatalf("the same sheet's sample changed between runs: %s", k)
		}
	}
	// Successive sheets must not count the same subset forever: over a few
	// sheets every key is drawn at least once.
	seen := map[string]bool{}
	for i := range 20 {
		for k := range SampleKeys(fmt.Sprintf("sheet-%d", i), keys, pct) {
			seen[k] = true
		}
	}
	if len(seen) != len(keys) {
		t.Fatalf("20 sheets of a 25%% sample drew only %d of %d keys", len(seen), len(keys))
	}
	if len(SampleKeys("s", keys, decimal.Zero)) != 10 || len(SampleKeys("s", keys, decimal.NewFromInt(100))) != 10 {
		t.Fatal("zero or 100 percent keeps every key")
	}
	if len(SampleKeys("s", nil, pct)) != 0 {
		t.Fatal("no keys, no sample")
	}
}

func TestValidatePlanSelection(t *testing.T) {
	base := CountPlan{Name: "p", Currency: "EUR", Unit: "PCS", TolerancePct: DefaultTolerancePct, ToleranceValue: DefaultToleranceValue}
	if err := ValidatePlan(base); err != nil {
		t.Fatal(err)
	}
	bad := []CountPlan{
		{Name: "p", Currency: "EUR", Unit: "PCS", ABCClass: "N"},
		{Name: "p", Currency: "EUR", Unit: "PCS", LocationFrom: "B", LocationTo: "A"},
		{Name: "p", Currency: "EUR", Unit: "PCS", SamplePct: decimal.NewFromInt(101)},
	}
	for i, p := range bad {
		if ValidatePlan(p) == nil {
			t.Fatalf("case %d should fail", i)
		}
	}
}
