package domain

import (
	"fmt"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/money"
)

// POCInput is the cost-to-cost position of one fixed-price project at a period
// end (BR-41.1). Amounts are in the project currency.
type POCInput struct {
	Currency            string
	ContractValue       decimal.Decimal
	CostToDate          decimal.Decimal
	EstimatedToComplete decimal.Decimal
	BilledToDate        decimal.Decimal
	PriorRevenueToDate  decimal.Decimal
}

// POCResult is the estimate outcome for the period.
type POCResult struct {
	EAC               decimal.Decimal // estimated at completion: cost to date + ETC
	POC               decimal.Decimal // completion fraction, 4 decimals (display)
	RevenueToDate     decimal.Decimal
	RevenueThisPeriod decimal.Decimal
	AccruedRevenue    decimal.Decimal // revenue earned but not billed (asset)
	DeferredRevenue   decimal.Decimal // billed in excess of revenue earned (liability)
	GrossProfitToDate decimal.Decimal
	LossProvision     decimal.Decimal // full expected loss, EAC above contract value (BR-41.2)
}

// ComputePOC applies BR-41.1 and BR-41.2. Revenue uses the unrounded completion
// ratio and rounds once, to the currency minor unit (half-even), which is what
// the PRD worked example and AC-41.1 check (288,888.89); the 4-decimal POC is
// reported for display only.
func ComputePOC(in POCInput) (POCResult, error) {
	if len(in.Currency) != 3 {
		return POCResult{}, fmt.Errorf("%w: currency", ErrInvalidInput)
	}
	for _, v := range []decimal.Decimal{in.ContractValue, in.CostToDate, in.EstimatedToComplete, in.BilledToDate, in.PriorRevenueToDate} {
		if v.IsNegative() {
			return POCResult{}, fmt.Errorf("%w: amounts must not be negative", ErrInvalidInput)
		}
	}
	round := func(d decimal.Decimal) decimal.Decimal { return d.RoundBank(money.MinorUnits(in.Currency)) }

	eac := in.CostToDate.Add(in.EstimatedToComplete)
	ratio := decimal.Zero
	if eac.IsPositive() {
		ratio = in.CostToDate.DivRound(eac, 20)
	}
	revenue := round(in.ContractValue.Mul(ratio))
	zero := decimal.Zero
	accrued := decimal.Max(revenue.Sub(in.BilledToDate), zero)
	deferred := decimal.Max(in.BilledToDate.Sub(revenue), zero)
	return POCResult{
		EAC:               eac,
		POC:               ratio.RoundBank(4),
		RevenueToDate:     revenue,
		RevenueThisPeriod: revenue.Sub(in.PriorRevenueToDate),
		AccruedRevenue:    accrued,
		DeferredRevenue:   deferred,
		GrossProfitToDate: revenue.Sub(in.CostToDate),
		LossProvision:     decimal.Max(eac.Sub(in.ContractValue), zero),
	}, nil
}

// ProvisionMovement is the GL change needed to move the loss provision from its
// prior balance to the required one. The provision never goes below zero, so a
// later improvement releases it (BR-41.2).
func ProvisionMovement(prior, required decimal.Decimal) decimal.Decimal {
	return decimal.Max(required, decimal.Zero).Sub(prior)
}
