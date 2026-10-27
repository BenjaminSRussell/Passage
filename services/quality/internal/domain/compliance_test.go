package domain

import (
	"errors"
	"fmt"
	"testing"

	"github.com/shopspring/decimal"
	"pgregory.net/rapid"

	"github.com/BenjaminSRussell/passage/platform/ids"
)

// dehp is bis(2-ethylhexyl) phthalate, a REACH SVHC; lead and cadmium are RoHS substances.
const (
	dehp    = "117-81-7"
	lead    = "7439-92-1"
	cadmium = "7440-43-9"
)

func svhcList(kind, mode string) ComplianceList {
	return ComplianceList{
		Code: "SVHC", Version: 3, Kind: kind, BlockMode: mode,
		Regions: []string{"EU"},
		Limits:  []LimitLine{{CAS: dehp, ThresholdPct: dec("0.1")}},
	}
}

func TestRollupMassFractionAndEvaluation(t *testing.T) {
	// AC-33.10: a BOM yielding 0.12 % w/w of an SVHC is reported; 0.09 % is not.
	cases := []struct {
		name     string
		fraction string
		percent  string
		code     string
	}{
		{"0.12 % triggers the reporting warning", "0.0012", "0.12", CodeReportingHit},
		{"0.09 % triggers nothing", "0.0009", "0.09", ""},
		{"exactly the threshold is not over it", "0.001", "0.1", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			nodes := map[string]Constituent{
				"ART": {ItemID: "ART", Parts: []MassPart{{ItemID: "SUB", MassFraction: dec(c.fraction)}}},
				"SUB": {ItemID: "SUB", Direct: map[string]decimal.Decimal{dehp: dec("1")}},
			}
			conc, err := Rollup("ART", nodes)
			if err != nil {
				t.Fatal(err)
			}
			if !conc[dehp].Mul(dec("100")).Equal(dec(c.percent)) {
				t.Fatalf("concentration %s, want %s %%", conc[dehp], c.percent)
			}
			res := EvaluateList(svhcList(ListReporting, BlockModeWarn), "EU", conc)
			if res.Code != c.code {
				t.Errorf("code %q, want %q", res.Code, c.code)
			}
			if c.code != "" && (res.Severity != "warning" || len(res.Hits) != 1) {
				t.Errorf("result %+v", res)
			}
		})
	}
}

func TestRestrictedListBlockAndWarn(t *testing.T) {
	conc := map[string]decimal.Decimal{dehp: dec("0.002")}
	block := EvaluateList(svhcList(ListRestricted, BlockModeBlock), "EU", conc)
	if block.Code != CodeRestricted || block.Severity != "error" || !block.Restricted {
		t.Errorf("BLOCK: %+v", block)
	}
	warn := EvaluateList(svhcList(ListRestricted, BlockModeWarn), "EU", conc)
	if warn.Code != CodeRestricted || warn.Severity != "warning" || !warn.Restricted {
		t.Errorf("WARN: %+v", warn)
	}
	// The list does not apply to a US destination, so the same composition passes.
	us := EvaluateList(svhcList(ListRestricted, BlockModeBlock), "US", conc)
	if us.Applies || us.Code != "" || len(us.Hits) != 0 {
		t.Errorf("US: %+v", us)
	}
}

func TestRollupRecursesThroughLevels(t *testing.T) {
	nodes := map[string]Constituent{
		"FINAL": {ItemID: "FINAL", Parts: []MassPart{{ItemID: "MID", MassFraction: dec("0.5")}}},
		"MID":   {ItemID: "MID", Parts: []MassPart{{ItemID: "RAW", MassFraction: dec("0.5")}, {ItemID: "RAW2", MassFraction: dec("0.25")}}},
		"RAW":   {ItemID: "RAW", Direct: map[string]decimal.Decimal{"S": dec("0.2")}},
		"RAW2":  {ItemID: "RAW2", Direct: map[string]decimal.Decimal{"S": dec("0.4")}},
	}
	conc, err := Rollup("FINAL", nodes)
	if err != nil {
		t.Fatal(err)
	}
	// 0.5 × (0.5 × 0.2 + 0.25 × 0.4) = 0.5 × 0.2 = 0.1
	if !conc["S"].Equal(dec("0.1")) {
		t.Errorf("roll-up = %s, want 0.1", conc["S"])
	}
}

