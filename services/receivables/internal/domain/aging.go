package domain

import (
	"sort"
	"time"

	"github.com/shopspring/decimal"
)

// Bucket is one aging column (FR-30.6, default period definition 0/30/60/90/180).
type Bucket string

const (
	BucketCurrent Bucket = "CURRENT"
	Bucket1to30   Bucket = "1-30"
	Bucket31to60  Bucket = "31-60"
	Bucket61to90  Bucket = "61-90"
	Bucket91to180 Bucket = "91-180"
	BucketOver180 Bucket = "OVER-180"
)

// bucketOrder is the display order of aging columns.
var bucketOrder = []Bucket{BucketCurrent, Bucket1to30, Bucket31to60, Bucket61to90, Bucket91to180, BucketOver180}

// AgingRow is one open item as seen by the aging run.
type AgingRow struct {
	Currency   string
	DueDate    time.Time
	OpenAmount decimal.Decimal
}

// AgingTotal is the sum of open amounts for one currency and bucket.
type AgingTotal struct {
	Currency string          `json:"currency"`
	Bucket   Bucket          `json:"bucket"`
	Amount   decimal.Decimal `json:"amount"`
}

// BucketFor returns the aging column for an item due on due, measured at asOf
// by whole days overdue. Items not yet due are CURRENT.
func BucketFor(asOf, due time.Time) Bucket {
	days := int(dayOf(asOf).Sub(dayOf(due)) / (24 * time.Hour))
	switch {
	case days <= 0:
		return BucketCurrent
	case days <= 30:
		return Bucket1to30
	case days <= 60:
		return Bucket31to60
	case days <= 90:
		return Bucket61to90
	case days <= 180:
		return Bucket91to180
	default:
		return BucketOver180
	}
}

// Age sums open amounts per currency and bucket. Credit-side rows (negative open
// amounts, FR-30.1) net against the debits of their bucket, and a bucket holding
// only credits is negative; only buckets that sum to zero are omitted.
func Age(asOf time.Time, rows []AgingRow) []AgingTotal {
	sums := map[[2]string]decimal.Decimal{}
	for _, r := range rows {
		k := [2]string{r.Currency, string(BucketFor(asOf, r.DueDate))}
		sums[k] = sums[k].Add(r.OpenAmount)
	}
	out := make([]AgingTotal, 0, len(sums))
	for k, v := range sums {
		if v.IsZero() {
			continue
		}
		out = append(out, AgingTotal{Currency: k[0], Bucket: Bucket(k[1]), Amount: v})
	}
	rank := map[Bucket]int{}
	for i, b := range bucketOrder {
		rank[b] = i
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Currency != out[j].Currency {
			return out[i].Currency < out[j].Currency
		}
		return rank[out[i].Bucket] < rank[out[j].Bucket]
	})
	return out
}

func dayOf(t time.Time) time.Time {
	y, m, d := t.UTC().Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}
