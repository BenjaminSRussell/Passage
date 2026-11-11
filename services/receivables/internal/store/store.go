// Package store persists open items, payments and settlements under row-level
// security. Every write that other contexts care about is written to the outbox
// in the same transaction.
package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/events"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/ledgerclient"
	"github.com/BenjaminSRussell/passage/platform/outbox"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/receivables/internal/billingclient"
	"github.com/BenjaminSRussell/passage/services/receivables/internal/domain"
	rcvevents "github.com/BenjaminSRussell/passage/services/receivables/internal/events"
	"github.com/BenjaminSRussell/passage/services/receivables/internal/fxclient"
	"github.com/BenjaminSRussell/passage/services/receivables/internal/taxclient"
)

const (
	CodeOpenItemNotFound = "AR-OPEN-ITEM-NOT-FOUND"
	CodePaymentNotFound  = "AR-PAYMENT-NOT-FOUND"
	CodePaymentDuplicate = "AR-PAYMENT-DUPLICATE"
	CodePaymentState     = "AR-PAYMENT-STATE"
	CodeIdempotencyReuse = "idempotency_key_reuse"
	CodeIdempotencyBusy  = "idempotency_in_progress"
	CodeVersionConflict  = "conflict_version"
	eventSource          = "receivables"
	lockNotAvailable     = "55P03"
	uniqueViolation      = "23505"
	paymentKeyConstraint = "payment_tenant_id_idempotency_key_key"
	idempotencyKeysPK    = "idempotency_keys_pkey"
	disputeActiveIndex   = "dispute_one_active_per_item"
	dateLayout           = "2006-01-02"
	openStatuses         = "('OPEN', 'PARTIALLY_SETTLED')"
)

const (
	// MatchPoolLimit caps the open items a single match reads (§5.30.19 budget).
	MatchPoolLimit = 500
	// MaxPageSize caps one open-item list page.
	MaxPageSize = 500
)

// OpenItem is a customer receivable (BR-30.4 / §5.30.4 open_item).
type OpenItem struct {
	ID                 string          `json:"id"`
	LegalEntityID      string          `json:"legalEntityId"`
	CustomerID         string          `json:"customerId"`
	SourceType         string          `json:"sourceType"`
	DocumentNumber     string          `json:"documentNumber"`
	DocumentDate       string          `json:"documentDate"`
	DueDate            string          `json:"dueDate"`
	Currency           string          `json:"currency"`
	Amount             decimal.Decimal `json:"amount"`
	OpenAmount         decimal.Decimal `json:"openAmount"`
	CashDiscountDate   *string         `json:"cashDiscountDate,omitempty"`
	CashDiscountAmount decimal.Decimal `json:"cashDiscountAmount"`
	DunningLevel       int             `json:"dunningLevel"`
	DunningBlock       bool            `json:"dunningBlock"`
	Status             string          `json:"status"`
	Version            int             `json:"version"`
	// Side is DEBIT for an invoice-like item and CREDIT for a credit note or an
	// unapplied payment, which carry negative amounts (FR-30.1).
	Side string `json:"side"`
	// ImportSource and LegacyKey identify an item loaded by the opening-item
	// import (the legacy system and the row's key there).
	ImportSource *string `json:"importSource,omitempty"`
	LegacyKey    *string `json:"legacyKey,omitempty"`
}

// OpenItemFilter narrows an open item read.
type OpenItemFilter struct {
	CustomerID   string
	OpenOnly     bool
	ImportSource string
}

// PaymentFilter narrows a payment list.
type PaymentFilter struct {
	Status     string
	CustomerID string
}

