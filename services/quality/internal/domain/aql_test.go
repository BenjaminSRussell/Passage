package domain

import (
	"errors"
	"testing"

	"github.com/shopspring/decimal"
	"pgregory.net/rapid"
)

func dec(s string) decimal.Decimal { return decimal.RequireFromString(s) }

// TestDrawSample_AQLFixture is AC-33.2: lots 5..600000 at AQL 2.5, level II.
func TestDrawSample_AQLFixture(t *testing.T) {
	cases := []struct {
		lot    string
		n      string
		ac, re int
	}{
		{"5", "5", 0, 1},
		{"50", "8", 0, 1},
		{"51", "20", 1, 2},
		{"500", "50", 3, 4},
		{"501", "80", 5, 6},
		{"3200", "125", 7, 8},
		{"10001", "315", 14, 15},
		{"600000", "1250", 21, 22},
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

// TestDrawSample_AQLCells checks every excerpt cell of BR-33.1 for the three AQL columns.
func TestDrawSample_AQLCells(t *testing.T) {
	lots := map[string]string{"A": "5", "B": "9", "C": "16", "D": "26", "E": "51", "F": "91", "G": "151", "H": "281", "J": "501", "K": "1201", "L": "3201", "M": "10001", "N": "35001", "P": "150001", "Q": "500001"}
	// Expected n and Ac/Re per AQL column, from the BR-33.1 excerpt (arrows resolved).
	want := map[string][3][3]string{
		"A": {{"13", "0", "1"}, {"8", "0", "1"}, {"5", "0", "1"}},
		"B": {{"13", "0", "1"}, {"8", "0", "1"}, {"5", "0", "1"}},
		"C": {{"13", "0", "1"}, {"8", "0", "1"}, {"5", "0", "1"}},
		"D": {{"13", "0", "1"}, {"8", "0", "1"}, {"13", "1", "2"}},
		"E": {{"13", "0", "1"}, {"20", "1", "2"}, {"13", "1", "2"}},
		"F": {{"32", "1", "2"}, {"20", "1", "2"}, {"20", "2", "3"}},
		"G": {{"32", "1", "2"}, {"32", "2", "3"}, {"32", "3", "4"}},
		"H": {{"50", "2", "3"}, {"50", "3", "4"}, {"50", "5", "6"}},
		"J": {{"80", "3", "4"}, {"80", "5", "6"}, {"80", "7", "8"}},
		"K": {{"125", "5", "6"}, {"125", "7", "8"}, {"125", "10", "11"}},
		"L": {{"200", "7", "8"}, {"200", "10", "11"}, {"200", "14", "15"}},
		"M": {{"315", "10", "11"}, {"315", "14", "15"}, {"315", "21", "22"}},
		"N": {{"500", "14", "15"}, {"500", "21", "22"}, {"500", "21", "22"}},
		"P": {{"800", "21", "22"}, {"800", "21", "22"}, {"800", "21", "22"}},
		"Q": {{"1250", "21", "22"}, {"1250", "21", "22"}, {"1250", "21", "22"}},
	}
	aqls := []string{"1.0", "2.5", "4.0"}
	for letter, rows := range want {
		for col, aql := range aqls {
			got, err := DrawSample(SamplingPlan{Method: MethodAQL, AQL: dec(aql)}, dec(lots[letter]))
			if err != nil {
				t.Fatalf("%s/%s: %v", letter, aql, err)
			}
			w := rows[col]
			// A sample larger than its lot is 100 % inspection, so n is capped at the lot.
			wantN := dec(w[0])
			if lotQty := dec(lots[letter]); wantN.GreaterThan(lotQty) {
				wantN = lotQty
			}
			if !got.SampleQty.Equal(wantN) || got.AcceptNo != atoi(w[1]) || got.RejectNo != atoi(w[2]) {
				t.Errorf("letter %s lot %s AQL %s: got n=%s %d/%d, want n=%s %s/%s", letter, lots[letter], aql, got.SampleQty, got.AcceptNo, got.RejectNo, wantN, w[1], w[2])
			}
		}
	}
}

func atoi(s string) int { return int(dec(s).IntPart()) }

func TestDrawSample_Errors(t *testing.T) {
	cases := []struct {
		name string
		plan SamplingPlan
		lot  string
		code string
	}{
		{"zero lot", SamplingPlan{Method: MethodFixed, FixedQty: dec("1")}, "0", CodeValidation},
		{"non-whole AQL lot", SamplingPlan{Method: MethodAQL, AQL: dec("2.5")}, "10.5", CodeValidation},
		{"unloaded AQL column", SamplingPlan{Method: MethodAQL, AQL: dec("6.5")}, "100", CodeTableNotLoaded},
		{"tightened needs II-B", SamplingPlan{Method: MethodAQL, AQL: dec("2.5"), Severity: SeverityTightened}, "100", CodeTableNotLoaded},
		{"unloaded level", SamplingPlan{Method: MethodAQL, AQL: dec("2.5"), InspectionLevel: "I"}, "100", CodeTableNotLoaded},
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

// TestDrawSample_Property: the sample never exceeds the lot, and Ac < Re.
func TestDrawSample_Property(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		lot := rapid.Int64Range(2, 2000000).Draw(t, "lot")
		aql := []string{"1.0", "2.5", "4.0"}[rapid.IntRange(0, 2).Draw(t, "aql")]
		got, err := DrawSample(SamplingPlan{Method: MethodAQL, AQL: dec(aql)}, decimal.NewFromInt(lot))
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
