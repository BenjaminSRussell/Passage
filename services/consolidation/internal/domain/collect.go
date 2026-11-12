package domain

import (
	"fmt"
	"sort"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/money"
)

// LedgerRow is one account's balance as the unit's ledger holds it: debit
// minus credit in the unit's currency.
type LedgerRow struct {
	Account string
	Net     decimal.Decimal
}

// UnitLedger is what one unit's ledger returned for the period.
type UnitLedger struct {
	UnitID   string
	Currency string
	Rows     []LedgerRow
}

// CollectRate is the pair of rates a currency is translated at: balance sheet
// accounts at the closing rate and income accounts at the average rate
// (IAS 21.39), both as units of the group currency per unit of the currency.
type CollectRate struct {
	Average decimal.Decimal
	Closing decimal.Decimal
}

// CollectLine is one collected account of one unit.
type CollectLine struct {
	UnitID               string
	MainAccount          string
	ConsolidationAccount string // empty when the account is not mapped
	Type                 string // empty when the account is not mapped
	Role                 string // the mapped account's role (asset, equity, ic_sales ...), empty when it has none
	UnitCurrency         string
	UnitAmount           decimal.Decimal
	Rate                 decimal.Decimal
	GroupAmount          decimal.Decimal
}

// Mapped reports whether the account has a consolidation account.
func (l CollectLine) Mapped() bool { return l.ConsolidationAccount != "" }

// Collect maps each unit's ledger balances to consolidation accounts and
// translates them into the group currency (FR-40.8). A unit in a foreign
// currency needs a rate for it (CNS-0927). Unmapped accounts are kept, in the
// closing rate, so the collection shows what the mapping does not cover.
// Each amount is rounded once to the group currency's minor unit.
func Collect(groupCurrency string, mapping []AccountMapping, units []UnitLedger, rates map[string]CollectRate) ([]CollectLine, error) {
	byAccount := make(map[string]AccountMapping, len(mapping))
	for _, m := range mapping {
		byAccount[m.MainAccount] = m
	}
	places := int32(money.MinorUnits(groupCurrency))
	var out []CollectLine
	for _, u := range units {
		var rate CollectRate
		switch {
		case u.Currency == groupCurrency:
			rate = CollectRate{Average: decimal.NewFromInt(1), Closing: decimal.NewFromInt(1)}
		default:
			r, ok := rates[u.Currency]
			if !ok || !r.Average.IsPositive() || !r.Closing.IsPositive() {
				return nil, &Error{Code: CodeCollectionError, Detail: fmt.Sprintf("unit %s is in %s and needs a positive average and closing rate to %s", u.UnitID, u.Currency, groupCurrency)}
			}
			rate = r
		}
		rows := append([]LedgerRow(nil), u.Rows...)
		sort.Slice(rows, func(i, j int) bool { return rows[i].Account < rows[j].Account })
		for _, row := range rows {
			line := CollectLine{UnitID: u.UnitID, MainAccount: row.Account, UnitCurrency: u.Currency, UnitAmount: row.Net, Rate: rate.Closing}
			if m, ok := byAccount[row.Account]; ok {
				line.ConsolidationAccount, line.Type, line.Role = m.ConsolidationAccount, m.Type, m.Role
				if m.Type == AccountIncome {
					line.Rate = rate.Average
				}
			}
			line.GroupAmount = row.Net.Mul(line.Rate).Round(places)
			out = append(out, line)
		}
	}
	return out, nil
}

// TBRow is one consolidation account of the consolidated trial balance with
// what each unit contributed.
type TBRow struct {
	Account string
	Type    string
	Total   decimal.Decimal
	Units   map[string]decimal.Decimal
}

// TrialBalance is the consolidated trial balance before eliminations.
type TrialBalance struct {
	Rows []TBRow
	// Unmapped lists the main accounts, per unit, that have no consolidation account.
	Unmapped []CollectLine
	// Total is the sum of every translated balance, mapped or not. A balanced
	// ledger nets to zero, so it is non-zero only by the translation
	// difference, which the run books as CTA.
	Total decimal.Decimal
	// UnitDifference is each unit's translation difference: the sum of all its
	// translated balances (zero for a unit in the group currency whose ledger
	// balances, mapped or not).
	UnitDifference map[string]decimal.Decimal
}

// BuildTrialBalance groups collected lines by consolidation account, in account
// order, with the per-unit amounts that make each total (drill to entity).
func BuildTrialBalance(lines []CollectLine) TrialBalance {
	tb := TrialBalance{UnitDifference: map[string]decimal.Decimal{}, Total: decimal.Zero}
	rows := map[string]*TBRow{}
	for _, l := range lines {
		tb.Total = tb.Total.Add(l.GroupAmount)
		tb.UnitDifference[l.UnitID] = tb.UnitDifference[l.UnitID].Add(l.GroupAmount)
		if !l.Mapped() {
			tb.Unmapped = append(tb.Unmapped, l)
			continue
		}
		r, ok := rows[l.ConsolidationAccount]
		if !ok {
			r = &TBRow{Account: l.ConsolidationAccount, Type: l.Type, Total: decimal.Zero, Units: map[string]decimal.Decimal{}}
			rows[l.ConsolidationAccount] = r
		}
		r.Total = r.Total.Add(l.GroupAmount)
		r.Units[l.UnitID] = r.Units[l.UnitID].Add(l.GroupAmount)
	}
	for _, r := range rows {
		tb.Rows = append(tb.Rows, *r)
	}
	sort.Slice(tb.Rows, func(i, j int) bool { return tb.Rows[i].Account < tb.Rows[j].Account })
	return tb
}
