package domain

import (
	"testing"

	"github.com/shopspring/decimal"
)

func sl(acct, side, amt string, dims map[string]string) LineInput {
	return LineInput{Account: acct, Side: side, Amount: decimal.RequireFromString(amt), Dimensions: dims, Currency: "USD"}
}

func perKey(t *testing.T, lines []LineInput, dim string) map[string]string {
	t.Helper()
	sums := map[string]decimal.Decimal{}
	for _, l := range lines {
		sums[l.Dimensions[dim]] = sums[l.Dimensions[dim]].Add(signed(l))
	}
	out := map[string]string{}
	for k, v := range sums {
		out[k] = v.StringFixed(2)
	}
	return out
}

// AC-11.16: payable and tax inherit the profit centre in proportion to the expense lines.
func TestSplittingVendorInvoiceProportional(t *testing.T) {
	rule := SplittingRule{SplittingDimensions: []string{"PC"}, ZeroBalanceClearingAccount: "199999"}
	in := []LineInput{
		sl("600000", "D", "600.00", map[string]string{"PC": "PC1"}),
		sl("600000", "D", "400.00", map[string]string{"PC": "PC2"}),
		sl("154000", "D", "190.00", nil),
		sl("211000", "C", "1190.00", nil),
	}
	out, trace, viol := ApplySplitting(rule, "USD", in)
	if len(viol) != 0 {
		t.Fatalf("violations: %v", viol)
	}
	got := map[string]string{}
	for _, l := range out {
		if l.Account == "211000" || l.Account == "154000" {
			got[l.Account+l.Dimensions["PC"]] = l.Amount.StringFixed(2)
		}
	}
	want := map[string]string{"211000PC1": "714.00", "211000PC2": "476.00", "154000PC1": "114.00", "154000PC2": "76.00"}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %s, want %s (all %v)", k, got[k], v, got)
		}
	}
	for k, v := range perKey(t, out, "PC") {
		if v != "0.00" {
			t.Errorf("profit centre %s nets %s, want 0.00", k, v)
		}
	}
	if len(out) != 6 {
		t.Errorf("lines = %d, want 6 (no clearing lines needed)", len(out))
	}
	if len(trace) != 4 || trace[0].Kind != SplitInherited {
		t.Errorf("trace = %+v", trace)
	}
	if out[2].Account != "154000" || out[3].Account != "211000" {
		t.Errorf("first parts must stay at their original index: %v %v", out[2].Account, out[3].Account)
	}
}

func TestSplittingRoundingKeepsTotal(t *testing.T) {
	rule := SplittingRule{SplittingDimensions: []string{"PC"}}
	in := []LineInput{
		sl("600000", "D", "1.00", map[string]string{"PC": "A"}),
		sl("600000", "D", "1.00", map[string]string{"PC": "B"}),
		sl("600000", "D", "1.00", map[string]string{"PC": "C"}),
		sl("211000", "C", "3.00", nil),
		sl("154000", "D", "0.01", nil),
		sl("154000", "C", "0.01", nil),
	}
	out, _, viol := ApplySplitting(rule, "USD", in)
	if len(viol) != 0 {
		t.Fatalf("violations: %v", viol)
	}
	sum := decimal.Zero
	for _, l := range out {
		if l.Account == "154000" && l.Side == "D" {
			sum = sum.Add(l.Amount)
		}
	}
	if !sum.Equal(decimal.RequireFromString("0.01")) {
		t.Errorf("split parts of 0.01 sum to %s", sum)
	}
	for k, v := range perKey(t, out, "PC") {
		if k != "" && v != "0.00" && k != "A" && k != "B" && k != "C" {
			t.Errorf("key %s nets %s", k, v)
		}
	}
}

func TestSplittingClearingLinesBalancePerKey(t *testing.T) {
	rule := SplittingRule{SplittingDimensions: []string{"PC"}, ZeroBalanceClearingAccount: "199999"}
	in := []LineInput{
		sl("600000", "D", "100.00", map[string]string{"PC": "PC1"}),
		sl("400000", "C", "100.00", map[string]string{"PC": "PC2"}),
	}
	out, trace, viol := ApplySplitting(rule, "USD", in)
	if len(viol) != 0 {
		t.Fatalf("violations: %v", viol)
	}
	if len(out) != 4 {
		t.Fatalf("lines = %d, want 4", len(out))
	}
	for k, v := range perKey(t, out, "PC") {
		if v != "0.00" {
			t.Errorf("key %s nets %s", k, v)
		}
	}
	if out[2].Account != "199999" || out[2].Side != "C" || out[2].Dimensions["PC"] != "PC1" || out[3].Side != "D" {
		t.Errorf("clearing lines wrong: %+v %+v", out[2], out[3])
	}
	if len(trace) != 2 || trace[0].Kind != SplitClearing || trace[0].SourceIndex != -1 {
		t.Errorf("trace = %+v", trace)
	}
}

func TestSplittingClearingAccountMissing(t *testing.T) {
	rule := SplittingRule{SplittingDimensions: []string{"PC"}}
	in := []LineInput{
		sl("600000", "D", "100.00", map[string]string{"PC": "PC1"}),
		sl("400000", "C", "100.00", map[string]string{"PC": "PC2"}),
	}
	_, _, viol := ApplySplitting(rule, "USD", in)
	if len(viol) != 1 || viol[0].Code != CodeSplitClearingMissing {
		t.Fatalf("violations = %v", viol)
	}
}

