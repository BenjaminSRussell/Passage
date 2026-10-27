package domain

import (
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/shopspring/decimal"
)

// workedExample is one entry of testdata/worked-examples.json: an input profile
// and the expected yearly totals published in the PRD.
type workedExample struct {
	Cost       string            `json:"cost"`
	Salvage    string            `json:"salvage"`
	Life       int               `json:"life"`
	Method     Method            `json:"method"`
	Factor     string            `json:"factor"`
	Switch     bool              `json:"switch"`
	InService  string            `json:"inService"`
	Convention Convention        `json:"convention"`
	Annual     map[string]string `json:"annual"`
}

// TestWorkedExampleFixtures_MatchPublishedYearlyTotals covers AC-37.1 and AC-37.2:
// the mid-month straight-line and 5-year MACRS examples must match to the cent.
func TestWorkedExampleFixtures_MatchPublishedYearlyTotals(t *testing.T) {
	raw, err := os.ReadFile("../../testdata/worked-examples.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixtures map[string]workedExample
	if err := json.Unmarshal(raw, &fixtures); err != nil {
		t.Fatal(err)
	}
	for name, ex := range fixtures {
		t.Run(name, func(t *testing.T) {
			inService, err := time.Parse("2006-01-02", ex.InService)
			if err != nil {
				t.Fatal(err)
			}
			lines, err := Schedule(Input{
				Cost:                 d(ex.Cost),
				Salvage:              d(ex.Salvage),
				Life:                 ex.Life,
				Method:               ex.Method,
				Factor:               d(ex.Factor),
				SwitchToStraightLine: ex.Switch,
				InService:            inService,
				Convention:           ex.Convention,
				Currency:             "USD",
			})
			if err != nil {
				t.Fatal(err)
			}
			got := map[string]decimal.Decimal{}
			for _, l := range lines {
				year := l.Period[:4]
				got[year] = got[year].Add(l.Amount)
			}
			for year, want := range ex.Annual {
				if g := got[year].StringFixed(2); g != want {
					t.Errorf("year %s: got %s, want %s", year, g, want)
				}
			}
		})
	}
}