// Payment is a customer payment (§5.30.4 payment).
type Payment struct {
	ID              string                  `json:"id"`
	LegalEntityID   string                  `json:"legalEntityId"`
	CustomerID      *string                 `json:"customerId,omitempty"`
	PaymentMode     string                  `json:"paymentMode"`
	Source          string                  `json:"source"`
	SourceRef       *string                 `json:"sourceRef,omitempty"`
	Amount          decimal.Decimal         `json:"amount"`
	UnappliedAmount decimal.Decimal         `json:"unappliedAmount"`
	Currency        string                  `json:"currency"`
	ReceivedDate    string                  `json:"receivedDate"`
	ValueDate       string                  `json:"valueDate"`
	PayerName       *string                 `json:"payerName,omitempty"`
	Remittance      []domain.RemittanceLine `json:"remittance"`
	Status          string                  `json:"status"`
	Version         int                     `json:"version"`
	// PayerAccountHash is the hex SHA-256 of the payer's normalized bank account
	// (R4); the account itself is never stored.
	PayerAccountHash *string `json:"payerAccountHash,omitempty"`
	// ShortPayReason explains a short payment (R7).
	ShortPayReason *string `json:"shortPayReason,omitempty"`
	// BankAccountRef is the account the money came to (the statement's account id, normalized); it picks the ledger account of the cash.
	BankAccountRef *string `json:"bankAccountRef,omitempty"`
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// ApplyRequest is the settlement plan for one payment. Open amounts and discount
// dates are filled from the locked open items by the store.
type ApplyRequest struct {
	Allocations []domain.Allocation
	Residual    decimal.Decimal
}

// SettlementRecord reports one persisted settlement.
type SettlementRecord struct {
	OpenItemID   string          `json:"openItemId"`
	Amount       decimal.Decimal `json:"amount"`
	CashDiscount decimal.Decimal `json:"cashDiscount"`
	WriteOff     decimal.Decimal `json:"writeOff"`
	OpenAfter    decimal.Decimal `json:"openAfter"`
	ItemStatus   string          `json:"itemStatus"`
	// ItemCurrency, ItemAmount and RealizedFX are set for a cross-currency settlement: the item's currency, what the settlement took
	// off the item in it, and the realized exchange difference in the legal entity's functional currency (positive for a gain).
	ItemCurrency string          `json:"itemCurrency,omitempty"`
	ItemAmount   decimal.Decimal `json:"itemAmount,omitempty"`
	RealizedFX   decimal.Decimal `json:"realizedFx,omitempty"`
}

// ApplyResult is the payment after apply and the settlements it created.
type ApplyResult struct {
	Payment     Payment            `json:"payment"`
	Settlements []SettlementRecord `json:"settlements"`
}

// Store is the persistence port the API depends on.
type Store interface {
	CreateOpenItem(ctx context.Context, in OpenItem) (OpenItem, error)
	ListOpenItems(ctx context.Context, f OpenItemFilter, limit int) ([]OpenItem, error)
	// GetOpenItem reads one open item; row-level security keeps it to the caller's legal entities.
	GetOpenItem(ctx context.Context, id string) (OpenItem, error)
	AgingRows(ctx context.Context) ([]domain.AgingRow, error)
	CreatePayment(ctx context.Context, in Payment, idempotencyKey string) (Payment, error)
	ListPayments(ctx context.Context, f PaymentFilter, limit int) ([]Payment, error)
	GetPayment(ctx context.Context, id string) (Payment, error)
	MatchPayment(ctx context.Context, id string) (domain.Result, error)
	ApplyPayment(ctx context.Context, id string, version int, req ApplyRequest, idempotencyKey string) (ApplyResult, error)
	OpenDispute(ctx context.Context, in Dispute, idempotencyKey string) (Dispute, error)
	GetDispute(ctx context.Context, id string) (Dispute, error)
	ListDisputes(ctx context.Context, f DisputeFilter, limit int) ([]Dispute, error)
	TransitionDispute(ctx context.Context, id string, version int, to string, note *string) (Dispute, error)
	CreatePromise(ctx context.Context, in Promise, idempotencyKey string) (Promise, error)
	ListPromises(ctx context.Context, f PromiseFilter, limit int) ([]Promise, error)
	CreateDunningRun(ctx context.Context, in DunningRun, idempotencyKey string) (DunningRun, error)
	ListDunningRuns(ctx context.Context, status string, limit int) ([]DunningRunSummary, error)
	GetDunningRun(ctx context.Context, id string) (DunningRun, error)
	TransitionDunningRun(ctx context.Context, id string, version int, to string) (DunningRun, error)
	CalculateInterest(ctx context.Context, req InterestRequest) (InterestResult, error)
	PostInterest(ctx context.Context, req InterestRequest, idempotencyKey string) (InterestResult, error)
	ListInterestNotes(ctx context.Context, customerID string, limit int) ([]InterestNote, error)
	ImportOpeningItems(ctx context.Context, in OpeningImport) (OpeningImportResult, error)
	ListOpeningImports(ctx context.Context, limit int) ([]OpeningImportSummary, error)
	ImportLockbox(ctx context.Context, in LockboxImport) (LockboxBatch, error)
	ListLockboxBatches(ctx context.Context, limit int) ([]LockboxBatch, error)
	Worklist(ctx context.Context, legalEntityID string, asOf time.Time) ([]domain.CustomerRisk, error)
	CreateCase(ctx context.Context, in Case, idempotencyKey string) (Case, error)
	ListCases(ctx context.Context, f CaseFilter, limit int) ([]Case, error)
	GetCase(ctx context.Context, id string) (Case, error)
	AssignCase(ctx context.Context, id string, version int, owner string) (Case, error)
	CloseCase(ctx context.Context, id string, version int, note *string) (Case, error)
	CreateConfirmationRun(ctx context.Context, in ConfirmationRun, idempotencyKey string) (ConfirmationRun, error)
	ListConfirmationRuns(ctx context.Context, limit int) ([]ConfirmationRun, error)
	GetConfirmationRun(ctx context.Context, id string) (ConfirmationRun, error)
	RecordConfirmationReply(ctx context.Context, runID string, in ConfirmationReply) (Confirmation, error)
	ConfirmationDifferences(ctx context.Context, runID string) ([]Confirmation, error)
	CreateDownPayment(ctx context.Context, in DownPayment, idempotencyKey string) (DownPayment, error)
	ListDownPayments(ctx context.Context, f DownPaymentFilter, limit int) ([]DownPayment, error)
	GetDownPayment(ctx context.Context, id string) (DownPayment, error)
	ReceiveDownPayment(ctx context.Context, id string, version int, receivedDate string) (DownPayment, error)
	CancelDownPayment(ctx context.Context, id string, version int) (DownPayment, error)
	ClearDownPayment(ctx context.Context, id string, version int, openItemID string, amount *decimal.Decimal) (DownPayment, error)
	ReleaseDownPayment(ctx context.Context, id string, version int) (DownPayment, error)
	ImportBankStatement(ctx context.Context, in BankStatementImport) (BankStatementBatch, error)
	ListBankStatementImports(ctx context.Context, limit int) ([]BankStatementBatch, error)
	CreateDeduction(ctx context.Context, in Deduction, idempotencyKey string) (Deduction, error)
	ListDeductions(ctx context.Context, f DeductionFilter, limit int) ([]Deduction, error)
	GetDeduction(ctx context.Context, id string) (Deduction, error)
	MatchDeduction(ctx context.Context, id string, version int, claim ClaimMatch) (Deduction, error)
	ApproveDeduction(ctx context.Context, id string, version int, note *string) (Deduction, error)
	DenyDeduction(ctx context.Context, id string, version int, reason string, note *string) (Deduction, error)
	SplitDeduction(ctx context.Context, id string, version int, parts []decimal.Decimal) (Deduction, error)
	WriteOffDeduction(ctx context.Context, id string, version int, mayApprove bool, note *string) (Deduction, error)
	Statement(ctx context.Context, legalEntityID, customerID string, asOf time.Time) ([]domain.StatementSection, error)
	RunAgingSnapshot(ctx context.Context, legalEntityID string, asOf time.Time) (SnapshotResult, error)
	ListBalanceSnapshots(ctx context.Context, f SnapshotFilter, limit int) ([]BalanceSnapshot, error)
	PostRMACredit(ctx context.Context, in RMACredit) (RMACreditNote, error)
	RetryRMACredit(ctx context.Context, id, postingDate string) (RMACreditView, error)
	CancelRMACredit(ctx context.Context, id string) (RMACreditView, error)
	BackfillRMAReservation(ctx context.Context, id, invoiceID string, lines []RMAInvoiceLine) (RMACreditView, error)
	BackfillRMAReservationFor(ctx context.Context, legalEntityID, rmaID, invoiceID string, lines []RMAInvoiceLine) (RMACreditView, error)
	ListRMACredits(ctx context.Context, f RMACreditFilter) ([]RMACreditView, error)
	SweepRMACredits(ctx context.Context, now time.Time, limit int) (RMASweepResult, error)
	BackfillGLJournals(ctx context.Context, req BackfillRequest) (BackfillResult, error)
	ListBankAccounts(ctx context.Context, legalEntityID string) ([]BankAccount, error)
	PutBankAccount(ctx context.Context, b BankAccount) error
	DeleteBankAccount(ctx context.Context, legalEntityID, currency, accountRef string) error
	ApplyRemittanceAdvice(ctx context.Context, legalEntityID string, lines []domain.AdviceLine) (AdviceResult, error)
	ApplyRemittanceAdviceWith(ctx context.Context, legalEntityID string, lines []domain.AdviceLine, opts AdviceOptions) (AdviceResult, error)
	AutoApplyPayments(ctx context.Context, paymentIDs []string) (AutoApplyResult, error)
	SettleCredit(ctx context.Context, req CreditSettlementRequest, idempotencyKey string) (CreditSettlementResult, error)
	ListGLJournals(ctx context.Context, f GLJournalFilter) ([]GLJournalView, error)
	PostDueJournals(ctx context.Context, now time.Time, limit int) (JournalSweepResult, error)
	RetryGLJournal(ctx context.Context, id, postingDate string) (GLJournalView, error)
	GLReconcile(ctx context.Context, legalEntityID, currency, asOf string) (GLReconciliation, error)
	GetFXSetting(ctx context.Context, legalEntityID string) (FXSetting, error)
	PutFXSetting(ctx context.Context, f FXSetting) (FXSetting, error)
	ReversePayment(ctx context.Context, id string, version int) (Payment, error)
	Ping(ctx context.Context) error
}

// Postgres implements Store on pgx.
type Postgres struct {
	pool        *pgxpool.Pool
	ledger      ledgerclient.Ledger
	rmaLedgers  map[string]string
	glLedgers   map[string]string
	taxAccounts map[string]map[string]string
	tax         taxclient.Recorder
	billing     billingclient.Reader
	reserver    billingclient.Reserver
	rates       fxclient.Rates
}

// New returns a Postgres store over pool.
func New(pool *pgxpool.Pool) *Postgres { return &Postgres{pool: pool} }

// Ping checks the database connection.
func (s *Postgres) Ping(ctx context.Context) error { return s.pool.Ping(ctx) }

const openItemColumns = `id, legal_entity_id, customer_id, source_type, document_number, document_date::text, due_date::text,
	currency, amount::text, open_amount::text, cash_discount_date::text, cash_discount_amount::text, dunning_level, dunning_block, status, version, import_source, legacy_key`

const paymentColumns = `id, legal_entity_id, customer_id, payment_mode, source, source_ref, amount::text, unapplied_amount::text,
	currency, received_date::text, value_date::text, payer_name, remittance, status, version, payer_account_hash, short_pay_reason, bank_account_ref`

type rowScanner interface{ Scan(dest ...any) error }

func scanOpenItem(row rowScanner) (OpenItem, error) {
	var o OpenItem
	var amount, open, discount string
	if err := row.Scan(&o.ID, &o.LegalEntityID, &o.CustomerID, &o.SourceType, &o.DocumentNumber, &o.DocumentDate, &o.DueDate,
		&o.Currency, &amount, &open, &o.CashDiscountDate, &discount, &o.DunningLevel, &o.DunningBlock, &o.Status, &o.Version,
		&o.ImportSource, &o.LegacyKey); err != nil {
		return OpenItem{}, err
	}
	var err error
	if o.Amount, err = decimal.NewFromString(amount); err != nil {
		return OpenItem{}, err
	}
	if o.OpenAmount, err = decimal.NewFromString(open); err != nil {
		return OpenItem{}, err
	}
	if o.CashDiscountAmount, err = decimal.NewFromString(discount); err != nil {
		return OpenItem{}, err
	}
	o.Side = "DEBIT"
	if o.Amount.IsNegative() {
		o.Side = "CREDIT"
	}
	return o, nil
}

func scanPayment(row rowScanner) (Payment, error) {
	var p Payment
	var amount, unapplied string
	var remittance []byte
	if err := row.Scan(&p.ID, &p.LegalEntityID, &p.CustomerID, &p.PaymentMode, &p.Source, &p.SourceRef, &amount, &unapplied,
		&p.Currency, &p.ReceivedDate, &p.ValueDate, &p.PayerName, &remittance, &p.Status, &p.Version,
		&p.PayerAccountHash, &p.ShortPayReason, &p.BankAccountRef); err != nil {
		return Payment{}, err
	}
	var err error
	if p.Amount, err = decimal.NewFromString(amount); err != nil {
		return Payment{}, err
	}
	if p.UnappliedAmount, err = decimal.NewFromString(unapplied); err != nil {
		return Payment{}, err
	}
	if err := json.Unmarshal(remittance, &p.Remittance); err != nil {
		return Payment{}, err
	}
	if p.Remittance == nil {
		p.Remittance = []domain.RemittanceLine{}
	}
	return p, nil
}

// CreateOpenItem records a receivable. Billing events will feed this in a later
// slice; tests and seeding use it directly.
func (s *Postgres) CreateOpenItem(ctx context.Context, in OpenItem) (OpenItem, error) {
	p, _ := tenant.FromContext(ctx)
	var out OpenItem
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		row := tx.QueryRow(ctx, `insert into open_item (tenant_id, id, legal_entity_id, customer_id, source_type, document_number,
			document_date, due_date, currency, amount, open_amount, cash_discount_date, cash_discount_amount, status)
			values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)
			returning `+openItemColumns,
			p.TenantID, ids.New(), in.LegalEntityID, in.CustomerID, in.SourceType, in.DocumentNumber, in.DocumentDate, in.DueDate,
			in.Currency, in.Amount.String(), in.OpenAmount.String(), in.CashDiscountDate, in.CashDiscountAmount.String(), ItemStatusFor(in.OpenAmount, in.Amount))
		var err error
		out, err = scanOpenItem(row)
		return err
	})
	return out, mapWriteErr(err)
}

