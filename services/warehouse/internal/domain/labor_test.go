package domain

import (
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"pgregory.net/rapid"
)

func TestTravel(t *testing.T) {
	origin := Coordinate{X: dec("0"), Y: dec("0"), Z: dec("0")}
	tests := []struct {
		name   string
		to     Coordinate
		metric TravelMetric
		want   string
	}{
		{"manhattan 3 4", Coordinate{X: dec("3"), Y: dec("4")}, TravelManhattan, "7"},
		{"euclidean 3 4 is 5", Coordinate{X: dec("3"), Y: dec("4")}, TravelEuclidean, "5"},
		{"euclidean diagonal rounds to 6 places", Coordinate{X: dec("1"), Y: dec("1")}, TravelEuclidean, "1.414214"},
		{"height counts in manhattan", Coordinate{X: dec("1"), Y: dec("2"), Z: dec("-3")}, TravelManhattan, "6"},
		{"same position is zero", origin, TravelEuclidean, "0"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Travel(origin, tc.to, tc.metric)
			if err != nil || !got.Equal(dec(tc.want)) {
				t.Fatalf("Travel = %s, %v; want %s", got, err, tc.want)
			}
		})
	}
	if _, err := Travel(origin, origin, "GRAPH"); err == nil {
		t.Error("GRAPH is not supported in this slice")
	}
}

// laborFormula is the fixture for AC-31.13: setup 10 s, 0.5 s/m, 2 s/unit, 3 s/LP, 0.1 s/kg.
func laborFormula() Formula {
	return Formula{SetupS: dec("10"), SecondsPerMeter: dec("0.5"), SecondsPerUnit: dec("2"), SecondsPerLP: dec("3"), SecondsPerKg: dec("0.1")}
}

func TestFormula_PlannedFixture(t *testing.T) {
	// 10 + 20×0.5 + 4×2 + 1×3 + 15.25×0.1 = 32.525, rounded to 32.5 s.
	m := Measure{TravelM: dec("20"), Qty: dec("4"), LPs: dec("1"), WeightKg: dec("15.25")}
	if got := laborFormula().Planned(m); !got.Equal(dec("32.5")) {
		t.Errorf("Planned = %s, want 32.5", got)
	}
	if got := laborFormula().Planned(Measure{}); !got.Equal(dec("10")) {
		t.Errorf("setup-only line = %s, want 10", got)
	}
}

func TestFormula_Validate(t *testing.T) {
	bad := laborFormula()
	bad.SecondsPerKg = dec("-0.1")
	if err := bad.Validate(); err == nil {
		t.Error("negative coefficient must be refused")
	}
	if err := laborFormula().Validate(); err != nil {
		t.Errorf("valid formula refused: %v", err)
	}
}

func TestFormula_PlannedHasOneDecimalProperty(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		m := Measure{
			TravelM:  decimal.New(rapid.Int64Range(0, 100000).Draw(t, "travel"), -2),
			Qty:      decimal.NewFromInt(rapid.Int64Range(0, 10000).Draw(t, "qty")),
			LPs:      decimal.NewFromInt(rapid.Int64Range(0, 20).Draw(t, "lps")),
			WeightKg: decimal.New(rapid.Int64Range(0, 1000000).Draw(t, "kg"), -3),
		}
		got := laborFormula().Planned(m)
		if got.Exponent() < -1 || got.IsNegative() {
			t.Fatalf("planned %s must be non-negative with at most one decimal", got)
		}
	})
}

func TestActual_SubtractsLongPausesOnly(t *testing.T) {
	start := time.Date(2026, 10, 21, 9, 0, 0, 0, time.UTC)
	at := func(m int) time.Time { return start.Add(time.Duration(m) * time.Minute) }
	threshold := 120 * time.Second
	tests := []struct {
		name         string
		c            Confirmation
		wantActual   string
		wantIndirect string
		wantErr      bool
	}{
		{
			name:       "no pauses",
			c:          Confirmation{StartedAt: start, ConfirmedAt: at(10)},
			wantActual: "600", wantIndirect: "0",
		},
		{
			name:       "pause equal to the threshold stays direct",
			c:          Confirmation{StartedAt: start, ConfirmedAt: at(10), Pauses: []Pause{{Start: at(1), End: at(3)}}},
			wantActual: "600", wantIndirect: "0",
		},
		{
			name:       "pause over the threshold is indirect",
			c:          Confirmation{StartedAt: start, ConfirmedAt: at(10), Pauses: []Pause{{Start: at(1), End: at(3)}, {Start: at(4), End: at(8)}}},
			wantActual: "360", wantIndirect: "240",
		},
		{
			name:    "confirmed before started",
			c:       Confirmation{StartedAt: at(5), ConfirmedAt: at(1)},
			wantErr: true,
		},
		{
			name:    "overlapping pauses would subtract time twice",
			c:       Confirmation{StartedAt: start, ConfirmedAt: at(10), Pauses: []Pause{{Start: at(4), End: at(8)}, {Start: at(1), End: at(5)}}},
			wantErr: true,
		},
		{
			name:    "pause before the line started",
			c:       Confirmation{StartedAt: at(2), ConfirmedAt: at(10), Pauses: []Pause{{Start: at(0), End: at(1)}}},
			wantErr: true,
		},
		{
			name:    "pause ending before it starts",
			c:       Confirmation{StartedAt: start, ConfirmedAt: at(10), Pauses: []Pause{{Start: at(5), End: at(4)}}},
			wantErr: true,
		},
		{
			name:    "pauses longer than the interval",
			c:       Confirmation{StartedAt: start, ConfirmedAt: at(1), Pauses: []Pause{{Start: at(0), End: at(5)}}},
			wantErr: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			actual, indirect, err := Actual(tc.c, threshold)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tc.wantErr)
			}
			if tc.wantErr {
				return
			}
			if !actual.Equal(dec(tc.wantActual)) || !indirect.Equal(dec(tc.wantIndirect)) {
				t.Errorf("actual %s indirect %s, want %s / %s", actual, indirect, tc.wantActual, tc.wantIndirect)
			}
		})
	}
}

