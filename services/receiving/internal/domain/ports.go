package domain

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/money"
)

const LedgerPostPath = "/v1/ledger/journal-entries:post"

var ErrPeriodClosed = errors.New("ledger period closed")

type PeriodStatus string

const (
	PeriodOpen              PeriodStatus = "open"
	PeriodOnHold            PeriodStatus = "on_hold"
	PeriodClosed            PeriodStatus = "closed"
	PeriodPermanentlyClosed PeriodStatus = "permanently_closed"
)

type JournalLine struct {
	Account string       `json:"account"`
	Amount  money.Amount `json:"amount"`
}

type DocumentSource struct {
	Context      string `json:"context"`
	DocumentType string `json:"documentType"`
	DocumentID   string `json:"documentId"`
	Purpose      string `json:"purpose"`
}

type JournalEntry struct {
	IdempotencyKey string         `json:"-"`
	LegalEntityID  string         `json:"legalEntityId"`
	PostingDate    time.Time      `json:"postingDate"`
	DocumentDate   time.Time      `json:"documentDate"`
	Source         DocumentSource `json:"source"`
	RateType       string         `json:"rateType"`
	Lines          []JournalLine  `json:"lines"`
}

type PostingProfile struct {
	Inventory     string
	GRIR          string
	PPV           string
	VendorControl string
}

type PeriodQuery struct {
	LedgerID       string
	FiscalPeriodID string
	PostingDate    time.Time
}

type PeriodControl interface {
	AP(ctx context.Context, q PeriodQuery) (PeriodStatus, error)
}

type LedgerPoster interface {
	Post(ctx context.Context, entry JournalEntry) (string, error)
}

type FakePeriods struct {
	Status PeriodStatus
	Err    error
}

func (f FakePeriods) AP(context.Context, PeriodQuery) (PeriodStatus, error) { return f.Status, f.Err }

type FakeLedger struct {
	Entries []JournalEntry
	Err     error
}

func (f *FakeLedger) Post(_ context.Context, entry JournalEntry) (string, error) {
	if f.Err != nil {
		return "", f.Err
	}
	f.Entries = append(f.Entries, entry)
	return fmt.Sprintf("fake-entry-%d", len(f.Entries)), nil
}

func (f *FakeLedger) Balance(account string) decimal.Decimal {
	total := decimal.Zero
	for _, e := range f.Entries {
		for _, l := range e.Lines {
			if l.Account == account {
				total = total.Add(l.Amount.Value)
			}
		}
	}
	return total
}
