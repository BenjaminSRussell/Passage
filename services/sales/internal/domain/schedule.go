package domain

import (
	"errors"
	"time"

	"github.com/shopspring/decimal"
)

// ReleaseBucket is one requirement date of a scheduling-agreement release with
// the cumulative quantity required up to and including that date.
type ReleaseBucket struct {
	Date        string          // yyyy-mm-dd
	CumRequired decimal.Decimal // non-decreasing across buckets
}

// ReleaseLine is an open quantity due on a date from a scheduling-agreement release.
type ReleaseLine struct {
	Date     string          `json:"date"`
	Quantity decimal.Decimal `json:"quantity"`
}

// ReleaseCheck is the reconciliation outcome for one release (BR-27.12).
type ReleaseCheck struct {
	Mismatch     bool
	Difference   decimal.Decimal // cumulative received minus cumulative shipped
	OpenQuantity decimal.Decimal // cumulative required minus cumulative shipped, floored at 0
	Lines        []ReleaseLine   // empty when Mismatch is true: a held release changes nothing
}

// CheckRelease reconciles a release's cumulative received quantity against the
// quantity shipped since the cumulative reset. A difference beyond tolerance
// holds the release with CUM_QTY_MISMATCH. Otherwise the open schedule lines are
// the increments of max(0, cumRequired - cumShipped) across the buckets, so they
// total the open quantity.
func CheckRelease(cumReceived, cumShipped, tolerance decimal.Decimal, buckets []ReleaseBucket) (ReleaseCheck, error) {
	if cumReceived.IsNegative() || cumShipped.IsNegative() || tolerance.IsNegative() {
		return ReleaseCheck{}, errors.New("cumulative quantities and tolerance must not be negative")
	}
	res := ReleaseCheck{Difference: cumReceived.Sub(cumShipped), Lines: []ReleaseLine{}}
	if res.Difference.Abs().GreaterThan(tolerance) {
		res.Mismatch = true
		res.OpenQuantity = decimal.Zero
		return res, nil
	}
	prevDate := ""
	prevCum := decimal.Zero
	prevNeed := decimal.Zero
	for i, b := range buckets {
		if _, err := time.Parse(backorderDayLayout, b.Date); err != nil {
			return ReleaseCheck{}, errors.New("bucket date must be yyyy-mm-dd")
		}
		if i > 0 && b.Date < prevDate {
			return ReleaseCheck{}, errors.New("buckets must be ordered by date")
		}
		if b.CumRequired.IsNegative() || b.CumRequired.LessThan(prevCum) {
			return ReleaseCheck{}, errors.New("cumulative required quantity must not be negative or decrease")
		}
		need := b.CumRequired.Sub(cumShipped)
		if need.IsNegative() {
			need = decimal.Zero
		}
		if delta := need.Sub(prevNeed); delta.IsPositive() {
			res.Lines = append(res.Lines, ReleaseLine{Date: b.Date, Quantity: delta})
		}
		prevNeed, prevCum, prevDate = need, b.CumRequired, b.Date
	}
	res.OpenQuantity = prevNeed
	return res, nil
}
