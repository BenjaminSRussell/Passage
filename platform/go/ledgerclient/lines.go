package ledgerclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"

	"github.com/shopspring/decimal"
)

const (
	DutyEntryLookup = "ledger.entry.lookup"

	linesPath    = "/v1/ledger/journal-lines"
	linesPerPage = 1000
	maxLinePages = 1000
)

var ErrTooManyPages = errors.New("ledger journal-lines: the ledger returned too many pages")

type LineQuery struct {
	LegalEntityID string
	LedgerID      string
	DateFrom      string
	DateTo        string
}

type JournalLine struct {
	EntryID     string
	Voucher     string
	PostingDate string
	LineNumber  int
	Account     string
	Credit      bool
	Amount      decimal.Decimal
	SourceType  string
	Description string
}

type LineReader interface {
	JournalLines(ctx context.Context, q LineQuery) ([]JournalLine, error)
}

type journalLineBody struct {
	EntryID     string `json:"entryId"`
	Voucher     string `json:"voucher"`
	PostingDate string `json:"postingDate"`
	LineNumber  int    `json:"lineNumber"`
	Account     string `json:"account"`
	DebitCredit string `json:"debitCredit"`
	AmountAcct  string `json:"amountAcct"`
	SourceType  string `json:"sourceType"`
	Description string `json:"description"`
}

type linesPage struct {
	Items      []journalLineBody `json:"items"`
	NextCursor string            `json:"nextCursor"`
}

func (c *Client) JournalLines(ctx context.Context, q LineQuery) ([]JournalLine, error) {
	var out []JournalLine
	cursor := ""
	for range maxLinePages {
		query := url.Values{"ledger": {q.LedgerID}, "date_from": {q.DateFrom}, "date_to": {q.DateTo}, "limit": {strconv.Itoa(linesPerPage)}}
		if cursor != "" {
			query.Set("cursor", cursor)
		}
		raw, err := c.do(ctx, "lines", q.LedgerID, http.MethodGet, linesPath, query, nil, "", q.LegalEntityID, DutyEntryLookup)
		if err != nil {
			return nil, err
		}
		var page linesPage
		if err := json.Unmarshal(raw, &page); err != nil {
			return nil, fmt.Errorf("ledger journal-lines: %w", err)
		}
		for _, l := range page.Items {
			amount, err := decimal.NewFromString(l.AmountAcct)
			if err != nil {
				return nil, fmt.Errorf("ledger journal-lines: line %d of entry %s: %w", l.LineNumber, l.EntryID, err)
			}
			out = append(out, JournalLine{EntryID: l.EntryID, Voucher: l.Voucher, PostingDate: l.PostingDate, LineNumber: l.LineNumber,
				Account: l.Account, Credit: l.DebitCredit == sideCredit, Amount: amount, SourceType: l.SourceType, Description: l.Description})
		}
		if page.NextCursor == "" {
			return out, nil
		}
		cursor = page.NextCursor
	}
	return nil, ErrTooManyPages
}
