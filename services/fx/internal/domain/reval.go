package domain

import (
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/BenjaminSRussell/passage/platform/money"
	"github.com/shopspring/decimal"
)

// Balance is an account's balance in a foreign currency (BalanceTxn) and its carrying value in the
// accounting currency (BalanceAcct). Dimensions is an opaque, caller-defined key echoed to RevalLine.
type Balance struct {
	Account     string
	Dimensions  string
	Currency    string
	BalanceTxn  decimal.Decimal
	BalanceAcct decimal.Decimal
}

// RevalLine is the revaluation adjustment for one Balance. Adjustment is positive for a gain and
// negative for a loss; exactly one of Gain and Loss is non-zero. The adjustment posts on PostingDate
// and reverses on ReversalDate.
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

// ReversalDate is the day after period end.
func ReversalDate(periodEnd time.Time) time.Time {
	return day(periodEnd).AddDate(0, 0, 1)
}

// Revalue computes closing-rate revaluation lines for every non-zero balance in a currency other than
// acctCurrency. It is all-or-nothing: if any closing rate is absent or not positive it returns no
// lines and an ErrRateMissing naming every such currency in sorted order.
func Revalue(bals []Balance, closing map[string]decimal.Decimal, acctCurrency string, periodEnd time.Time) ([]RevalLine, error) {
	posting := day(periodEnd)
	missing := map[string]struct{}{}
	out := make([]RevalLine, 0, len(bals))
	for _, b := range bals {
		if b.BalanceTxn.IsZero() || b.Currency == acctCurrency {
			continue
		}
		r, ok := closing[b.Currency]
		if !ok || r.Sign() <= 0 {
			missing[b.Currency] = struct{}{}
			continue
		}
		revalued := money.Amount{Value: b.BalanceTxn.Mul(r), Currency: acctCurrency}.Round()
		adj := money.Amount{Value: revalued.Value.Sub(b.BalanceAcct), Currency: acctCurrency}
		out = append(out, RevalLine{
			Account: b.Account, Dimensions: b.Dimensions, Currency: b.Currency, Adjustment: adj,
			Gain:        money.Amount{Value: decimal.Max(adj.Value, decimal.Zero), Currency: acctCurrency},
			Loss:        money.Amount{Value: decimal.Max(adj.Value.Neg(), decimal.Zero), Currency: acctCurrency},
			PostingDate: posting, ReversalDate: ReversalDate(periodEnd),
		})
	}
	if len(missing) > 0 {
		cur := slices.Sorted(maps.Keys(missing))
		return nil, fmt.Errorf("%w: no closing rate to %s on %s for %s", ErrRateMissing, acctCurrency, posting.Format(time.DateOnly), strings.Join(cur, ", "))
	}
	return out, nil
}
