package domain

import (
	"errors"
	"testing"

	"github.com/shopspring/decimal"
	"pgregory.net/rapid"
)

// Expected values are worked by hand from the 2025 Pub 15-T Worksheet 1A
// (annual percentage method, automated payroll systems); each comment shows
// the adjusted annual wage and the schedule row.
func TestFederalWithholding_PubFifteenT(t *testing.T) {
	rs := US2025()
	single := W4{FilingStatus: FilingSingle}
	tests := []struct {
		name    string
		w4      W4
		wage    string
		periods int
		want    string
	}{
		// 3,760 × 26 − 8,600 = 89,160 → 5,578.50 + 22% × 34,285 = 13,121.20 / 26.
		{"BR-43.1 worker on a 2020+ W-4", single, "3760", 26, "504.66"},
		// Married filing separately uses the Single schedule.
		{"married filing separately", W4{FilingStatus: FilingMarriedFilingSeparately}, "3760", 26, "504.66"},
		// Pre-2020 W-4, 0 allowances: 97,760 → 5,578.50 + 22% × 42,885 = 15,013.20 / 26.
		// This is the arithmetic the PRD BR-43.1 example performs.
		{"pre-2020 W-4 single, 0 allowances", W4{FilingStatus: FilingSingle, Legacy: true}, "3760", 26, "577.43"},
		// 97,760 − 12,900 = 84,860 → 2,385 + 12% × 43,910 = 7,654.20 / 26.
		{"married filing jointly", W4{FilingStatus: FilingMarriedFilingJointly}, "3760", 26, "294.39"},
		// 5,000 × 12 − 8,600 = 51,400 → 1,700 + 12% × 20,500 = 4,160 / 12.
		{"head of household monthly", W4{FilingStatus: FilingHeadOfHousehold}, "5000", 12, "346.67"},
		// Step 2 box: 97,760 on the checkbox schedule → 8,825.50 + 24% × 38,585 = 18,085.90 / 26.
		{"Step 2 checkbox single", W4{FilingStatus: FilingSingle, MultipleJobs: true}, "3760", 26, "695.61"},
		// 504.6615 − 2,000 / 26 + 25.
		{"Step 3 credit and Step 4(c) extra", W4{FilingStatus: FilingSingle, DependentsCredit: dec("2000"), ExtraWithholding: dec("25")}, "3760", 26, "452.74"},
		// 97,760 + 5,200 − 2,600 − 8,600 = 91,760 → 5,578.50 + 22% × 36,885 = 13,693.20 / 26.
		{"Step 4(a) other income and 4(b) deductions", W4{FilingStatus: FilingSingle, OtherIncome: dec("5200"), Deductions: dec("2600")}, "3760", 26, "526.66"},
		// 500 × 26 − 8,600 = 4,400, below the 6,400 zero-rate row.
		{"wage under the zero-rate row", single, "500", 26, "0"},
		// 1,100 / 26 = 42.31 tentative, less the 153.85 credit, floors at zero.
		{"credit larger than tentative withholding", W4{FilingStatus: FilingSingle, DependentsCredit: dec("4000")}, "1000", 26, "0"},
		// Pre-2020 married, 2 allowances: 78,000 − 8,600 = 69,400 → 2,385 + 12% × 28,450 = 5,799 / 52.
		{"pre-2020 W-4 married, 2 allowances weekly", W4{FilingStatus: FilingMarriedFilingJointly, Legacy: true, Allowances: 2}, "1500", 52, "111.52"},
		// 720,000 − 8,600 = 711,400 → 188,769.75 + 37% × 78,650 = 217,870.25 / 12.
		{"top bracket", single, "60000", 12, "18155.85"},
		{"zero wage has no withholding", W4{FilingStatus: FilingSingle, ExtraWithholding: dec("25")}, "0", 26, "0"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, _, err := FederalWithholding(rs, tc.w4, dec(tc.wage), tc.periods)
			if err != nil || !got.Equal(dec(tc.want)) {
				t.Fatalf("got %s, %v; want %s", got, err, tc.want)
			}
		})
	}
}

func TestFederalWithholdingRecordsTheScheduleRow(t *testing.T) {
	_, ref, err := FederalWithholding(US2025(), W4{FilingStatus: FilingSingle}, dec("3760"), 26)
	if err != nil {
		t.Fatal(err)
	}
	if ref == nil || ref.Schedule != ScheduleStandard || !ref.Over.Equal(dec("54875")) || !ref.Rate.Equal(dec("0.22")) || !ref.AdjustedWage.Equal(dec("89160")) {
		t.Fatalf("bracket = %+v", ref)
	}
}

func TestFederalWithholdingFailsClosed(t *testing.T) {
	rs := US2025()
	tests := []struct {
		name    string
		rs      RuleSet
		w4      W4
		periods int
	}{
		{"unknown filing status", rs, W4{FilingStatus: "MARRIED"}, 26},
		{"head of household on a pre-2020 W-4", rs, W4{FilingStatus: FilingHeadOfHousehold, Legacy: true}, 26},
		{"zero periods", rs, W4{FilingStatus: FilingSingle}, 0},
		{"pack without schedules", RuleSet{Code: "EMPTY"}, W4{FilingStatus: FilingSingle}, 26},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := FederalWithholding(tc.rs, tc.w4, dec("3760"), tc.periods)
			var de *Error
			if !errors.As(err, &de) || de.Code != CodeNoRule {
				t.Fatalf("want %s, got %v", CodeNoRule, err)
			}
		})
	}
}

