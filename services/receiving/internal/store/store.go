package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/events"
	"github.com/BenjaminSRussell/passage/platform/fieldcrypt"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/domain"
)

var (
	ErrPendingNotFound   = errors.New("pending posting not found")
	ErrPendingFinished   = errors.New("pending posting is already finished")
	ErrPendingClaimed    = errors.New("pending posting is being resolved")
	ErrPendingTooRecent  = errors.New("pending posting may still have a command in flight")
	ErrPendingNotClaimed = errors.New("pending posting is not claimed by a resolve")
	ErrPostingSuperseded = errors.New("the pending posting was resolved while the command was running")
	ErrNotFound          = errors.New("receipt not found")
	ErrGrirNotFound      = errors.New("no open GR/IR item for the receipt line")
	ErrLineInvoiced      = errors.New("the purchase order line has invoiced GR/IR quantity")
	ErrInvoiceNotFound   = errors.New("vendor invoice not found")
)

type DuplicatePackingSlipError struct{ ReceiptNumber string }

func (e *DuplicatePackingSlipError) Error() string {
	return "packing slip already received on " + e.ReceiptNumber
}

type Line struct {
	LineNo           int
	POLineID         string
	POLineNo         int
	OtherReceived    decimal.Decimal
	ProductID        string
	Qty              decimal.Decimal
	InvoicedQty      decimal.Decimal
	ClearedAmount    decimal.Decimal
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
	// CWQty is the catch-weight count (cases). Qty is the actual weight when
	// CWQty is set, in the PO line's weight unit.
	CWQty     int
	CWNominal decimal.Decimal
	CWMin     decimal.Decimal
	CWMax     decimal.Decimal
	// SiteID and WarehouseID are the line's delivery destination: the PO
	// line's, copied when the line is bound to it, or the one keyed on a line
	// without a PO line (#480). receipt.posted carries them so inventory can
	// book the receipt.
	SiteID      string
	WarehouseID string
	ExpiryDate  time.Time
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
	Version         int
	CorrectionSeq   int
	Date            time.Time
	Currency        string
	ExchangeRate    decimal.Decimal
	PostingDate     *time.Time
	PostedAt        *time.Time
	PostedBy        string
	LedgerEntryID   string
	Source          string
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

type PendingPosting struct {
	BaseKey  string
	Subjects []string
	Hash     string
	Journal  []byte
}

type PendingRow struct {
	Key       string
	BaseKey   string
	Subjects  []string
	Hash      string
	Journal   []byte
	Outcome   string
	CreatedAt time.Time
	DoneAt    *time.Time
}

const (
	OutcomePosted   = "POSTED"
	OutcomeRejected = "REJECTED"
	OutcomeReversed = "REVERSED"
)

type LineChange struct {
	LineNo int
	Qty    decimal.Decimal
	// CWQty is the catch-weight count after the change; 0 keeps the stored count.
	CWQty      int
	Amount     decimal.Decimal
	AmountAcct decimal.Decimal
}

type Adjustment struct {
	Lines         []LineChange
	LedgerEntryID string
	Event         *events.Event
}

type AdjustFunc func(Receipt) (Adjustment, error)

type InvoiceLine struct {
	ReceiptID     string
	ReceiptLineNo int
	Qty           decimal.Decimal
	UnitPrice     decimal.Decimal
	Amount        decimal.Decimal
	StandardCost  bool
	Service       bool
	GrirCleared   decimal.Decimal
}

type Invoice struct {
	ID               string
	LegalEntityID    string
	VendorID         string
	InvoiceNumber    string
	InvoiceDate      time.Time
	PostingDate      time.Time
	Currency         string
	LedgerID         string
	FiscalPeriodID   string
	LedgerEntryID    string
	Total            decimal.Decimal
	CreatedAt        time.Time
	Source           string
	Status           string
	PostedBy         string
	OneTimePayeeID   string
	AuditReason      string
	AuditRuleVersion int
	ReleasedBy       string
	ReleasedAt       *time.Time
	RejectedBy       string
	RejectedAt       *time.Time
	RejectReason     string
	TaxAmount        decimal.Decimal
	TaxCode          string
	TaxRatePct       decimal.Decimal
	SupplyDate       time.Time
	Lines            []InvoiceLine
}

type InvoiceOutcome struct {
	LedgerEntryID string
	Total         decimal.Decimal
	Blocked       bool
	Clearings     []decimal.Decimal
	Event         events.Event
	SelfBill      SelfBillBuild
}

type SelfBillBuild func(number string) (SelfBill, events.Event, error)

type InvoiceFunc func(items []*domain.GrirItem) (InvoiceOutcome, error)

type ReleaseFunc func(inv Invoice) (InvoiceOutcome, error)

type RejectFunc func(inv Invoice) (events.Event, error)

type PriceConfirmation struct {
	Price decimal.Decimal
	By    string
}

type ErsPriceFunc func(old decimal.NullDecimal) (events.Event, error)

type AuditPolicyFunc func(old, next domain.AuditPolicy) (events.Event, error)

type ErsVendorFunc func(old bool) (events.Event, error)

type InvoiceAdjustment struct {
	ID                string
	OriginalInvoiceID string
	LegalEntityID     string
	VendorID          string
	Kind              string
	DocumentNumber    string
	DocumentDate      time.Time
	PostingDate       time.Time
	Currency          string
	LedgerID          string
	FiscalPeriodID    string
	LedgerEntryID     string
	Total             decimal.Decimal
	CreatedAt         time.Time
	Lines             []domain.AdjustmentLine
}

type InvoiceBase struct {
	Invoice Invoice
	Lines   []domain.InvoiceLineState
}

type AdjustmentOutcome struct {
	LedgerEntryID string
	Total         decimal.Decimal
	Event         events.Event
}

type InvoiceAdjustmentFunc func(base InvoiceBase) (AdjustmentOutcome, error)

type Settlement struct {
	ID              string
	Kind            string
	SourceRef       string
	LegalEntityID   string
	VendorID        string
	Currency        string
	Amount          decimal.Decimal
	PostingDate     time.Time
	LedgerID        string
	FiscalPeriodID  string
	LedgerEntryID   string
	CreatedAt       time.Time
	PurchaseOrderID string
	POLineNo        int
	ProductID       string
}

type SettlementOutcome struct {
	LedgerEntryID string
	Event         events.Event
}

type SettlementFunc func() (SettlementOutcome, error)

type ReceiptFilter struct {
	VendorID        string
	PurchaseOrderID string
	State           domain.ReceiptState
	From, To        time.Time
	CursorCreatedAt time.Time
	CursorID        string
	Limit           int
}

type InvoiceFilter struct {
	VendorID        string
	ReceiptID       string
	PurchaseOrderID string
	Status          string
	CursorCreatedAt time.Time
	CursorID        string
	Limit           int
}

type CaptureFilter struct {
	Status          string
	CursorCreatedAt time.Time
	CursorID        string
	Limit           int
}

type RegisterFunc func(Receipt, map[domain.SerialKey]struct{}) ([]Line, error)

type PostFunc func(Receipt) (PostOutcome, error)

type Store interface {
	PayeeErasureStore
	Ping(ctx context.Context) error
	CreateArrival(ctx context.Context, in Receipt) (Receipt, error)
	DirectReceipt(ctx context.Context, in Receipt, fn RegisterFunc) (Receipt, error)
	RecordedSerials(ctx context.Context, productIDs []string) (map[domain.SerialKey]struct{}, error)
	Register(ctx context.Context, id string, fn RegisterFunc) (Receipt, error)
	Post(ctx context.Context, id string, fn PostFunc) (Receipt, error)
	Correct(ctx context.Context, id string, fn AdjustFunc) (Receipt, error)
	Cancel(ctx context.Context, id string, fn AdjustFunc) (Receipt, error)
	CreateOneTimePayee(ctx context.Context, in OneTimePayee) (OneTimePayee, error)
	CreateCapture(ctx context.Context, in Capture) (out Capture, created bool, err error)
	CreateSheet(ctx context.Context, in ServiceSheet, limits map[int]decimal.Decimal) (ServiceSheet, error)
	GetSheet(ctx context.Context, id string) (ServiceSheet, error)
	DecideSheet(ctx context.Context, id, by string, accept bool, reason string, fn SheetDecisionFunc) (ServiceSheet, error)
	AcceptSheet(ctx context.Context, id, by string, fn SheetAcceptFunc) (ServiceSheet, error)
	CreateAsn(ctx context.Context, in Asn, ordered map[int]decimal.Decimal) (Asn, error)
	GetAsn(ctx context.Context, id string) (Asn, error)
	ListAsns(ctx context.Context, f AsnFilter) ([]Asn, error)
	StartAsnReceipt(ctx context.Context, id string, rec Receipt) (Asn, Receipt, error)
	GetSelfBill(ctx context.Context, invoiceID string) (SelfBill, error)
	GetCapture(ctx context.Context, id string) (Capture, error)
	ListCaptures(ctx context.Context, f CaptureFilter) ([]Capture, error)
	FindCapture(ctx context.Context, legalEntityID, digest string) (Capture, bool, error)
	PostInvoice(ctx context.Context, in Invoice, fn InvoiceFunc) (Invoice, error)
	ReleaseInvoice(ctx context.Context, id, legalEntityID, by string, fn ReleaseFunc) (Invoice, error)
	RejectInvoice(ctx context.Context, id, legalEntityID, by, reason string, fn RejectFunc) (Invoice, error)
	InvoiceVendor(ctx context.Context, id string) (string, error)
	RejectedInvoiceCount(ctx context.Context, vendorID, number string) (int, error)
	SetErsVendor(ctx context.Context, legalEntityID, vendorID string, enabled bool, by string, fn ErsVendorFunc) error
	ErsVendor(ctx context.Context, legalEntityID, vendorID string) (bool, error)
	ReceivedOnPurchaseOrder(ctx context.Context, purchaseOrderID string) (map[int]decimal.Decimal, error)
	ConfirmErsPrice(ctx context.Context, legalEntityID, receiptID string, lineNo int, price decimal.Decimal, by string, fn ErsPriceFunc) error
	ErsPriceConfirmations(ctx context.Context, receiptID string) (map[int]PriceConfirmation, error)
	SetAuditPolicy(ctx context.Context, legalEntityID string, policy domain.AuditPolicy, by string, fn AuditPolicyFunc) error
	AuditPolicy(ctx context.Context, legalEntityID string) (domain.AuditPolicy, error)
	PostInvoiceAdjustment(ctx context.Context, in InvoiceAdjustment, fn InvoiceAdjustmentFunc) (InvoiceAdjustment, error)
	PostSettlement(ctx context.Context, in Settlement, fn SettlementFunc) (out Settlement, replayed bool, err error)
	SettledSources(ctx context.Context, kind string, sources []string) (map[string]struct{}, error)
	GetReceipt(ctx context.Context, id string) (Receipt, error)
	ListReceipts(ctx context.Context, f ReceiptFilter) ([]Receipt, error)
	ListGrir(ctx context.Context, f GrirFilter) (GrirPage, error)
	ListInvoices(ctx context.Context, f InvoiceFilter) ([]Invoice, error)
	GetInvoice(ctx context.Context, id string) (Invoice, error)
	ReservePosting(ctx context.Context, p PendingPosting) (string, error)
	ReleasePosting(ctx context.Context, key string) error
	EnsureNoPending(ctx context.Context, subject string) error
	GetPending(ctx context.Context, key string) (PendingRow, error)
	ListPending(ctx context.Context) ([]PendingRow, error)
	ClaimPending(ctx context.Context, key string, minAge, lease time.Duration) (PendingRow, error)
	ReleaseClaim(ctx context.Context, key string) error
	FinishPending(ctx context.Context, key, outcome string) error
	ClaimKey(ctx context.Context, key, hash string, lockFor time.Duration) (ClaimResult, error)
	CompleteKey(ctx context.Context, key string, status int, body []byte) error
	ReleaseKey(ctx context.Context, key string) error
}

var _ Store = (*Postgres)(nil)

type Postgres struct {
	pool    *pgxpool.Pool
	pending *pgxpool.Pool
	crypt   *fieldcrypt.Keys
}

func New(pool *pgxpool.Pool) *Postgres { return &Postgres{pool: pool} }

func (s *Postgres) Ping(ctx context.Context) error { return s.pool.Ping(ctx) }
