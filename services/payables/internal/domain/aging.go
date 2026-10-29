package domain

import (
	"sort"
	"time"

	"github.com/shopspring/decimal"
)

type Bucket string

const (
	BucketCurrent Bucket = "CURRENT"
	Bucket1to30   Bucket = "1-30"
	Bucket31to60  Bucket = "31-60"
	Bucket61to90  Bucket = "61-90"
	Bucket91to180 Bucket = "91-180"
	BucketOver180 Bucket = "OVER-180"
)

var bucketRank = map[Bucket]int{BucketCurrent: 0, Bucket1to30: 1, Bucket31to60: 2, Bucket61to90: 3, Bucket91to180: 4, BucketOver180: 5}

type AgingRow struct {
	VendorID   string
	Currency   string
	DueDate    time.Time
	OpenAmount decimal.Decimal
}

type AgingTotal struct {
	VendorID string          `json:"vendorId,omitempty"`
	Currency string          `json:"currency"`
	Bucket   Bucket          `json:"bucket"`
	Amount   decimal.Decimal `json:"amount"`
}

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

func Age(asOf time.Time, rows []AgingRow, byVendor bool) []AgingTotal {
	sums := map[[3]string]decimal.Decimal{}
	for _, r := range rows {
		if !r.OpenAmount.IsPositive() {
			continue
		}
		vendor := ""
		if byVendor {
			vendor = r.VendorID
		}
		k := [3]string{vendor, r.Currency, string(BucketFor(asOf, r.DueDate))}
		sums[k] = sums[k].Add(r.OpenAmount)
	}
	out := make([]AgingTotal, 0, len(sums))
	for k, v := range sums {
		out = append(out, AgingTotal{VendorID: k[0], Currency: k[1], Bucket: Bucket(k[2]), Amount: v})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].VendorID != out[j].VendorID {
			return out[i].VendorID < out[j].VendorID
		}
		if out[i].Currency != out[j].Currency {
			return out[i].Currency < out[j].Currency
		}
		return bucketRank[out[i].Bucket] < bucketRank[out[j].Bucket]
	})
	return out
}

type Reconciliation struct {
	Currency        string           `json:"currency"`
	SubledgerTotal  decimal.Decimal  `json:"subledgerTotal"`
	LedgerBalance   *decimal.Decimal `json:"ledgerBalance"`
	Difference      *decimal.Decimal `json:"difference"`
	Reconciled      bool             `json:"reconciled"`
	LedgerAvailable bool             `json:"ledgerAvailable"`
}

func Reconcile(totals []AgingTotal, ledgerCurrency string, ledgerBalance decimal.Decimal) []Reconciliation {
	sub := map[string]decimal.Decimal{ledgerCurrency: decimal.Zero}
	for _, t := range totals {
		sub[t.Currency] = sub[t.Currency].Add(t.Amount)
	}
	out := make([]Reconciliation, 0, len(sub))
	for currency, total := range sub {
		r := Reconciliation{Currency: currency, SubledgerTotal: total}
		if currency == ledgerCurrency {
			diff := total.Sub(ledgerBalance)
			r.LedgerBalance, r.Difference, r.LedgerAvailable, r.Reconciled = &ledgerBalance, &diff, true, diff.IsZero()
		}
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Currency < out[j].Currency })
	return out
}

func dayOf(t time.Time) time.Time {
	y, m, d := t.UTC().Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}
