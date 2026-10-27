package domain

import (
	"sort"

	"github.com/shopspring/decimal"
)

// JournalBalance is the journal's balance of one main account for a period.
type JournalBalance struct {
	MainAccount string          `json:"mainAccount"`
	Amount      decimal.Decimal `json:"amount"`
}

// ReconLine compares one primary element: the journal balance of the accounts
// it maps against the primary (Journal-origin) cost entries it holds.
type ReconLine struct {
	ElementID  string          `json:"costElementId"`
	Journal    decimal.Decimal `json:"journal"`
	CostLedger decimal.Decimal `json:"costLedger"`
	Difference decimal.Decimal `json:"difference"`
}

// Reconciliation is the FR-38.11 result for one legal entity, period and currency.
type Reconciliation struct {
	Reconciled       bool             `json:"reconciled"`
	Lines            []ReconLine      `json:"lines"`
	UnmappedAccounts []JournalBalance `json:"unmappedAccounts"`
}

// Reconcile maps each journal balance to its primary element and compares the
// element totals with the cost ledger's primary entries (FR-38.11, AC-38.6).
// Accounts no primary element maps are listed, not compared: they never enter
// the cost ledger. Reconciled means every element differs by exactly 0.00.
func Reconcile(ranges []AccountRange, journal []JournalBalance, ledger map[string]decimal.Decimal) Reconciliation {
	byElement := map[string]decimal.Decimal{}
	out := Reconciliation{Reconciled: true, Lines: []ReconLine{}, UnmappedAccounts: []JournalBalance{}}
	for _, b := range journal {
		id, err := MapPrimary(ranges, b.MainAccount)
		if err != nil {
			out.UnmappedAccounts = append(out.UnmappedAccounts, b)
			continue
		}
		byElement[id] = byElement[id].Add(b.Amount)
	}
	for id := range ledger {
		if _, ok := byElement[id]; !ok {
			byElement[id] = decimal.Zero
		}
	}
	ids := make([]string, 0, len(byElement))
	for id := range byElement {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		l := ReconLine{ElementID: id, Journal: byElement[id], CostLedger: ledger[id]}
		l.Difference = l.Journal.Sub(l.CostLedger)
		if !l.Difference.IsZero() {
			out.Reconciled = false
		}
		out.Lines = append(out.Lines, l)
	}
	return out
}
