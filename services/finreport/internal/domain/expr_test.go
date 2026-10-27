package domain

import (
	"errors"
	"reflect"
	"testing"

	"github.com/shopspring/decimal"
	"pgregory.net/rapid"
)

func lookupFrom(vals map[string]Value) func(string) (Value, error) {
	return func(ref string) (Value, error) {
		v, ok := vals[ref]
		if !ok {
			return Value{}, errf(CodeExpression, "unknown reference %s", ref)
		}
		return v, nil
	}
}

func TestExpr_Eval(t *testing.T) {
	vals := map[string]Value{
		"R1": {V: decimal.NewFromInt(10)},
		"R2": {V: decimal.NewFromInt(4)},
		"R3": {V: decimal.Zero},
		"R4": Blank,
	}
	tests := []struct {
		src   string
		want  Value
		isErr bool
	}{
		{"R1+R2*2", Value{V: decimal.NewFromInt(18)}, false},
		{"(R1-R2)/2", Value{V: decimal.NewFromInt(3)}, false},
		{"-R2+R1", Value{V: decimal.NewFromInt(6)}, false},
		{"2*-3", Value{V: decimal.NewFromInt(-6)}, false},
		{"1.5*R2", Value{V: decimal.NewFromInt(6)}, false},
		{"R1-(R2-(R2-1))", Value{V: decimal.NewFromInt(9)}, false},
		{"R1/R3", Blank, false},
		{"R1+R4", Blank, false},
		{"-R4", Blank, false},
		{"R2*(R4/R1)", Blank, false},
		{"", Value{}, true},
		{"R1 +", Value{}, true},
		{"R1 R2", Value{}, true},
		{"(R1", Value{}, true},
		{"R1)", Value{}, true},
		{"2 $ 3", Value{}, true},
		{"1..2", Value{}, true},
		{"R1*R2", Value{V: decimal.NewFromInt(40)}, false},
	}
	for _, tc := range tests {
		t.Run(tc.src, func(t *testing.T) {
			e, err := ParseExpr(tc.src)
			if tc.isErr {
				var de *Error
				if !errors.As(err, &de) || de.Code != CodeExpression {
					t.Fatalf("parse %q: err = %v, want %s", tc.src, err, CodeExpression)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			got, err := e.Eval(lookupFrom(vals))
			if err != nil {
				t.Fatal(err)
			}
			if got.Blank != tc.want.Blank || (!got.Blank && !got.V.Equal(tc.want.V)) {
				t.Fatalf("eval %q = %+v, want %+v", tc.src, got, tc.want)
			}
		})
	}
}

func TestExpr_Refs(t *testing.T) {
	e, err := ParseExpr("R2+R1*R2-(R3)")
	if err != nil {
		t.Fatal(err)
	}
	if got := e.Refs(); !reflect.DeepEqual(got, []string{"R2", "R1", "R3"}) {
		t.Fatalf("refs = %v", got)
	}
}

func TestExpr_UnknownReferenceFromLookup(t *testing.T) {
	e, _ := ParseExpr("R9+1")
	if _, err := e.Eval(lookupFrom(map[string]Value{})); err == nil {
		t.Fatal("unknown reference must surface as an error")
	}
}

// Property: evaluation agrees with exact integer arithmetic, and division by a
// zero denominator is blank rather than an error.
func TestExpr_MatchesExactArithmetic(t *testing.T) {
	e, err := ParseExpr("(R1+R2)*R3-R1*R2")
	if err != nil {
		t.Fatal(err)
	}
	rapid.Check(t, func(t *rapid.T) {
		a := rapid.IntRange(-1000, 1000).Draw(t, "a")
		b := rapid.IntRange(-1000, 1000).Draw(t, "b")
		c := rapid.IntRange(-1000, 1000).Draw(t, "c")
		got, err := e.Eval(lookupFrom(map[string]Value{
			"R1": {V: decimal.NewFromInt(int64(a))},
			"R2": {V: decimal.NewFromInt(int64(b))},
			"R3": {V: decimal.NewFromInt(int64(c))},
		}))
		if err != nil {
			t.Fatal(err)
		}
		want := int64(a+b)*int64(c) - int64(a)*int64(b)
		if got.Blank || got.V.IntPart() != want {
			t.Fatalf("a=%d b=%d c=%d: got %+v, want %d", a, b, c, got, want)
		}
		div, _ := ParseExpr("R1/R3")
		q, err := div.Eval(lookupFrom(map[string]Value{"R1": {V: decimal.NewFromInt(int64(a))}, "R3": {V: decimal.NewFromInt(int64(c))}}))
		if err != nil {
			t.Fatal(err)
		}
		if (c == 0) != q.Blank {
			t.Fatalf("c=%d: blank = %v", c, q.Blank)
		}
	})
}
