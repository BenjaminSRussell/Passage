package domain

import "github.com/shopspring/decimal"

// Filing statuses on Form W-4. A W-4 from 2019 or earlier (Legacy) only knows
// SINGLE and MARRIED_FILING_JOINTLY ("Married"); married filing separately and
// "married, but withhold at the higher Single rate" use the Single schedules.
const (
	FilingSingle                  = "SINGLE"
	FilingMarriedFilingJointly    = "MARRIED_FILING_JOINTLY"
	FilingMarriedFilingSeparately = "MARRIED_FILING_SEPARATELY"
	FilingHeadOfHousehold         = "HEAD_OF_HOUSEHOLD"
)

// Withholding rate schedules of the Pub 15-T annual percentage method.
const (
	ScheduleStandard = "STANDARD" // Step 2 box not checked, and every pre-2020 W-4
	ScheduleCheckbox = "CHECKBOX" // Step 2 box checked (multiple jobs)
)

// Bracket is one row of an annual percentage-method schedule (Pub 15-T
// Worksheet 1A columns A, C and D): for an adjusted annual wage of at least
// Over, the tentative annual withholding is BaseTax + Rate × (wage − Over).
// Brackets are held in ascending Over order; the last one is unbounded.
type Bracket struct {
	Over    decimal.Decimal
	BaseTax decimal.Decimal
	Rate    decimal.Decimal
}

// RuleSet is a versioned, country- and year-specific tax and contribution pack.
// A pay statement records Code on every line (the frozen binding, FR-43.2).
type RuleSet struct {
	Code    string // e.g. US-FED-2025.1
	Country string
	TaxYear int
	Version int
	Status  string // DRAFT or PUBLISHED
	Author  string

	// Schedules maps ScheduleStandard / ScheduleCheckbox to the brackets per
	// table filing status (SINGLE, MARRIED_FILING_JOINTLY, HEAD_OF_HOUSEHOLD).
	Schedules map[string]map[string][]Bracket
	// Step2Adjustment is Worksheet 1A line 1g: subtracted from the annual wage
	// of a 2020+ W-4 when the Step 2 box is not checked.
	Step2Adjustment map[string]decimal.Decimal
	// AllowanceValue is the annual amount per withholding allowance on a W-4
	// from 2019 or earlier (Worksheet 1A line 1k).
	AllowanceValue decimal.Decimal

	SSRate     decimal.Decimal
	SSWageBase decimal.Decimal

	MedicareRate                decimal.Decimal
	AdditionalMedicareRate      decimal.Decimal
	AdditionalMedicareThreshold decimal.Decimal

	FUTARate     decimal.Decimal
	FUTAWageBase decimal.Decimal

	SupplementalRate      decimal.Decimal
	SupplementalHighRate  decimal.Decimal
	SupplementalThreshold decimal.Decimal

	// StateRates maps a state code to a flat rate on state taxable wages
	// (federal taxable wages for the flat-rate states shipped here).
	StateRates map[string]decimal.Decimal
}

func dec(s string) decimal.Decimal { return decimal.RequireFromString(s) }

// PackFor returns the built-in rule set for a country and tax year. Only the
// US 2025 reference pack is shipped in this slice; anything else is unknown
// and callers must fail closed (PAY-RULE-001).
func PackFor(country string, taxYear int) (RuleSet, bool) {
	if country == "US" && taxYear == 2025 {
		return US2025(), true
	}
	return RuleSet{}, false
}

// schedule builds a bracket list from (over, baseTax, rate) triples.
func schedule(rows ...[3]string) []Bracket {
	out := make([]Bracket, len(rows))
	for i, r := range rows {
		out[i] = Bracket{Over: dec(r[0]), BaseTax: dec(r[1]), Rate: dec(r[2])}
	}
	return out
}