func TestRollupRejectsBadBOMs(t *testing.T) {
	cycle := map[string]Constituent{
		"A": {ItemID: "A", Parts: []MassPart{{ItemID: "B", MassFraction: dec("0.5")}}},
		"B": {ItemID: "B", Parts: []MassPart{{ItemID: "A", MassFraction: dec("0.5")}}},
	}
	over := map[string]Constituent{
		"A": {ItemID: "A", Parts: []MassPart{{ItemID: "B", MassFraction: dec("0.7")}, {ItemID: "C", MassFraction: dec("0.6")}}},
		"B": {ItemID: "B"},
		"C": {ItemID: "C"},
	}
	deep := map[string]Constituent{}
	for i := 0; i < 25; i++ {
		id := fmt.Sprintf("N%02d", i)
		deep[id] = Constituent{ItemID: id, Parts: []MassPart{{ItemID: fmt.Sprintf("N%02d", i+1), MassFraction: dec("1")}}}
	}
	deep["N25"] = Constituent{ItemID: "N25"}
	cases := []struct {
		name  string
		nodes map[string]Constituent
		root  string
	}{
		{"cycle", cycle, "A"},
		{"fractions above one", over, "A"},
		{"missing item", cycle, "Z"},
		{"too deep", deep, "N00"},
	}
	for _, c := range cases {
		if _, err := Rollup(c.root, c.nodes); err == nil {
			t.Errorf("%s: expected an error", c.name)
		}
	}
}

func TestValidCAS(t *testing.T) {
	for cas, want := range map[string]bool{
		lead: true, cadmium: true, dehp: true, "50-00-0": true, // formaldehyde
		"7439-92-2": false, // wrong check digit
		"117-81":    false, "1-23-4": false, "12345678-12-3": false, "SVHC-1": false, "": false,
	} {
		if got := ValidCAS(cas); got != want {
			t.Errorf("ValidCAS(%q) = %v, want %v", cas, got, want)
		}
	}
}

func TestValidateComplianceList(t *testing.T) {
	if f := ValidateComplianceList(svhcList(ListRestricted, BlockModeBlock)); len(f) != 0 {
		t.Errorf("valid list: %v", f)
	}
	bad := ComplianceList{Code: "svhc list", Kind: "OTHER", BlockMode: "X", Regions: []string{"EU", "EU", "europe"},
		Limits: []LimitLine{{CAS: "7439-92-2", ThresholdPct: dec("101")}, {CAS: lead, ThresholdPct: dec("0.1")}, {CAS: lead, ThresholdPct: dec("0")}}}
	f := ValidateComplianceList(bad)
	for _, k := range []string{"code", "kind", "blockMode", "regions[1]", "regions[2]", "limits[0].cas", "limits[0].thresholdPct", "limits[2].cas", "limits[2].thresholdPct"} {
		if f[k] == "" {
			t.Errorf("missing field error %s in %v", k, f)
		}
	}
	if f["version"] != "" || f["limits[1].cas"] != "" {
		t.Errorf("the version is the store's and a first CAS is valid: %v", f)
	}
	if f := ValidateComplianceList(ComplianceList{Code: "RO", Kind: ListRestricted, BlockMode: BlockModeWarn}); f["regions"] == "" || f["limits"] == "" {
		t.Errorf("empty regions and limits: %v", f)
	}
}

