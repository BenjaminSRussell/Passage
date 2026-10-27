package domain

import (
	"fmt"
	"time"

	"github.com/shopspring/decimal"
)

// MaxSnapshotLines bounds the product x site lines of one month-end snapshot.
const MaxSnapshotLines = 2000

// SnapshotLine is one product x site stream inside a month-end aging snapshot.
// Standard is read only by STANDARD and the STANDARD fallback; DefaultType is
// the default valuation type of a split-valued product (FR-20.16).
type SnapshotLine struct {
	ProductID   string
	SiteID      string
	Standard    decimal.Decimal
	DefaultType string
	Txns        []Txn
}

// SnapshotLineResult is the aging of one line, per valuation type when the
// product is split, and its tie-out to the settled remaining value.
type SnapshotLineResult struct {
	ProductID      string
	SiteID         string
	Report         AgingReport
	PerType        []TypeAging
	RemainingValue decimal.Decimal
	Ties           bool
}

// Snapshot is the aging of every line as of one date, summed per bucket.
type Snapshot struct {
	Buckets        []BucketTotal
	TotalQty       decimal.Decimal
	TotalValue     decimal.Decimal
	RemainingValue decimal.Decimal
	// Ties is true when every line's buckets equal its remaining value
	// (FR-20.17: the aging totals tie to the value report).
	Ties  bool
	Lines []SnapshotLineResult
}

// BuildSnapshot ages every line as of asOf under base, the same way the aging
// preview ages one product (AgingByValuationType), and sums the buckets. A
// product x site listed twice or a transaction dated after asOf is refused.
func BuildSnapshot(lines []SnapshotLine, asOf time.Time, buckets []Bucket, base Options) (Snapshot, error) {
	if len(lines) == 0 || len(lines) > MaxSnapshotLines {
		return Snapshot{}, &Error{CodeInvalidTxn, fmt.Sprintf("a snapshot holds 1 to %d product x site lines", MaxSnapshotLines)}
	}
	if len(buckets) == 0 {
		return Snapshot{}, &Error{CodeInvalidTxn, "aging needs at least one bucket"}
	}
	out := Snapshot{TotalQty: decimal.Zero, TotalValue: decimal.Zero, RemainingValue: decimal.Zero, Ties: true}
	for _, b := range buckets {
		out.Buckets = append(out.Buckets, BucketTotal{Label: b.Label, Qty: decimal.Zero, Value: decimal.Zero})
	}
	day := dateOnly(asOf)
	seen := make(map[[2]string]bool, len(lines))
	for _, ln := range lines {
		key := [2]string{ln.ProductID, ln.SiteID}
		if seen[key] {
			return Snapshot{}, &Error{CodeInvalidTxn, fmt.Sprintf("product %s at site %s is on more than one snapshot line", ln.ProductID, ln.SiteID)}
		}
		seen[key] = true
		for _, t := range ln.Txns {
			if dateOnly(t.FinancialDate).After(day) {
				return Snapshot{}, &Error{CodeInvalidTxn, fmt.Sprintf("transaction %s is dated after the as-of date", t.ID)}
			}
		}
		opt := base
		opt.Standard = ln.Standard
		rep, perType, remaining, err := AgingByValuationType(ln.Txns, opt, ln.DefaultType, asOf, buckets)
		if de, ok := err.(*Error); ok {
			return Snapshot{}, &Error{de.Code, fmt.Sprintf("product %s at site %s: %s", ln.ProductID, ln.SiteID, de.Detail)}
		}
		if err != nil {
			return Snapshot{}, err
		}
		ties := rep.TotalValue.Equal(remaining)
		out.Lines = append(out.Lines, SnapshotLineResult{ProductID: ln.ProductID, SiteID: ln.SiteID, Report: rep,
			PerType: perType, RemainingValue: remaining, Ties: ties})
		for i := range out.Buckets {
			out.Buckets[i].Qty = out.Buckets[i].Qty.Add(rep.Buckets[i].Qty)
			out.Buckets[i].Value = out.Buckets[i].Value.Add(rep.Buckets[i].Value)
		}
		out.TotalQty = out.TotalQty.Add(rep.TotalQty)
		out.TotalValue = out.TotalValue.Add(rep.TotalValue)
		out.RemainingValue = out.RemainingValue.Add(remaining)
		out.Ties = out.Ties && ties
	}
	return out, nil
}
