package domain

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/BenjaminSRussell/passage/platform/money"
	"github.com/shopspring/decimal"
)

var (
	// ErrRoundingExceeded means the residual after rounding is larger than half a minor unit per line.
	ErrRoundingExceeded = errors.New("LEDGER_ROUNDING_EXCEEDED")
	// ErrUnbalanced means an entry does not net to zero in every currency or has no line to absorb a residual.
	ErrUnbalanced = errors.New("LEDGER_UNBALANCED")
)

// Mode selects the rounding rule applied to converted amounts.
type Mode int

const (
	// HalfAwayFromZero rounds ties away from zero.
	HalfAwayFromZero Mode = iota
	// HalfEven rounds ties to the even neighbour (banker's rounding).
	HalfEven
)

// Line is one journal line in its transaction currency; No identifies it in errors and breaks ties.
type Line struct {
	No     int
	Amount money.Amount
}

// Target is a destination currency and, keyed by source currency, the Resolution used to reach it.
type Target struct {
	Currency    string
	Resolutions map[string]Resolution
}

// RateSet holds the accounting and reporting targets of an entry.
type RateSet struct {
	Acct Target
	Rpt  Target
}

// ConvertedLine is a Line with its accounting and reporting amounts; AcctAdjusted and
// RptAdjusted mark the line that absorbed the rounding residual in that column.
type ConvertedLine struct {
	No           int
	Txn          money.Amount
	Acct         money.Amount
	Rpt          money.Amount
	AcctAdjusted bool
	RptAdjusted  bool
}

// Convert converts a to the currency to using res, rounding once to the target's minor units.
func Convert(a money.Amount, to string, res Resolution) (money.Amount, error) {
	return convert(a, to, res, HalfAwayFromZero)
}

func convert(a money.Amount, to string, res Resolution, mode Mode) (money.Amount, error) {
	if err := checkCurrency(a.Currency); err != nil {
		return money.Amount{}, err
	}
	if err := checkCurrency(to); err != nil {
		return money.Amount{}, err
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
	if mode == HalfEven {
		return money.Amount{Value: v.RoundBank(money.MinorUnits(to)), Currency: to}, nil
	}
	return money.Amount{Value: v, Currency: to}.Round(), nil
}

// ConvertEntry converts a balanced entry into the accounting and reporting currencies. Every
// currency must net to zero in the transaction column, and each derived column is reconciled
// to zero by moving the rounding residual onto the largest line. Currency codes are validated
// like Convert, and unbalanced currencies are reported in sorted order.
func ConvertEntry(lines []Line, rates RateSet, mode Mode) ([]ConvertedLine, error) {
	if len(lines) == 0 {
		return nil, fmt.Errorf("%w: no lines", ErrUnbalanced)
	}
	if err := checkCurrency(rates.Acct.Currency); err != nil {
		return nil, err
	}
	if err := checkCurrency(rates.Rpt.Currency); err != nil {
		return nil, err
	}
	sums := map[string]decimal.Decimal{}
	for _, l := range lines {
		if err := checkCurrency(l.Amount.Currency); err != nil {
			return nil, fmt.Errorf("line %d: %w", l.No, err)
		}
		sums[l.Amount.Currency] = sums[l.Amount.Currency].Add(l.Amount.Value)
	}
	var off []string
	for _, cur := range slices.Sorted(maps.Keys(sums)) {
		if !sums[cur].IsZero() {
			off = append(off, fmt.Sprintf("%s off by %s", cur, sums[cur]))
		}
	}
	if len(off) > 0 {
		return nil, fmt.Errorf("%w: %s", ErrUnbalanced, strings.Join(off, ", "))
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
			return nil, nil, fmt.Errorf("line %d: %w", l.No, err)
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