func TestLatestListsKeepsHighestVersionByCode(t *testing.T) {
	all := []ComplianceList{{Code: "ROHS", Version: 1}, {Code: "SVHC", Version: 2}, {Code: "ROHS", Version: 3}, {Code: "SVHC", Version: 1}}
	got := LatestLists(all)
	if len(got) != 2 || got[0].Code != "ROHS" || got[0].Version != 3 || got[1].Code != "SVHC" || got[1].Version != 2 {
		t.Fatalf("latest = %+v", got)
	}
}

func TestValidateConstituent(t *testing.T) {
	self, other := ids.New(), ids.New()
	tests := []struct {
		name    string
		in      Constituent
		wantKey string
	}{
		{"nothing declared", Constituent{ItemID: self}, "direct"},
		{"fraction in range", Constituent{ItemID: self, Direct: map[string]decimal.Decimal{dehp: dec("0.0024")}}, ""},
		{"negative fraction", Constituent{ItemID: self, Direct: map[string]decimal.Decimal{dehp: dec("-0.1")}}, "direct." + dehp},
		{"fraction above one", Constituent{ItemID: self, Direct: map[string]decimal.Decimal{dehp: dec("1.5")}}, "direct." + dehp},
		{"substances above one together", Constituent{ItemID: self, Direct: map[string]decimal.Decimal{dehp: dec("0.6"), lead: dec("0.5")}}, "direct"},
		{"substance not a CAS number", Constituent{ItemID: self, Direct: map[string]decimal.Decimal{"lead": dec("0.1")}}, "direct.lead"},
		{"part not a UUID", Constituent{ItemID: self, Parts: []MassPart{{ItemID: "a", MassFraction: dec("0.5")}}}, "parts[0].itemId"},
		{"part is the item itself", Constituent{ItemID: self, Parts: []MassPart{{ItemID: self, MassFraction: dec("0.5")}}}, "parts[0].itemId"},
		{"part listed twice", Constituent{ItemID: self, Parts: []MassPart{{ItemID: other, MassFraction: dec("0.2")}, {ItemID: other, MassFraction: dec("0.2")}}}, "parts[1].itemId"},
		{"part fraction zero", Constituent{ItemID: self, Parts: []MassPart{{ItemID: other, MassFraction: dec("0")}}}, "parts[0].massFraction"},
		{"parts sum above one", Constituent{ItemID: self, Parts: []MassPart{{ItemID: other, MassFraction: dec("0.7")}, {ItemID: ids.New(), MassFraction: dec("0.4")}}}, "parts"},
		{"parts sum exactly one", Constituent{ItemID: self, Parts: []MassPart{{ItemID: other, MassFraction: dec("0.6")}, {ItemID: ids.New(), MassFraction: dec("0.4")}}}, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fields := ValidateConstituent(tc.in)
			if tc.wantKey == "" {
				if len(fields) != 0 {
					t.Fatalf("want valid, got %v", fields)
				}
				return
			}
			if _, ok := fields[tc.wantKey]; !ok {
				t.Fatalf("want field %q in %v", tc.wantKey, fields)
			}
		})
	}
}

