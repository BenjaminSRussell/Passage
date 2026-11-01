package domain

import (
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"
)

func ruleLine(account, amount string, dims map[string]string) LineInput {
	return LineInput{Account: account, Side: SideDebit, Amount: decimal.RequireFromString(amount), Dimensions: dims}
}

// AC-11.14: accounts 6000-6999 require DEPT when the amount is above 1,000.
func acRule() ValidationRule {
	return ValidationRule{ID: "r1", Sequence: 10, Name: "DEPT on expenses", Level: RuleLevelLine, Severity: SeverityError, Active: true,
		Message: "Department is required on large expenses",
		Prerequisite: []RuleCondition{
			{Field: FieldAccount, Op: CondBetween, Values: []string{"6000", "6999"}},
			{Field: FieldAmount, Op: CondGt, Values: []string{"1000"}}},
		Checks: []RuleCondition{{Field: "DEPT", Op: CondNotBlank}}}
}

func TestEvaluateRules_AC11_14(t *testing.T) {
	lines := []LineInput{
		ruleLine("6100", "1500.00", nil),                             // fails: large, no DEPT
		ruleLine("6100", "500.00", nil),                              // small: passes
		ruleLine("6100", "1500.00", map[string]string{"DEPT": "D1"}), // has DEPT
		ruleLine("7000", "5000.00", nil),                             // other account
		ruleLine("1010", "10.00", nil),
	}
	out := EvaluateRules([]ValidationRule{acRule()}, RuleHeader{PostingDate: time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)}, nil, lines)
	if len(out.Errors) != 1 || out.Errors[0].LineIndex == nil || *out.Errors[0].LineIndex != 0 || out.Errors[0].Code != CodeValidationRuleFail {
		t.Fatalf("want one error on line 0, got %+v", out)
	}
	if !strings.Contains(out.Errors[0].Message, "rule r1") || len(out.Warnings) != 0 {
		t.Fatalf("message %q warnings %v", out.Errors[0].Message, out.Warnings)
	}
}

func TestEvaluateRules_WarningInactiveAndHeader(t *testing.T) {
	warn := acRule()
	warn.ID, warn.Severity = "w", SeverityWarning
	off := acRule()
	off.ID, off.Active = "off", false
	doc := time.Date(2026, 3, 10, 0, 0, 0, 0, time.UTC)
	header := ValidationRule{ID: "h", Sequence: 20, Name: "Document date", Level: RuleLevelHeader, Severity: SeverityError, Active: true,
		Message:      "Document date must not be after the posting date",
		Prerequisite: []RuleCondition{{Field: FieldDocumentDate, Op: CondNotBlank}},
		Checks:       []RuleCondition{{Field: FieldDocumentDate, Op: CondLte, Values: []string{"2026-03-05"}}}}
	count := ValidationRule{ID: "c", Name: "Few lines", Level: RuleLevelHeader, Severity: SeverityWarning, Active: true, Message: "x",
		Checks: []RuleCondition{{Field: FieldLineCount, Op: CondLt, Values: []string{"2"}}}}
	lines := []LineInput{ruleLine("6100", "1500.00", nil), ruleLine("1010", "1500.00", nil)}
	out := EvaluateRules([]ValidationRule{warn, off, header, count},
		RuleHeader{PostingDate: time.Date(2026, 3, 5, 0, 0, 0, 0, time.UTC), DocumentDate: &doc}, nil, lines)
	if len(out.Errors) != 1 || out.Errors[0].LineIndex != nil || !strings.Contains(out.Errors[0].Message, "rule h") {
		t.Fatalf("errors: %+v", out.Errors)
	}
	if len(out.Warnings) != 2 {
		t.Fatalf("warnings: %+v", out.Warnings)
	}
	// Without a document date the prerequisite does not hold.
	out = EvaluateRules([]ValidationRule{header}, RuleHeader{PostingDate: doc}, nil, lines)
	if len(out.Errors) != 0 {
		t.Fatalf("prerequisite should skip: %+v", out.Errors)
	}
}

func TestEvaluateRules_AccountTypeSourceAndTotal(t *testing.T) {
	r := ValidationRule{ID: "t", Level: RuleLevelLine, Severity: SeverityError, Active: true, Message: "m",
		Prerequisite: []RuleCondition{{Field: FieldSourceType, Op: CondEq, Values: []string{"manual_journal"}},
			{Field: FieldAccountType, Op: CondIn, Values: []string{"expense", "revenue"}}},
		Checks: []RuleCondition{{Field: FieldText, Op: CondNotBlank}}}
	accounts := map[string]Account{"6100": {Number: "6100", Type: "expense"}, "1010": {Number: "1010", Type: "asset"}}
	lines := []LineInput{ruleLine("6100", "1.00", nil), ruleLine("1010", "1.00", nil)}
	out := EvaluateRules([]ValidationRule{r}, RuleHeader{SourceType: "manual_journal"}, accounts, lines)
	if len(out.Errors) != 1 || *out.Errors[0].LineIndex != 0 {
		t.Fatalf("%+v", out.Errors)
	}
	if out := EvaluateRules([]ValidationRule{r}, RuleHeader{SourceType: "ap_invoice"}, accounts, lines); len(out.Errors) != 0 {
		t.Fatalf("other source: %+v", out.Errors)
	}
	total := ValidationRule{ID: "d", Level: RuleLevelHeader, Severity: SeverityError, Active: true, Message: "big",
		Checks: []RuleCondition{{Field: FieldTotalDebit, Op: CondLte, Values: []string{"1.50"}}}}
	if out := EvaluateRules([]ValidationRule{total}, RuleHeader{}, accounts, []LineInput{ruleLine("6100", "1.00", nil), ruleLine("6100", "1.00", nil)}); len(out.Errors) != 1 {
		t.Fatalf("total debit 2.00 over 1.50: %+v", out.Errors)
	}
}

