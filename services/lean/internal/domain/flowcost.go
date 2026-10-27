package domain

import (
	"fmt"
	"strings"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/money"
)

// Flow costing period statuses (PRD 4.24.4 FlowCostingPeriod).
const (
	PeriodOpen    = "open"
	PeriodSettled = "settled"
)

// maxAmount bounds a period amount so it fits numeric(20,6).
func maxAmount() decimal.Decimal { return decimal.New(1, 14) }

// maxListedCards is how many open cards a LEAN-0003 message names.
const maxListedCards = 20

// PeriodCosts are the amounts a flow period settles with (FR-24.12), all in
// the period's currency.
type PeriodCosts struct {
	Material   money.Amount
	Conversion money.Amount
	Output     money.Amount
	Variance   money.Amount
}

// ValidCurrency reports whether c is a three-letter upper-case ISO 4217 code.
func ValidCurrency(c string) bool {
	if len(c) != 3 {
		return false
	}
	for _, r := range c {
		if r < 'A' || r > 'Z' {
			return false
		}
	}
	return true
}

// SettleCosts applies BR-24.9: variance = material + conversion − output.
// Each amount must be zero or more, below 10^14 and carry no more decimals
// than the currency's minor unit, so nothing is rounded away; the variance is
// then exact. Violations are COM-0001.
func SettleCosts(currency string, material, conversion, output decimal.Decimal) (PeriodCosts, error) {
	if !ValidCurrency(currency) {
		return PeriodCosts{}, errorf(CodeInvalid, "currency must be a three-letter ISO 4217 code")
	}
	amount := func(name string, v decimal.Decimal) (money.Amount, error) {
		if v.IsNegative() || !v.LessThan(maxAmount()) {
			return money.Amount{}, errorf(CodeInvalid, "%s must be zero or more and below 10^14", name)
		}
		if minor := money.MinorUnits(currency); !v.Equal(v.Round(minor)) {
			return money.Amount{}, errorf(CodeInvalid, "%s has more than %d decimals for %s", name, minor, currency)
		}
		return money.Amount{Value: v, Currency: currency}.Round(), nil
	}
	var c PeriodCosts
	var err error
	if c.Material, err = amount("materialCost", material); err != nil {
		return PeriodCosts{}, err
	}
	if c.Conversion, err = amount("conversionCost", conversion); err != nil {
		return PeriodCosts{}, err
	}
	if c.Output, err = amount("outputValue", output); err != nil {
		return PeriodCosts{}, err
	}
	if c.Variance, err = money.Sum(currency, c.Material, c.Conversion, c.Output.Neg()); err != nil {
		return PeriodCosts{}, err
	}
	return c, nil
}

// CheckSettleable implements BR-24.9: a flow period settles only when every card
// of the period is Completed or Emptied. The caller passes the cards that have
// jobs in the period, with ID set to what the message should name (the store
// passes card numbers). Otherwise the result is LEAN-0003 listing the open
// cards (the first 20, then how many more).
func CheckSettleable(kanbans []Kanban) error {
	var open []string
	for _, k := range kanbans {
		if k.State != Completed && k.State != Emptied {
			open = append(open, k.ID)
		}
	}
	if len(open) == 0 {
		return nil
	}
	listed := open
	more := ""
	if len(open) > maxListedCards {
		listed = open[:maxListedCards]
		more = fmt.Sprintf(" and %d more", len(open)-maxListedCards)
	}
	return errorf(CodeOpenKanbans, "period cannot be settled: %d kanbans open (%s%s)", len(open), strings.Join(listed, ", "), more)
}