// AC-33.10 through EvaluateItem: an article of which 5 % by mass is a plasticiser
// carrying 2.4 % DEHP holds 0.12 % w/w and is a REACH Art. 33 reporting hit in the
// EU only; at 1.8 % DEHP (0.09 % w/w) nothing is reported.
func TestEvaluateItemReportingThresholdAndRegion(t *testing.T) {
	reach := ComplianceList{Code: "REACH-SVHC", Version: 1, Kind: ListReporting, BlockMode: BlockModeWarn, Regions: []string{"EU"},
		Limits: []LimitLine{{CAS: dehp, ThresholdPct: dec("0.1")}}}
	rohs := ComplianceList{Code: "ROHS", Version: 2, Kind: ListRestricted, BlockMode: BlockModeBlock, Regions: []string{"EU", "CN"},
		Limits: []LimitLine{{CAS: lead, ThresholdPct: dec("0.1")}, {CAS: cadmium, ThresholdPct: dec("0.01")}}}
	for _, tc := range []struct {
		dehpInPart string
		wantCode   string
	}{{"0.024", CodeReportingHit}, {"0.018", ""}} {
		nodes := map[string]Constituent{
			"ART":  {ItemID: "ART", Parts: []MassPart{{ItemID: "PLAS", MassFraction: dec("0.05")}}},
			"PLAS": {ItemID: "PLAS", Direct: map[string]decimal.Decimal{dehp: dec(tc.dehpInPart)}},
		}
		res, err := EvaluateItem("ART", nodes, []ComplianceList{reach, rohs}, "EU")
		if err != nil || len(res) != 2 {
			t.Fatalf("EU: %+v %v", res, err)
		}
		if res[0].Code != tc.wantCode || res[1].Code != "" || res[1].Restricted {
			t.Errorf("DEHP %s in part: %+v", tc.dehpInPart, res)
		}
		us, err := EvaluateItem("ART", nodes, []ComplianceList{reach, rohs}, "US")
		if err != nil || len(us) != 0 {
			t.Errorf("no list applies in the US: %+v %v", us, err)
		}
	}
	if _, err := EvaluateItem("ART", map[string]Constituent{"ART": {ItemID: "ART"}}, nil, "eu"); err == nil {
		t.Error("a lower-case region must be refused")
	}
}

func TestRollupCycleAndDepthHaveTheirCodes(t *testing.T) {
	cycle := map[string]Constituent{
		"A": {ItemID: "A", Parts: []MassPart{{ItemID: "B", MassFraction: dec("0.5")}}},
		"B": {ItemID: "B", Parts: []MassPart{{ItemID: "A", MassFraction: dec("0.5")}}},
	}
	_, err := Rollup("A", cycle)
	var de *Error
	if !errors.As(err, &de) || de.Code != CodeCompositionCycle {
		t.Errorf("cycle: %v, want %s", err, CodeCompositionCycle)
	}
}

func TestComplianceResultSameOutcome(t *testing.T) {
	a := ComplianceResult{Restricted: true, Code: CodeRestricted, Severity: SeverityError, Hits: []Hit{{CAS: lead, PercentWW: dec("0.20"), ThresholdPct: dec("0.1")}}}
	b := a
	b.Hits = []Hit{{CAS: lead, PercentWW: dec("0.2"), ThresholdPct: dec("0.1")}}
	if !a.SameOutcome(b) {
		t.Error("equal decimals with different scale are the same outcome")
	}
	b.Hits = []Hit{{CAS: lead, PercentWW: dec("0.21"), ThresholdPct: dec("0.1")}}
	if a.SameOutcome(b) {
		t.Error("a changed concentration is a new outcome")
	}
	if a.SameOutcome(ComplianceResult{Hits: []Hit{}}) {
		t.Error("a cleared hit is a new outcome")
	}
}

func TestEvaluateListPropertyStrictThreshold(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		// Concentrations at 6 decimals, thresholds at 3 decimals (percent w/w).
		c := rapid.Int64Range(0, 1000000).Draw(t, "c")
		th := rapid.Int64Range(1, 9999).Draw(t, "threshold")
		conc := decimal.New(c, -6)
		thresh := decimal.New(th, -3)
		list := ComplianceList{Code: "L", Version: 1, Kind: ListRestricted, BlockMode: BlockModeBlock,
			Regions: []string{"EU"}, Limits: []LimitLine{{CAS: lead, ThresholdPct: thresh}}}
		res := EvaluateList(list, "EU", map[string]decimal.Decimal{lead: conc})
		over := conc.Mul(decimal.NewFromInt(100)).GreaterThan(thresh)
		if res.Restricted != over || (len(res.Hits) == 1) != over {
			t.Fatalf("c=%s threshold=%s over=%v result=%+v", conc, thresh, over, res)
		}
	})
}