// Each schedule row's base tax must equal the previous row's base plus its
// rate over the row width; this catches a mistyped table value. The IRS rounds
// the half-dollar thresholds of the Single checkbox schedule, so allow 0.25.
func TestScheduleRowsAreContinuous(t *testing.T) {
	for sched, byStatus := range US2025().Schedules {
		for status, rows := range byStatus {
			for i := 1; i < len(rows); i++ {
				prev, cur := rows[i-1], rows[i]
				want := prev.BaseTax.Add(cur.Over.Sub(prev.Over).Mul(prev.Rate))
				if want.Sub(cur.BaseTax).Abs().GreaterThan(dec("0.25")) {
					t.Errorf("%s %s row %d: base %s, continuity gives %s", sched, status, i, cur.BaseTax, want)
				}
				if !cur.Over.GreaterThan(prev.Over) {
					t.Errorf("%s %s rows out of order at %d", sched, status, i)
				}
			}
		}
	}
}

func TestFederalWithholdingIsMonotonicInWage(t *testing.T) {
	rs := US2025()
	statuses := []string{FilingSingle, FilingMarriedFilingJointly, FilingHeadOfHousehold}
	rapid.Check(t, func(t *rapid.T) {
		w4 := W4{FilingStatus: statuses[rapid.IntRange(0, 2).Draw(t, "status")], MultipleJobs: rapid.Bool().Draw(t, "step2")}
		a := decimal.New(rapid.Int64Range(0, 5_000_000).Draw(t, "aCents"), -2)
		b := a.Add(decimal.New(rapid.Int64Range(0, 1_000_000).Draw(t, "deltaCents"), -2))
		wa, _, errA := FederalWithholding(rs, w4, a, 26)
		wb, _, errB := FederalWithholding(rs, w4, b, 26)
		if errA != nil || errB != nil {
			t.Fatalf("errors %v %v", errA, errB)
		}
		if wb.LessThan(wa) || wa.IsNegative() || wb.GreaterThan(b) {
			t.Fatalf("withholding %s on %s and %s on %s", wa, a, wb, b)
		}
	})
}

func TestSocialSecurityWageBaseCap(t *testing.T) {
	rs := US2025()
	tests := []struct {
		name, gross, ytd, wantTax, wantBase string
	}{
		{"below the base", "4000", "0", "248.00", "4000"},
		{"AC-43.2 crossing the base", "4000", "174000", "130.20", "2100"},
		{"past the base", "4000", "180000", "0", "0"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tax, base := SocialSecurity(rs, dec(tc.gross), dec(tc.ytd))
			if !tax.Equal(dec(tc.wantTax)) || !base.Equal(dec(tc.wantBase)) {
				t.Fatalf("got tax %s base %s; want %s %s", tax, base, tc.wantTax, tc.wantBase)
			}
		})
	}
}

func TestMedicareAdditionalStartsOnCrossingPeriod(t *testing.T) {
	rs := US2025()
	tests := []struct{ name, gross, ytd, want string }{
		{"regular only", "4000", "0", "58.00"},
		// 14.50435 + 0.9% × 500.30 = 19.00705: one rounding gives 19.01, two give 19.00.
		{"rounded once over both parts", "1000.30", "199500", "19.01"},
		{"crossing 200,000", "4000", "199000", "85.00"}, // 58.00 + 0.9% × 3000
		{"entirely above 200,000", "4000", "250000", "94.00"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := Medicare(rs, dec(tc.gross), dec(tc.ytd)); !got.Equal(dec(tc.want)) {
				t.Fatalf("got %s want %s", got, tc.want)
			}
		})
	}
}

func TestEmployerMedicareExcludesAdditionalTax(t *testing.T) {
	// The 0.9% additional Medicare tax is employee-only: the employer pays
	// 1.45% even on wages above 200,000.
	if got := MedicareEmployer(US2025(), dec("4000")); !got.Equal(dec("58.00")) {
		t.Fatalf("employer Medicare = %s, want 58.00", got)
	}
}

func TestFUTAAndSupplemental(t *testing.T) {
	rs := US2025()
	if got := FUTA(rs, dec("4000"), dec("0")); !got.Equal(dec("24.00")) {
		t.Fatalf("FUTA first period = %s", got)
	}
	if got := FUTA(rs, dec("4000"), dec("6500")); !got.Equal(dec("3.00")) {
		t.Fatalf("FUTA crossing 7,000 = %s", got)
	}
	// BR-43.11: 5,000 bonus at 22% flat.
	if got := SupplementalFederal(rs, dec("5000"), dec("0")); !got.Equal(dec("1100.00")) {
		t.Fatalf("supplemental bonus = %s", got)
	}
	// Portion above 1,000,000 cumulative is taxed at 37%: 1000×22% + 4000×37%.
	if got := SupplementalFederal(rs, dec("5000"), dec("999000")); !got.Equal(dec("1700.00")) {
		t.Fatalf("supplemental over 1,000,000 = %s", got)
	}
}

func TestSocialSecurityNeverOverWithholds(t *testing.T) {
	rs := US2025()
	rapid.Check(t, func(t *rapid.T) {
		gross := decimal.New(rapid.Int64Range(0, 20_000_000).Draw(t, "grossCents"), -2)
		ytd := decimal.New(rapid.Int64Range(0, 40_000_000).Draw(t, "ytdCents"), -2)
		tax, base := SocialSecurity(rs, gross, ytd)
		if tax.GreaterThan(gross.Mul(rs.SSRate).Add(dec("0.005"))) {
			t.Fatalf("tax %s above rate on gross %s", tax, gross)
		}
		if base.GreaterThan(gross) || (base.IsPositive() && ytd.Add(base).GreaterThan(rs.SSWageBase)) {
			t.Fatalf("base %s pushes ytd %s past the wage base", base, ytd)
		}
	})
}