// ItemStatusFor is OPEN for an untouched item and SETTLED when nothing is open.
func ItemStatusFor(open, amount decimal.Decimal) string {
	switch {
	case open.IsZero():
		return "SETTLED"
	case open.Equal(amount):
		return "OPEN"
	default:
		return "PARTIALLY_SETTLED"
	}
}

// ListOpenItems reads open items ordered by due date, bounded by limit.
func (s *Postgres) ListOpenItems(ctx context.Context, f OpenItemFilter, limit int) ([]OpenItem, error) {
	items := []OpenItem{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+openItemColumns+` from open_item
			where ($1::text = '' or customer_id::text = $1::text)
			and (not $2::bool or status in `+openStatuses+`)
			and ($4::text = '' or import_source = $4::text)
			order by due_date, id limit $3`, f.CustomerID, f.OpenOnly, limit, f.ImportSource)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			o, err := scanOpenItem(rows)
			if err != nil {
				return err
			}
			items = append(items, o)
		}
		return rows.Err()
	})
	return items, err
}

func (s *Postgres) GetOpenItem(ctx context.Context, id string) (OpenItem, error) {
	var out OpenItem
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		out, err = scanOpenItem(tx.QueryRow(ctx, `select `+openItemColumns+` from open_item where id = $1`, id))
		return notFound(err, CodeOpenItemNotFound, "open item not found")
	})
	return out, err
}

