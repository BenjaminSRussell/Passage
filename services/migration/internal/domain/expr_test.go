package domain

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func evalExprText(t *testing.T, src string, row map[string]any) (string, error) {
	t.Helper()
	p, err := ParseExpr(src)
	if err != nil {
		t.Fatalf("parse %q: %v", src, err)
	}
	v, err := p.Eval(func(path string) any { return Lookup(row, path) })
	return v.text(), err
}

func TestExpr_Evaluates(t *testing.T) {
	row := map[string]any{"QTY": "12", "PRICE": "2.50", "CODE": " ab ", "N": float64(3), "EMPTY": "", "addr": map[string]any{"city": "Oslo"}, "Unit price": "4"}
	cases := []struct{ src, want string }{
		{"QTY * PRICE", "30"},
		{"QTY + 1 - 2 * 3", "7"},
		{"(QTY + 1) * 2", "26"},
		{"-QTY", "-12"},
		{"QTY / 8", "1.5"},
		{"N * 2", "6"},
		{"round(PRICE / 3, 2)", "0.83"},
		{"round(2.5, 0)", "2"},
		{"abs(0 - 4.5)", "4.5"},
		{"min(QTY, 3, N)", "3"},
		{"max(QTY, 3, N)", "12"},
		{"max(EMPTY, MISSING)", ""},
		{"upper(trim(CODE))", "AB"},
		{"lower('ABC')", "abc"},
		{"concat('X-', trim(CODE), MISSING, 7)", "X-ab7"},
		{"substr('ABCDE', 2, 3)", "BCD"},
		{"substr('ABCDE', 4)", "DE"},
		{"substr('ABCDE', 9)", ""},
		{"substr('ABCDE', 4, 99)", "DE"},
		{"len('héllo')", "5"},
		{"coalesce(EMPTY, MISSING, 'dflt')", "dflt"},
		{"coalesce(CODE, 'dflt')", "ab"},
		{"isempty(EMPTY) && isempty(MISSING) && !isempty(CODE)", "true"},
		{"if(QTY > 10, 'big', 'small')", "big"},
		{"if(QTY > 10 && PRICE < 2, 'a', 'b')", "b"},
		{"if(MISSING > 1, 'a', 'b')", "b"},
		{"QTY == 12 && QTY == '12' && PRICE >= 2.5", "true"},
		{"'007' == '7'", "false"},
		{"'007' == 7", "true"},
		{"QTY != 12 || addr.city == 'Oslo'", "true"},
		{"`Unit price` * 2", "8"},
		{"MISSING == null", "true"},
		{"MISSING != null", "false"},
		{"MISSING + 1", ""},
		{"-MISSING", ""},
		{"true && null", "false"},
		{"1.5 + .5", "2"},
		{"if(true, 1, 1 / 0)", "1"},
	}
	for _, c := range cases {
		got, err := evalExprText(t, c.src, row)
		if err != nil {
			t.Errorf("%s: %v", c.src, err)
		} else if got != c.want {
			t.Errorf("%s = %q, want %q", c.src, got, c.want)
		}
	}
}

func TestExpr_RuntimeErrors(t *testing.T) {
	row := map[string]any{"QTY": "12", "NAME": "pump", "obj": map[string]any{"a": 1}}
	cases := map[string]string{
		"QTY / 0":                 "division by zero",
		"NAME * 2":                "not a number",
		"obj + 1":                 "not a scalar",
		"NAME > 3":                "not a number",
		"true < false":            "compares only with a boolean",
		"!NAME":                   "needs a boolean",
		"NAME && true":            "needs booleans",
		"if(NAME, 1, 2)":          "boolean condition",
		"round(1.5, 9)":           "scale",
		"round(1.5, 0.5)":         "scale",
		"substr('abc', 0)":        "from 1",
		"substr('abc', 1, 0 - 1)": "length",
	}
	for src, want := range cases {
		_, err := evalExprText(t, src, row)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: err = %v, want %q", src, err, want)
		}
	}
}

