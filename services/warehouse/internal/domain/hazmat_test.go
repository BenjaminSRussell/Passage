package domain

import (
	"strings"
	"testing"

	"github.com/shopspring/decimal"
)

func hazCompliance(t *testing.T, rel HazmatRel, limits map[string]decimal.Decimal) Compliance {
	t.Helper()
	profiles := []HazmatProfile{
		{ItemID: "SOLV-3", StorageClass: "3", Class: "3", UNNumber: "UN1263"},
		{ItemID: "OX-51", StorageClass: "5.1", Class: "5.1", UNNumber: "UN1479"},
	}
	c, err := NewCompliance(profiles, []SegregationPair{{A: "3", B: "5.1", Rel: rel}}, limits)
	if err != nil {
		t.Fatalf("NewCompliance: %v", err)
	}
	return c
}

// hazLoc is a location in a fire-containment section. A nil stock map is empty.
func hazLoc(name, section, rack string, pick int, stock map[string]decimal.Decimal, weight string) Location {
	l := loc("id-"+name, name, "HAZ", pick)
	l.FireSection = section
	l.RackGroup = rack
	l.Stock = stock
	if len(stock) > 0 {
		l.Used.LPs = 1
	}
	if weight != "" {
		l.Used.Weight = dec(weight)
	}
	return l
}

func hazLine(item, qty, weight string) Line {
	return Line{ItemID: item, WorkType: "PURCHASE_PUTAWAY", Direction: DirectionPut, Warehouse: "WH1",
		Qty: dec(qty), Weight: dec(weight), Volume: dec("0")}
}

func TestEvaluateWithCompliance_SegregationAndLimits(t *testing.T) {
	oxen := map[string]decimal.Decimal{"OX-51": dec("10")}
	paint := map[string]decimal.Decimal{"PAINT": dec("1")}
	tests := []struct {
		name      string
		rel       HazmatRel
		limits    map[string]decimal.Decimal
		line      Line
		locations []Location
		wantLoc   string // empty = blocked
		wantWhy   string // substring the trace must contain
	}{
		{
			// AC-31.14: FCS-02 holds 5.1 oxidizers, so a class 3 PUT skips it and the trace says why.
			name: "prohibited pair excludes the whole section (AC-31.14)",
			rel:  RelProhibited,
			line: hazLine("SOLV-3", "10", "20"),
			locations: []Location{
				hazLoc("H-01-01-01", "FCS-02", "RK-1", 1, oxen, "50"),
				hazLoc("H-01-01-02", "FCS-02", "RK-2", 2, nil, ""),
				hazLoc("H-03-01-01", "FCS-01", "RK-3", 30, nil, ""),
			},
			wantLoc: "H-03-01-01",
			wantWhy: "hazmat: class 3 vs 5.1 prohibited in FCS-02",
		},
		{
			name: "separate pair refuses only the rack that holds the other class",
			rel:  RelSeparate,
			line: hazLine("SOLV-3", "10", "20"),
			locations: []Location{
				hazLoc("H-01-01-01", "FCS-02", "RK-1", 1, oxen, "50"),
				hazLoc("H-01-02-01", "FCS-02", "RK-1", 2, nil, ""),
				hazLoc("H-02-01-01", "FCS-02", "RK-2", 3, nil, ""),
			},
			wantLoc: "H-02-01-01",
			wantWhy: "hazmat: class 3 vs 5.1 separate in rack RK-1",
		},
		{
			name: "allowed pair does not restrict the section",
			rel:  RelAllowed,
			line: hazLine("SOLV-3", "10", "20"),
			locations: []Location{
				hazLoc("H-01-01-01", "FCS-02", "RK-1", 1, oxen, "50"),
				hazLoc("H-01-01-02", "FCS-02", "RK-1", 2, nil, ""),
			},
			wantLoc: "H-01-01-02",
		},
		{
			name:   "section limit is exceeded by the line",
			rel:    RelAllowed,
			limits: map[string]decimal.Decimal{"FCS-02": dec("1000")},
			line:   hazLine("SOLV-3", "10", "20"),
			locations: []Location{
				hazLoc("H-01-01-01", "FCS-02", "RK-1", 1, oxen, "990"),
				hazLoc("H-01-01-02", "FCS-02", "RK-1", 2, nil, ""),
				hazLoc("H-03-01-01", "FCS-01", "RK-3", 3, nil, ""),
			},
			wantLoc: "H-03-01-01",
			wantWhy: "hazmat: section FCS-02 limit 1000 kg exceeded (1010/1000)",
		},
		{
			// BR-31.15 sums hazmat quantity only: 990 kg of unprofiled paint leaves room.
			name:   "non-hazmat weight does not count toward the section limit",
			rel:    RelAllowed,
			limits: map[string]decimal.Decimal{"FCS-02": dec("1000")},
			line:   hazLine("SOLV-3", "10", "20"),
			locations: []Location{
				hazLoc("H-01-01-01", "FCS-02", "RK-1", 1, paint, "990"),
				hazLoc("H-01-01-02", "FCS-02", "RK-1", 2, nil, ""),
			},
			wantLoc: "H-01-01-02",
		},
		{
			name:   "section limit is met exactly and admits the line",
			rel:    RelAllowed,
			limits: map[string]decimal.Decimal{"FCS-02": dec("1000")},
			line:   hazLine("SOLV-3", "10", "10"),
			locations: []Location{
				hazLoc("H-01-01-01", "FCS-02", "RK-1", 1, oxen, "990"),
				hazLoc("H-01-01-02", "FCS-02", "RK-1", 2, nil, ""),
			},
			wantLoc: "H-01-01-02",
		},
		{
			name: "location allowed-classes list refuses other classes",
			rel:  RelAllowed,
			line: hazLine("OX-51", "10", "5"),
			locations: []Location{
				func() Location {
					l := hazLoc("H-01-01-01", "FCS-01", "RK-1", 1, nil, "")
					l.AllowedStorageClasses = []string{"3"}
					return l
				}(),
				hazLoc("H-01-01-02", "FCS-01", "RK-1", 2, nil, ""),
			},
			wantLoc: "H-01-01-02",
			wantWhy: "hazmat: class 5.1 not allowed at H-01-01-01",
		},
		{
			name: "non-hazmat item ignores segregation",
			rel:  RelProhibited,
			line: hazLine("WASHER", "10", "20"),
			locations: []Location{
				hazLoc("H-01-01-01", "FCS-02", "RK-1", 1, oxen, "50"),
				hazLoc("H-01-01-02", "FCS-02", "RK-1", 2, nil, ""),
			},
			wantLoc: "H-01-01-02",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			comp := hazCompliance(t, tc.rel, tc.limits)
			got := EvaluateWithCompliance(demoDirectives(), tc.line, tc.locations, comp)
			if tc.wantLoc == "" {
				if !got.Blocked || got.Code != CodeNoLocation {
					t.Fatalf("want blocked, got %+v", got)
				}
			} else if got.Location == nil || got.Location.Name != tc.wantLoc {
				t.Fatalf("want %s, got %+v", tc.wantLoc, got)
			}
			if tc.wantWhy == "" {
				return
			}
			var found bool
			for _, s := range got.Trace {
				found = found || strings.Contains(s.Reason, tc.wantWhy)
			}
			if !found {
				t.Errorf("trace lacks %q: %+v", tc.wantWhy, got.Trace)
			}
		})
	}
}

