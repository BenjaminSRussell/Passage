package domain

import (
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/money"
)

// dep100Book is one entry of testdata/dep-100.json. The expected amounts come
// from testdata/gen_dep100.py, which shares no code with this package: MACRS
// rows from the IRS Pub. 946 tables, straight-line and declining-balance rows
// from an exact-rational implementation of the documented rules.
type dep100Book struct {
	Name        string            `json:"name"`
	Currency    string            `json:"currency"`
	Cost        string            `json:"cost"`
	Salvage     string            `json:"salvage"`
	Life        int               `json:"life"`
	Method      Method            `json:"method"`
	Factor      string            `json:"factor"`
	Switch      bool              `json:"switch"`
	Convention  Convention        `json:"convention"`
	InService   string            `json:"inService"`
	Source      string            `json:"source"`
	Periods     int               `json:"periods"`
	FirstPeriod string            `json:"firstPeriod"`
	Total       string            `json:"total"`
	Annual      map[string]string `json:"annual"`
	Amounts     []string          `json:"amounts"`
}

// TestDep100Fixtures_MatchPublishedTablesToTheCent is the Wave 37 proof
// "100 assets x methods x conventions match published tables (IRS MACRS,
// straight-line edge cases) to the cent": every period, every year and the
// total of each of the 100 books equal the independently computed fixture.
func TestDep100Fixtures_MatchPublishedTablesToTheCent(t *testing.T) {
	raw, err := os.ReadFile("../../testdata/dep-100.json")
	if err != nil {
		t.Fatal(err)
	}
	var fx struct {
		Books []dep100Book `json:"books"`
	}
	if err := json.Unmarshal(raw, &fx); err != nil {
		t.Fatal(err)
	}
	if len(fx.Books) != 100 {
		t.Fatalf("fixture has %d books, want 100", len(fx.Books))
	}
	seen := map[string]int{}
	for _, b := range fx.Books {
		seen[string(b.Method)+"/"+string(b.Convention)]++
		t.Run(b.Name, func(t *testing.T) {
			inService, err := time.Parse("2006-01-02", b.InService)
			if err != nil {
				t.Fatal(err)
			}
			lines, err := Schedule(Input{Cost: d(b.Cost), Salvage: d(b.Salvage), Life: b.Life, Method: b.Method, Factor: d(b.Factor),
				SwitchToStraightLine: b.Switch, InService: inService, Convention: b.Convention, Currency: b.Currency})
			if err != nil {
				t.Fatal(err)
			}
			minor := money.MinorUnits(b.Currency)
			if len(lines) != b.Periods || len(lines) != len(b.Amounts) {
				t.Fatalf("%d periods, want %d (%s)", len(lines), b.Periods, b.Source)
			}
			if lines[0].Period != b.FirstPeriod {
				t.Errorf("first period %s, want %s", lines[0].Period, b.FirstPeriod)
			}
			annual := map[string]decimal.Decimal{}
			for i, l := range lines {
				if got := l.Amount.StringFixed(minor); got != b.Amounts[i] {
					t.Errorf("period %s: got %s, want %s (%s)", l.Period, got, b.Amounts[i], b.Source)
				}
				annual[l.Period[:4]] = annual[l.Period[:4]].Add(l.Amount)
			}
			if len(annual) != len(b.Annual) {
				t.Errorf("%d years, want %d", len(annual), len(b.Annual))
			}
			for year, want := range b.Annual {
				if got := annual[year].StringFixed(minor); got != want {
					t.Errorf("year %s: got %s, want %s", year, got, want)
				}
			}
			if got := TotalAmount(lines).StringFixed(minor); got != b.Total {
				t.Errorf("total %s, want %s", got, b.Total)
			}
		})
	}
	// Every method and convention the engine offers is in the set.
	for _, k := range []string{"MACRS/HalfYear", "MACRS/MidQuarter", "StraightLine/None", "StraightLine/FullMonth", "StraightLine/MidMonth15",
		"DecliningBalance/None", "DecliningBalance/HalfYear", "DecliningBalance/MidQuarter"} {
		if seen[k] == 0 {
			t.Errorf("no fixture for %s", k)
		}
	}
}

