package domain

import (
	"fmt"
	"math/big"
	"sort"

	"github.com/shopspring/decimal"
)

// Bundle split bases (BR-25.1, site setting "Bundle split basis", default quantity).
const (
	SplitByQuantity = "quantity"
	SplitEqual      = "equal"
)

// CodeBundleInvalid is returned when a bundle cannot be split (no jobs, negative
// hours or quantity, an unknown basis) or when a bundle's jobs arrive in more than
// one batch, which would count its hours twice.
const CodeBundleInvalid = "SF-0113"

// BundleMember is one job of a bundle with the good quantity reported for it.
// Quantities are decimals (qty_good is numeric(18,4)).
type BundleMember struct {
	JobID   string
	QtyGood decimal.Decimal
}

// SplitBundleHours splits the hours of a bundle across its jobs (BR-25.1). The
// result has the same order as members. The total is rounded to 0.01 h first
// (the job-card precision); shares are whole hundredths that sum exactly to it:
// the largest remainder method hands the leftover hundredths to the largest
// fractional shares, ties going to the earlier member. Basis quantity weights by
// QtyGood; when the quantities sum to zero, or the basis is equal, every job gets
// the same weight. The arithmetic is exact integer arithmetic, so no quantity or
// duration can overflow it.
func SplitBundleHours(total decimal.Decimal, members []BundleMember, basis string) ([]decimal.Decimal, error) {
	if len(members) == 0 {
		return nil, &Error{Code: CodeBundleInvalid, Detail: "a bundle needs at least one job"}
	}
	if total.IsNegative() {
		return nil, &Error{Code: CodeBundleInvalid, Detail: "bundle hours cannot be negative"}
	}
	if basis != SplitByQuantity && basis != SplitEqual {
		return nil, &Error{Code: CodeBundleInvalid, Detail: fmt.Sprintf("unknown split basis %q", basis)}
	}
	weights := make([]*big.Int, len(members))
	sum := new(big.Int)
	if basis == SplitByQuantity {
		// Scale every quantity by the same power of ten so the weights are integers.
		exp := int32(0)
		for _, m := range members {
			if m.QtyGood.IsNegative() {
				return nil, &Error{Code: CodeBundleInvalid, Detail: fmt.Sprintf("job %s has a negative good quantity", m.JobID)}
			}
			if e := m.QtyGood.Exponent(); e < exp {
				exp = e
			}
		}
		for i, m := range members {
			weights[i] = m.QtyGood.Shift(-exp).BigInt()
			sum.Add(sum, weights[i])
		}
	}
	if sum.Sign() == 0 {
		// Equal basis, or no good quantity reported: every job weighs the same.
		for i := range weights {
			weights[i] = big.NewInt(1)
		}
		sum.SetInt64(int64(len(members)))
	}
	cents := total.Round(2).Shift(2).BigInt()

	shares := make([]*big.Int, len(members))
	rems := make([]*big.Int, len(members))
	assigned := new(big.Int)
	for i, w := range weights {
		shares[i], rems[i] = new(big.Int).QuoRem(new(big.Int).Mul(cents, w), sum, new(big.Int))
		assigned.Add(assigned, shares[i])
	}
	order := make([]int, len(members))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool { return rems[order[a]].Cmp(rems[order[b]]) > 0 })
	// The leftover is below len(members): each remainder is below sum.
	left := new(big.Int).Sub(cents, assigned).Int64()
	for k := int64(0); k < left; k++ {
		shares[order[k]].Add(shares[order[k]], big.NewInt(1))
	}
	out := make([]decimal.Decimal, len(members))
	for i, s := range shares {
		out[i] = decimal.NewFromBigInt(s, -2)
	}
	return out, nil
}