func TestRuleConditionHolds(t *testing.T) {
	cases := []struct {
		c    RuleCondition
		v    string
		want bool
	}{
		{RuleCondition{Op: CondGt, Values: []string{"1000"}}, "1000.01", true},
		{RuleCondition{Op: CondGt, Values: []string{"1000"}}, "1000", false},
		{RuleCondition{Op: CondGte, Values: []string{"1000"}}, "1000.00", true},
		{RuleCondition{Op: CondLt, Values: []string{"9"}}, "10", false}, // numeric, not text
		{RuleCondition{Op: CondBetween, Values: []string{"6000", "6999"}}, "6999", true},
		{RuleCondition{Op: CondBetween, Values: []string{"6000", "6999"}}, "7000", false},
		{RuleCondition{Op: CondLte, Values: []string{"2026-03-05"}}, "2026-03-05", true},
		{RuleCondition{Op: CondGt, Values: []string{"1"}}, "", false},
		{RuleCondition{Op: CondNe, Values: []string{"A"}}, "", true},
		{RuleCondition{Op: CondBlank}, "", true},
		{RuleCondition{Op: "zz"}, "1", false},
		// Numeric fields compare eq, ne and in as numbers: a 1500.00 line equals 1500.
		{RuleCondition{Field: FieldAmount, Op: CondEq, Values: []string{"1500"}}, "1500.00", true},
		{RuleCondition{Field: FieldAmount, Op: CondNe, Values: []string{"1500"}}, "1500.00", false},
		{RuleCondition{Field: FieldAmount, Op: CondIn, Values: []string{"10", "1500.0"}}, "1500", true},
		{RuleCondition{Field: FieldLineCount, Op: CondIn, Values: []string{"3"}}, "2", false},
		// Text fields keep exact matching.
		{RuleCondition{Field: FieldText, Op: CondEq, Values: []string{"1500"}}, "1500.00", false},
	}
	for i, c := range cases {
		if got := RuleConditionHolds(c.c, c.v); got != c.want {
			t.Errorf("case %d (%s %v on %q): got %v", i, c.c.Op, c.c.Values, c.v, got)
		}
	}
}

func TestValidateRule(t *testing.T) {
	attrs := []string{"DEPT"}
	if p := ValidateRule(acRule(), attrs); len(p) != 0 {
		t.Fatalf("valid rule refused: %v", p)
	}
	bad := ValidationRule{Name: "", Level: "doc", Severity: "fatal", Sequence: 0, Message: "",
		Checks:       []RuleCondition{{Field: "NOPE", Op: CondEq, Values: []string{"a"}}},
		Prerequisite: []RuleCondition{{Field: FieldAmount, Op: CondGt, Values: []string{"abc"}}}}
	p := ValidateRule(bad, attrs)
	for _, k := range []string{"name", "level", "severity", "sequence", "message", "checks[0]", "prerequisite[0]"} {
		if p[k] == "" {
			t.Errorf("missing problem for %s: %v", k, p)
		}
	}
	h := acRule()
	h.Level = RuleLevelHeader
	if p := ValidateRule(h, attrs); p["prerequisite[0]"] == "" || p["checks[0]"] == "" {
		t.Fatalf("header rule reading line fields: %v", p)
	}
	arity := acRule()
	arity.Checks = []RuleCondition{{Field: "DEPT", Op: CondBetween, Values: []string{"1"}}, {Field: "DEPT", Op: CondBlank, Values: []string{"x"}},
		{Field: "DEPT", Op: CondIn}}
	p = ValidateRule(arity, attrs)
	if p["checks[0]"] == "" || p["checks[1]"] == "" || p["checks[2]"] == "" {
		t.Fatalf("arity: %v", p)
	}
	dates := ValidationRule{Name: "d", Level: RuleLevelHeader, Severity: SeverityError, Sequence: 1, Message: "m",
		Checks: []RuleCondition{{Field: FieldPostingDate, Op: CondLte, Values: []string{"2026-3-5"}},
			{Field: FieldDocumentDate, Op: CondBetween, Values: []string{"2026-01-01", "2026-12-31"}}}}
	if p := ValidateRule(dates, nil); p["checks[0]"] == "" || p["checks[1]"] != "" {
		t.Fatalf("date values must be YYYY-MM-DD: %v", p)
	}
	none := acRule()
	none.Checks = nil
	if ValidateRule(none, attrs)["checks"] == "" {
		t.Fatal("a rule needs a check")
	}
}
