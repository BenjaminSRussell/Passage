package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/shopspring/decimal"
)

// Line kinds of a PayResultLine.
const (
	KindEarning  = "EARNING"
	KindPreTax   = "PRETAX"
	KindTax      = "TAX"
	KindPostTax  = "POSTTAX"
	KindEmployer = "EMPLOYER"
)

// Run types accepted by Calculate. A bonus is withheld at the supplemental rate.
const (
	RunRegular = "REGULAR"
	RunBonus   = "OFF_CYCLE_BONUS"
)

// Earning is one earning amount for the period.
type Earning struct {
	Code   string
	Amount decimal.Decimal
}

// Deduction is a requested deduction for the period. Post-tax deductions use
// Priority, Policy and the arrears balance; pre-tax deductions are taken in full.
// A pre-tax deduction always reduces federal and state taxable wages; it also
// reduces FICA and FUTA wages only when ReducesFICA is set (Section 125 medical,
// HSA through a cafeteria plan), never for a 401(k) (BR-43.1).
type Deduction struct {
	Code             string
	Amount           decimal.Decimal
	ReducesFICA      bool
	Priority         int
	Policy           ArrearsPolicy
	ArrearsBalance   decimal.Decimal
	RecoveryLimitPct decimal.Decimal
}

// Match is an employer match on a pre-tax deduction: Percent of the employee
// contribution, limited to CapRate of gross (BR-43.1: 50% of the first 6%).
type Match struct {
	Code    string
	Percent decimal.Decimal
	CapRate decimal.Decimal
}

// YTD carries cumulative wage bases before this period.
type YTD struct {
	FICAWages         decimal.Decimal
	FUTAWages         decimal.Decimal
	SupplementalWages decimal.Decimal
}

// Input is one worker's period.
type Input struct {
	RunType  string
	Periods  int
	W4       W4
	State    string // state code with a flat rate in the pack, or "" for none
	Earnings []Earning
	PreTax   []Deduction
	PostTax  []Deduction
	Match    *Match
	YTD      YTD
}

// Line is one PayResultLine: the amount, its basis, the rule version it used
// and, for federal withholding, the schedule row (8.43.1 explain path).
type Line struct {
	Code        string          `json:"code"`
	Kind        string          `json:"kind"`
	Amount      decimal.Decimal `json:"amount"`
	BasisAmount decimal.Decimal `json:"basisAmount"`
	RuleVersion string          `json:"ruleVersion"`
	Bracket     *BracketRef     `json:"bracket,omitempty"`
}

// ArrearsEntry reports how one post-tax deduction moved its arrears balance.
type ArrearsEntry struct {
	Code      string          `json:"code"`
	Created   decimal.Decimal `json:"created"`
	Recovered decimal.Decimal `json:"recovered"`
	Balance   decimal.Decimal `json:"balance"`
}

// Statement is the gross-to-net result for one worker and period.
type Statement struct {
	Gross           decimal.Decimal `json:"gross"`
	FedTaxableWages decimal.Decimal `json:"taxableWagesFed"`
	FICAWages       decimal.Decimal `json:"ficaWages"`
	Net             decimal.Decimal `json:"net"`
	EmployerCost    decimal.Decimal `json:"employerCost"`
	Lines           []Line          `json:"lines"`
	Arrears         []ArrearsEntry  `json:"arrears"`
}

