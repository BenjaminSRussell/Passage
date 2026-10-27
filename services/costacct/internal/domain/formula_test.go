package domain

import (
	"errors"
	"testing"

	"github.com/shopspring/decimal"
	"pgregory.net/rapid"
)

// fataler is satisfied by *testing.T and *rapid.T.
type fataler interface {
	Helper()
	Fatalf(format string, args ...any)
}

func mustParse(t fataler, src string) *Formula {
	t.Helper()
	f, err := ParseFormula(src)
	if err != nil {
		t.Fatalf("parse %q: %v", src, err)
	}
	return f
}

func TestParseFormula_Rejects(t *testing.T) {
	cases := map[string]string{
		"empty":             "   ",
		"trailing operator": "1 +",
		"unknown name":      "foo * 2",
		"bare reference":    "m * 2",
		"bad prefix":        "x.inspectionLots",
		"bad ref name":      "m.1bad",
		"unknown function":  "sqrt(4)",
		"if without cmp":    "if(1, 2, 3)",
		"max one argument":  "max(1)",
		"two dots number":   "1.2.3",
		"trailing dot":      "1. + 2",
		"stray bang":        "!m.x",
		"bad character":     "2 $ 3",
		"unclosed paren":    "(1 + 2",
		"trailing tokens":   "1 2",
		"too long":          repeatChar('1', MaxFormulaLength+1),
	}
	for name, src := range cases {
		if _, err := ParseFormula(src); err == nil {
			t.Errorf("%s: %q parsed, want error", name, src)
		}
	}
}

func TestParseFormula_ErrorsCarryPosition(t *testing.T) {
	_, err := ParseFormula("1 + foo")
	var fe *FormulaError
	if !errors.As(err, &fe) || fe.Pos != 4 {
		t.Fatalf("err = %v, want FormulaError at position 4", err)
	}
}

func TestParseFormula_Refs(t *testing.T) {
	f := mustParse(t, "25 * m.inspectionLots + 0.005 * b.materialCost + m.inspectionLots")
	got := f.Refs()
	want := []string{"b.materialCost", "m.inspectionLots"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("refs = %v, want %v", got, want)
	}
}

func TestFormula_Eval(t *testing.T) {
	env := Env{
		"m.a":   dec("4"),
		"m.b":   dec("2.5"),
		"m.z":   decimal.Zero,
		"b.mat": dec("20000"),
	}
	cases := []struct {
		src     string
		want    string
		divZero bool
	}{
		{"2 + 3 * 4", "14", false},
		{"(2 + 3) * 4", "20", false},
		{"2 - 3 - 1", "-2", false},
		{"-m.a + 3", "-1", false},
		{"2 × m.a ÷ 4", "2", false},
		{"25 * m.a + 0.005 * b.mat", "200", false}, // AC-38.14
		{"min(m.a, m.b)", "2.5", false},
		{"max(m.a, m.b)", "4", false},
		{"if(m.a > 3, 10, 20)", "10", false},
		{"if(m.a <= 3, 10, 20)", "20", false},
		{"if(m.a != 4, 1, 2)", "2", false},
		{"if(m.a = 4, 1, 2)", "1", false},
		{"m.missing + 1", "1", false},
		{"10 / m.z", "0", true},
		{"m.a / m.b", "1.6", false},
	}
	for _, c := range cases {
		f := mustParse(t, c.src)
		got, div := f.Eval(env)
		if !got.Equal(dec(c.want)) {
			t.Errorf("%q = %s, want %s", c.src, got, c.want)
		}
		if div != c.divZero {
			t.Errorf("%q divByZero = %v, want %v", c.src, div, c.divZero)
		}
	}
}

// TestEvaluateTemplate_WorkedExample is AC-38.14: 25.00 × 4 lots + 0.5% × 20,000.00
// gives 200.00 to the receiver and credits the sender 200.00.
func TestEvaluateTemplate_WorkedExample(t *testing.T) {
	lines := []TemplateLine{{ReceiverID: "order-1", ElementID: "el-1",
		Formula: mustParse(t, "25.00 * m.inspectionLots + 0.005 * b.materialCost")}}
	envs := map[string]Env{"order-1": {"m.inspectionLots": dec("4"), "b.materialCost": dec("20000")}}
	res, err := EvaluateTemplate(lines, envs, 2, false)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Lines[0].Amount.Equal(dec("200.00")) || !res.TotalIn.Equal(dec("200.00")) {
		t.Fatalf("result = %+v, want 200.00", res)
	}
}

func TestEvaluateTemplate_NegativeAndWarnings(t *testing.T) {
	lines := []TemplateLine{{ReceiverID: "r", ElementID: "e", Formula: mustParse(t, "0 - m.x")}}
	envs := map[string]Env{"r": {"m.x": dec("5")}}
	if _, err := EvaluateTemplate(lines, envs, 2, false); !errors.Is(err, ErrNegativeReceiver) {
		t.Fatalf("err = %v, want ErrNegativeReceiver", err)
	}
	res, err := EvaluateTemplate(lines, envs, 2, true)
	if err != nil {
		t.Fatalf("allow-negative: %v", err)
	}
	if !res.Lines[0].Amount.Equal(dec("-5.00")) {
		t.Fatalf("amount = %s, want -5.00", res.Lines[0].Amount)
	}

	div := []TemplateLine{{ReceiverID: "r", ElementID: "e", Formula: mustParse(t, "100 / m.zero")}}
	res, err = EvaluateTemplate(div, map[string]Env{"r": {}}, 2, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Warnings) != 1 || !res.Lines[0].DivByZero || !res.Lines[0].Amount.IsZero() {
		t.Fatalf("division by zero: %+v", res)
	}
}

// TestParseFormula_NeverPanics: arbitrary input is either parsed or rejected.
func TestParseFormula_NeverPanics(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		src := rapid.String().Draw(t, "src")
		_, _ = ParseFormula(src)
	})
}

func repeatChar(c byte, n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = c
	}
	return string(b)
}
