package domain

import (
	"fmt"
	"regexp"
	"time"

	"github.com/shopspring/decimal"
)

// Commodity pricing codes (FR-26.21; message catalog PRC-*).
const (
	// CodeIndexStale is the PRC-0016 warning: the newest index value is older than the
	// calculation allows. The run still proceeds; the caller decides.
	CodeIndexStale = "PRC-0016"
	// CodeBasisNotFound is raised for an unknown cost basis type.
	CodeBasisNotFound = "PRC-0050"
	// CodeCalculationNotFound is raised for an unknown price calculation.
	CodeCalculationNotFound = "PRC-0051"
	// CodeNoIndexValue is raised when a run finds no index value on or before its date.
	CodeNoIndexValue = "PRC-0052"
	// CodePriceNotPositive is raised when index x factor + margin rounds to nothing or is
	// too large for a unit price (NUMERIC(19,6): fewer than 14 integer digits).
	CodePriceNotPositive = "PRC-0053"
	// CodeBasisCodeTaken is raised when a legal entity already has a basis with that code.
	CodeBasisCodeTaken = "PRC-0054"
	// CodeCurrencyMismatch is raised when a calculation prices in another currency than the
	// cost basis is quoted in: there is no cross-currency conversion (cross_currency_fallback
	// defaults to false), so the factor would silently carry an exchange rate.
	CodeCurrencyMismatch = "PRC-0055"
	// CodeRunOwnJournal refuses the post of a commodity run's journal by the user who ran it
	// (CommodityPriceRun conflicts with PricingJournalPost, PRD 5.26.17).
	CodeRunOwnJournal = "PRC-0056"
)

// Margin kinds of a price calculation.
const (
	MarginPercent = "PERCENT"
	MarginAmount  = "AMOUNT"
)

// Defaults of the commodity_index_rounding parameter (decimals for index x factor, 2 to 6) and
// of the staleness window; there is no parameter table yet, so each calculation carries them.
const (
	DefaultIndexRounding   = 6
	DefaultMaxIndexAgeDays = 7
)

// UnitPricePlaces is the precision of a stored unit price (FR-26.7, NUMERIC(19,6)).
const UnitPricePlaces = 6

// maxNumeric bounds a NUMERIC(19,6) value: fewer than 14 integer digits.
var maxNumeric = decimal.New(1, 13)

var basisCode = regexp.MustCompile(`^[A-Z0-9_-]{1,32}$`)

// CommodityBasis is a cost basis type: the thing an index is quoted for, in a currency and unit.
type CommodityBasis struct {
	ID            string
	LegalEntityID string
	Code          string
	Name          string
	Currency      string
	Unit          string
	Version       int
}

// IndexValue is one dated, versioned index quote. A correction is a higher Revision of the
// same day; a run reads the highest revision of the latest day on or before its date.
type IndexValue struct {
	BasisID  string
	AsOf     time.Time
	Revision int
	Value    decimal.Decimal
	Source   string
}

// CommodityCalc turns the latest index of a basis into the price of one product for one
// customer scope: index x Factor rounded to Rounding decimals, plus the margin. It
// reprices the ACTIVE PRICE records on exactly that key (quantity break 0, no variant, no campaign).
type CommodityCalc struct {
	ID              string
	LegalEntityID   string
	Name            string
	BasisID         string
	Factor          decimal.Decimal
	MarginKind      string
	Margin          decimal.Decimal
	Rounding        int
	MaxIndexAgeDays int
	CustomerKind    CustomerKind
	CustomerKey     string
	ProductID       string
	Unit            string
	Currency        string
	Withdrawn       bool
	Version         int
}

// ValidateBasis returns field errors for a cost basis type; an empty map means valid.
func ValidateBasis(b CommodityBasis) map[string]string {
	f := map[string]string{}
	if !basisCode.MatchString(b.Code) {
		f["code"] = "is required: capitals, digits, - and _, at most 32 characters"
	}
	if b.Name == "" || len(b.Name) > 120 {
		f["name"] = "is required, at most 120 characters"
	}
	if !isCurrencyCode(b.Currency) {
		f["currency"] = "must be an ISO 4217 code in capitals"
	}
	if b.Unit == "" || len(b.Unit) > 16 {
		f["unit"] = "is required, at most 16 characters"
	}
	return f
}

// ValidateIndexValue returns field errors for an index quote.
func ValidateIndexValue(v IndexValue) map[string]string {
	f := map[string]string{}
	if v.AsOf.IsZero() {
		f["asOf"] = "must be a date in YYYY-MM-DD form"
	}
	if !v.Value.IsPositive() {
		f["value"] = "must be a decimal greater than 0"
	} else if v.Value.Exponent() < -6 || v.Value.GreaterThanOrEqual(maxNumeric) {
		f["value"] = "must have at most 6 decimals and fewer than 14 integer digits"
	}
	if len(v.Source) > 120 {
		f["source"] = "at most 120 characters"
	}
	return f
}