func TestExpr_ParseErrors(t *testing.T) {
	deep := strings.Repeat("(", 40) + "1" + strings.Repeat(")", 40)
	terms := strings.TrimSuffix(strings.Repeat("1+", 150), "+")
	cases := map[string]string{
		"":                       "empty",
		"  ":                     "empty",
		"1 +":                    "ends early",
		"(1 + 2":                 "expected )",
		"1 2":                    "unexpected",
		"foo(1)":                 "unknown function",
		"round(1)":               "takes 2 to 2",
		"if(1, 2)":               "takes 3 to 3",
		"coalesce(1)":            "takes 2 to 10",
		"'abc":                   "unterminated",
		"`abc":                   "unterminated",
		"1 = 2":                  "unexpected",
		"1 ; 2":                  "unexpected",
		"1.2.3":                  "bad number",
		"2x":                     "unexpected",
		deep:                     "nested deeper",
		terms:                    "more than",
		strings.Repeat("1", 501): "longer than",
		"min(1,":                 "ends early",
		"concat('a' 'b')":        "expected )",
		"system.exit(1)":         "unknown function",
	}
	for src, want := range cases {
		_, err := ParseExpr(src)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%.40q: err = %v, want %q", src, err, want)
		}
	}
}

func TestExpr_RefsInOrderOnce(t *testing.T) {
	p, err := ParseExpr("A + B * A + `C d` + f(x.y)")
	if err == nil {
		t.Fatalf("f is not a function, parse should fail: %v", p)
	}
	p, err = ParseExpr("A + B * A + `C d` + abs(x.y)")
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(p.Refs(), "|"); got != "A|B|C d|x.y" {
		t.Errorf("refs = %s", got)
	}
}

const exprMapping = `{"entity":"item","version":1,"fields":[
 {"source":"SKU","target":"sku","type":"string","required":true},
 {"rule":"expr","expr":"QTY * PRICE","target":"value","type":"decimal","scale":2},
 {"rule":"expr","expr":"if(QTY > 100, 'BULK', 'STD')","target":"band","type":"string"},
 {"rule":"expr","expr":"MISSING","target":"note","type":"string","default":"none"},
 {"rule":"expr","expr":"QTY > 0","target":"active","type":"bool"}],
 "rejectWhen":[{"field":"value","op":"expr","value":"value > 1000 && band == 'STD'","code":"ITEM-VALUE-BAND"}],
 "tests":[
  {"name":"std","input":{"SKU":"A","QTY":"10","PRICE":"2.555"},"expect":{"sku":"A","value":"25.55","band":"STD","note":"none","active":"true"}},
  {"name":"bulk","input":{"SKU":"A","QTY":"200","PRICE":"9"},"expect":{"sku":"A","value":"1800.00","band":"BULK","note":"none","active":"true"}},
  {"name":"band","input":{"SKU":"A","QTY":"100","PRICE":"11"},"expectRejects":["ITEM-VALUE-BAND"]},
  {"name":"text qty","input":{"SKU":"A","QTY":"ten","PRICE":"1"},"expectRejects":["MIG-MAP-COERCE","MIG-MAP-COERCE","MIG-MAP-COERCE"]}]}`

func TestMapping_ExprRuleAndRejectWhen(t *testing.T) {
	m, err := ParseMapping([]byte(exprMapping))
	if err != nil {
		t.Fatal(err)
	}
	if failed := m.RunTests(); len(failed) > 0 {
		t.Fatalf("mapping tests failed: %v", failed)
	}
	out, warnings, rejects := m.ApplyWithWarnings(map[string]any{"SKU": "A", "QTY": 10, "PRICE": 2.5555})
	if len(rejects) > 0 || out["value"] != "25.56" || len(warnings) != 1 || warnings[0].Code != CodePrecisionLoss {
		t.Errorf("out=%v warnings=%v rejects=%v", out, warnings, rejects)
	}
}

func TestMapping_ExprNullHonoursRequiredAndTemplate(t *testing.T) {
	m, err := ParseMapping([]byte(`{"entity":"x","version":1,"fields":[{"rule":"expr","expr":"A + B","target":"total","type":"decimal","required":true}]}`))
	if err != nil {
		t.Fatal(err)
	}
	_, rejects := m.Apply(map[string]any{"A": "1"})
	if len(rejects) != 1 || rejects[0].Code != CodeMapRequired {
		t.Fatalf("rejects = %v", rejects)
	}
	out, _, _, fromTemplate := m.applyTemplated(map[string]any{"A": "1"}, map[string]string{"total": "5"})
	if out["total"] != "5" || len(fromTemplate) != 1 {
		t.Errorf("template not used: %v %v", out, fromTemplate)
	}
}

