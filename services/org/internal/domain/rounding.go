package domain

import (
	"fmt"
	"math/big"

	"github.com/shopspring/decimal"
)

const CodeRoundingUnit = "ORG-CUR-ROUNDING-UNIT"

const quotientPrecision = 28

type Method string

const (
	HalfEven Method = "half_even"
	HalfUp   Method = "half_up"
	Down     Method = "down"
	Up       Method = "up"
)

var (
	minRoundingUnit = decimal.New(1, -6)
	maxRoundingUnit = decimal.New(1000, 0)
	two             = decimal.NewFromInt(2)
	half            = decimal.New(5, -1)
	ten             = big.NewInt(10)
)

type Purpose string

const (
	PurposeGeneral  Purpose = "general"
	PurposePrice    Purpose = "price"
	PurposePurchase Purpose = "purchase"
	PurposeSales    Purpose = "sales"
	PurposeTax      Purpose = "tax"
	PurposeCash     Purpose = "cash"
)

func ParsePurpose(s string) (Purpose, error) {
	switch p := Purpose(s); p {
	case PurposeGeneral, PurposePrice, PurposePurchase, PurposeSales, PurposeTax, PurposeCash:
		return p, nil
	}
	return "", fmt.Errorf("unknown rounding purpose %q", s)
}

func ParseMethod(s string) (Method, error) {
	switch m := Method(s); m {
	case HalfEven, HalfUp, Down, Up:
		return m, nil
	}
	return "", fmt.Errorf("unknown rounding method %q", s)
}

// Round returns unit x R(amount/unit). Ties under half_up go away from zero and
// ties under half_even go to the even multiple; down rounds toward zero and up
// rounds away from zero. A unit that is not positive returns amount unchanged.
func Round(amount, unit decimal.Decimal, method Method) decimal.Decimal {
	if !unit.IsPositive() {
		return amount
	}
	q := amount.DivRound(unit, quotientPrecision)
	var r decimal.Decimal
	switch method {
	case HalfUp:
		r = q.Round(0)
	case Down:
		r = q.Truncate(0)
	case Up:
		r = q.Ceil()
		if q.IsNegative() {
			r = q.Floor()
		}
	default:
		r = roundHalfEven(q)
	}
	return unit.Mul(r)
}

func roundHalfEven(q decimal.Decimal) decimal.Decimal {
	floor := q.Floor()
	switch q.Sub(floor).Cmp(half) {
	case -1:
		return floor
	case 1:
		return floor.Add(decimal.NewFromInt(1))
	}
	if floor.Mod(two).IsZero() {
		return floor
	}
	return floor.Add(decimal.NewFromInt(1))
}

// ValidateUnit accepts a unit from 0.000001 to 1000 that is a power of ten no finer
// than 10^-minorUnits, or 5 or 25 times a power of ten.
func ValidateUnit(unit decimal.Decimal, minorUnits int32) error {
	if minorUnits < 0 || minorUnits > 4 {
		return &Error{Code: CodeRoundingUnit, Detail: fmt.Sprintf("minor units %d are outside 0 to 4", minorUnits)}
	}
	if unit.LessThan(minRoundingUnit) || unit.GreaterThan(maxRoundingUnit) {
		return &Error{Code: CodeRoundingUnit, Detail: fmt.Sprintf("unit %s is outside 0.000001 to 1000", unit)}
	}
	coefficient := new(big.Int).Set(unit.Coefficient())
	remainder := new(big.Int)
	exponent := unit.Exponent()
	for {
		quotient, rem := new(big.Int).QuoRem(coefficient, ten, remainder)
		if rem.Sign() != 0 {
			break
		}
		coefficient = quotient
		exponent++
	}
	if coefficient.IsInt64() {
		switch coefficient.Int64() {
		case 1:
			if exponent >= -minorUnits {
				return nil
			}
			return &Error{Code: CodeRoundingUnit, Detail: fmt.Sprintf("unit %s is finer than the currency minor unit", unit)}
		case 5, 25:
			return nil
		}
	}
	return &Error{Code: CodeRoundingUnit, Detail: fmt.Sprintf("unit %s must be a power of ten or 0.05, 0.25 or 0.5 times one", unit)}
}
