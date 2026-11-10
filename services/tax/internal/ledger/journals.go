package ledger

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ledgerclient"
	"github.com/BenjaminSRussell/passage/services/tax/internal/domain"
	"github.com/BenjaminSRussell/passage/services/tax/internal/saft"
)

type Extract struct {
	Accounts []saft.Account
	Journals []saft.Journal
}

func notOwned() error {
	return &domain.Error{Code: domain.CodeValidation, Field: "ledger_id", Detail: "ledger_id is not a ledger of this legal entity"}
}

func Audit(ctx context.Context, src ledgerclient.AuditReader, legalEntityID, ledgerID, currency string, period domain.Period) (Extract, error) {
	if !wholeMonths(period.From.Time, period.To.Time) {
		return Extract{}, &domain.Error{Code: domain.CodeValidation, Field: "period_from", Detail: "the ledger extract must cover whole calendar months"}
	}
	info, err := src.Ledger(ctx, legalEntityID, ledgerID)
	switch {
	case errors.Is(err, ledgerclient.ErrNotVisible) || ledgerclient.Rejected(err):
		return Extract{}, notOwned()
	case err != nil:
		return Extract{}, fmt.Errorf("%w: ledger lookup: %w", ErrRead, err)
	case info.LegalEntityID != legalEntityID:
		return Extract{}, notOwned()
	case info.Currency != currency:
		return Extract{}, &domain.Error{Code: domain.CodeCurrencyMismatch, Field: "currency", Detail: "currency differs from the ledger's accounting currency"}
	}
	chart, err := src.Accounts(ctx, legalEntityID, info.ChartCode)
	if err != nil {
		return Extract{}, fmt.Errorf("%w: chart of accounts: %w", ErrRead, err)
	}
	lines, err := src.JournalLines(ctx, ledgerclient.LineQuery{LegalEntityID: legalEntityID, LedgerID: ledgerID,
		DateFrom: period.From.Format(dateLayout), DateTo: period.To.Format(dateLayout)})
	if err != nil {
		return Extract{}, fmt.Errorf("%w: journal lines: %w", ErrRead, err)
	}
	journals, err := Journals(lines)
	if err != nil {
		return Extract{}, err
	}
	accounts, err := accountsWithBalances(ctx, src, legalEntityID, ledgerID, chart, period)
	if err != nil {
		return Extract{}, err
	}
	return Extract{Accounts: accounts, Journals: journals}, nil
}

func accountsWithBalances(ctx context.Context, src ledgerclient.AuditReader, legalEntityID, ledgerID string, chart map[string]ledgerclient.ChartAccount, period domain.Period) ([]saft.Account, error) {
	numbers := make([]string, 0, len(chart))
	for n := range chart {
		numbers = append(numbers, n)
	}
	if len(numbers) == 0 {
		return nil, nil
	}
	slices.SortFunc(numbers, strings.Compare)
	query := ledgerclient.BalanceQuery{LegalEntityID: legalEntityID, LedgerID: ledgerID, Account: numbers[0], AccountTo: numbers[len(numbers)-1]}
	query.PeriodTo = period.From.AddDate(0, -1, 0).Format(periodLayout)
	opening, err := src.Balances(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("%w: opening balances: %w", ErrRead, err)
	}
	query.PeriodTo = period.To.Format(periodLayout)
	closing, err := src.Balances(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("%w: closing balances: %w", ErrRead, err)
	}
	net := func(set ledgerclient.AccountBalances) map[string]decimal.Decimal {
		out := make(map[string]decimal.Decimal, len(set.Rows))
		for _, r := range set.Rows {
			out[r.Account] = r.Net()
		}
		return out
	}
	openingNet, closingNet := net(opening), net(closing)
	out := make([]saft.Account, 0, len(numbers))
	for _, n := range numbers {
		out = append(out, saft.Account{ID: n, Description: chart[n].Name, Type: chart[n].Type, Opening: openingNet[n], Closing: closingNet[n]})
	}
	return out, nil
}

const (
	journalType    = "GL"
	defaultJournal = "GL"
	dateLayout     = "2006-01-02"
)

func Journals(lines []ledgerclient.JournalLine) ([]saft.Journal, error) {
	var order []string
	byID := map[string]*saft.Journal{}
	txIndex := map[string]int{}
	for _, l := range lines {
		if l.Amount.IsZero() {
			continue
		}
		date, err := time.Parse(dateLayout, l.PostingDate)
		if err != nil {
			return nil, fmt.Errorf("%w: entry %s: posting date %q", ErrRead, l.EntryID, l.PostingDate)
		}
		id := l.SourceType
		if id == "" {
			id = defaultJournal
		}
		j, ok := byID[id]
		if !ok {
			j = &saft.Journal{ID: id, Description: id, Type: journalType}
			byID[id] = j
			order = append(order, id)
		}
		key := id + "|" + l.EntryID
		i, ok := txIndex[key]
		if !ok {
			j.Transactions = append(j.Transactions, saft.Transaction{ID: l.EntryID, Date: date, Description: l.Voucher})
			i = len(j.Transactions) - 1
			txIndex[key] = i
		}
		line := saft.Line{RecordID: fmt.Sprint(l.LineNumber), AccountID: l.Account, Description: l.Text}
		if l.Credit {
			line.Credit = l.Amount
		} else {
			line.Debit = l.Amount
		}
		j.Transactions[i].Lines = append(j.Transactions[i].Lines, line)
	}
	out := make([]saft.Journal, 0, len(order))
	for _, id := range order {
		out = append(out, *byID[id])
	}
	return out, nil
}
