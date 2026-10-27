package domain

import (
	"testing"

	"github.com/shopspring/decimal"
)

func TestFormula_EvaluatesWithPrecedence(t *testing.T) {
	vars := map[string]decimal.Decimal{"a": d("2"), "b": d("3"), "cav_2": d("4")}
	cases := []struct{ src, want string }{
		{"1 + 2 * 3", "7"},
		{"(1 + 2) * 3", "9"},
		{"a * b + cav_2", "10"},
		{"a - b - 1", "-2"},
		{"12 / a / b", "2"},
		{"-a * b", "-6"},
		{"--a", "2"},
		{"0.25 * (a + b) ", "1.25"},
	}
	for _, tc := range cases {
		f, err := ParseFormula(tc.src)
		if err != nil {
			t.Errorf("%q: %v", tc.src, err)
			continue
		}
		got, err := f.Eval(vars)
		if err != nil {
			t.Errorf("%q: eval %v", tc.src, err)
			continue
		}
		if !got.Equal(d(tc.want)) {
			t.Errorf("%q = %s, want %s", tc.src, got, tc.want)
		}
	}
}

func TestFormula_IdentsAreDistinctAndSorted(t *testing.T) {
	f, err := ParseFormula("weight * cavities + weight / hours")
	if err != nil {
		t.Fatal(err)
	}
	got := f.Idents()
	if len(got) != 3 || got[0] != "cavities" || got[1] != "hours" || got[2] != "weight" {
		t.Errorf("idents = %v", got)
	}
}

func TestFormula_RejectsMalformedInput(t *testing.T) {
	deep := ""
	for i := 0; i < maxFormulaDepth+2; i++ {
		deep += "("
	}
	deep += "1"
	for i := 0; i < maxFormulaDepth+2; i++ {
		deep += ")"
	}
	long := "1"
	for len(long) <= maxFormulaLength {
		long += "+1"
	}
	for _, src := range []string{"", "   ", "1 +", "(1", "1)", "1..2", "a b", "2 ^ 3", "*1", deep, long} {
		if _, err := ParseFormula(src); err == nil {
			t.Errorf("ParseFormula(%q) accepted", src)
		}
	}
}

func TestFormula_EvalErrors(t *testing.T) {
	f, _ := ParseFormula("a / b")
	if _, err := f.Eval(map[string]decimal.Decimal{"a": d("1"), "b": d("0")}); err == nil {
		t.Error("division by zero accepted")
	}
	if _, err := f.Eval(map[string]decimal.Decimal{"a": d("1")}); err == nil {
		t.Error("missing characteristic accepted")
	}
	if _, err := (Formula{}).Eval(nil); err == nil {
		t.Error("an unparsed formula evaluated")
	}
}
