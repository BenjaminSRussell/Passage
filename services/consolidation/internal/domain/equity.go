package domain

import (
	"fmt"

	"github.com/shopspring/decimal"
)

// RuleEquity is the rule code of the equity-method entries (FR-40.18).
const RuleEquity = "EQ"

// Accounts used by the equity method.
const (
	AcctInvestmentAssociate = "INVESTMENT_ASSOCIATE"
	AcctShareOfAssociates   = "SHARE_OF_PROFIT_ASSOCIATES"
	AcctDividendIncome      = "DIVIDEND_INCOME"
	// AcctAssociateTranslation is the translation reserve (OCI) the exchange difference on a foreign associate's carrying amount goes
	// to (IAS 21.39(c), IAS 21.44, IAS 28.10).
	AcctAssociateTranslation = "ASSOCIATE_TRANSLATION_RESERVE"
)

// RuleEquityFX is the rule code of the retranslation of a foreign associate's carrying amount.
const RuleEquityFX = "EQ-FX"

// Associate holds the inputs of the equity method for one associate or joint
// venture. Associates are not line-consolidated (FR-40.18).
type Associate struct {
	Currency          string
	OwnershipPct      decimal.Decimal
	OpeningCarrying   decimal.Decimal
	NetIncome         decimal.Decimal
	DividendsReceived decimal.Decimal
	// Foreign is set for an associate whose functional currency is not the group currency. The carrying amount is then kept in the
	// associate's currency and retranslated at the closing rate (IAS 21.44, IAS 28): OpeningCarrying, NetIncome and DividendsReceived
	// above are not used for the computation, only Foreign's amounts.
	Foreign *ForeignAssociate
}

// ForeignAssociate is the equity method of a foreign associate in its own currency, with the rates of the period. Rates are group
// currency per unit of the associate's currency.
type ForeignAssociate struct {
	UnitCurrency    string
	OpeningCarrying decimal.Decimal // associate currency
	NetIncome       decimal.Decimal // the associate's whole profit of the period, associate currency
	Dividends       decimal.Decimal // the investor's share of the dividends the associate declared, associate currency
	OpeningRate     decimal.Decimal
	AverageRate     decimal.Decimal
	ClosingRate     decimal.Decimal
}

// EquityResult is the share of profit, the closing carrying amount and the entries.
// UnrecognizedLoss is the part of the investor's share of a loss that exceeds the
// carrying amount and is not recognized (IAS 28.38); it is zero otherwise.
type EquityResult struct {
	ShareOfProfit    decimal.Decimal
	UnrecognizedLoss decimal.Decimal
	ClosingCarrying  decimal.Decimal
	Entries          []Entry
	// For a foreign associate: the closing carrying amount in its own currency and the exchange difference (group currency) the
	// retranslation booked to the translation reserve.
	ClosingCarryingUnit decimal.Decimal
	FXDifference        decimal.Decimal
}

// EquityMethod applies the equity method (FR-40.18). Share of profit is ownership
// × associate net income. Dividends received reduce the investment. The worked
// example is 30 % of 200,000.00 with 300,000.00 opening and 20,000.00 dividends,
// which gives 60,000.00 share of profit and a 340,000.00 carrying amount.
//
// A share of losses is recognized only down to a zero carrying amount; the rest
// is reported as UnrecognizedLoss (IAS 28.38). Dividends above the carrying
// amount after the share of profit are rejected, because the investment cannot
// go negative and the excess needs a top-side entry (FR-40.19).
func EquityMethod(a Associate) (EquityResult, error) {
	if len(a.Currency) != 3 {
		return EquityResult{}, invalid("currency must be an ISO 4217 code")
	}
	if !a.OwnershipPct.IsPositive() || a.OwnershipPct.GreaterThan(decimal.NewFromInt(50)) {
		return EquityResult{}, invalid("equity method applies above 0 and at most 50 percent ownership")
	}
	if a.Foreign != nil {
		return foreignEquityMethod(a)
	}
	if a.DividendsReceived.IsNegative() || a.OpeningCarrying.IsNegative() {
		return EquityResult{}, invalid("opening carrying amount and dividends received must not be negative")
	}
	opening := round(a.OpeningCarrying, a.Currency)
	share := round(a.NetIncome.Mul(a.OwnershipPct).Div(decimal.NewFromInt(100)), a.Currency)
	unrecognized := decimal.Zero
	if opening.Add(share).IsNegative() {
		unrecognized = opening.Add(share).Neg()
		share = opening.Neg()
	}
	div := round(a.DividendsReceived, a.Currency)
	closing := opening.Add(share).Sub(div)
	if closing.IsNegative() {
		return EquityResult{}, invalid(fmt.Sprintf("dividends received exceed the carrying amount by %s", closing.Neg().StringFixed(2)))
	}
	res := EquityResult{
		ShareOfProfit:    share,
		UnrecognizedLoss: unrecognized,
		ClosingCarrying:  closing,
	}
	res.Entries = leg(res.Entries, RuleEquity, AcctInvestmentAssociate, share)
	res.Entries = leg(res.Entries, RuleEquity, AcctShareOfAssociates, share.Neg())
	res.Entries = leg(res.Entries, RuleEquity, AcctDividendIncome, div)
	res.Entries = leg(res.Entries, RuleEquity, AcctInvestmentAssociate, div.Neg())
	if err := CheckBalanced(res.Entries); err != nil {
		return EquityResult{}, err
	}
	return res, nil
}