func TestCompliance_PickAndZeroValueAreUnchecked(t *testing.T) {
	comp := hazCompliance(t, RelProhibited, nil)
	locs := []Location{hazLoc("H-01-01-01", "FCS-02", "RK-1", 1, map[string]decimal.Decimal{"OX-51": dec("1")}, "5")}
	secs := comp.sectionsOf(locs)
	pick := hazLine("SOLV-3", "1", "1")
	pick.Direction = DirectionPick
	if reason, ok := comp.admitHazmat(pick, locs[0], secs); !ok {
		t.Errorf("PICK must not be segregation-checked, got %q", reason)
	}
	var zero Compliance
	if reason, ok := zero.admitHazmat(hazLine("SOLV-3", "1", "1"), locs[0], nil); !ok {
		t.Errorf("zero compliance must admit, got %q", reason)
	}
	if zero.sectionsOf(locs) != nil {
		t.Error("zero compliance must not build a section view")
	}
}

func TestNewCompliance_RejectsBadReferenceData(t *testing.T) {
	good := HazmatProfile{ItemID: "SOLV-3", StorageClass: "3"}
	tests := []struct {
		name     string
		profiles []HazmatProfile
		pairs    []SegregationPair
		limits   map[string]decimal.Decimal
	}{
		{"duplicate profile", []HazmatProfile{good, good}, nil, nil},
		{"missing storage class", []HazmatProfile{{ItemID: "X"}}, nil, nil},
		{"bad UN number", []HazmatProfile{{ItemID: "X", StorageClass: "3", UNNumber: "1263"}}, nil, nil},
		{"unknown relation", nil, []SegregationPair{{A: "3", B: "5.1", Rel: "maybe"}}, nil},
		{"conflicting relations", nil, []SegregationPair{{A: "3", B: "5.1", Rel: RelSeparate}, {A: "5.1", B: "3", Rel: RelProhibited}}, nil},
		{"pair without class", nil, []SegregationPair{{A: "3", Rel: RelProhibited}}, nil},
		{"negative section limit", nil, nil, map[string]decimal.Decimal{"FCS-02": dec("-1")}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := NewCompliance(tc.profiles, tc.pairs, tc.limits); err == nil {
				t.Fatal("want an error")
			}
		})
	}
}

func TestNewCompliance_SymmetricMatrixAndRepeatedPairs(t *testing.T) {
	c, err := NewCompliance(nil, []SegregationPair{{A: "5.1", B: "3", Rel: RelProhibited}, {A: "3", B: "5.1", Rel: RelProhibited}}, nil)
	if err != nil {
		t.Fatalf("identical repeated pair is allowed: %v", err)
	}
	if c.Matrix.Rel("3", "5.1") != RelProhibited || c.Matrix.Rel("5.1", "3") != RelProhibited {
		t.Error("matrix must be symmetric")
	}
	if c.Matrix.Rel("3", "3") != RelAllowed {
		t.Error("unlisted pairs are allowed")
	}
}

func TestHazmatProfile_Validate(t *testing.T) {
	negative := decimal.NewFromInt(-1)
	tests := []struct {
		name    string
		p       HazmatProfile
		wantErr bool
	}{
		{"complete profile", HazmatProfile{ItemID: "SOLV-3", UNNumber: "UN1263", StorageClass: "3"}, false},
		{"no UN number is fine", HazmatProfile{ItemID: "SOLV-3", StorageClass: "3"}, false},
		{"no item", HazmatProfile{StorageClass: "3"}, true},
		{"no storage class", HazmatProfile{ItemID: "SOLV-3"}, true},
		{"UN number with five digits", HazmatProfile{ItemID: "SOLV-3", UNNumber: "UN12634", StorageClass: "3"}, true},
		{"negative net explosive mass", HazmatProfile{ItemID: "SOLV-3", StorageClass: "1.1", NetExplosiveMassKg: &negative}, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.p.Validate(); (err != nil) != tc.wantErr {
				t.Fatalf("Validate() = %v, wantErr %v", err, tc.wantErr)
			}
		})
	}
}
