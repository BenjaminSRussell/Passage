package domain

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/shopspring/decimal"
)

// Wave 33 proof, AC3 (issue #33): "AQL sampling sizes match ANSI/ASQ Z1.4
// tables". testdata/z14-tables.json is written by testdata/z14_reference.py
// (python3 -I), which rebuilds Tables I, II-A, II-B and II-C from their
// diagonal structure and the edge cells read off the standard; it never reads
// the engine's cell-by-cell transcription in z14.go.

type z14Reference struct {
	AQLs   []string                         `json:"aqls"`
	Bands  []*int64                         `json:"bands"`
	TableI map[string][]string              `json:"tableI"`
	Plans  map[string]map[string][][3]int64 `json:"plans"`
}

func loadZ14(t *testing.T) z14Reference {
	t.Helper()
	raw, err := os.ReadFile("testdata/z14-tables.json")
	if err != nil {
		t.Fatal(err)
	}
	var ref z14Reference
	if err := json.Unmarshal(raw, &ref); err != nil {
		t.Fatal(err)
	}
	if len(ref.AQLs) != 26 || len(ref.Bands) != 15 || len(ref.TableI) != 7 || len(ref.Plans) != 3 {
		t.Fatalf("reference is incomplete: %d AQLs, %d bands, %d levels, %d tables", len(ref.AQLs), len(ref.Bands), len(ref.TableI), len(ref.Plans))
	}
	return ref
}

// TestZ14_MasterTablesMatchReference compares all 3 x 16 x 26 = 1,248 resolved
// cells of Tables II-A, II-B and II-C.
func TestZ14_MasterTablesMatchReference(t *testing.T) {
	ref := loadZ14(t)
	cells := 0
	for sev, letters := range ref.Plans {
		for letter, row := range letters {
			for j, want := range row {
				got, ok := Z14Resolve(Severity(sev), letter, dec(ref.AQLs[j]))
				cells++
				if !ok || got.SampleSize != want[0] || int64(got.AcceptNo) != want[1] || int64(got.RejectNo) != want[2] {
					t.Errorf("%s %s AQL %s: got %+v ok=%v, want n=%d %d/%d", sev, letter, ref.AQLs[j], got, ok, want[0], want[1], want[2])
				}
			}
		}
	}
	if cells != 3*16*26 {
		t.Fatalf("compared %d cells, want 1248", cells)
	}
}

// TestDrawSample_MatchesZ14Reference sizes a lot at both edges of every Table I
// band, at every inspection level, AQL column and severity, through the public
// sizing function: the code letter is Table I's and the sample is the resolved
// plan's, capped at the lot (100 % inspection).
func TestDrawSample_MatchesZ14Reference(t *testing.T) {
	ref := loadZ14(t)
	checked := 0
	for level, letters := range ref.TableI {
		low := int64(2)
		for b, upTo := range ref.Bands {
			high := low + 999_999 // the open band: any lot above 500,000
			if upTo != nil {
				high = *upTo
			}
			letter := letters[b]
			for _, lot := range []int64{low, high} {
				for sev, plans := range ref.Plans {
					for j, aql := range ref.AQLs {
						want := plans[letter][j]
						got, err := DrawSample(SamplingPlan{Method: MethodAQL, AQL: dec(aql), Severity: Severity(sev), InspectionLevel: level}, decimal.NewFromInt(lot))
						checked++
						if err != nil {
							t.Errorf("%s lot %d %s AQL %s: %v", level, lot, sev, aql, err)
							continue
						}
						wantN := min(want[0], lot)
						if got.CodeLetter != letter || got.SampleQty.IntPart() != wantN || !got.SampleQty.Equal(decimal.NewFromInt(wantN)) ||
							int64(got.AcceptNo) != want[1] || int64(got.RejectNo) != want[2] || got.Severity != Severity(sev) || got.InspectionLevel != level {
							t.Errorf("%s lot %d %s AQL %s: got %s n=%s %d/%d, want %s n=%d %d/%d",
								level, lot, sev, aql, got.CodeLetter, got.SampleQty, got.AcceptNo, got.RejectNo, letter, wantN, want[1], want[2])
						}
					}
				}
			}
			if upTo != nil {
				low = *upTo + 1
			}
		}
	}
	if checked != 7*15*2*3*26 {
		t.Fatalf("checked %d draws", checked)
	}
}

