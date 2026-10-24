package domain

import (
	"errors"
	"fmt"

	"github.com/BenjaminSRussell/passage/platform/money"
	"github.com/shopspring/decimal"
)

var (
	ErrRoundingExceeded = errors.New("LEDGER_ROUNDING_EXCEEDED")
	ErrUnbalanced       = errors.New("LEDGER_UNBALANCED")
)

type Mode int

const (
	HalfAwayFromZero Mode = iota
	HalfEven
)

type Line struct {
	No     int
	Amount money.Amount
}

type Target struct {
	Currency    string
	Resolutions map[string]Resolution
}

type RateSet struct {
	Acct Target
	Rpt  Target
}

type ConvertedLine struct {
	No           int
	Txn          money.Amount
	Acct         money.Amount
	Rpt          money.Amount
	AcctAdjusted bool
	RptAdjusted  bool
}

func Convert(a money.Amount, to string, res Resolution) (money.Amount, error) {
	return convert(a, to, res, HalfAwayFromZero)
}

func convert(a money.Amount, to string, res Resolution, mode Mode) (money.Amount, error) {
	if len(a.Currency) != 3 {
		return money.Amount{}, fmt.Errorf("%w: %q", money.ErrCurrencyMismatch, a.Currency)
	}
	if a.Currency == to {
		return a, nil
	}
	if len(res.Factors) == 0 {
		return money.Amount{}, fmt.Errorf("%w: %s/%s", ErrRateMissing, a.Currency, to)
	}
	v := a.Value
	for _, f := range res.Factors {
		v = v.Mul(f)
	}
	out := money.Amount{Value: v, Currency: to}
	if mode == HalfEven {
		return money.Amount{Value: v.RoundBank(money.MinorUnits(to)), Currency: to}, nil
	}
	return out.Round(), nil
}

func ConvertEntry(lines []Line, rates RateSet, mode Mode) ([]ConvertedLine, error) {
	if len(lines) == 0 {
		return nil, fmt.Errorf("%w: no lines", ErrUnbalanced)
	}
	sums := map[string]decimal.Decimal{}
	for _, l := range lines {
		if len(l.Amount.Currency) != 3 {
			return nil, fmt.Errorf("%w: line %d currency %q", money.ErrCurrencyMismatch, l.No, l.Amount.Currency)
		}
		sums[l.Amount.Currency] = sums[l.Amount.Currency].Add(l.Amount.Value)
	}
	for cur, s := range sums {
		if !s.IsZero() {
			return nil, fmt.Errorf("%w: %s off by %s", ErrUnbalanced, cur, s)
		}
	}
	acct, acctAdj, err := deriveColumn(lines, rates.Acct, mode)
	if err != nil {
		return nil, err
	}
	rpt, rptAdj, err := deriveColumn(lines, rates.Rpt, mode)
	if err != nil {
		return nil, err
	}
	out := make([]ConvertedLine, len(lines))
	for i, l := range lines {
		out[i] = ConvertedLine{
			No: l.No, Txn: l.Amount,
			Acct:         money.Amount{Value: acct[i], Currency: rates.Acct.Currency},
			Rpt:          money.Amount{Value: rpt[i], Currency: rates.Rpt.Currency},
			AcctAdjusted: acctAdj[i], RptAdjusted: rptAdj[i],
		}
	}
	return out, nil
}

func deriveColumn(lines []Line, t Target, mode Mode) ([]decimal.Decimal, []bool, error) {
	rounded := make([]decimal.Decimal, len(lines))
	for i, l := range lines {
		c, err := convert(l.Amount, t.Currency, t.Resolutions[l.Amount.Currency], mode)
		if err != nil {
			return nil, nil, err
		}
		rounded[i] = c.Value
	}
	return allocateResidual(lines, rounded, t.Currency)
}

func allocateResidual(lines []Line, rounded []decimal.Decimal, currency string) ([]decimal.Decimal, []bool, error) {
	adjusted := make([]bool, len(lines))
	residual := decimal.Zero
	for _, v := range rounded {
		residual = residual.Add(v)
	}
	if residual.IsZero() {
		return rounded, adjusted, nil
	}
	unit := decimal.New(1, -money.MinorUnits(currency))
	tolerance := unit.Mul(decimal.NewFromInt(int64(len(lines)))).Div(decimal.NewFromInt(2))
	if residual.Abs().GreaterThan(tolerance) {
		return nil, nil, fmt.Errorf("%w: residual %s %s exceeds tolerance %s", ErrRoundingExceeded, residual, currency, tolerance)
	}
	target := -1
	for i, l := range lines {
		if l.Amount.Value.Sign() != residual.Sign() {
			continue
		}
		if target < 0 || l.Amount.Value.Abs().GreaterThan(lines[target].Amount.Value.Abs()) ||
			(l.Amount.Value.Abs().Equal(lines[target].Amount.Value.Abs()) && l.No < lines[target].No) {
			target = i
		}
	}
	if target < 0 {
		return nil, nil, fmt.Errorf("%w: no line can absorb residual %s %s", ErrUnbalanced, residual, currency)
	}
	out := append([]decimal.Decimal(nil), rounded...)
	out[target] = out[target].Sub(residual)
	adjusted[target] = true
	return out, adjusted, nil
}