// foreignEquityMethod applies the equity method in the associate's own currency and retranslates the carrying amount at the closing
// rate (IAS 21.44, IAS 28.25 with IAS 21.39). Flagged for accountant sign-off (#512).
//
//	opening (FC)  = previous closing (FC), or the cost at the rate of acquisition
//	share   (FC)  = ownership x profit, limited to the carrying amount (IAS 28.38)
//	closing (FC)  = opening + share - dividends
//	group amounts: opening at the opening rate, share and dividends at the average rate, closing at the closing rate
//	exchange difference = closing - (opening + share - dividends), all in group currency, to the translation reserve (OCI)
//
// Each group amount is rounded once to the group currency's minor unit, so the entries balance by construction.
func foreignEquityMethod(a Associate) (EquityResult, error) {
	f := a.Foreign
	if len(f.UnitCurrency) != 3 {
		return EquityResult{}, invalid("associate currency must be an ISO 4217 code")
	}
	if !f.OpeningRate.IsPositive() || !f.AverageRate.IsPositive() || !f.ClosingRate.IsPositive() {
		return EquityResult{}, invalid("a foreign associate needs positive opening, average and closing rates")
	}
	if f.Dividends.IsNegative() || f.OpeningCarrying.IsNegative() {
		return EquityResult{}, invalid("opening carrying amount and dividends received must not be negative")
	}
	unitPlaces := f.UnitCurrency
	openFC := round(f.OpeningCarrying, unitPlaces)
	shareFC := round(f.NetIncome.Mul(a.OwnershipPct).Div(decimal.NewFromInt(100)), unitPlaces)
	unrecognizedFC := decimal.Zero
	if openFC.Add(shareFC).IsNegative() {
		unrecognizedFC = openFC.Add(shareFC).Neg()
		shareFC = openFC.Neg()
	}
	divFC := round(f.Dividends, unitPlaces)
	closeFC := openFC.Add(shareFC).Sub(divFC)
	if closeFC.IsNegative() {
		return EquityResult{}, invalid(fmt.Sprintf("dividends received exceed the carrying amount by %s %s", closeFC.Neg().StringFixed(2), f.UnitCurrency))
	}
	cur := a.Currency
	open := round(openFC.Mul(f.OpeningRate), cur)
	share := round(shareFC.Mul(f.AverageRate), cur)
	div := round(divFC.Mul(f.AverageRate), cur)
	closing := round(closeFC.Mul(f.ClosingRate), cur)
	fx := closing.Sub(open).Sub(share).Add(div)
	res := EquityResult{
		ShareOfProfit:       share,
		UnrecognizedLoss:    round(unrecognizedFC.Mul(f.AverageRate), cur),
		ClosingCarrying:     closing,
		ClosingCarryingUnit: closeFC,
		FXDifference:        fx,
	}
	res.Entries = leg(res.Entries, RuleEquity, AcctInvestmentAssociate, share)
	res.Entries = leg(res.Entries, RuleEquity, AcctShareOfAssociates, share.Neg())
	res.Entries = leg(res.Entries, RuleEquity, AcctDividendIncome, div)
	res.Entries = leg(res.Entries, RuleEquity, AcctInvestmentAssociate, div.Neg())
	res.Entries = leg(res.Entries, RuleEquityFX, AcctInvestmentAssociate, fx)
	res.Entries = leg(res.Entries, RuleEquityFX, AcctAssociateTranslation, fx.Neg())
	if err := CheckBalanced(res.Entries); err != nil {
		return EquityResult{}, err
	}
	return res, nil
}

// Divestiture holds the inputs of a full disposal of a subsidiary (FR-40.18).
type Divestiture struct {
	Currency          string
	Proceeds          decimal.Decimal
	NetAssetsCarrying decimal.Decimal
	Goodwill          decimal.Decimal
	NCICarrying       decimal.Decimal
	CTARecycled       decimal.Decimal
}

// DivestitureGain is proceeds − (group carrying amount of net assets + goodwill −
// NCI) + CTA recycled from OCI, rounded to the currency minor unit.
func DivestitureGain(d Divestiture) (decimal.Decimal, error) {
	if len(d.Currency) != 3 {
		return decimal.Zero, invalid("currency must be an ISO 4217 code")
	}
	gain := d.Proceeds.Sub(d.NetAssetsCarrying.Add(d.Goodwill).Sub(d.NCICarrying)).Add(d.CTARecycled)
	return round(gain, d.Currency), nil
}

// CheckChangeOfControl rejects a move from an existing equity interest to control
// (CNS-0910). Such a step acquisition needs remeasurement of the held interest,
// which is out of scope, so the user records top-side entries (FR-40.19). A first
// acquisition from zero is not a step.
func CheckChangeOfControl(prior, next decimal.Decimal) error {
	hundred := decimal.NewFromInt(100)
	if prior.IsNegative() || next.IsNegative() || prior.GreaterThan(hundred) || next.GreaterThan(hundred) {
		return invalid("ownership must be between 0 and 100 percent")
	}
	half := decimal.NewFromInt(50)
	if prior.IsPositive() && !prior.GreaterThan(half) && next.GreaterThan(half) {
		return &Error{Code: CodeChangeOfControl, Detail: fmt.Sprintf("ownership moves from %s%% to %s%%; step acquisition is not supported", prior.StringFixed(2), next.StringFixed(2))}
	}
	return nil
}