// AgingRows returns the open amount per currency and due date across every open
// item the caller may see. Grouping in SQL keeps the snapshot complete however
// many items are open.
func (s *Postgres) AgingRows(ctx context.Context) ([]domain.AgingRow, error) {
	out := []domain.AgingRow{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select currency, due_date::text, sum(open_amount)::text from open_item
			where status in `+openStatuses+` group by currency, due_date order by currency, due_date`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var cur, due, sum string
			if err := rows.Scan(&cur, &due, &sum); err != nil {
				return err
			}
			amount, err := decimal.NewFromString(sum)
			if err != nil {
				return err
			}
			out = append(out, domain.AgingRow{Currency: cur, DueDate: parseDay(due), OpenAmount: amount})
		}
		return rows.Err()
	})
	return out, err
}

// ListPayments reads payments, newest received first, bounded by limit.
func (s *Postgres) ListPayments(ctx context.Context, f PaymentFilter, limit int) ([]Payment, error) {
	out := []Payment{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+paymentColumns+` from payment
			where ($1::text = '' or status = $1::text) and ($2::text = '' or customer_id::text = $2::text)
			order by received_date desc, created_at desc, id limit $3`, f.Status, f.CustomerID, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			pay, err := scanPayment(rows)
			if err != nil {
				return err
			}
			out = append(out, pay)
		}
		return rows.Err()
	})
	return out, err
}

// CreatePayment records a received payment and enqueues payment.received.v1.
// A repeated Idempotency-Key returns the stored payment when the request matches.
func (s *Postgres) CreatePayment(ctx context.Context, in Payment, idempotencyKey string) (Payment, error) {
	p, _ := tenant.FromContext(ctx)
	var out Payment
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		replay, err := scanPayment(tx.QueryRow(ctx, `select `+paymentColumns+` from payment where tenant_id = $1 and idempotency_key = $2`, p.TenantID, idempotencyKey))
		if err == nil {
			if !sameRequest(replay, in) {
				return httpx.NewProblem(http.StatusUnprocessableEntity, CodeIdempotencyReuse, "Idempotency-Key was already used with a different request body")
			}
			out = replay
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		in.ID = ids.New()
		remittance, err := json.Marshal(in.Remittance)
		if err != nil {
			return err
		}
		row := tx.QueryRow(ctx, `insert into payment (tenant_id, id, legal_entity_id, customer_id, payment_mode, source, source_ref,
			amount, unapplied_amount, currency, received_date, value_date, payer_name, remittance, status, idempotency_key,
			payer_account_hash, short_pay_reason, bank_account_ref)
			values ($1, $2, $3, $4, $5, $6, $7, $8, $8, $9, $10, $11, $12, $13, 'RECEIVED', $14, $15, $16, $17)
			returning `+paymentColumns,
			p.TenantID, in.ID, in.LegalEntityID, in.CustomerID, in.PaymentMode, in.Source, in.SourceRef, in.Amount.String(),
			in.Currency, in.ReceivedDate, in.ValueDate, in.PayerName, remittance, idempotencyKey, in.PayerAccountHash, in.ShortPayReason, in.BankAccountRef)
		out, err = scanPayment(row)
		if err != nil {
			return err
		}
		if out, err = confirmAdvised(ctx, tx, out); err != nil {
			return err
		}
		if err := receiptJournal(ctx, tx, p.TenantID, out); err != nil {
			return err
		}
		return enqueue(ctx, tx, "payment", out.ID, rcvevents.PaymentReceived, p.TenantID, rcvevents.PaymentReceivedData{
			PaymentID: out.ID, LegalEntityID: out.LegalEntityID, CustomerID: out.CustomerID, Source: out.Source,
			Amount: out.Amount, Currency: out.Currency, Remittance: toEventRemittance(out.Remittance),
		})
	})
	return out, mapWriteErr(err)
}

// sameRequest compares the fields a client controls, for idempotent replay.
func sameRequest(stored, in Payment) bool {
	return stored.LegalEntityID == in.LegalEntityID && stored.Amount.Equal(in.Amount) && stored.Currency == in.Currency &&
		stored.PaymentMode == in.PaymentMode && stored.Source == in.Source && stored.ReceivedDate == in.ReceivedDate &&
		stored.ValueDate == in.ValueDate && eqStrPtr(stored.CustomerID, in.CustomerID) && eqStrPtr(stored.SourceRef, in.SourceRef) &&
		eqStrPtr(stored.PayerName, in.PayerName) && sameRemittance(stored.Remittance, in.Remittance) &&
		eqStrPtr(stored.PayerAccountHash, in.PayerAccountHash) && eqStrPtr(stored.ShortPayReason, in.ShortPayReason)
}

func sameRemittance(a, b []domain.RemittanceLine) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Reference != b[i].Reference || !a[i].Amount.Equal(b[i].Amount) {
			return false
		}
	}
	return true
}

func eqStrPtr(a, b *string) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

// GetPayment reads one payment.
func (s *Postgres) GetPayment(ctx context.Context, id string) (Payment, error) {
	var out Payment
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		out, err = scanPayment(tx.QueryRow(ctx, `select `+paymentColumns+` from payment where id = $1`, id))
		return notFound(err, CodePaymentNotFound, "payment not found")
	})
	return out, err
}

// MatchPayment scores candidate open items for a payment without changing any
// state (BR-30.1). Candidates are open items in the payment's currency (FX
// settlement is not enabled) of the payment's customer, or of all customers of
// the legal entity when the payer is unidentified.
func (s *Postgres) MatchPayment(ctx context.Context, id string) (domain.Result, error) {
	var res domain.Result
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		pay, err := scanPayment(tx.QueryRow(ctx, `select `+paymentColumns+` from payment where id = $1`, id))
		if err != nil {
			return notFound(err, CodePaymentNotFound, "payment not found")
		}
		customer := ""
		if pay.CustomerID != nil {
			customer = *pay.CustomerID
		}
		// Items whose document number appears in the remittance are read first, so
		// the MatchPoolLimit cap never drops a referenced invoice of an unidentified
		// payer; the matcher still checks each reference as a whole token (R1).
		// The strpos substring test below only orders the read; it never decides a match.
		refs := make([]string, 0, len(pay.Remittance))
		for _, line := range pay.Remittance {
			refs = append(refs, strings.TrimSpace(line.Reference))
		}
		rows, err := tx.Query(ctx, `select `+openItemColumns+` from open_item
			where legal_entity_id = $1 and currency = $2 and ($3::text = '' or customer_id::text = $3::text) and status in `+openStatuses+`
			and amount > 0
			order by (length(document_number) >= $5 and strpos($6::text, upper(document_number)) > 0) desc, due_date, id limit $4`,
			pay.LegalEntityID, pay.Currency, customer, MatchPoolLimit, domain.MaxRefLength, strings.ToUpper(strings.Join(refs, " ")))
		if err != nil {
			return err
		}
		defer rows.Close()
		var pool []domain.Item
		for rows.Next() {
			o, err := scanOpenItem(rows)
			if err != nil {
				return err
			}
			pool = append(pool, toDomainItem(o))
		}
		if err := rows.Err(); err != nil {
			return err
		}
		dp := domain.Payment{Amount: pay.UnappliedAmount, ValueDate: parseDay(pay.ValueDate), Remittance: pay.Remittance,
			PayerName: valueOrEmpty(pay.PayerName), ShortPayReason: valueOrEmpty(pay.ShortPayReason)}
		if dp.PayerCustomers, err = payerCustomers(ctx, tx, pay.LegalEntityID, pay.PayerAccountHash); err != nil {
			return err
		}
		if dp.PayerName != "" {
			if dp.LearnedNames, err = learnedPayerNames(ctx, tx, pay.LegalEntityID, poolCustomers(pool)); err != nil {
				return err
			}
		}
		res = domain.Match(dp, pool)
		return nil
	})
	return res, err
}

func toDomainItem(o OpenItem) domain.Item {
	it := domain.Item{ID: o.ID, CustomerID: o.CustomerID, Currency: o.Currency, DocumentNumber: o.DocumentNumber,
		DueDate: parseDay(o.DueDate), OpenAmount: o.OpenAmount, DiscountAmount: o.CashDiscountAmount}
	if o.CashDiscountDate != nil {
		d := parseDay(*o.CashDiscountDate)
		it.DiscountDate = &d
	}
	return it
}

// ApplyPayment settles a payment against open items in one transaction. It takes
// the payment row lock, replays a repeated Idempotency-Key, checks the version
// (If-Match), locks each item with NOWAIT (a busy item is 409 AR-0002),
// validates the plan, updates items and payment, writes settlements and enqueues
// payment.applied.v1. Items must be in the payment's legal entity and currency
// and, once the payer is known, belong to the payer. An unidentified payment
// applied to one customer's items becomes that customer's payment.
func (s *Postgres) ApplyPayment(ctx context.Context, id string, version int, req ApplyRequest, idempotencyKey string) (ApplyResult, error) {
	p, _ := tenant.FromContext(ctx)
	var out ApplyResult
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		pay, err := scanPayment(tx.QueryRow(ctx, `select `+paymentColumns+` from payment where id = $1 for update`, id))
		if err != nil {
			return notFound(err, CodePaymentNotFound, "payment not found")
		}
		hash := applyHash(id, version, req)
		if ok, err := replayJSON(ctx, tx, p.TenantID, idempotencyKey, hash, &out); err != nil || ok {
			return err
		}
		if pay.Version != version {
			return httpx.NewProblem(http.StatusPreconditionFailed, CodeVersionConflict, "payment changed since it was read")
		}
		if !appliable(pay.Status) {
			return httpx.NewProblem(http.StatusConflict, CodePaymentState, "payment in status "+pay.Status+" cannot be applied")
		}
		if len(req.Allocations) == 0 && pay.Status == "ON_ACCOUNT" {
			return httpx.NewProblem(http.StatusBadRequest, domain.CodeApplyMismatch, "the payment is already on account; name the open items to settle")
		}
		customer := pay.CustomerID
		var fxSet *FXSetting
		crossItems := map[string]OpenItem{}
		allocs := make([]domain.Allocation, 0, len(req.Allocations))
		for _, a := range req.Allocations {
			item, err := lockOpenItem(ctx, tx, a.OpenItemID)
			if err != nil {
				return err
			}
			if item.LegalEntityID != pay.LegalEntityID {
				return httpx.NewProblem(http.StatusBadRequest, domain.CodeApplyMismatch, "open item "+a.OpenItemID+" belongs to another legal entity")
			}
			if item.Currency != pay.Currency {
				if fxSet == nil {
					set, err := loadFXSetting(ctx, tx, pay.LegalEntityID)
					if err != nil {
						return err
					}
					fxSet = &set
				}
				if !fxSet.CrossCurrencyEnabled {
					return httpx.NewProblem(http.StatusBadRequest, domain.CodeApplyMismatch,
						"open item "+a.OpenItemID+" is in "+item.Currency+", the payment in "+pay.Currency+"; cross-currency settlement is not enabled")
				}
				if !a.ItemCurrencyAmount.IsPositive() {
					return httpx.NewProblem(http.StatusBadRequest, domain.CodeApplyMismatch,
						"open item "+a.OpenItemID+" is in "+item.Currency+", the payment in "+pay.Currency+"; give itemAmount, what the settlement takes off the item in "+item.Currency)
				}
				crossItems[a.OpenItemID] = item
			} else if a.ItemCurrencyAmount.IsPositive() {
				return httpx.NewProblem(http.StatusBadRequest, domain.CodeApplyMismatch,
					"open item "+a.OpenItemID+" is in the payment's currency; itemAmount is only for an item in another currency")
			}
			if customer == nil {
				c := item.CustomerID
				customer = &c
			} else if *customer != item.CustomerID {
				return httpx.NewProblem(http.StatusBadRequest, domain.CodeApplyMismatch, "open item "+a.OpenItemID+" belongs to another customer")
			}
			a.Open = item.OpenAmount
			a.ItemAmount = item.Amount
			a.DiscountAvailable = item.CashDiscountAmount
			if item.CashDiscountDate != nil {
				d := parseDay(*item.CashDiscountDate)
				a.DiscountDate = &d
			}
			allocs = append(allocs, a)
		}
		if err := domain.ValidateApply(parseDay(pay.ValueDate), pay.UnappliedAmount, allocs, req.Residual); err != nil {
			return domainProblemFrom(err)
		}
		prices := map[string]settlementPricing{}
		var priced []settlementPricing
		for _, a := range allocs {
			if item, ok := crossItems[a.OpenItemID]; ok {
				price, err := s.priceCrossCurrency(ctx, *fxSet, pay, item, a)
				if err != nil {
					return err
				}
				prices[a.OpenItemID] = price
				priced = append(priced, price)
			}
		}
		out.Settlements = []SettlementRecord{}
		settled := []rcvevents.SettlementData{}
		for _, a := range allocs {
			newOpen := a.NewOpen()
			status := domain.ApplyStatus(newOpen)
			if _, err := tx.Exec(ctx, `update open_item set open_amount = $3::numeric, status = $4, version = version + 1, updated_at = now()
				where tenant_id = $1 and id = $2`, p.TenantID, a.OpenItemID, newOpen.String(), status); err != nil {
				return err
			}
			rec := SettlementRecord{OpenItemID: a.OpenItemID, Amount: a.Amount, CashDiscount: a.CashDiscount,
				WriteOff: a.WriteOff, OpenAfter: newOpen, ItemStatus: status}
			if price, ok := prices[a.OpenItemID]; ok {
				if _, err := tx.Exec(ctx, `insert into settlement (tenant_id, id, legal_entity_id, payment_id, open_item_id, settled_amount, cash_discount_taken, write_off_amount,
						item_currency, item_amount, payment_rate, item_rate, fx_gain_loss)
					values ($1, $2, $3, $4, $5, $6, 0, 0, $7, $8, $9, $10, $11)`, p.TenantID, ids.New(), pay.LegalEntityID, pay.ID, a.OpenItemID,
					a.Amount.String(), price.itemCurrency, a.ItemCurrencyAmount.String(), price.paymentRate.String(), price.itemRate.String(), price.fx.Difference.String()); err != nil {
					return err
				}
				rec.ItemCurrency, rec.ItemAmount, rec.RealizedFX = price.itemCurrency, a.ItemCurrencyAmount, price.fx.Difference
			} else if _, err := tx.Exec(ctx, `insert into settlement (tenant_id, id, legal_entity_id, payment_id, open_item_id, settled_amount, cash_discount_taken, write_off_amount)
				values ($1, $2, $3, $4, $5, $6, $7, $8)`, p.TenantID, ids.New(), pay.LegalEntityID, pay.ID, a.OpenItemID,
				a.Amount.String(), a.CashDiscount.String(), a.WriteOff.String()); err != nil {
				return err
			}
			out.Settlements = append(out.Settlements, rec)
			settled = append(settled, rcvevents.SettlementData{OpenItemID: a.OpenItemID, Amount: a.Amount, CashDiscount: a.CashDiscount, WriteOff: a.WriteOff})
		}
		newStatus := "APPLIED"
		if !req.Residual.IsZero() {
			newStatus = "ON_ACCOUNT"
		}
		pay, err = scanPayment(tx.QueryRow(ctx, `update payment set unapplied_amount = $3::numeric, status = $4, customer_id = $5, version = version + 1
			where tenant_id = $1 and id = $2 returning `+paymentColumns, p.TenantID, pay.ID, req.Residual.String(), newStatus, customer))
		if err != nil {
			return err
		}
		out.Payment = pay
		if err := applyJournal(ctx, tx, p.TenantID, pay, allocs); err != nil {
			return err
		}
		if len(priced) > 0 {
			if err := realizedFXJournal(ctx, tx, p.TenantID, *fxSet, pay, priced); err != nil {
				return err
			}
		}
		// Learn the payer's account and name for the customer it paid (R4, R5).
		if err := learnPayer(ctx, tx, p.TenantID, pay.LegalEntityID, pay.CustomerID, pay.PayerAccountHash, pay.PayerName); err != nil {
			return err
		}
		if err := enqueue(ctx, tx, "payment", pay.ID, rcvevents.PaymentApplied, p.TenantID, rcvevents.PaymentAppliedData{
			PaymentID: pay.ID, LegalEntityID: pay.LegalEntityID, CustomerID: pay.CustomerID, Currency: pay.Currency,
			Settlements: settled, Residual: req.Residual, Status: newStatus,
		}); err != nil {
			return err
		}
		return rememberJSON(ctx, tx, p.TenantID, idempotencyKey, hash, out)
	})
	return out, mapWriteErr(err)
}

// applyHash identifies one apply command for Idempotency-Key replay.
func applyHash(id string, version int, req ApplyRequest) string {
	parts := []string{"apply", id, strconv.Itoa(version), req.Residual.String()}
	for _, a := range req.Allocations {
		parts = append(parts, a.OpenItemID, a.Amount.String(), a.CashDiscount.String(), a.WriteOff.String(), a.ItemCurrencyAmount.String())
	}
	sum := sha256.Sum256([]byte(strings.Join(parts, "|")))
	return hex.EncodeToString(sum[:])
}

// replayJSON loads the stored response for key into out. It reports false when
// the key is new, and idempotency_key_reuse when the key named another request.
// It runs after the payment row lock, so a concurrent retry waits for the first
// call and then replays it.
func replayJSON(ctx context.Context, tx pgx.Tx, tenantID, key, hash string, out any) (bool, error) {
	if key == "" {
		return false, nil
	}
	var stored string
	var body []byte
	err := tx.QueryRow(ctx, `select request_hash, response_body from idempotency_keys where tenant_id = $1 and key = $2`, tenantID, key).Scan(&stored, &body)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if stored != hash {
		return false, httpx.NewProblem(http.StatusUnprocessableEntity, CodeIdempotencyReuse, "Idempotency-Key was already used with a different request")
	}
	return true, json.Unmarshal(body, out)
}

// rememberJSON stores a command's response under its key in the command's transaction.
func rememberJSON(ctx context.Context, tx pgx.Tx, tenantID, key, hash string, v any) error {
	if key == "" {
		return nil
	}
	body, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `insert into idempotency_keys (tenant_id, key, request_hash, response_status, response_body) values ($1, $2, $3, 200, $4)`,
		tenantID, key, hash, body)
	return err
}

func appliable(status string) bool {
	return status == "RECEIVED" || status == "SUGGESTED" || status == "ON_ACCOUNT"
}

// lockOpenItem reads one debit-side open item under a NOWAIT row lock (BR-30.7).
// A credit-side item (credit note, unapplied payment) is refused: settling
// credits against debits is a later slice (AR-0019).
func lockOpenItem(ctx context.Context, tx pgx.Tx, id string) (OpenItem, error) {
	item, err := lockAnyOpenItem(ctx, tx, id)
	if err != nil {
		return OpenItem{}, err
	}
	if item.Amount.IsNegative() {
		return OpenItem{}, domainProblem(domain.CodeCreditItem, "open item "+item.DocumentNumber+" is a credit; credits are not settled by this operation")
	}
	return item, nil
}

func lockAnyOpenItem(ctx context.Context, tx pgx.Tx, id string) (OpenItem, error) {
	item, err := scanOpenItem(tx.QueryRow(ctx, `select `+openItemColumns+` from open_item where id = $1 for update nowait`, id))
	if err == nil {
		return item, nil
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == lockNotAvailable {
		return OpenItem{}, domainProblem(domain.CodeItemLocked, "open item "+id+" is locked by another user")
	}
	return OpenItem{}, notFound(err, CodeOpenItemNotFound, "open item not found")
}

func toEventRemittance(in []domain.RemittanceLine) []rcvevents.RemittanceLine {
	out := make([]rcvevents.RemittanceLine, 0, len(in))
	for _, r := range in {
		out = append(out, rcvevents.RemittanceLine{Reference: r.Reference, Amount: r.Amount})
	}
	return out
}

func enqueue(ctx context.Context, tx pgx.Tx, aggregateType, aggregateID, eventType, tenantID string, data any) error {
	evt, err := events.New(eventType, eventSource, aggregateID, tenantID, data)
	if err != nil {
		return err
	}
	return outbox.Enqueue(ctx, tx, aggregateType, aggregateID, evt)
}

func domainProblem(code, detail string) error {
	return httpx.NewProblem(http.StatusConflict, code, detail)
}

func domainProblemFrom(err error) error {
	var de *domain.Error
	if errors.As(err, &de) {
		return httpx.NewProblem(de.Status, de.Code, de.Detail)
	}
	return err
}

func notFound(err error, code, detail string) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return httpx.NewProblem(http.StatusNotFound, code, detail)
	}
	return err
}

func mapWriteErr(err error) error {
	if err == nil {
		return nil
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation {
		switch {
		case pgErr.ConstraintName == paymentKeyConstraint || pgErr.ConstraintName == idempotencyKeysPK ||
			strings.HasSuffix(pgErr.ConstraintName, "_idempotency_key_key"):
			// Two requests with one key raced; the loser retries and replays.
			return httpx.NewProblem(http.StatusConflict, CodeIdempotencyBusy, "a request with this Idempotency-Key is in progress; retry")
		case pgErr.ConstraintName == disputeActiveIndex:
			return httpx.NewProblem(http.StatusConflict, domain.CodeDisputeExists, "the open item already has an active dispute")
		}
		return httpx.NewProblem(http.StatusConflict, CodePaymentDuplicate, "a payment or open item with this reference already exists")
	}
	return err
}

func parseDay(s string) time.Time {
	t, err := time.Parse(dateLayout, s)
	if err != nil {
		return time.Time{}
	}
	return t
}

// ParseDay parses a YYYY-MM-DD date as UTC midnight.
func ParseDay(s string) (time.Time, error) { return time.Parse(dateLayout, s) }
