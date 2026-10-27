package domain

import (
	"testing"

	"github.com/shopspring/decimal"
	"pgregory.net/rapid"
)

func line(n int, qty, pct string, scalable, percent bool) FormulaLine {
	return FormulaLine{LineNumber: n, ItemID: "ITEM-" + string(rune('A'+n-1)), Type: LineIngredient,
		Qty: dec(qty), Percent: dec(pct), Scalable: scalable, PercentControlled: percent}
}

// TestScale_AC241 is AC-24.1: a 1,000 kg formula with a 50 kg fixed line scaled to 2,500 kg.
func TestScale_AC241(t *testing.T) {
	f := Formula{Size: dec("1000"), Lines: []FormulaLine{
		line(1, "600", "", true, false),
		line(2, "350", "", true, false),
		line(3, "50", "", false, false),
	}}
	res, err := Scale(f, dec("2500"))
	if err != nil {
		t.Fatal(err)
	}
	if !res.ScaleFactor.Equal(dec("2.5")) {
		t.Fatalf("factor = %s", res.ScaleFactor)
	}
	want := []string{"1500", "875", "50"}
	for i, w := range want {
		if !res.Lines[i].Qty.Equal(dec(w)) {
			t.Fatalf("line %d qty = %s, want %s", i+1, res.Lines[i].Qty, w)
		}
	}
}

func TestValidateFormula_PercentSum(t *testing.T) {
	tests := []struct {
		name  string
		pcts  []string
		valid bool
	}{
		{"exactly 100", []string{"60", "40"}, true},
		{"within tolerance", []string{"60", "40.01"}, true},
		{"99.5 fails (AC-24.9)", []string{"59.5", "40"}, false},
		{"100.02 fails", []string{"60", "40.02"}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := Formula{Size: dec("100")}
			for i, p := range tc.pcts {
				f.Lines = append(f.Lines, line(i+1, "0", p, true, true))
			}
			err := ValidateFormula(f)
			if (err == nil) != tc.valid {
				t.Fatalf("ValidateFormula = %v, want valid %v", err, tc.valid)
			}
			if err != nil && err.(*Error).Code != CodePercentSum {
				t.Fatalf("code = %s", err.(*Error).Code)
			}
		})
	}
}

func TestScale_RejectsNonPositive(t *testing.T) {
	f := Formula{Size: dec("100"), Lines: []FormulaLine{line(1, "100", "", true, false)}}
	if _, err := Scale(f, dec("0")); err == nil {
		t.Fatal("zero batch size accepted")
	}
	if _, err := Scale(Formula{Size: dec("0")}, dec("10")); err == nil {
		t.Fatal("zero formula size accepted")
	}
}

// TestScale_PercentLinesSumToBatch is the BR-24.1 property: percent-controlled
// lines that sum to 100 always sum to the batch size exactly.
func TestScale_PercentLinesSumToBatch(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		n := rapid.IntRange(1, 6).Draw(t, "lines")
		weights := make([]int64, n)
		var total int64
		for i := range weights {
			weights[i] = rapid.Int64Range(1, 1000).Draw(t, "weight")
			total += weights[i]
		}
		lines := make([]FormulaLine, n)
		pctSum := decimal.Zero
		for i := range lines {
			p := decimal.NewFromInt(weights[i]).Mul(oneHundred()).DivRound(decimal.NewFromInt(total), 4)
			if i == n-1 {
				p = oneHundred().Sub(pctSum)
			}
			pctSum = pctSum.Add(p)
			lines[i] = FormulaLine{LineNumber: i + 1, ItemID: "I", Type: LineIngredient, Qty: decimal.Zero,
				Percent: p, Scalable: true, PercentControlled: true}
		}
		batch := decimal.NewFromInt(rapid.Int64Range(1, 1_000_000).Draw(t, "batch"))
		res, err := Scale(Formula{Size: batch, Lines: lines}, batch)
		if err != nil {
			t.Fatal(err)
		}
		sum := decimal.Zero
		for _, l := range res.Lines {
			sum = sum.Add(l.Qty)
		}
		if !sum.Equal(batch) {
			t.Fatalf("sum %s != batch %s", sum, batch)
		}
	})
}

func TestValidateFormula_LineRules(t *testing.T) {
	ok := line(1, "10", "", true, false)
	tests := []struct {
		name  string
		lines []FormulaLine
		code  string
	}{
		{"no lines", nil, CodeInvalid},
		{"duplicate line number", []FormulaLine{ok, line(1, "5", "", true, false)}, CodeInvalid},
		{"zero line number", []FormulaLine{line(0, "5", "", true, false)}, CodeInvalid},
		{"unknown type", []FormulaLine{{LineNumber: 1, Type: "widget", Qty: dec("1")}}, CodeInvalid},
		{"negative qty", []FormulaLine{line(1, "-1", "", true, false)}, CodeInvalid},
		{"percent above 100", []FormulaLine{line(1, "0", "100.5", true, false)}, CodeInvalid},
		{"percent-controlled co-product", []FormulaLine{{LineNumber: 1, Type: LineCoProduct, Percent: dec("100"), Scalable: true, PercentControlled: true}}, CodeInvalid},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateFormula(Formula{Size: dec("100"), Lines: tc.lines})
			if err == nil || err.(*Error).Code != tc.code {
				t.Fatalf("err = %v, want %s", err, tc.code)
			}
		})
	}
	if err := ValidateFormula(Formula{Size: dec("100"), Lines: []FormulaLine{ok}}); err != nil {
		t.Fatalf("valid formula refused: %v", err)
	}
}

// BR-24.1: the rounding residual stays on the inputs. Three equal thirds of a
// 3 kg formula scaled to 1 kg round to 0.333333 each; the residual 0.000001
// goes to the first largest input, and the co-product output takes none.
func TestScale_ResidualOnLargestInputNotOnOutputs(t *testing.T) {
	f := Formula{Size: dec("3"), Lines: []FormulaLine{
		line(1, "1", "", true, false),
		line(2, "1", "", true, false),
		line(3, "1", "", true, false),
		{LineNumber: 4, ItemID: "OUT", Type: LineCoProduct, Qty: dec("2"), Scalable: true},
	}}
	res, err := Scale(f, dec("1"))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"0.333334", "0.333333", "0.333333", "0.666667"}
	for i, w := range want {
		if !res.Lines[i].Qty.Equal(dec(w)) {
			t.Fatalf("line %d = %s, want %s", i+1, res.Lines[i].Qty, w)
		}
	}
	inputs := res.Lines[0].Qty.Add(res.Lines[1].Qty).Add(res.Lines[2].Qty)
	if !inputs.Equal(dec("1")) {
		t.Fatalf("inputs sum to %s, want 1", inputs)
	}
}