// ValidateCalc returns field errors for a price calculation.
func ValidateCalc(c CommodityCalc) map[string]string {
	f := map[string]string{}
	if c.Name == "" || len(c.Name) > 120 {
		f["name"] = "is required, at most 120 characters"
	}
	if !c.Factor.IsPositive() || c.Factor.Exponent() < -6 || c.Factor.GreaterThanOrEqual(maxNumeric) {
		f["factor"] = "must be a decimal greater than 0 with at most 6 decimals and fewer than 14 integer digits"
	}
	switch c.MarginKind {
	case MarginPercent, MarginAmount:
	default:
		f["marginKind"] = "must be PERCENT or AMOUNT"
	}
	if c.Margin.IsNegative() || c.Margin.Exponent() < -6 || c.Margin.GreaterThanOrEqual(maxNumeric) {
		f["margin"] = "must be a decimal of at least 0 with at most 6 decimals and fewer than 14 integer digits"
	} else if c.MarginKind == MarginPercent && c.Margin.GreaterThan(decimal.NewFromInt(1000)) {
		f["margin"] = "a percentage is at most 1000"
	}
	if c.Rounding < 2 || c.Rounding > 6 {
		f["rounding"] = "must be from 2 to 6 decimals"
	}
	if c.MaxIndexAgeDays < 1 || c.MaxIndexAgeDays > 365 {
		f["maxIndexAgeDays"] = "must be from 1 to 365"
	}
	switch c.CustomerKind {
	case CustomerKindAll:
		if c.CustomerKey != "" {
			f["customerKey"] = "must be empty when customerKind is all"
		}
	case CustomerKindCustomer, CustomerKindGroup:
		if c.CustomerKey == "" || len(c.CustomerKey) > 64 {
			f["customerKey"] = "is required, at most 64 characters"
		}
	default:
		f["customerKind"] = "must be customer, customer_group or all"
	}
	if c.ProductID == "" || len(c.ProductID) > 64 {
		f["productId"] = "is required, at most 64 characters"
	}
	if c.Unit == "" || len(c.Unit) > 16 {
		f["unit"] = "is required, at most 16 characters"
	}
	if !isCurrencyCode(c.Currency) {
		f["currency"] = "must be an ISO 4217 code in capitals"
	}
	return f
}

// CalcPrice is index x factor plus margin. commodity_index_rounding (PRD 5.26.13) is the
// number of decimals for index x factor only: the product is rounded half to even to the
// calculation's decimals, then the margin is added to that rounded base (an amount as is, a
// percentage of the base) and the price is rounded half to even to the unit price precision
// of 6 decimals (FR-26.7, BR-26.3). An amount margin is therefore never rounded away.
func CalcPrice(c CommodityCalc, index decimal.Decimal) decimal.Decimal {
	base := index.Mul(c.Factor).RoundBank(int32(c.Rounding))
	if c.MarginKind == MarginPercent {
		return base.Mul(decimal.NewFromInt(100).Add(c.Margin)).Shift(-2).RoundBank(UnitPricePlaces)
	}
	return base.Add(c.Margin).RoundBank(UnitPricePlaces)
}

// RunChange is one ACTIVE PRICE record a run would reprice.
type RunChange struct {
	Record    Record
	NewAmount decimal.Decimal
}

// RunPlan is the outcome of a commodity price calculation run before anything is written.
type RunPlan struct {
	Index     IndexValue
	Price     decimal.Decimal
	Stale     bool
	AgeDays   int
	Changes   []RunChange
	Unchanged int
}

// Targets reports whether r is a record the calculation reprices: an ACTIVE PRICE record on
// the calculation's customer scope, product, unit and currency at quantity break 0 without
// variant or campaign, valid on asOf.
func (c CommodityCalc) Targets(r Record, asOf time.Time) bool {
	return r.Type == TypePrice && r.Status == StatusActive && r.CustomerKind == c.CustomerKind && r.CustomerKey == c.CustomerKey &&
		r.ProductKind == ProductKindProduct && r.ProductKey == c.ProductID && r.Unit == c.Unit && r.Currency == c.Currency &&
		r.FromQuantity.IsZero() && r.Variant == "" && r.CampaignID == "" &&
		dayOf(r.ValidFrom) <= dayOf(asOf) && dayOf(r.ValidTo) >= dayOf(asOf)
}

// PlanRun prices the calculation at the given index value and lists the records that change.
// It is pure: records come in the order given (the store sorts them by id) and the same
// inputs give the same plan. A non-positive price is refused (PRC-0053).
func PlanRun(c CommodityCalc, idx IndexValue, asOf time.Time, stored []Record) (RunPlan, *Error) {
	price := CalcPrice(c, idx.Value)
	if !price.IsPositive() {
		return RunPlan{}, &Error{Code: CodePriceNotPositive, Detail: fmt.Sprintf("index %s gives a price of %s after rounding to %d decimals", idx.Value, price, c.Rounding)}
	}
	if price.GreaterThanOrEqual(maxNumeric) {
		return RunPlan{}, &Error{Code: CodePriceNotPositive, Detail: fmt.Sprintf("index %s gives a price of %s, more than a unit price can hold (fewer than 14 integer digits)", idx.Value, price)}
	}
	age := int(asOf.Sub(idx.AsOf).Hours() / 24)
	plan := RunPlan{Index: idx, Price: price, AgeDays: age, Stale: age > c.MaxIndexAgeDays}
	for _, r := range stored {
		if !c.Targets(r, asOf) {
			continue
		}
		if r.Amount.Equal(price) {
			plan.Unchanged++
			continue
		}
		plan.Changes = append(plan.Changes, RunChange{Record: r, NewAmount: price})
	}
	return plan, nil
}

// JournalLines turns the plan's changes into UPDATE lines of a price journal.
func (p RunPlan) JournalLines() []JournalLine {
	lines := make([]JournalLine, 0, len(p.Changes))
	for i, ch := range p.Changes {
		rec := ch.Record
		rec.Amount = ch.NewAmount
		lines = append(lines, JournalLine{No: i + 1, Op: OpUpdate, RecordID: rec.ID, ExpectedVersion: ch.Record.Version, Record: rec})
	}
	return lines
}
