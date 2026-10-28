package domain

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/money"
)

const (
	LedgerPostPath        = "/v1/ledger/journal-entries"
	LedgerReversePathTmpl = "/v1/ledger/journal-entries/%s/reverse"
)

var (
	ErrPeriodClosed   = errors.New("ledger period closed")
	ErrLedgerConflict = errors.New("ledger idempotency conflict")
	ErrLedgerRejected = errors.New("ledger rejected the entry")
)

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
	IdempotencyKey string            `json:"-"`
	LedgerEntryID  string            `json:"-"`
	GrirCleared    []decimal.Decimal `json:"-"`
	LegalEntityID  string            `json:"legalEntityId"`
	LedgerID       string            `json:"ledgerId"`
	PostingDate    time.Time         `json:"postingDate"`
	DocumentDate   time.Time         `json:"documentDate"`
	Source         DocumentSource    `json:"source"`
	RateType       string            `json:"rateType"`
	Lines          []JournalLine     `json:"lines"`
}

type PostingProfile struct {
	LedgerID      string
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

type LedgerReverser interface {
	Reverse(ctx context.Context, entryID, idempotencyKey string, postingDate time.Time) error
}

type fakePosting struct {
	hash string
	id   string
}

type FakeLedger struct {
	Entries  []JournalEntry
	Reversed []string
	Err      error
	posted   map[string]fakePosting
}

func (f *FakeLedger) Post(_ context.Context, entry JournalEntry) (string, error) {
	if f.Err != nil {
		return "", f.Err
	}
	raw, err := json.Marshal(entry)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	hash := hex.EncodeToString(sum[:])
	if prior, ok := f.posted[entry.IdempotencyKey]; ok {
		if prior.hash != hash {
			return "", ErrLedgerConflict
		}
		return prior.id, nil
	}
	if f.posted == nil {
		f.posted = map[string]fakePosting{}
	}
	f.Entries = append(f.Entries, entry)
	id := fmt.Sprintf("fake-entry-%d", len(f.Entries))
	f.posted[entry.IdempotencyKey] = fakePosting{hash: hash, id: id}
	return id, nil
}

func (f *FakeLedger) Reverse(_ context.Context, entryID, _ string, _ time.Time) error {
	if f.Err != nil {
		return f.Err
	}
	f.Reversed = append(f.Reversed, entryID)
	return nil
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
