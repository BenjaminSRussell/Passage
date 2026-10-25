package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/events"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/domain"
)

var ErrNotFound = errors.New("receipt not found")

type DuplicatePackingSlipError struct{ ReceiptNumber string }

func (e *DuplicatePackingSlipError) Error() string {
	return "packing slip already received on " + e.ReceiptNumber
}

type Line struct {
	LineNo           int
	POLineID         string
	ProductID        string
	Qty              decimal.Decimal
	OrderedRemaining decimal.Decimal
	OverDeliveryPct  decimal.Decimal
	UnitPrice        decimal.Decimal
	Currency         string
	Unit             string
	Batch            string
	LocationID       string
	InventoryStatus  string
	BatchTracked     bool
	SerialTracked    bool
	Serials          []string
	Amount           decimal.NullDecimal
	AmountAcct       decimal.NullDecimal
}

type Receipt struct {
	ID              string
	Number          string
	LegalEntityID   string
	VendorID        string
	PurchaseOrderID string
	PackingSlip     string
	Carrier         string
	PalletCount     *int
	State           domain.ReceiptState
	Date            time.Time
	Currency        string
	ExchangeRate    decimal.Decimal
	PostingDate     *time.Time
	PostedAt        *time.Time
	LedgerEntryID   string
	CreatedAt       time.Time
	Lines           []Line
}

type LineAmount struct {
	LineNo     int
	Amount     decimal.Decimal
	AmountAcct decimal.Decimal
}

type PostOutcome struct {
	PostingDate   time.Time
	LedgerEntryID string
	Amounts       []LineAmount
	Event         events.Event
}

type ReceiptFilter struct {
	VendorID        string
	PurchaseOrderID string
	State           domain.ReceiptState
	From, To        time.Time
	CursorCreatedAt time.Time
	CursorID        string
	Limit           int
}

type RegisterFunc func(Receipt, map[domain.SerialKey]struct{}) ([]Line, error)

type PostFunc func(Receipt) (PostOutcome, error)

type Store interface {
	Ping(ctx context.Context) error
	CreateArrival(ctx context.Context, in Receipt) (Receipt, error)
	RecordedSerials(ctx context.Context, productIDs []string) (map[domain.SerialKey]struct{}, error)
	Register(ctx context.Context, id string, fn RegisterFunc) (Receipt, error)
	Post(ctx context.Context, id string, fn PostFunc) (Receipt, error)
	GetReceipt(ctx context.Context, id string) (Receipt, error)
	ListReceipts(ctx context.Context, f ReceiptFilter) ([]Receipt, error)
}

type Postgres struct{ pool *pgxpool.Pool }

func New(pool *pgxpool.Pool) *Postgres { return &Postgres{pool: pool} }

func (s *Postgres) Ping(ctx context.Context) error { return s.pool.Ping(ctx) }