// TestDrawSample_Z14HandCells are cells read directly off the master tables
// (hand-derived literals), chosen where the AQL 1.0 / 2.5 / 4.0 excerpt the
// engine used to carry disagreed with Z1.4, and at the edges of the tables.
func TestDrawSample_Z14HandCells(t *testing.T) {
	cases := []struct {
		name, level, aql string
		sev              Severity
		lot              int64
		letter           string
		n                int64
		ac, re           int
	}{
		// Table II-A, level II.
		{"K at 1.0 is 3/4, not 5/6", "II", "1.0", SeverityNormal, 1201, "K", 125, 3, 4},
		{"J at 1.0 is 2/3", "II", "1.0", SeverityNormal, 1200, "J", 80, 2, 3},
		{"H at 1.0 is 1/2", "II", "1.0", SeverityNormal, 500, "H", 50, 1, 2},
		{"F at 1.0 points up to E", "II", "1.0", SeverityNormal, 150, "F", 13, 0, 1},
		{"G at 1.0 points down to H", "II", "1.0", SeverityNormal, 151, "G", 50, 1, 2},
		{"P at 1.0 is 14/15", "II", "1.0", SeverityNormal, 150001, "P", 800, 14, 15},
		{"D at 2.5 points up to C (n=5, not 8)", "II", "2.5", SeverityNormal, 50, "D", 5, 0, 1},
		{"Q at 2.5 points up to N (n=500, not 1250)", "II", "2.5", SeverityNormal, 600000, "Q", 500, 21, 22},
		{"P at 2.5 points up to N", "II", "2.5", SeverityNormal, 150001, "P", 500, 21, 22},
		{"B at 4.0 is 0/1 on 3", "II", "4.0", SeverityNormal, 15, "B", 3, 0, 1},
		{"C at 4.0 points up to B", "II", "4.0", SeverityNormal, 25, "C", 3, 0, 1},
		{"N at 4.0 points up to M", "II", "4.0", SeverityNormal, 35001, "N", 315, 21, 22},
		{"A at 6.5 is 0/1 on 2", "II", "6.5", SeverityNormal, 8, "A", 2, 0, 1},
		{"A at 10 points down to C", "II", "10", SeverityNormal, 8, "A", 5, 1, 2},
		{"A at 1000 is 30/31", "II", "1000", SeverityNormal, 2, "A", 2, 30, 31},
		{"B at 1000 is 44/45", "II", "1000", SeverityNormal, 9, "B", 3, 44, 45},
		{"F at 100 points up to E's 21/22", "II", "100", SeverityNormal, 91, "F", 13, 21, 22},
		{"R at 0.015 points up to P", "III", "0.015", SeverityNormal, 500001, "R", 800, 0, 1},
		{"R at 0.010 points up to Q", "III", "0.010", SeverityNormal, 500001, "R", 1250, 0, 1},
		{"R at 0.65 is 21/22 on 2000", "III", "0.65", SeverityNormal, 500001, "R", 2000, 21, 22},
		// Table II-B.
		{"tightened R at 0.025 points down to S (3150)", "III", "0.025", SeverityTightened, 500001, "R", 3150, 1, 2},
		{"tightened A at 10 points down to D", "II", "10", SeverityTightened, 8, "A", 8, 1, 2},
		{"tightened K at 2.5 is 5/6", "II", "2.5", SeverityTightened, 1201, "K", 125, 5, 6},
		{"tightened B at 1000 is 41/42", "II", "1000", SeverityTightened, 9, "B", 3, 41, 42},
		// Table II-C.
		{"reduced K at 2.5 is 3/6 on 50", "II", "2.5", SeverityReduced, 1201, "K", 50, 3, 6},
		{"reduced A at 10 points down to C's 0/2", "II", "10", SeverityReduced, 8, "A", 2, 0, 2},
		{"reduced B at 40 is 2/4", "II", "40", SeverityReduced, 9, "B", 2, 2, 4},
		{"reduced C at 1000 points up to B's 30/31", "II", "1000", SeverityReduced, 16, "C", 2, 30, 31},
		{"reduced R at 0.025 is 0/2 on 800", "III", "0.025", SeverityReduced, 500001, "R", 800, 0, 2},
		// Table I, special levels.
		{"S-1 lot 500001 is D", "S-1", "6.5", SeverityNormal, 500001, "D", 8, 1, 2},
		{"S-4 lot 35001 is J", "S-4", "1.0", SeverityNormal, 35001, "J", 80, 2, 3},
		{"I lot 10001 is K", "I", "4.0", SeverityNormal, 10001, "K", 125, 10, 11},
	}
	for _, c := range cases {
		got, err := DrawSample(SamplingPlan{Method: MethodAQL, AQL: dec(c.aql), Severity: c.sev, InspectionLevel: c.level}, decimal.NewFromInt(c.lot))
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		if got.CodeLetter != c.letter || !got.SampleQty.Equal(decimal.NewFromInt(c.n)) || got.AcceptNo != c.ac || got.RejectNo != c.re {
			t.Errorf("%s: got %s n=%s %d/%d, want %s n=%d %d/%d", c.name, got.CodeLetter, got.SampleQty, got.AcceptNo, got.RejectNo, c.letter, c.n, c.ac, c.re)
		}
	}
}
