package domain

import (
	"fmt"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/money"
)

// EVMInput is the earned-value position as of a date (FR-41.10).
type EVMInput struct {
	Currency string
	BAC      decimal.Decimal // budget at completion
	PV       decimal.Decimal // planned value
	AC       decimal.Decimal // actual cost
	POC      decimal.Decimal // completion fraction, 0..1
}

// EVMResult holds the indices. CPI, SPI and EAC are nil when their denominator
// is zero, so callers cannot mistake an undefined index for a value.
type EVMResult struct {
	EV  decimal.Decimal
	CPI *decimal.Decimal
	SPI *decimal.Decimal
	EAC *decimal.Decimal
	VAC *decimal.Decimal
}

// ComputeEVM applies EV = BAC × POC, CPI = EV / AC, SPI = EV / PV, EAC = BAC / CPI
// and VAC = BAC − EAC. Indices round to 4 decimals and money to the minor unit.
func ComputeEVM(in EVMInput) (EVMResult, error) {
	if len(in.Currency) != 3 {
		return EVMResult{}, fmt.Errorf("%w: currency", ErrInvalidInput)
	}
	if in.POC.IsNegative() || in.POC.GreaterThan(decimal.NewFromInt(1)) {
		return EVMResult{}, fmt.Errorf("%w: poc must be between 0 and 1", ErrInvalidInput)
	}
	ev := in.BAC.Mul(in.POC).RoundBank(money.MinorUnits(in.Currency))
	res := EVMResult{EV: ev}
	if in.AC.IsPositive() {
		cpi := ev.DivRound(in.AC, 4)
		res.CPI = &cpi
		if in.BAC.IsPositive() && cpi.IsPositive() {
			eac := in.BAC.DivRound(cpi, money.MinorUnits(in.Currency))
			vac := in.BAC.Sub(eac)
			res.EAC = &eac
			res.VAC = &vac
		}
	}
	if in.PV.IsPositive() {
		spi := ev.DivRound(in.PV, 4)
		res.SPI = &spi
	}
	return res, nil
}
