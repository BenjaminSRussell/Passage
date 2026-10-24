package domain

import (
	"fmt"
	"time"

	"github.com/BenjaminSRussell/passage/platform/money"
	"github.com/shopspring/decimal"
)

type Balance struct {
	Account     string
	Dimensions  string
	Currency    string
	BalanceTxn  decimal.Decimal
	BalanceAcct decimal.Decimal
}

type RevalLine struct {
	Account      string
	Dimensions   string
	Currency     string
	Adjustment   money.Amount
	Gain         money.Amount
	Loss         money.Amount
	PostingDate  time.Time
	ReversalDate time.Time
}

func ReversalDate(periodEnd time.Time) time.Time {
	return day(periodEnd).AddDate(0, 0, 1)
}

func Revalue(bals []Balance, closing map[string]decimal.Decimal, acctCurrency string, periodEnd time.Time) ([]RevalLine, error) {
	selected := make([]Balance, 0, len(bals))
	for _, b := range bals {
		if b.BalanceTxn.IsZero() || b.Currency == acctCurrency {
			continue
		}
		if r, ok := closing[b.Currency]; !ok || r.Sign() <= 0 {
			return nil, fmt.Errorf("%w: no closing rate for %s/%s on %s", ErrRateMissing, b.Currency, acctCurrency, day(periodEnd).Format(time.DateOnly))
		}
		selected = append(selected, b)
	}
	out := make([]RevalLine, 0, len(selected))
	for _, b := range selected {
		revalued := money.Amount{Value: b.BalanceTxn.Mul(closing[b.Currency]), Currency: acctCurrency}.Round()
		adj := money.Amount{Value: revalued.Value.Sub(b.BalanceAcct), Currency: acctCurrency}
		line := RevalLine{
			Account: b.Account, Dimensions: b.Dimensions, Currency: b.Currency, Adjustment: adj,
			Gain:        money.Amount{Value: decimal.Max(adj.Value, decimal.Zero), Currency: acctCurrency},
			Loss:        money.Amount{Value: decimal.Max(adj.Value.Neg(), decimal.Zero), Currency: acctCurrency},
			PostingDate: day(periodEnd), ReversalDate: ReversalDate(periodEnd),
		}
		out = append(out, line)
	}
	return out, nil
}
