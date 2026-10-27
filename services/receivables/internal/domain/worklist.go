package domain

import (
	"sort"
	"time"

	"github.com/shopspring/decimal"
)

// WorkItem is one open item as the collections worklist sees it.
type WorkItem struct {
	CustomerID   string
	Currency     string
	DueDate      time.Time
	OpenAmount   decimal.Decimal
	DunningLevel int
}

// CustomerRisk is one worklist row: a customer's exposure in one currency and its risk score.
type CustomerRisk struct {
	CustomerID     string          `json:"customerId"`
	Currency       string          `json:"currency"`
	TotalOpen      decimal.Decimal `json:"totalOpen"`
	Overdue        decimal.Decimal `json:"overdue"`
	OldestDays     int             `json:"oldestDays"`
	AvgDaysLate    int             `json:"avgDaysLate"`
	DunningLevel   int             `json:"dunningLevel"`
	OpenDisputes   int             `json:"openDisputes"`
	BrokenPromises int             `json:"brokenPromises"`
	RiskScore      int             `json:"riskScore"`
}

// Worklist rolls open items up to one row per customer and currency, scores each
// row with RiskScore, and ranks riskiest first (FR-30.6, FR-30.10). Disputes and
// broken promises are counted per customer and attributed to the customer's
// first currency row, so they are not repeated across currencies.
// Items with a non-positive open amount are ignored.
func Worklist(asOf time.Time, items []WorkItem, disputes, brokenPromises map[string]int) []CustomerRisk {
	type key struct{ customer, currency string }
	type acc struct {
		total, overdue, weightedDays decimal.Decimal
		oldest, level                int
	}
	sums := map[key]*acc{}
	asOf = dayOf(asOf)
	for _, it := range items {
		if !it.OpenAmount.IsPositive() {
			continue
		}
		k := key{it.CustomerID, it.Currency}
		a := sums[k]
		if a == nil {
			a = &acc{}
			sums[k] = a
		}
		a.total = a.total.Add(it.OpenAmount)
		if it.DunningLevel > a.level {
			a.level = it.DunningLevel
		}
		late := int(dayOf(asOf).Sub(dayOf(it.DueDate)) / (24 * time.Hour))
		if late > 0 {
			a.overdue = a.overdue.Add(it.OpenAmount)
			a.weightedDays = a.weightedDays.Add(it.OpenAmount.Mul(decimal.NewFromInt(int64(late))))
			if late > a.oldest {
				a.oldest = late
			}
		}
	}

	keys := make([]key, 0, len(sums))
	for k := range sums {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].customer != keys[j].customer {
			return keys[i].customer < keys[j].customer
		}
		return keys[i].currency < keys[j].currency
	})

	out := make([]CustomerRisk, 0, len(keys))
	attributed := map[string]bool{}
	for _, k := range keys {
		a := sums[k]
		row := CustomerRisk{CustomerID: k.customer, Currency: k.currency, TotalOpen: a.total, Overdue: a.overdue,
			OldestDays: a.oldest, DunningLevel: a.level}
		if a.overdue.IsPositive() {
			row.AvgDaysLate = int(a.weightedDays.Div(a.overdue).Round(0).IntPart())
		}
		if !attributed[k.customer] {
			attributed[k.customer] = true
			row.OpenDisputes = disputes[k.customer]
			row.BrokenPromises = brokenPromises[k.customer]
		}
		row.RiskScore = RiskScore(RiskInputs{OverdueAmount: a.overdue, TotalOpen: a.total, AvgDaysLate: row.AvgDaysLate,
			DunningLevel: row.DunningLevel, OpenDisputes: row.OpenDisputes, BrokenPromises: row.BrokenPromises})
		out = append(out, row)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].RiskScore != out[j].RiskScore {
			return out[i].RiskScore > out[j].RiskScore
		}
		if c := out[i].Overdue.Cmp(out[j].Overdue); c != 0 {
			return c > 0
		}
		if out[i].CustomerID != out[j].CustomerID {
			return out[i].CustomerID < out[j].CustomerID
		}
		return out[i].Currency < out[j].Currency
	})
	return out
}

// BrokenPromise reports whether a promise to pay for an item is broken at asOf:
// the promise no longer holds (date + PromiseGraceDays has passed) and the item
// is still open. It is the complement of PromiseHolds for an open item.
func BrokenPromise(asOf, promiseDate time.Time, openAmount decimal.Decimal) bool {
	return openAmount.IsPositive() && !PromiseHolds(dayOf(promiseDate), dayOf(asOf))
}