func TestOutlier_BandIsInclusive(t *testing.T) {
	// BR-31.14: outside 0.2× to 5× planned is an outlier; the band edges are not.
	planned := dec("100")
	tests := []struct {
		actual string
		want   bool
	}{
		{"20", false},
		{"19.9", true},
		{"500", false},
		{"500.1", true},
	}
	for _, tc := range tests {
		if got := Outlier(planned, dec(tc.actual)); got != tc.want {
			t.Errorf("Outlier(100, %s) = %v, want %v", tc.actual, got, tc.want)
		}
	}
}

func TestPerformances_SumsNonOutliersPerGroup(t *testing.T) {
	lines := []LineResult{
		{UserID: "u1", Shift: "S1", Planned: dec("10"), Actual: dec("20")},
		{UserID: "u1", Shift: "S1", Planned: dec("10"), Actual: dec("10")},
		{UserID: "u1", Shift: "S1", Planned: dec("10"), Actual: dec("1000"), Outlier: true},
		{UserID: "u2", Shift: "S2", Planned: dec("30"), Actual: dec("60")},
		{UserID: "u3", Shift: "S2", Planned: dec("5"), Actual: dec("0"), Outlier: false},
	}
	byUser := Performances(lines, func(l LineResult) string { return l.UserID })
	if p := byUser["u1"]; p.Lines != 3 || p.Outliers != 1 || !p.Planned.Equal(dec("20")) || !p.Actual.Equal(dec("30")) || !p.Percent.Equal(dec("66.7")) {
		t.Errorf("u1 = %+v, want 3 lines, 1 outlier, 20/30, 66.7%%", p)
	}
	if p := byUser["u2"]; !p.Percent.Equal(dec("50")) {
		t.Errorf("u2 percent = %s, want 50", p.Percent)
	}
	if p := byUser["u3"]; !p.Percent.IsZero() {
		t.Errorf("zero actual must report 0 percent, got %s", p.Percent)
	}
	byShift := Performances(lines, func(l LineResult) string { return l.Shift })
	if p := byShift["S2"]; p.Lines != 2 || !p.Planned.Equal(dec("35")) {
		t.Errorf("shift S2 = %+v, want 2 lines and planned 35", p)
	}
}

func TestLaborLine_PlannedActualAndOutlierWarning(t *testing.T) {
	start := time.Date(2026, 10, 21, 9, 0, 0, 0, time.UTC)
	m := Measure{TravelM: dec("20"), Qty: dec("4"), LPs: dec("1"), WeightKg: dec("15.25")} // planned 32.5 s
	c := Confirmation{LineID: "W-1/1", UserID: "u1", Shift: "S1", StartedAt: start, ConfirmedAt: start.Add(40 * time.Second)}
	r, warning, err := LaborLine(laborFormula(), m, c, DefaultIndirectThreshold)
	if err != nil {
		t.Fatal(err)
	}
	if !r.Planned.Equal(dec("32.5")) || !r.Actual.Equal(dec("40")) || r.Outlier || warning != "" || r.UserID != "u1" {
		t.Errorf("line = %+v, warning %q; want 32.5 planned, 40 actual, not an outlier", r, warning)
	}
	c.ConfirmedAt = start.Add(5 * time.Second) // below 0.2 × 32.5 = 6.5 s
	r, warning, err = LaborLine(laborFormula(), m, c, DefaultIndirectThreshold)
	if err != nil {
		t.Fatal(err)
	}
	if !r.Outlier || warning != "WMS-0118: labor line W-1/1 excluded as outlier (actual 5 s vs planned 32.5 s)" {
		t.Errorf("want WMS-0118 outlier, got %+v %q", r, warning)
	}
	bad := laborFormula()
	bad.SetupS = dec("-1")
	if _, _, err := LaborLine(bad, m, c, DefaultIndirectThreshold); err == nil {
		t.Error("an invalid formula must be refused")
	}
}
