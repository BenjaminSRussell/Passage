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
// row with RiskScore, and ranks riskiest first (FR-30.6, FR-30.10). It lists the
// customers and currencies that have an open debit to work; credit-side items
// (credit notes, unapplied payments, FR-30.1) net into the row as in
// CustomerBalances but do not make a row of their own. Disputes and broken
// promises are counted per customer and attributed to the customer's first
// listed currency row, so they are not repeated across currencies.
func Worklist(asOf time.Time, items []WorkItem, disputes, brokenPromises map[string]int) []CustomerRisk {
	return rollUp(asOf, items, disputes, brokenPromises, true)
}

// CustomerBalances rolls open items up like Worklist, net of credits, but keeps
// every customer and currency with an open item, including one that holds only
// credits and is net in credit (a negative total). It feeds the balance snapshot
// and customerbalance.changed.v1, which report the net open balance.
//
// Credits net against the customer's debits in that currency oldest due date
// first, so a credit clears overdue debt before current debt: the total is debits
// less credits, the overdue amount, average days late and oldest days are those
// of the debits the credits leave open, and a debit the credits clear entirely
// adds nothing (not even its dunning level). Items with a zero open amount are
// ignored.
func CustomerBalances(asOf time.Time, items []WorkItem, disputes, brokenPromises map[string]int) []CustomerRisk {
	return rollUp(asOf, items, disputes, brokenPromises, false)
}

func rollUp(asOf time.Time, items []WorkItem, disputes, brokenPromises map[string]int, debitsOnly bool) []CustomerRisk {
	type key struct{ customer, currency string }
	type acc struct {
		debits                       []WorkItem
		credit                       decimal.Decimal // magnitude of the credits
		total, overdue, weightedDays decimal.Decimal
		oldest, level                int
	}
	sums := map[key]*acc{}
	asOf = dayOf(asOf)
	for _, it := range items {
		if it.OpenAmount.IsZero() {
			continue
		}
		k := key{it.CustomerID, it.Currency}
		a := sums[k]
		if a == nil {
			a = &acc{}
			sums[k] = a
		}
		if it.OpenAmount.IsNegative() {
			a.credit = a.credit.Add(it.OpenAmount.Neg())
			continue
		}
		a.debits = append(a.debits, it)
	}
	for k, a := range sums {
		if debitsOnly && len(a.debits) == 0 {
			delete(sums, k)
			continue
		}
		sort.SliceStable(a.debits, func(i, j int) bool { return a.debits[i].DueDate.Before(a.debits[j].DueDate) })
		credit := a.credit
		for _, it := range a.debits {
			open := it.OpenAmount
			if credit.IsPositive() {
				used := decimal.Min(credit, open)
				credit = credit.Sub(used)
				open = open.Sub(used)
			}
			if !open.IsPositive() {
				continue
			}
			a.total = a.total.Add(open)
			if it.DunningLevel > a.level {
				a.level = it.DunningLevel
			}
			late := int(asOf.Sub(dayOf(it.DueDate)) / (24 * time.Hour))
			if late > 0 {
				a.overdue = a.overdue.Add(open)
				a.weightedDays = a.weightedDays.Add(open.Mul(decimal.NewFromInt(int64(late))))
				if late > a.oldest {
					a.oldest = late
				}
			}
		}
		// Credit the debits did not absorb leaves the customer net in credit.
		a.total = a.total.Sub(credit)
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
