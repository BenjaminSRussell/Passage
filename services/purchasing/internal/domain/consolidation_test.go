package domain

import (
	"fmt"
	"testing"

	"pgregory.net/rapid"
)

// AC-17.9: 10 approved lines across 2 vendors and 2 sites, default 7-day window → 4 POs.
func TestConsolidate_AC179FourPOs(t *testing.T) {
	type spec struct{ v, s, date string }
	specs := []spec{
		{"V1", "S1", "2027-03-01"}, {"V1", "S1", "2027-03-03"},
		{"V1", "S2", "2027-03-02"}, {"V1", "S2", "2027-03-04"}, {"V1", "S2", "2027-03-05"},
		{"V2", "S1", "2027-03-01"}, {"V2", "S1", "2027-03-06"}, {"V2", "S1", "2027-03-07"},
		{"V2", "S2", "2027-03-02"}, {"V2", "S2", "2027-03-03"},
	}
	var lines []ConsolidationLine
	for i, s := range specs {
		lines = append(lines, ConsolidationLine{RequisitionLineID: fmt.Sprintf("rl-%02d", i), LegalEntityID: "LE", VendorID: s.v,
			SiteID: s.s, Currency: "USD", RequestedDate: day(s.date)})
	}
	groups, err := Consolidate(lines, DefaultConsolidationWindowDays)
	if err != nil {
		t.Fatal(err)
	}
	want := []struct {
		key   string
		lines int
	}{{"V1/S1", 2}, {"V1/S2", 3}, {"V2/S1", 3}, {"V2/S2", 2}}
	if len(groups) != len(want) {
		t.Fatalf("groups = %v, want 4", groups)
	}
	for i, g := range groups {
		if k := g.VendorID + "/" + g.SiteID; k != want[i].key || len(g.Lines) != want[i].lines {
			t.Errorf("group %d = %s with %d lines, want %s with %d", i, k, len(g.Lines), want[i].key, want[i].lines)
		}
	}
}

func TestConsolidate_SplitsOnWindowCurrencyAndLegalEntity(t *testing.T) {
	base := ConsolidationLine{LegalEntityID: "LE", VendorID: "V", SiteID: "S", Currency: "USD"}
	at := func(id, date string, change func(*ConsolidationLine)) ConsolidationLine {
		l := base
		l.RequisitionLineID, l.RequestedDate = id, day(date)
		change(&l)
		return l
	}
	same := func(*ConsolidationLine) {}
	cases := []struct {
		name   string
		lines  []ConsolidationLine
		window int
		want   int
	}{
		{"7 days apart is the next window", []ConsolidationLine{at("a", "2027-03-01", same), at("b", "2027-03-08", same)}, 7, 2},
		{"6 days apart shares the window", []ConsolidationLine{at("a", "2027-03-01", same), at("b", "2027-03-07", same)}, 7, 1},
		{"window anchors on the first line, not a chain", []ConsolidationLine{at("a", "2027-03-01", same), at("b", "2027-03-05", same), at("c", "2027-03-09", same)}, 7, 2},
		{"a wide window keeps far dates together", []ConsolidationLine{at("a", "2027-03-01", same), at("b", "2027-03-20", same)}, 60, 1},
		{"currency splits", []ConsolidationLine{at("a", "2027-03-01", same), at("b", "2027-03-01", func(l *ConsolidationLine) { l.Currency = "EUR" })}, 7, 2},
		{"legal entity splits", []ConsolidationLine{at("a", "2027-03-01", same), at("b", "2027-03-01", func(l *ConsolidationLine) { l.LegalEntityID = "LE2" })}, 7, 2},
		{"no site is its own stream", []ConsolidationLine{at("a", "2027-03-01", same), at("b", "2027-03-01", func(l *ConsolidationLine) { l.SiteID = "" })}, 7, 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			groups, err := Consolidate(tc.lines, tc.window)
			if err != nil {
				t.Fatal(err)
			}
			if len(groups) != tc.want {
				t.Fatalf("groups = %v, want %d", groups, tc.want)
			}
		})
	}
}

func TestConsolidate_WindowBounds(t *testing.T) {
	line := ConsolidationLine{RequisitionLineID: "a", VendorID: "V", Currency: "USD", RequestedDate: day("2027-03-01")}
	for _, w := range []int{0, -1, 61} {
		if _, err := Consolidate([]ConsolidationLine{line}, w); codeOf(err) != CodeConsolidationWindow {
			t.Errorf("window %d: code %q", w, codeOf(err))
		}
	}
	for _, w := range []int{1, 60} {
		if _, err := Consolidate([]ConsolidationLine{line}, w); err != nil {
			t.Errorf("window %d: %v", w, err)
		}
	}
}

// Every input line lands in exactly one group; each group shares its key, its
// dates fall inside one window, and the result does not depend on input order.
func TestConsolidate_PropertyPartition(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		n := rapid.IntRange(1, 60).Draw(t, "n")
		window := rapid.IntRange(1, 60).Draw(t, "window")
		var lines []ConsolidationLine
		for i := 0; i < n; i++ {
			off := rapid.IntRange(0, 120).Draw(t, "offset")
			lines = append(lines, ConsolidationLine{
				RequisitionLineID: fmt.Sprintf("rl-%03d", i),
				LegalEntityID:     fmt.Sprintf("le%d", rapid.IntRange(1, 2).Draw(t, "le")),
				VendorID:          fmt.Sprintf("v%d", rapid.IntRange(1, 3).Draw(t, "vendor")),
				SiteID:            fmt.Sprintf("s%d", rapid.IntRange(1, 2).Draw(t, "site")),
				Currency:          rapid.SampledFrom([]string{"USD", "EUR"}).Draw(t, "currency"),
				RequestedDate:     Day{day("2027-01-01").AddDate(0, 0, off)},
			})
		}
		groups, err := Consolidate(lines, window)
		if err != nil {
			t.Fatal(err)
		}
		seen := map[string]int{}
		for _, g := range groups {
			for _, l := range g.Lines {
				seen[l.RequisitionLineID]++
				if !l.sameStream(g) {
					t.Fatalf("line %s in wrong group %s", l.RequisitionLineID, g)
				}
				if span := int(l.RequestedDate.Sub(g.WindowStart.Time).Hours() / 24); span < 0 || span >= window {
					t.Fatalf("line %s is %d days into group %s (window %d)", l.RequisitionLineID, span, g, window)
				}
			}
		}
		if len(seen) != n {
			t.Fatalf("grouped %d distinct lines, want %d", len(seen), n)
		}
		for id, c := range seen {
			if c != 1 {
				t.Fatalf("line %s grouped %d times", id, c)
			}
		}
		reversed := make([]ConsolidationLine, n)
		for i, l := range lines {
			reversed[n-1-i] = l
		}
		again, _ := Consolidate(reversed, window)
		if fmt.Sprint(again) != fmt.Sprint(groups) {
			t.Fatalf("order-dependent result: %v vs %v", again, groups)
		}
	})
}