func TestMapping_ExprPublishRefusals(t *testing.T) {
	bad := map[string]string{
		"syntax":       `{"entity":"x","version":1,"fields":[{"rule":"expr","expr":"A +","target":"t","type":"string"}]}`,
		"no expr":      `{"entity":"x","version":1,"fields":[{"rule":"expr","target":"t","type":"string"}]}`,
		"with source":  `{"entity":"x","version":1,"fields":[{"rule":"expr","expr":"1","source":"A","target":"t","type":"string"}]}`,
		"with fn":      `{"entity":"x","version":1,"fields":[{"rule":"expr","expr":"1","fn":"ax_noyes","target":"t","type":"bool"}]}`,
		"expr on copy": `{"entity":"x","version":1,"fields":[{"source":"A","expr":"1","target":"t","type":"string"}]}`,
		"bad default":  `{"entity":"x","version":1,"fields":[{"rule":"expr","expr":"A","target":"t","type":"int","default":"x"}]}`,
		"cond target":  `{"entity":"x","version":1,"fields":[{"source":"A","target":"t","type":"string"}],"rejectWhen":[{"field":"t","op":"expr","value":"u > 1","code":"X-Y"}]}`,
		"cond syntax":  `{"entity":"x","version":1,"fields":[{"source":"A","target":"t","type":"string"}],"rejectWhen":[{"field":"t","op":"expr","value":"t >","code":"X-Y"}]}`,
		"cond values":  `{"entity":"x","version":1,"fields":[{"source":"A","target":"t","type":"string"}],"rejectWhen":[{"field":"t","op":"expr","values":["t"],"code":"X-Y"}]}`,
	}
	for name, doc := range bad {
		if _, err := ParseMapping([]byte(doc)); err == nil {
			t.Errorf("%s: published", name)
		} else if e, ok := err.(*Error); !ok || e.Code != CodeMapInvalid {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}

func TestMapping_ExprConditionFailureRejectsRow(t *testing.T) {
	m, err := ParseMapping([]byte(`{"entity":"x","version":1,"fields":[{"source":"A","target":"t","type":"string"}],"rejectWhen":[{"field":"t","op":"expr","value":"t > 1","code":"X-Y"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	_, rejects := m.Apply(map[string]any{"A": "abc"})
	if len(rejects) != 1 || rejects[0].Code != "X-Y" || !strings.Contains(rejects[0].Detail, "failed") {
		t.Errorf("rejects = %v", rejects)
	}
	if _, rejects = m.Apply(map[string]any{"A": "5"}); len(rejects) != 1 || !strings.Contains(rejects[0].Detail, "matched") {
		t.Errorf("rejects = %v", rejects)
	}
	if out, rejects := m.Apply(map[string]any{"A": "0"}); len(rejects) != 0 || out["t"] != "0" {
		t.Errorf("out=%v rejects=%v", out, rejects)
	}
}

func TestExpr_LexerReadsRunesNotBytes(t *testing.T) {
	// A multi-byte rune whose lead byte is a Latin-1 letter (× is C3 97, € is E2 82 AC) once made the lexer loop
	// forever on an empty identifier.
	for _, src := range []string{"A × 2", "€", "A + 😀", "A ÷ B"} {
		if _, err := ParseExpr(src); err == nil || !strings.Contains(err.Error(), "unexpected") {
			t.Errorf("%q: err = %v", src, err)
		}
	}
	got, err := evalExprText(t, "Größe * 2 + Müller.Menge", map[string]any{"Größe": "3", "Müller": map[string]any{"Menge": "1"}})
	if err != nil || got != "7" {
		t.Errorf("unicode columns = %q, %v", got, err)
	}
}

func TestExpr_QuotedColumnIsNeverAKeywordOrFunction(t *testing.T) {
	got, err := evalExprText(t, "`true` == 'Y' && `null` == 'N'", map[string]any{"true": "Y", "null": "N"})
	if err != nil || got != "true" {
		t.Errorf("quoted keyword columns = %q, %v", got, err)
	}
	if _, err := ParseExpr("`round`(1, 2)"); err == nil {
		t.Error("a quoted name followed by ( must not call a function")
	}
}

func TestExpr_NumbersStayBounded(t *testing.T) {
	row := map[string]any{
		"HUGE":    "1e999999999",
		"TINY":    "1e-999999999",
		"BIGF":    1e300,
		"DIGITS":  strings.Repeat("9", 100000),
		"FINE":    "0." + strings.Repeat("0", 39) + "1",
		"ZEROS":   "1." + strings.Repeat("0", 60),
		"NEAR":    "999999999999999999999999999999",
		"QUARTER": "0.25",
	}
	for src, want := range map[string]string{
		"HUGE + 1":       "1e30 or more",
		"HUGE == 1":      "1e30 or more",
		"TINY + 1":       "decimal places",
		"BIGF * 1":       "1e30 or more",
		"DIGITS + 1":     "not a number",
		"FINE * 0.1":     "decimal places",
		"NEAR + 1":       "1e30 or more",
		"NEAR * NEAR":    "1e30 or more",
		"1 / (FINE * 1)": "1e30 or more",
		"QUARTER * QUARTER * " + strings.TrimSuffix(strings.Repeat("QUARTER * ", 40), " * "): "decimal places",
	} {
		start := time.Now()
		_, err := evalExprText(t, src, row)
		if err == nil || !strings.Contains(err.Error(), want) || !errors.Is(err, errExprRange) && want != "not a number" {
			t.Errorf("%.40s: err = %v, want %q", src, err, want)
		}
		if d := time.Since(start); d > time.Second {
			t.Errorf("%.40s took %v", src, d)
		}
	}
	if got, err := evalExprText(t, "ZEROS + FINE", row); err != nil || got != "1.0000000000000000000000000000000000000001" {
		t.Errorf("trailing zeros past 40 places are kept exact: %q %v", got, err)
	}
	if _, err := ParseExpr("1000000000000000000000000000000 + 1"); err == nil || !strings.Contains(err.Error(), "1e30") {
		t.Errorf("an out-of-range literal must fail at publish: %v", err)
	}
}

func TestExpr_TextOperationsOnLongInput(t *testing.T) {
	long := strings.Repeat("é", maxExprString)
	row := map[string]any{"NOTES": long}
	for src, want := range map[string]string{
		"substr(NOTES, 1, 3)":                                       "ééé",
		"substr(NOTES, 9999, 10000000000000000000)":                 "éé",
		"substr(NOTES, 2, 9223372036854775807) == substr(NOTES, 2)": "true",
		"substr('abc', 10000000000000000000)":                       "",
		"substr('abc', 2, 0)":                                       "",
		"len(NOTES)":                                                "10000",
	} {
		if got, err := evalExprText(t, src, row); err != nil || got != want {
			t.Errorf("%.50s = %.20q, %v; want %.20q", src, got, err, want)
		}
	}
	for _, src := range []string{"upper(NOTES)", "concat(NOTES, 'x')", "concat('a', NOTES, NOTES)"} {
		if _, err := evalExprText(t, src, row); err == nil || !strings.Contains(err.Error(), "longer than") {
			t.Errorf("%s: err = %v", src, err)
		}
	}
}

func TestMapping_ExprOutOfRangeRejectsRange(t *testing.T) {
	m, err := ParseMapping([]byte(`{"entity":"x","version":1,"fields":[{"rule":"expr","expr":"A * 2","target":"n","type":"decimal"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, rejects := m.Apply(map[string]any{"A": "1e999999999"}); len(rejects) != 1 || rejects[0].Code != CodeMapRange {
		t.Errorf("rejects = %v", rejects)
	}
	if _, rejects := m.Apply(map[string]any{"A": "x"}); len(rejects) != 1 || rejects[0].Code != CodeMapCoerce {
		t.Errorf("rejects = %v", rejects)
	}
}

func FuzzParseExprNeverPanics(f *testing.F) {
	for _, s := range []string{"A+B", "if(a,b,c)", "substr('a',1)", "((((", "`x", "1/0", "A × 2", "€", "`true`", "substr(A, 1, 10000000000000000000)", "A * B / C", "concat(A, B)", "round(A, 2)"} {
		f.Add(s)
	}
	values := []any{"1", nil, "", "1e999999999", "-0.5", "abc", float64(1e300), int64(-7), true, map[string]any{"x": 1}, strings.Repeat("ab", 6000)}
	f.Fuzz(func(t *testing.T, s string) {
		p, err := ParseExpr(s)
		if err != nil {
			return
		}
		for i, v := range values {
			start := time.Now()
			_, _ = p.Eval(func(path string) any { return values[(i+len(path))%len(values)] })
			_, _ = p.Eval(func(string) any { return v })
			if d := time.Since(start); d > 2*time.Second {
				t.Fatalf("%q took %v", s, d)
			}
		}
	})
}
