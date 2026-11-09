package ledger

import (
	"fmt"
	"time"

	"github.com/BenjaminSRussell/passage/platform/ledgerclient"
	"github.com/BenjaminSRussell/passage/services/tax/internal/saft"
)

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
		line := saft.Line{RecordID: fmt.Sprint(l.LineNumber), AccountID: l.Account, Description: l.Description}
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