func TestSplittingNoSourceAndDefault(t *testing.T) {
	in := []LineInput{sl("600000", "D", "100.00", nil), sl("211000", "C", "100.00", nil)}
	_, _, viol := ApplySplitting(SplittingRule{SplittingDimensions: []string{"PC"}}, "USD", in)
	if len(viol) != 2 || viol[0].Code != CodeSplitNoSource || *viol[1].LineIndex != 1 {
		t.Fatalf("violations = %v", viol)
	}
	rule := SplittingRule{SplittingDimensions: []string{"PC"}, DefaultSplittingValue: map[string]string{"PC": "DEF"}}
	out, trace, viol := ApplySplitting(rule, "USD", in)
	if len(viol) != 0 || out[0].Dimensions["PC"] != "DEF" || out[1].Dimensions["PC"] != "DEF" || trace[0].Kind != SplitDefault {
		t.Fatalf("out=%+v trace=%+v viol=%v", out, trace, viol)
	}
}

func TestSplittingPartialLineTakesDefault(t *testing.T) {
	rule := SplittingRule{SplittingDimensions: []string{"PC", "SEG"}, DefaultSplittingValue: map[string]string{"SEG": "S0"}}
	in := []LineInput{
		sl("600000", "D", "10.00", map[string]string{"PC": "PC1"}),
		sl("211000", "C", "10.00", nil),
	}
	out, trace, viol := ApplySplitting(rule, "USD", in)
	if len(viol) != 0 || out[0].Dimensions["SEG"] != "S0" || out[1].Dimensions["PC"] != "PC1" || out[1].Dimensions["SEG"] != "S0" {
		t.Fatalf("out=%+v viol=%v", out, viol)
	}
	// The default given to the partly carried line is in the trace, like an inherited value.
	if len(trace) != 2 || trace[0].Kind != SplitDefault || trace[0].LineIndex != 0 || trace[0].Dimensions["SEG"] != "S0" ||
		trace[1].Kind != SplitInherited || trace[1].LineIndex != 1 {
		t.Fatalf("trace = %+v", trace)
	}
	_, _, viol = ApplySplitting(SplittingRule{SplittingDimensions: []string{"PC", "SEG"}}, "USD", in)
	if len(viol) != 1 || viol[0].Field != "lines.dimensions.SEG" {
		t.Fatalf("violations = %v", viol)
	}
}

func TestSplittingSkipsUnbalancedEntry(t *testing.T) {
	in := []LineInput{sl("600000", "D", "100.00", map[string]string{"PC": "A"}), sl("211000", "C", "90.00", nil)}
	out, trace, viol := ApplySplitting(SplittingRule{SplittingDimensions: []string{"PC"}}, "USD", in)
	if len(out) != 2 || trace != nil || viol != nil {
		t.Fatalf("unbalanced entry must pass through: %v %v %v", out, trace, viol)
	}
}

// A line finer than the currency allows is left for the scale check: splitting would round the
// parts to whole cents and hide the error.
func TestSplittingLeavesOverPreciseEntryAlone(t *testing.T) {
	in := []LineInput{
		sl("600000", "D", "10.00", map[string]string{"PC": "A"}),
		sl("154000", "D", "0.005", nil),
		sl("211000", "C", "10.005", nil),
	}
	out, trace, viol := ApplySplitting(SplittingRule{SplittingDimensions: []string{"PC"}}, "USD", in)
	if len(out) != 3 || trace != nil || viol != nil || !out[2].Amount.Equal(decimal.RequireFromString("10.005")) || out[2].Dimensions["PC"] != "" {
		t.Fatalf("over-precise entry must pass through: %+v %v %v", out, trace, viol)
	}
}

func TestValidateSplittingRule(t *testing.T) {
	attrs := []string{"PC", "SEG"}
	ok := SplittingRule{SourceType: "vendor_invoice", SplittingDimensions: []string{"PC"}, DefaultSplittingValue: map[string]string{"PC": "X1"}}
	if p := ValidateSplittingRule(ok, attrs); len(p) != 0 {
		t.Fatalf("valid rule rejected: %v", p)
	}
	bad := SplittingRule{SourceType: "", SplittingDimensions: []string{"PC", "PC", "NOPE"}, DefaultSplittingValue: map[string]string{"SEG": "a b"}}
	p := ValidateSplittingRule(bad, attrs)
	for _, f := range []string{"sourceType", "splittingDimensions", "defaultSplittingValue.SEG"} {
		if p[f] == "" {
			t.Errorf("no problem for %s: %v", f, p)
		}
	}
}

// BR-11.2: a split line's statistical quantity is divided like its amount, so
// the parts add up to the original quantity instead of each repeating it.
func TestSplittingDividesStatisticalQuantity(t *testing.T) {
	rule := SplittingRule{SplittingDimensions: []string{"PC"}, ZeroBalanceClearingAccount: "199999"}
	freight := sl("211000", "C", "1000.00", nil)
	freight.Quantity, freight.Unit = decimal.RequireFromString("10"), "KG"
	in := []LineInput{
		sl("600000", "D", "666.67", map[string]string{"PC": "PC1"}),
		sl("600000", "D", "333.33", map[string]string{"PC": "PC2"}),
		freight,
	}
	out, _, viol := ApplySplitting(rule, "USD", in)
	if len(viol) != 0 {
		t.Fatalf("violations: %v", viol)
	}
	total := decimal.Zero
	got := map[string]string{}
	for _, l := range out {
		if l.Account != "211000" {
			continue
		}
		if l.Unit != "KG" {
			t.Errorf("part %v lost its unit", l.Dimensions)
		}
		total = total.Add(l.Quantity)
		got[l.Dimensions["PC"]] = l.Quantity.StringFixed(QuantityScale)
	}
	if !total.Equal(decimal.RequireFromString("10")) {
		t.Errorf("split quantities add up to %s, want 10 (%v)", total, got)
	}
	if got["PC1"] != "6.666700" || got["PC2"] != "3.333300" {
		t.Errorf("quantity shares = %v, want PC1 6.666700 and PC2 3.333300", got)
	}
}
