package domain

import (
	"sort"
	"time"

	"github.com/shopspring/decimal"
)

// StatementCandidate is one document that may appear on a customer statement, with
// the part of it still open at the statement date. Debits (invoices, fees, interest,
// re-bills) are positive; unapplied payments are credits and negative. A credit has
// no due date.
type StatementCandidate struct {
	SourceType     string
	DocumentNumber string
	DocumentDate   time.Time
	DueDate        *time.Time
	Currency       string
	Amount         decimal.Decimal
	OpenAsOf       decimal.Decimal
}

// StatementLine is one open document on a statement with its running balance.
type StatementLine struct {
	SourceType     string          `json:"sourceType"`
	DocumentNumber string          `json:"documentNumber"`
	DocumentDate   string          `json:"documentDate"`
	DueDate        *string         `json:"dueDate"`
	Amount         decimal.Decimal `json:"amount"`
	OpenAmount     decimal.Decimal `json:"openAmount"`
	RunningBalance decimal.Decimal `json:"runningBalance"`
}

// StatementSection is the statement for one currency: lines, closing balance and
// an aging footer over the open debits.
type StatementSection struct {
	Currency       string          `json:"currency"`
	Lines          []StatementLine `json:"lines"`
	ClosingBalance decimal.Decimal `json:"closingBalance"`
	Aging          []AgingTotal    `json:"aging"`
}

// BuildStatement lists the documents still open at asOf, one section per currency
// (alphabetical), each ordered by document date and number with a running balance,
// the closing balance and an aging footer (FR-30.13, BR-30.10). It only reads.
// Documents with nothing open at asOf are omitted.
func BuildStatement(asOf time.Time, docs []StatementCandidate) []StatementSection {
	byCurrency := map[string][]StatementCandidate{}
	for _, d := range docs {
		if !d.OpenAsOf.IsZero() {
			byCurrency[d.Currency] = append(byCurrency[d.Currency], d)
		}
	}
	currencies := make([]string, 0, len(byCurrency))
	for c := range byCurrency {
		currencies = append(currencies, c)
	}
	sort.Strings(currencies)

	out := make([]StatementSection, 0, len(currencies))
	for _, cur := range currencies {
		list := byCurrency[cur]
		sort.SliceStable(list, func(i, j int) bool {
			if !list[i].DocumentDate.Equal(list[j].DocumentDate) {
				return list[i].DocumentDate.Before(list[j].DocumentDate)
			}
			return list[i].DocumentNumber < list[j].DocumentNumber
		})
		sec := StatementSection{Currency: cur, Lines: make([]StatementLine, 0, len(list))}
		running := decimal.Zero
		var debits []AgingRow
		for _, d := range list {
			running = running.Add(d.OpenAsOf)
			line := StatementLine{
				SourceType:     d.SourceType,
				DocumentNumber: d.DocumentNumber,
				DocumentDate:   d.DocumentDate.Format(statementDateLayout),
				Amount:         d.Amount,
				OpenAmount:     d.OpenAsOf,
				RunningBalance: running,
			}
			if d.DueDate != nil {
				due := d.DueDate.Format(statementDateLayout)
				line.DueDate = &due
				debits = append(debits, AgingRow{Currency: cur, DueDate: *d.DueDate, OpenAmount: d.OpenAsOf})
			}
			sec.Lines = append(sec.Lines, line)
		}
		sec.ClosingBalance = running
		sec.Aging = Age(asOf, debits)
		out = append(out, sec)
	}
	return out
}

const statementDateLayout = "2006-01-02"