// US2025 is the US-FED-2025.1 reference pack: the 2025 Pub 15-T annual
// percentage method schedules for automated payroll systems (Worksheet 1A,
// standard and Step 2 checkbox schedules for every filing status), FICA, FUTA,
// supplemental rates and the Colorado flat rate.
func US2025() RuleSet {
	return RuleSet{
		Code: "US-FED-2025.1", Country: "US", TaxYear: 2025, Version: 1, Status: "PUBLISHED", Author: "pack-loader",
		Schedules: map[string]map[string][]Bracket{
			ScheduleStandard: {
				FilingMarriedFilingJointly: schedule(
					[3]string{"0", "0", "0"},
					[3]string{"17100", "0", "0.10"},
					[3]string{"40950", "2385", "0.12"},
					[3]string{"114050", "11157", "0.22"},
					[3]string{"223800", "35302", "0.24"},
					[3]string{"411700", "80398", "0.32"},
					[3]string{"518150", "114462", "0.35"},
					[3]string{"768700", "202154.50", "0.37"},
				),
				FilingSingle: schedule(
					[3]string{"0", "0", "0"},
					[3]string{"6400", "0", "0.10"},
					[3]string{"18325", "1192.50", "0.12"},
					[3]string{"54875", "5578.50", "0.22"},
					[3]string{"109750", "17651", "0.24"},
					[3]string{"203700", "40199", "0.32"},
					[3]string{"256925", "57231", "0.35"},
					[3]string{"632750", "188769.75", "0.37"},
				),
				FilingHeadOfHousehold: schedule(
					[3]string{"0", "0", "0"},
					[3]string{"13900", "0", "0.10"},
					[3]string{"30900", "1700", "0.12"},
					[3]string{"78750", "7442", "0.22"},
					[3]string{"117250", "15912", "0.24"},
					[3]string{"211200", "38460", "0.32"},
					[3]string{"264400", "55484", "0.35"},
					[3]string{"640250", "187031.50", "0.37"},
				),
			},
			ScheduleCheckbox: {
				FilingMarriedFilingJointly: schedule(
					[3]string{"0", "0", "0"},
					[3]string{"15000", "0", "0.10"},
					[3]string{"26925", "1192.50", "0.12"},
					[3]string{"63475", "5578.50", "0.22"},
					[3]string{"118350", "17651", "0.24"},
					[3]string{"212300", "40199", "0.32"},
					[3]string{"265525", "57231", "0.35"},
					[3]string{"390800", "101077.25", "0.37"},
				),
				FilingSingle: schedule(
					[3]string{"0", "0", "0"},
					[3]string{"7500", "0", "0.10"},
					[3]string{"13463", "596.25", "0.12"},
					[3]string{"31738", "2789.25", "0.22"},
					[3]string{"59175", "8825.50", "0.24"},
					[3]string{"106150", "20099.50", "0.32"},
					[3]string{"132763", "28615.50", "0.35"},
					[3]string{"320675", "94384.88", "0.37"},
				),
				FilingHeadOfHousehold: schedule(
					[3]string{"0", "0", "0"},
					[3]string{"11250", "0", "0.10"},
					[3]string{"19750", "850", "0.12"},
					[3]string{"43675", "3721", "0.22"},
					[3]string{"62925", "7956", "0.24"},
					[3]string{"109900", "19230", "0.32"},
					[3]string{"136500", "27742", "0.35"},
					[3]string{"324425", "93515.75", "0.37"},
				),
			},
		},
		Step2Adjustment: map[string]decimal.Decimal{
			FilingMarriedFilingJointly: dec("12900"),
			FilingSingle:               dec("8600"),
			FilingHeadOfHousehold:      dec("8600"),
		},
		AllowanceValue: dec("4300"),
		SSRate:         dec("0.062"), SSWageBase: dec("176100"),
		MedicareRate: dec("0.0145"), AdditionalMedicareRate: dec("0.009"), AdditionalMedicareThreshold: dec("200000"),
		FUTARate: dec("0.006"), FUTAWageBase: dec("7000"),
		SupplementalRate: dec("0.22"), SupplementalHighRate: dec("0.37"), SupplementalThreshold: dec("1000000"),
		StateRates: map[string]decimal.Decimal{"CO": dec("0.044")},
	}
}

// CheckPublish enforces BR-43.8: the publisher of a rule set must not be its author.
func CheckPublish(author, publisher string) error {
	if author != "" && author == publisher {
		return &Error{Code: CodeRuleSoD, Detail: "publisher must differ from author"}
	}
	return nil
}
