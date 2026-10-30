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
	PurchaseOrderPathTmpl = "/v1/procurement/purchase-orders/%s"
)

var (
	ErrPeriodClosed          = errors.New("ledger period closed")
	ErrFiscalPeriodNotFound  = errors.New("no fiscal period covers the posting date")
	ErrFiscalPeriodAmbiguous = errors.New("several fiscal periods cover the posting date")
	ErrLedgerConflict        = errors.New("ledger idempotency conflict")
	ErrLedgerRejected        = errors.New("ledger rejected the entry")

	ErrPurchaseOrderNotFound  = errors.New("purchase order not found")
	ErrPurchaseOrderForbidden = errors.New("purchase order is not accessible to the caller")
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
	LegalEntityID  string
	LedgerID       string
	FiscalPeriodID string
	PostingDate    time.Time
}

type ResolvedPeriod struct {
	FiscalPeriodID string
	Status         PeriodStatus
}

type PeriodControl interface {
	AP(ctx context.Context, q PeriodQuery) (PeriodStatus, error)
	Resolve(ctx context.Context, q PeriodQuery) (ResolvedPeriod, error)
}

type LedgerPoster interface {
	Post(ctx context.Context, entry JournalEntry) (string, error)
}

type PurchaseOrderLine struct {
	LineNo      int
	ProductID   string
	Unit        string
	Qty         decimal.Decimal
	UnitPrice   decimal.Decimal
	PriceUnit   decimal.Decimal
	DiscountPct decimal.Decimal
	ReceivedQty decimal.Decimal
	InvoicedQty decimal.Decimal
	Status      string
}

var hundredPct = decimal.NewFromInt(100)

func (l PurchaseOrderLine) NetUnitPrice() decimal.Decimal {
	net := l.UnitPrice.Mul(hundredPct.Sub(l.DiscountPct)).Div(hundredPct)
	if l.PriceUnit.Sign() <= 0 {
		return net
	}
	return net.DivRound(l.PriceUnit, 12)
}

func (l PurchaseOrderLine) QtyRemaining() decimal.Decimal {
	return decimal.Max(l.Qty.Sub(l.InvoicedQty), decimal.Zero)
}

type PurchaseOrder struct {
	ID             string
	VendorID       string
	Currency       string
	LegalEntityID  string
	ApprovalStatus string
	DocumentStatus string
	Lines          []PurchaseOrderLine
}

const (
	ApprovalApproved  = "APPROVED"
	ApprovalConfirmed = "CONFIRMED"
	StatusCancelled   = "CANCELLED"
)

func (o PurchaseOrder) Usable() bool {
	return o.ApprovalStatus == ApprovalApproved || o.ApprovalStatus == ApprovalConfirmed
}

func (l PurchaseOrderLine) Usable() bool { return l.Status != StatusCancelled }

type PurchaseOrders interface {
	Get(ctx context.Context, legalEntityID, id string) (PurchaseOrder, error)
}

type FakePurchaseOrders struct {
	Orders map[string]PurchaseOrder
	Err    error
}

func (f FakePurchaseOrders) Get(_ context.Context, _, id string) (PurchaseOrder, error) {
	if f.Err != nil {
		return PurchaseOrder{}, f.Err
	}
	if po, ok := f.Orders[id]; ok {
		return po, nil
	}
	return PurchaseOrder{}, ErrPurchaseOrderNotFound
}

const FakeFiscalPeriodID = "99999999-9999-4999-8999-999999999999"

type FakePeriods struct {
	Status     PeriodStatus
	Err        error
	PeriodID   string
	ResolveErr error
}

func (f FakePeriods) AP(context.Context, PeriodQuery) (PeriodStatus, error) { return f.Status, f.Err }

func (f FakePeriods) Resolve(context.Context, PeriodQuery) (ResolvedPeriod, error) {
	if f.ResolveErr != nil {
		return ResolvedPeriod{}, f.ResolveErr
	}
	id := f.PeriodID
	if id == "" {
		id = FakeFiscalPeriodID
	}
	return ResolvedPeriod{FiscalPeriodID: id, Status: f.Status}, f.Err
}

type LedgerReverser interface {
	Reverse(ctx context.Context, entryID, idempotencyKey, legalEntityID string, postingDate time.Time) error
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

func (f *FakeLedger) Reverse(_ context.Context, entryID, _, _ string, _ time.Time) error {
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
