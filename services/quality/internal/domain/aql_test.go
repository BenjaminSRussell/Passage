package domain

import (
	"errors"
	"testing"

	"github.com/shopspring/decimal"
	"pgregory.net/rapid"
)

func dec(s string) decimal.Decimal { return decimal.RequireFromString(s) }

// TestDrawSample_AQLFixture is AC-33.2: lots 5..600000 at AQL 2.5, level II.
// Two of the PRD's expected cells disagree with Z1.4 Table II-A and follow the
// table here: lot 50 is code D, whose AQL 2.5 cell points up to C (n = 5, not
// 8), and lot 600,000 is code Q, whose cell points up to N (n = 500, not 1,250).
func TestDrawSample_AQLFixture(t *testing.T) {
	cases := []struct {
		lot    string
		n      string
		ac, re int
	}{
		{"5", "5", 0, 1},
		{"50", "5", 0, 1},
		{"51", "20", 1, 2},
		{"500", "50", 3, 4},
		{"501", "80", 5, 6},
		{"3200", "125", 7, 8},
		{"10001", "315", 14, 15},
		{"600000", "500", 21, 22},
	}
	for _, c := range cases {
		got, err := DrawSample(SamplingPlan{Method: MethodAQL, AQL: dec("2.5")}, dec(c.lot))
		if err != nil {
			t.Fatalf("lot %s: %v", c.lot, err)
		}
		if !got.SampleQty.Equal(dec(c.n)) || got.AcceptNo != c.ac || got.RejectNo != c.re {
			t.Errorf("lot %s: got n=%s ac=%d re=%d, want n=%s ac=%d re=%d", c.lot, got.SampleQty, got.AcceptNo, got.RejectNo, c.n, c.ac, c.re)
		}
	}
}

func TestDrawSample_Errors(t *testing.T) {
	cases := []struct {
		name string
		plan SamplingPlan
		lot  string
		code string
	}{
		{"zero lot", SamplingPlan{Method: MethodFixed, FixedQty: dec("1")}, "0", CodeValidation},
		{"non-whole AQL lot", SamplingPlan{Method: MethodAQL, AQL: dec("2.5")}, "10.5", CodeValidation},
		{"AQL that is no Z1.4 column", SamplingPlan{Method: MethodAQL, AQL: dec("7.0")}, "100", CodeTableNotLoaded},
		{"unknown severity", SamplingPlan{Method: MethodAQL, AQL: dec("2.5"), Severity: "STRICT"}, "100", CodeValidation},
		{"unknown level", SamplingPlan{Method: MethodAQL, AQL: dec("2.5"), InspectionLevel: "IV"}, "100", CodeValidation},
		{"unknown method", SamplingPlan{Method: "RANDOM"}, "100", CodeValidation},
		{"percent over 100", SamplingPlan{Method: MethodPercent, Percent: dec("101")}, "100", CodeValidation},
		{"AQL between columns", SamplingPlan{Method: MethodAQL, AQL: dec("2.45")}, "100", CodeTableNotLoaded},
	}
	for _, c := range cases {
		_, err := DrawSample(c.plan, dec(c.lot))
		var de *Error
		if !errors.As(err, &de) || de.Code != c.code {
			t.Errorf("%s: got %v, want code %s", c.name, err, c.code)
		}
	}
}

func TestDrawSample_FixedAndPercent(t *testing.T) {
	got, err := DrawSample(SamplingPlan{Method: MethodFixed, FixedQty: dec("10")}, dec("4"))
	if err != nil || !got.SampleQty.Equal(dec("4")) {
		t.Fatalf("fixed capped at lot: %v %v", got, err)
	}
	got, err = DrawSample(SamplingPlan{Method: MethodPercent, Percent: dec("10")}, dec("101"))
	if err != nil || !got.SampleQty.Equal(dec("11")) {
		t.Fatalf("percent rounds up: %v %v", got, err)
	}
}

// TestDrawSample_Property: at every level, AQL and severity the sample never
// exceeds the lot, and Ac < Re.
func TestDrawSample_Property(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		lot := rapid.Int64Range(2, 2000000).Draw(t, "lot")
		aql := rapid.SampledFrom(AQLColumns).Draw(t, "aql")
		level := rapid.SampledFrom(InspectionLevels).Draw(t, "level")
		sev := rapid.SampledFrom([]Severity{SeverityNormal, SeverityTightened, SeverityReduced}).Draw(t, "severity")
		got, err := DrawSample(SamplingPlan{Method: MethodAQL, AQL: dec(aql), InspectionLevel: level, Severity: sev}, decimal.NewFromInt(lot))
		if err != nil {
			t.Fatal(err)
		}
		if got.SampleQty.GreaterThan(decimal.NewFromInt(lot)) || got.SampleQty.LessThan(decimal.NewFromInt(1)) {
			t.Fatalf("sample %s outside 1..%d", got.SampleQty, lot)
		}
		if got.AcceptNo >= got.RejectNo {
			t.Fatalf("ac %d must be below re %d", got.AcceptNo, got.RejectNo)
		}
	})
}

func TestValidatePlan(t *testing.T) {
	ok := []SamplingPlan{
		{Method: MethodFixed, FixedQty: dec("5")},
		{Method: MethodPercent, Percent: dec("12.5")},
		{Method: MethodAQL, AQL: dec("4.0"), Severity: SeverityNormal, InspectionLevel: "II"},
		{Method: MethodFixed, FixedQty: dec("5"), Severity: SeverityTightened},
	}
	for _, p := range ok {
		if f := ValidatePlan("plan", p); len(f) != 0 {
			t.Errorf("%+v: %v", p, f)
		}
	}
	bad := map[string]SamplingPlan{
		"plan.fixedQty":        {Method: MethodFixed, FixedQty: dec("2.5")},
		"plan.percent":         {Method: MethodPercent},
		"plan.aql":             {Method: MethodAQL, AQL: dec("0.65")},
		"plan.severity":        {Method: MethodAQL, AQL: dec("2.5"), Severity: SeverityReduced},
		"plan.inspectionLevel": {Method: MethodAQL, AQL: dec("2.5"), InspectionLevel: "S-1"},
		"plan.method":          {Method: "RANDOM"},
	}
	for field, p := range bad {
		if f := ValidatePlan("plan", p); f[field] == "" {
			t.Errorf("%s: want an error, got %v", field, f)
		}
	}
	if n := (SamplingPlan{Method: MethodAQL}).Normalized(); n.Severity != SeverityNormal || n.InspectionLevel != InspectionLevelII {
		t.Errorf("defaults: %+v", n)
	}
}
