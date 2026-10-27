package domain

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/shopspring/decimal"
	"pgregory.net/rapid"
)

// TestCompileRuleRejectsMalformedExpressions: a plan with a broken rule must
// not be saved, so every malformed shape here is a compile error.
func TestCompileRuleRejectsMalformedExpressions(t *testing.T) {
	cases := map[string]string{
		"empty":             "",
		"only spaces":       "   ",
		"dangling operator": "service_days >=",
		"dangling and":      "employment_type == 'FULL_TIME' &&",
		"unterminated":      "employment_type == 'FULL_TIME",
		"bad character":     "service_days # 30",
		"unclosed paren":    "(service_days >= 30",
		"unclosed list":     "employment_type in ['FULL_TIME'",
		"two expressions":   "service_days 30",
		"bad number":        "service_days >= 3.4.5",
		"lone minus":        "service_days >= -",
		"arithmetic":        "service_days - 1 >= 30",
		"single ampersand":  "true & false",
	}
	for name, src := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := CompileRule(src); !errors.Is(err, ErrRuleSyntax) {
				t.Fatalf("CompileRule(%q) err = %v, want ErrRuleSyntax", src, err)
			}
		})
	}
}

// TestRuleEvalTable covers the CEL subset of BR-42.6 against fixed facts.
func TestRuleEvalTable(t *testing.T) {
	env := Env{
		"employment_type": "PART_TIME",
		"fte":             decimal.RequireFromString("0.60"),
		"service_days":    decimal.NewFromInt(30),
		"legal_entity":    "US-01",
		"job.type":        "HOURLY",
	}
	cases := []struct {
		src  string
		want bool
	}{
		{"service_days >= 30", true},
		{"service_days > 30", false},
		{"service_days <= 30", true},
		{"service_days < 30", false},
		{"service_days == 30.0", true},
		{"service_days != 30", false},
		{"fte >= 0.5", true},
		{"fte == 0.6", true},
		{"employment_type in ['FULL_TIME', 'PART_TIME']", true},
		{"employment_type in ['FULL_TIME']", false},
		{"legal_entity == 'US-01' && job.type == 'HOURLY'", true},
		{"legal_entity == 'DE-01' || service_days >= 30", true},
		{"!(employment_type == 'TEMP')", true},
		{"!(service_days >= 30) && fte > 0", false},
		{"true && false", false},
		{"false || true", true},
		{"(service_days >= 30 && fte >= 1) || employment_type in ['PART_TIME']", true},
		{"service_days > -1", true},
		{"service_days in [-5, 30]", true},
	}
	for _, c := range cases {
		t.Run(c.src, func(t *testing.T) {
			r, err := CompileRule(c.src)
			if err != nil {
				t.Fatalf("compile: %v", err)
			}
			got, err := r.Eval(env)
			if err != nil {
				t.Fatalf("eval: %v", err)
			}
			if got != c.want {
				t.Fatalf("eval = %v, want %v", got, c.want)
			}
		})
	}
}

// TestRuleEvalErrors: unknown facts and type mismatches are errors, never a
// silent false, so a missing fact cannot exclude or include a worker by accident.
func TestRuleEvalErrors(t *testing.T) {
	env := Env{"employment_type": "FULL_TIME", "service_days": decimal.NewFromInt(90)}
	cases := []struct {
		src  string
		want error
	}{
		{"age >= 18", ErrUnknownIdentifier},
		{"job.type == 'SALARIED'", ErrUnknownIdentifier},
		{"service_days", ErrRuleSyntax},
		{"employment_type == 3", ErrRuleSyntax},
		{"service_days == 'x'", ErrRuleSyntax},
		{"!service_days", ErrRuleSyntax},
		{"employment_type < 'Z'", nil}, // strings order; evaluates to a boolean
	}
	for _, c := range cases {
		t.Run(c.src, func(t *testing.T) {
			r, err := CompileRule(c.src)
			if err != nil {
				t.Fatalf("compile: %v", err)
			}
			_, err = r.Eval(env)
			if c.want == nil {
				if err != nil {
					t.Fatalf("eval: %v", err)
				}
				return
			}
			if !errors.Is(err, c.want) {
				t.Fatalf("eval err = %v, want %v", err, c.want)
			}
		})
	}
}

// TestRuleShortCircuitSkipsUnknownFact: the unused side of && and || is not
// read, so a rule may guard an optional fact such as age.
func TestRuleShortCircuitSkipsUnknownFact(t *testing.T) {
	env := Env{"employment_type": "TEMP"}
	for src, want := range map[string]bool{
		"employment_type == 'FULL_TIME' && age >= 18": false,
		"employment_type == 'TEMP' || age >= 18":      true,
	} {
		r, err := CompileRule(src)
		if err != nil {
			t.Fatalf("compile %q: %v", src, err)
		}
		got, err := r.Eval(env)
		if err != nil || got != want {
			t.Fatalf("%q = (%v, %v), want (%v, nil)", src, got, err, want)
		}
	}
}

// TestPropServiceDaysComparison: a compiled threshold agrees with integer
// comparison for every threshold and fact the generator draws.
func TestPropServiceDaysComparison(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		threshold := rapid.IntRange(0, 1000).Draw(t, "threshold")
		service := rapid.IntRange(-50, 1000).Draw(t, "service")
		r, err := CompileRule(fmt.Sprintf("service_days >= %d", threshold))
		if err != nil {
			t.Fatalf("compile: %v", err)
		}
		got, err := r.Eval(Env{"service_days": decimal.NewFromInt(int64(service))})
		if err != nil {
			t.Fatalf("eval: %v", err)
		}
		if want := service >= threshold; got != want {
			t.Fatalf("service %d >= %d = %v, want %v", service, threshold, got, want)
		}
	})
}

// TestCompileRuleBoundsNestingAndLength: a rule nested deeper than the parser
// allows, or longer than MaxRuleLength, is a syntax error, not a stack overflow.
func TestCompileRuleBoundsNestingAndLength(t *testing.T) {
	deep := strings.Repeat("(", 40) + "true" + strings.Repeat(")", 40)
	if _, err := CompileRule(deep); !errors.Is(err, ErrRuleSyntax) {
		t.Fatalf("40 nested parentheses: err = %v, want ErrRuleSyntax", err)
	}
	if _, err := CompileRule(strings.Repeat("!", 40) + "true"); !errors.Is(err, ErrRuleSyntax) {
		t.Fatalf("40 negations: err = %v, want ErrRuleSyntax", err)
	}
	if _, err := CompileRule(strings.Repeat("(", 10) + "true" + strings.Repeat(")", 10)); err != nil {
		t.Fatalf("10 nested parentheses: %v", err)
	}
	long := "service_days >= 30" + strings.Repeat(" ", MaxRuleLength)
	if _, err := CompileRule(long); !errors.Is(err, ErrRuleSyntax) {
		t.Fatalf("rule of %d characters: err = %v, want ErrRuleSyntax", len(long), err)
	}
}