// Calculate runs gross-to-net for one worker under one rule set (FR-43.5, FR-43.7).
// Order: earnings, pre-tax deductions, federal/FICA/state taxes on the taxable
// base, then post-tax deductions from the remaining net with arrears, then employer
// costs. Every amount is rounded once, per line.
func Calculate(rs RuleSet, in Input) (Statement, error) {
	var st Statement
	gross := decimal.Zero
	for _, e := range in.Earnings {
		gross = gross.Add(e.Amount)
		st.Lines = append(st.Lines, Line{Code: e.Code, Kind: KindEarning, Amount: e.Amount, BasisAmount: e.Amount, RuleVersion: rs.Code})
	}
	st.Gross = gross

	preTotal := decimal.Zero
	ficaExempt := decimal.Zero
	contrib := map[string]decimal.Decimal{}
	for _, d := range in.PreTax {
		amt := d.Amount.Round(2)
		preTotal = preTotal.Add(amt)
		if d.ReducesFICA {
			ficaExempt = ficaExempt.Add(amt)
		}
		contrib[d.Code] = contrib[d.Code].Add(amt)
		st.Lines = append(st.Lines, Line{Code: d.Code, Kind: KindPreTax, Amount: amt, BasisAmount: gross, RuleVersion: rs.Code})
	}
	if preTotal.GreaterThan(gross) {
		return Statement{}, &Error{Code: CodeInvalidInput, Detail: fmt.Sprintf("pre-tax deductions %s exceed gross %s", preTotal.StringFixed(2), gross.StringFixed(2))}
	}
	fedWages := gross.Sub(preTotal)
	ficaWages := gross.Sub(ficaExempt)
	st.FedTaxableWages = fedWages
	st.FICAWages = ficaWages

	var (
		fed     decimal.Decimal
		bracket *BracketRef
	)
	if in.RunType == RunBonus {
		fed = SupplementalFederal(rs, fedWages, in.YTD.SupplementalWages)
	} else {
		var err error
		fed, bracket, err = FederalWithholding(rs, in.W4, fedWages, in.Periods)
		if err != nil {
			return Statement{}, err
		}
	}
	taxTotal := fed
	st.Lines = append(st.Lines, Line{Code: "FED_WH", Kind: KindTax, Amount: fed, BasisAmount: fedWages, RuleVersion: rs.Code, Bracket: bracket})

	ss, ssBase := SocialSecurity(rs, ficaWages, in.YTD.FICAWages)
	med := Medicare(rs, ficaWages, in.YTD.FICAWages)
	taxTotal = taxTotal.Add(ss).Add(med)
	st.Lines = append(st.Lines,
		Line{Code: "SS_EE", Kind: KindTax, Amount: ss, BasisAmount: ssBase, RuleVersion: rs.Code},
		Line{Code: "MED_EE", Kind: KindTax, Amount: med, BasisAmount: ficaWages, RuleVersion: rs.Code},
	)

	if in.State != "" {
		rate, ok := rs.StateRates[in.State]
		if !ok {
			return Statement{}, &Error{Code: CodeNoRule, Detail: fmt.Sprintf("no state rate for %s in %s", in.State, rs.Code)}
		}
		stateTax := fedWages.Mul(rate).Round(2)
		taxTotal = taxTotal.Add(stateTax)
		st.Lines = append(st.Lines, Line{Code: "STATE_" + in.State + "_WH", Kind: KindTax, Amount: stateTax, BasisAmount: fedWages, RuleVersion: rs.Code})
	}

	netBefore := gross.Sub(preTotal).Sub(taxTotal)
	postTotal := allocatePostTax(&st, rs, in.PostTax, netBefore)
	st.Net = netBefore.Sub(postTotal)

	// Employer FICA matches the employee rates but never the additional
	// Medicare tax; FUTA wages follow FICA wages for the deductions shipped here.
	futa := FUTA(rs, ficaWages, in.YTD.FUTAWages)
	medER := MedicareEmployer(rs, ficaWages)
	employer := futa.Add(ss).Add(medER)
	st.Lines = append(st.Lines,
		Line{Code: "FUTA", Kind: KindEmployer, Amount: futa, BasisAmount: ficaWages, RuleVersion: rs.Code},
		Line{Code: "SS_ER", Kind: KindEmployer, Amount: ss, BasisAmount: ssBase, RuleVersion: rs.Code},
		Line{Code: "MED_ER", Kind: KindEmployer, Amount: medER, BasisAmount: ficaWages, RuleVersion: rs.Code},
	)
	if in.Match != nil {
		basis := decimal.Min(contrib[in.Match.Code], gross.Mul(in.Match.CapRate))
		matchAmt := basis.Mul(in.Match.Percent).Round(2)
		employer = employer.Add(matchAmt)
		st.Lines = append(st.Lines, Line{Code: "MATCH_" + in.Match.Code, Kind: KindEmployer, Amount: matchAmt, BasisAmount: basis, RuleVersion: rs.Code})
	}
	st.EmployerCost = employer
	return st, nil
}

// allocatePostTax applies arrears (BR-43.10) and appends POSTTAX lines and arrears
// entries to st. Recovery is added to the due amount, capped by the limit.
func allocatePostTax(st *Statement, rs RuleSet, deductions []Deduction, netBefore decimal.Decimal) decimal.Decimal {
	byCode := make(map[string]Deduction, len(deductions))
	recovery := make(map[string]decimal.Decimal, len(deductions))
	claims := make([]Claim, 0, len(deductions))
	for _, d := range deductions {
		rec := RecoveryDue(d.Amount, d.ArrearsBalance, d.RecoveryLimitPct)
		byCode[d.Code] = d
		recovery[d.Code] = rec
		claims = append(claims, Claim{Code: d.Code, Priority: d.Priority, Due: d.Amount.Add(rec), Policy: d.Policy})
	}
	total := decimal.Zero
	for _, a := range AllocateDeductions(netBefore, claims) {
		d := byCode[a.Code]
		rec := recovery[a.Code]
		total = total.Add(a.Taken)
		st.Lines = append(st.Lines, Line{Code: a.Code, Kind: KindPostTax, Amount: a.Taken, BasisAmount: netBefore, RuleVersion: rs.Code})
		// The normal part that was not taken becomes arrears (never under
		// NO_ARREARS, which skips it); recovery paid comes off the balance only
		// from what was taken above the normal amount.
		created := decimal.Max(decimal.Zero, d.Amount.Sub(a.Taken))
		if d.Policy == PolicyNoArrears {
			created = decimal.Zero
		}
		recovered := decimal.Max(decimal.Zero, decimal.Min(rec, a.Taken.Sub(d.Amount)))
		if created.IsPositive() || recovered.IsPositive() {
			st.Arrears = append(st.Arrears, ArrearsEntry{
				Code: a.Code, Created: created, Recovered: recovered,
				Balance: d.ArrearsBalance.Sub(recovered).Add(created),
			})
		}
	}
	return total
}

// Hash is the SHA-256 of the statement lines. A finalized statement is immutable:
// any change to a line changes the hash (FR-43.7).
func (s Statement) Hash() (string, error) {
	raw, err := json.Marshal(s.Lines)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}