// TestMACRSTables_AgreeWithTheFormula guards the transcription of Pub. 946
// Tables A-1 to A-5 against a second, independent derivation: each row sums to
// 100% and every rate is within 0.02 percentage point of the 200% / 150%
// declining-balance-with-switch formula the IRS built the tables from
// (computed here by the DecliningBalance engine on a large basis). The IRS
// rounds its rates (Table A-3 7-year: 17.85 for 17.857 in year 1, carried
// into 3.34 for 3.326 in year 8), so exact equality is not expected.
func TestMACRSTables_AgreeWithTheFormula(t *testing.T) {
	basis := d("100000000") // rates to 1e-8 of a percent
	tolerance := d("0.02")
	quarterDay := map[string]string{"HY": "2026-07-01", "Q1": "2026-02-14", "Q2": "2026-05-20", "Q3": "2026-08-31", "Q4": "2026-11-03"}
	for _, row := range []string{"HY", "Q1", "Q2", "Q3", "Q4"} {
		for _, life := range []int{3, 5, 7, 10, 15, 20} {
			rates, ok := MACRSRates(row, life)
			if !ok {
				t.Fatalf("no row %s %d", row, life)
			}
			if len(rates) != life+1 {
				t.Errorf("%s %d-year: %d rates, want %d", row, life, len(rates), life+1)
			}
			if sum := sumRates(rates); !sum.Equal(hundred) {
				t.Errorf("%s %d-year: rates sum to %s", row, life, sum)
			}
			factor := "2"
			if life >= 15 {
				factor = "1.5"
			}
			conv := ConventionMidQuarter
			if row == "HY" {
				conv = ConventionHalfYear
			}
			inService, _ := time.Parse("2006-01-02", quarterDay[row])
			lines, err := Schedule(Input{Cost: basis, Salvage: decimal.Zero, Life: life, Method: MethodDecliningBalance, Factor: d(factor),
				SwitchToStraightLine: true, InService: inService, Convention: conv, Currency: "USD"})
			if err != nil {
				t.Fatal(err)
			}
			if len(lines) != len(rates) {
				t.Fatalf("%s %d-year: formula has %d years, table %d", row, life, len(lines), len(rates))
			}
			for i, l := range lines {
				formula := l.Amount.Mul(hundred).Div(basis)
				if diff := formula.Sub(rates[i]).Abs(); diff.GreaterThan(tolerance) {
					t.Errorf("%s %d-year, year %d: table %s, formula %s", row, life, i+1, rates[i], formula.StringFixed(4))
				}
			}
		}
	}
	if _, ok := MACRSRates("HY", 27); ok {
		t.Error("a 27-year row must not exist")
	}
}

func sumRates(rates []decimal.Decimal) decimal.Decimal {
	s := decimal.Zero
	for _, r := range rates {
		s = s.Add(r)
	}
	return s
}

// TestMACRS_Rejections covers the profile checks of the table method.
func TestMACRS_Rejections(t *testing.T) {
	base := Input{Cost: d("1000"), Salvage: decimal.Zero, Life: 5, Method: MethodMACRS, InService: time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC),
		Convention: ConventionHalfYear, Currency: "USD"}
	for name, mut := range map[string]func(*Input){
		"salvage":    func(in *Input) { in.Salvage = d("1") },
		"life 4":     func(in *Input) { in.Life = 4 },
		"life 39":    func(in *Input) { in.Life = 39 },
		"convention": func(in *Input) { in.Convention = ConventionNone },
		"mid-month":  func(in *Input) { in.Convention = ConventionMidMonth15 },
	} {
		in := base
		mut(&in)
		if _, err := Schedule(in); ErrorCode(err) != CodeInvalidDepreciation {
			t.Errorf("%s: err = %v, want %s", name, err, CodeInvalidDepreciation)
		}
	}
	if _, err := Schedule(base); err != nil {
		t.Fatalf("valid MACRS profile: %v", err)
	}
}
