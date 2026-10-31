package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/money"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/inventory/internal/domain"
	invevents "github.com/BenjaminSRussell/passage/services/inventory/internal/events"
)

// ExternalSourceTypes are the origin types other services register through
// transactions:apply (waves 17–21, 27): purchase order lines, receipt lines,
// sales order lines and production order lines. The service's own documents
// (journals, blockings, transfers, consignment) never come through it.
var ExternalSourceTypes = []string{"PURCH_LINE", "RECEIPT_LINE", "SALES_LINE", "PROD_LINE"}

// CodeKeyInFlight is the 409 answered when the unique idempotency key of a
// status change is taken by a request this caller cannot see (another legal
// entity); a same-scope retry waits on the key lock and replays instead.
const CodeKeyInFlight = "idempotency_key_conflict"

// StatusChange is the audit row of one registration or status move
// (InventoryTransactionStatusChange, FR-16.1). From is empty for CREATE.
type StatusChange struct {
	ID            string
	LegalEntityID string
	TransactionID string
	Command       domain.StepCommand
	From          domain.Status
	To            domain.Status
	Qty           decimal.Decimal
	Date          string
	CreatedBy     string
	OccurredAt    time.Time
}

// CreateTransactionInput registers one transaction for an external origin at
// its first status (the registration half of transactions:apply). The origin
// (SourceType, SourceID) registers one transaction; Reference is the source
// document number shown in the inquiry. Receipts are valued at Qty × CostPrice,
// issues at the row's running average (CostPrice is the fallback when the row
// has no posted stock); vendor-owned stock is never valued (BR-16.14).
type CreateTransactionInput struct {
	LegalEntityID  string
	SourceType     string
	SourceID       string
	Reference      string
	ProductID      string
	Dims           domain.Dimensions
	Qty            decimal.Decimal
	EnteredQty     decimal.Decimal
	EnteredUnit    string
	Status         domain.Status
	Currency       string
	CostPrice      decimal.Decimal
	Date           time.Time
	IdempotencyKey string
	Negative       domain.NegativePolicy
}

// StatusStepInput is one status move of an inventory transaction (FR-16.2,
// BR-16.1). The move applies to the whole transaction quantity; splitting is
// not built (NOT-IMPLEMENTED.md). For STEP, From is the status the caller read
// (a stale view is 412) and To the target. A named command ignores From and To:
// the move follows from the transaction's current status. Date is the physical
// or financial date the move sets (zero: today, UTC). CostAmount is the posted
// value of a receipt stepped to PURCHASED (default: its physical cost amount).
type StatusStepInput struct {
	TransactionID  string
	Command        domain.StepCommand
	From           domain.Status
	To             domain.Status
	Date           time.Time
	CostAmount     *decimal.Decimal
	IdempotencyKey string
	Negative       domain.NegativePolicy
}

// stepTxn is one transaction read under its row lock, with what the move, the
// projection and the events need.
type stepTxn struct {
	id, legalEntityID, originID, sourceType, journalID, reference string
	product, currency, unit, physicalDate, financialDate          string
	corrects                                                      string
	hash                                                          []byte
	dims                                                          domain.Dimensions
	qty, enteredQty, costPhysical                                 decimal.Decimal
	costPosted                                                    *decimal.Decimal
	status                                                        domain.Status
	direction                                                     domain.Direction
}

// stepDate is the date of a move: the requested date or today (UTC).
func stepDate(d time.Time) time.Time {
	if d.IsZero() {
		return time.Now().UTC()
	}
	return d
}

// CreateTransaction registers a transaction for an external origin at a
// physical or expected status, in one database transaction with the projection
// delta (under the row lock, with the negative policy), the CREATE audit row and
// the transaction.posted / onhand.changed events. A repeated Idempotency-Key
// replays the stored audit row with applied=false.
func (s *Postgres) CreateTransaction(ctx context.Context, in CreateTransactionInput) (StatusChange, bool, error) {
	if !slices.Contains(ExternalSourceTypes, in.SourceType) {
		return StatusChange{}, false, problem(http.StatusBadRequest, domain.CodeInvalid, "sourceType must be one of "+strings.Join(ExternalSourceTypes, ", "))
	}
	if err := domain.ValidateCreate(in.Qty, in.Status); err != nil {
		return StatusChange{}, false, problem(http.StatusUnprocessableEntity, domain.CodeStatusStep, err.Error())
	}
	dims, err := in.Dims.Normalize()
	if err != nil {
		return StatusChange{}, false, domainProblem(&domain.Error{Code: domain.CodeInvalid, Detail: err.Error()})
	}
	if strings.TrimSpace(dims[domain.DimSite]) == "" {
		return StatusChange{}, false, domainProblem(&domain.Error{Code: domain.CodeDimensionMandatory, Detail: "dimension site is mandatory"})
	}
	in.Dims = dims
	date := stepDate(in.Date)
	hash := createRequestHash(in)
	tp, _ := tenant.FromContext(ctx)
	var out StatusChange
	applied := false
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if err := lockKey(ctx, tx, "status", tp.TenantID, in.IdempotencyKey); err != nil {
			return err
		}
		if prev, found, err := statusChangeByKey(ctx, tx, tp.TenantID, in.IdempotencyKey, hash); err != nil || found {
			out = prev
			return err
		}
		// One origin registers one transaction; the lock makes the check race-free.
		if err := lockKey(ctx, tx, "origin", tp.TenantID, in.SourceType+"|"+in.SourceID); err != nil {
			return err
		}
		var existing string
		err := tx.QueryRow(ctx, `select t.id::text from inventory_transaction_origin o
			join inventory_transaction t on t.tenant_id = o.tenant_id and t.origin_id = o.id
			where o.tenant_id = $1 and o.source_type = $2 and o.source_id = $3`, tp.TenantID, in.SourceType, in.SourceID).Scan(&existing)
		if err == nil {
			return problem(http.StatusConflict, domain.CodeStatusOrigin,
				fmt.Sprintf("origin %s %s already has transaction %s; move it with a STEP", in.SourceType, in.SourceID, existing))
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		vendorOwned := domain.VendorOwned(dims)
		r := &row{product: in.ProductID, dims: dims, hash: dims.Hash()}
		currency := in.Currency
		if vendorOwned {
			currency = "" // vendor-owned rows are never valued, in any currency
		}
		if err := lockRow(ctx, tx, tp.TenantID, in.LegalEntityID, currency, r); err != nil {
			return err
		}
		value := decimal.Zero
		switch {
		case vendorOwned:
		case in.Qty.Sign() > 0:
			value = domain.ReceiptValue(in.Qty, in.CostPrice, in.Currency)
		default:
			value = r.oh.IssueValue(in.Qty, in.CostPrice, in.Currency)
		}
		if err := r.oh.Post(in.Status, in.Qty, value, 1); err != nil {
			return err
		}
		if err := checkStepNegative(r, vendorOwned, in.Negative); err != nil {
			return err
		}
		if err := checkStepFreeze(ctx, tx, tp.TenantID, in.LegalEntityID, r); err != nil {
			return err
		}
		if err := saveRow(ctx, tx, tp.TenantID, in.LegalEntityID, r, 1); err != nil {
			return err
		}
		changeID := ids.New()
		doc := document{legalEntityID: in.LegalEntityID, currency: in.Currency, date: date, sourceType: in.SourceType,
			referenceID: strings.TrimSpace(in.Reference), statusChangeID: changeID}
		pl := domain.PostedLine{Line: domain.JournalLine{ProductID: in.ProductID, Dims: dims, Qty: in.Qty, EnteredQty: in.EnteredQty,
			EnteredUnit: in.EnteredUnit, CostPrice: in.CostPrice}, Status: in.Status, Value: value}
		txnID, err := writeTransaction(ctx, tx, tp.TenantID, doc, in.SourceID, r, pl)
		if err != nil {
			return err
		}
		out = StatusChange{ID: changeID, LegalEntityID: in.LegalEntityID, TransactionID: txnID, Command: domain.CommandCreate,
			To: in.Status, Qty: in.Qty, Date: date.Format(dateLayout), CreatedBy: tp.UserID}
		if err := insertStatusChange(ctx, tx, tp.TenantID, &out, in.IdempotencyKey, hash); err != nil {
			return err
		}
		applied = true
		return enqueueStatusOnHand(ctx, tx, tp.TenantID, in.LegalEntityID, r, in.Status)
	})
	return out, applied, keyConflict(err)
}

// ApplyStatusStep moves one transaction to a new status in one database
// transaction: the transaction row is locked, the move validated against the
// status model and the command (BR-16.1), the projection row locked and moved
// by the whole quantity with the negative policy checked under the lock, the
// status, dates and posted amount updated, the audit row written and the events
// enqueued. A repeated Idempotency-Key replays the stored row (applied=false).
func (s *Postgres) ApplyStatusStep(ctx context.Context, in StatusStepInput) (StatusChange, bool, error) {
	date := stepDate(in.Date)
	hash := stepRequestHash(in)
	tp, _ := tenant.FromContext(ctx)
	var out StatusChange
	applied := false
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if err := lockKey(ctx, tx, "status", tp.TenantID, in.IdempotencyKey); err != nil {
			return err
		}
		if prev, found, err := statusChangeByKey(ctx, tx, tp.TenantID, in.IdempotencyKey, hash); err != nil || found {
			out = prev
			return err
		}
		t, err := loadStepTxn(ctx, tx, tp.TenantID, in.TransactionID)
		if err != nil {
			return err
		}
		from, to := t.status, in.To
		if in.Command == domain.CommandStep {
			if in.From != t.status {
				return problem(http.StatusPreconditionFailed, CodeConflictVersion,
					fmt.Sprintf("transaction is at %s, not %s; reread and retry", t.status, in.From))
			}
		} else {
			var ok bool
			if to, ok = domain.NamedMove(in.Command, from); !ok {
				return problem(http.StatusUnprocessableEntity, domain.CodeStatusStep,
					fmt.Sprintf("%s does not apply to a transaction at %s", in.Command, from))
			}
		}
		if err := domain.ValidateStep(in.Command, from, to); err != nil {
			return problem(http.StatusUnprocessableEntity, domain.CodeStatusStep, err.Error())
		}
		vendorOwned := domain.VendorOwned(t.dims)
		if vendorOwned && domain.IsFinancial(to) {
			return problem(http.StatusUnprocessableEntity, domain.CodeOwnershipValue,
				"vendor-owned stock cannot reach a financial status; post an ownership change journal first")
		}
		if in.CostAmount != nil && (t.direction != domain.ReceiptDirection || to != domain.StatusPurchased) {
			return problem(http.StatusBadRequest, domain.CodeInvalid, "costAmount applies only to a receipt stepped to PURCHASED")
		}
		r := &row{product: t.product, dims: t.dims, hash: t.hash}
		currency := t.currency
		if vendorOwned {
			currency = ""
		}
		if err := lockRow(ctx, tx, tp.TenantID, t.legalEntityID, currency, r); err != nil {
			return err
		}
		var posted *decimal.Decimal
		if domain.IsFinancial(to) {
			v := financialValue(t, r, in.CostAmount)
			posted = &v
		}
		oldValue := decimal.Zero
		if t.costPosted != nil {
			oldValue = *t.costPosted
		}
		if err := r.oh.Post(from, t.qty, oldValue, -1); err != nil {
			return err
		}
		newValue := oldValue
		if posted != nil {
			newValue = *posted
		}
		if err := r.oh.Post(to, t.qty, newValue, 1); err != nil {
			return err
		}
		if err := checkStepNegative(r, vendorOwned, in.Negative); err != nil {
			return err
		}
		if err := checkStepFreeze(ctx, tx, tp.TenantID, t.legalEntityID, r); err != nil {
			return err
		}
		if err := saveRow(ctx, tx, tp.TenantID, t.legalEntityID, r, 1); err != nil {
			return err
		}
		if err := moveTransaction(ctx, tx, tp.TenantID, &t, from, to, date, posted); err != nil {
			return err
		}
		out = StatusChange{ID: ids.New(), LegalEntityID: t.legalEntityID, TransactionID: t.id, Command: in.Command, From: from, To: to,
			Qty: t.qty, Date: date.Format(dateLayout), CreatedBy: tp.UserID}
		if err := insertStatusChange(ctx, tx, tp.TenantID, &out, in.IdempotencyKey, hash); err != nil {
			return err
		}
		applied = true
		if err := enqueueStepTransaction(ctx, tx, tp.TenantID, t, out); err != nil {
			return err
		}
		return enqueueStatusOnHand(ctx, tx, tp.TenantID, t.legalEntityID, r, to)
	})
	return out, applied, keyConflict(err)
}

// financialValue is the posted value of a transaction reaching a financial
// status: a receipt at the given cost amount or its physical cost amount; an
// issue at the row's running average, its own physical unit cost the fallback
// when the row has no posted stock (as journals value issues).
func financialValue(t stepTxn, r *row, costAmount *decimal.Decimal) decimal.Decimal {
	if t.direction == domain.ReceiptDirection {
		if costAmount != nil {
			return money.Amount{Value: *costAmount, Currency: t.currency}.Round().Value
		}
		return t.costPhysical
	}
	unit := t.costPhysical.Abs().Div(t.qty.Abs())
	return r.oh.IssueValue(t.qty, unit, t.currency)
}

// checkStepNegative applies the negative policy to a moved row. Vendor-owned
// stock never goes negative, whatever the item model group says (BR-16.14).
func checkStepNegative(r *row, vendorOwned bool, neg domain.NegativePolicy) error {
	if vendorOwned {
		neg = domain.NegativePolicy{}
	}
	return checkNegative(r, neg)
}

// moveTransaction writes the new status of t. A forward move into a physical
// status from an expected one sets the physical date; a financial status sets
// the financial date and the posted cost amount (FR-16.11). Quantity and
// identity are never touched (the append-only trigger enforces it).
func moveTransaction(ctx context.Context, tx pgx.Tx, tenantID string, t *stepTxn, from, to domain.Status, date time.Time, posted *decimal.Decimal) error {
	d := date.Format(dateLayout)
	if !domain.IsPhysical(from) && !domain.IsFinancial(from) && (domain.IsPhysical(to) || domain.IsFinancial(to)) && domain.CanStep(from, to) {
		t.physicalDate = d
	}
	var postedText *string
	if posted != nil {
		v := posted.String()
		postedText = &v
		t.financialDate = d
		t.costPosted = posted
	}
	var financial *string
	if t.financialDate != "" {
		financial = &t.financialDate
	}
	issue, receipt := statusColumns(t.direction, to)
	_, err := tx.Exec(ctx, `update inventory_transaction set issue_status = $3, receipt_status = $4, physical_date = $5::date,
		financial_date = $6::date, cost_amount_posted = coalesce($7::numeric, cost_amount_posted)
		where tenant_id = $1 and id = $2`, tenantID, t.id, issue, receipt, t.physicalDate, financial, postedText)
	t.status = to
	return err
}

// statusColumns returns the issue and receipt status columns for status s.
func statusColumns(dir domain.Direction, s domain.Status) (*string, *string) {
	v := string(s)
	if dir == domain.IssueDirection {
		return &v, nil
	}
	return nil, &v
}

// insertStatusChange writes the audit row c (FR-16.1, BR-16.1) and fills its
// occurrence time.
func insertStatusChange(ctx context.Context, tx pgx.Tx, tenantID string, c *StatusChange, key, hash string) error {
	var from, by *string
	if c.From != "" {
		f := string(c.From)
		from = &f
	}
	if ids.Valid(c.CreatedBy) {
		by = &c.CreatedBy
	}
	return tx.QueryRow(ctx, `insert into inventory_transaction_status_change
		(tenant_id, legal_entity_id, id, transaction_id, command, from_status, to_status, qty, status_date, idempotency_key, request_hash, created_by)
		values ($1, $2, $3, $4, $5, $6, $7, $8, $9::date, $10, $11, $12) returning occurred_at`,
		tenantID, c.LegalEntityID, c.ID, c.TransactionID, string(c.Command), from, string(c.To), c.Qty.String(), c.Date, key, hash, by).
		Scan(&c.OccurredAt)
}

// keyConflict turns a unique violation on the idempotency key or the origin
// (held by a row in a legal entity outside the caller's scope, which RLS hides
// from the checks) into a 409 instead of a 500.
func keyConflict(err error) error {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != uniqueViolation {
		return err
	}
	switch {
	case strings.Contains(pgErr.ConstraintName, "idempotency_key"):
		return problem(http.StatusConflict, CodeKeyInFlight, "Idempotency-Key is already used by another status change")
	case strings.Contains(pgErr.ConstraintName, "source_id"):
		return problem(http.StatusConflict, domain.CodeStatusOrigin, "the origin already has a transaction")
	}
	return err
}

// enqueueStatusOnHand emits onhand.changed for a row after a registration or
// move into status to; the delta is the change of to's bucket.
func enqueueStatusOnHand(ctx context.Context, tx pgx.Tx, tenantID, legalEntityID string, r *row, to domain.Status) error {
	bucket := string(to)
	if domain.IsFinancial(to) {
		bucket = invevents.BucketPosted
	}
	return enqueueOnHand(ctx, tx, tenantID, legalEntityID, r, bucket, r.oh.BucketQty(to).Sub(r.before.BucketQty(to)))
}

// enqueueStepTransaction emits transaction.posted for a status change (PRD
// 3.16.10: "any transaction insert or status change").
func enqueueStepTransaction(ctx context.Context, tx pgx.Tx, tenantID string, t stepTxn, c StatusChange) error {
	issue, receipt := statusColumns(t.direction, c.To)
	from := string(c.From)
	data := invevents.TransactionPostedData{
		TransactionID: t.id, LegalEntityID: t.legalEntityID, SourceType: t.sourceType, OriginID: t.originID, ProductID: t.product,
		DimensionSet: map[string]string(t.dims), Qty: t.qty.String(), IssueStatus: issue, ReceiptStatus: receipt, PhysicalDate: t.physicalDate,
		Currency: t.currency, CostAmountPhysical: t.costPhysical.String(), StatusChangeID: &c.ID, PreviousStatus: &from,
	}
	if t.financialDate != "" {
		data.FinancialDate = &t.financialDate
	}
	if t.costPosted != nil {
		v := t.costPosted.String()
		data.CostAmountPosted = &v
	}
	if t.journalID != "" {
		data.JournalID = &t.journalID
	}
	if t.reference != "" {
		data.ReferenceID = &t.reference
	}
	if t.corrects != "" {
		data.CorrectsTransactionID = &t.corrects
	}
	return enqueue(ctx, tx, "transaction", t.id, invevents.TransactionPosted, tenantID, data)
}

// loadStepTxn reads one transaction with its origin and dimension set and
// locks the transaction row. An id outside the tenant or the caller's legal
// entities is not found (RLS).
func loadStepTxn(ctx context.Context, tx pgx.Tx, tenantID, id string) (stepTxn, error) {
	var t stepTxn
	var raw []byte
	var qty, entered, physical, issue, receipt string
	var posted *string
	err := tx.QueryRow(ctx, `select t.legal_entity_id::text, t.origin_id::text, o.source_type, coalesce(l.journal_id::text, ''),
		coalesce(o.reference_number, ''), t.product_id::text, t.dimension_set_hash, d.dims, t.qty::text, t.entered_qty::text, t.entered_unit,
		t.currency::text, t.cost_amount_physical::text, t.cost_amount_posted::text, coalesce(t.issue_status, ''), coalesce(t.receipt_status, ''),
		to_char(t.physical_date, 'YYYY-MM-DD'), coalesce(to_char(t.financial_date, 'YYYY-MM-DD'), ''), coalesce(t.corrects_transaction_id::text, '')
		from inventory_transaction t
		join inventory_transaction_origin o on o.tenant_id = t.tenant_id and o.id = t.origin_id
		join dimension_set d on d.tenant_id = t.tenant_id and d.hash = t.dimension_set_hash
		left join inventory_journal_line l on o.source_type in ('INVENT_JOURNAL_LINE', 'OWNERSHIP_CHANGE', 'TRANSFER_IN') and l.tenant_id = o.tenant_id and l.id = o.source_id
		where t.tenant_id = $1 and t.id = $2 for update of t`, tenantID, id).Scan(
		&t.legalEntityID, &t.originID, &t.sourceType, &t.journalID, &t.reference, &t.product, &t.hash, &raw, &qty, &entered, &t.unit,
		&t.currency, &physical, &posted, &issue, &receipt, &t.physicalDate, &t.financialDate, &t.corrects)
	if errors.Is(err, pgx.ErrNoRows) {
		return stepTxn{}, problem(http.StatusNotFound, domain.CodeTransactionNotFound, "inventory transaction not found")
	}
	if err != nil {
		return stepTxn{}, err
	}
	if err := json.Unmarshal(raw, &t.dims); err != nil {
		return stepTxn{}, err
	}
	t.id = id
	t.qty, t.enteredQty, t.costPhysical = decimal.RequireFromString(qty), decimal.RequireFromString(entered), decimal.RequireFromString(physical)
	if posted != nil {
		v := decimal.RequireFromString(*posted)
		t.costPosted = &v
	}
	if issue != "" {
		t.status, t.direction = domain.Status(issue), domain.IssueDirection
	} else {
		t.status, t.direction = domain.Status(receipt), domain.ReceiptDirection
	}
	return t, nil
}

const statusChangeColumns = `id::text, legal_entity_id::text, transaction_id::text, command, coalesce(from_status, ''), to_status, qty::text,
	to_char(status_date, 'YYYY-MM-DD'), coalesce(created_by::text, ''), occurred_at`

func scanStatusChange(row pgx.Row, extra ...any) (StatusChange, error) {
	var c StatusChange
	var cmd, from, to, qty string
	dest := append([]any{&c.ID, &c.LegalEntityID, &c.TransactionID, &cmd, &from, &to, &qty, &c.Date, &c.CreatedBy, &c.OccurredAt}, extra...)
	if err := row.Scan(dest...); err != nil {
		return StatusChange{}, err
	}
	c.Command, c.From, c.To = domain.StepCommand(cmd), domain.Status(from), domain.Status(to)
	c.Qty = decimal.RequireFromString(qty)
	return c, nil
}

// statusChangeByKey returns the audit row stored under an idempotency key. A
// row stored for a different request is idempotency_key_reuse.
func statusChangeByKey(ctx context.Context, tx pgx.Tx, tenantID, key, hash string) (StatusChange, bool, error) {
	var stored string
	c, err := scanStatusChange(tx.QueryRow(ctx, `select `+statusChangeColumns+`, request_hash
		from inventory_transaction_status_change where tenant_id = $1 and idempotency_key = $2`, tenantID, key), &stored)
	if errors.Is(err, pgx.ErrNoRows) {
		return StatusChange{}, false, nil
	}
	if err != nil {
		return StatusChange{}, false, err
	}
	if stored != hash {
		return StatusChange{}, false, problem(http.StatusUnprocessableEntity, CodeIdempotencyReuse,
			"Idempotency-Key was already used for a different status change")
	}
	return c, true, nil
}

// fingerprint hashes request fields, so a key reused for another request is
// rejected (422) rather than replayed.
func fingerprint(parts ...string) string {
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x1f")))
	return hex.EncodeToString(sum[:])
}

// requestedDate is the date as the caller sent it: a retry without a date on
// the next day must replay, not count as another request.
func requestedDate(d time.Time) string {
	if d.IsZero() {
		return ""
	}
	return d.Format(dateLayout)
}

func stepRequestHash(in StatusStepInput) string {
	cost := ""
	if in.CostAmount != nil {
		cost = in.CostAmount.String()
	}
	from, to := string(in.From), string(in.To)
	if in.Command != domain.CommandStep {
		from, to = "", "" // a named command's move follows from the current status
	}
	return fingerprint("STEP", in.TransactionID, string(in.Command), from, to, requestedDate(in.Date), cost)
}

func createRequestHash(in CreateTransactionInput) string {
	raw, _ := json.Marshal(in.Dims)
	return fingerprint("CREATE", in.LegalEntityID, in.SourceType, in.SourceID, strings.TrimSpace(in.Reference), in.ProductID, string(raw),
		in.Qty.String(), in.EnteredQty.String(), in.EnteredUnit, string(in.Status), in.Currency, in.CostPrice.String(), requestedDate(in.Date))
}

// StatusChangeFilter selects audit rows for the status-change inquiry. Empty
// fields do not filter; Cursor is the id of the last row of the previous page
// (ids are UUIDv7, so id order is creation order).
type StatusChangeFilter struct {
	TransactionID string
	Command       domain.StepCommand
	Cursor        string
	Limit         int
}

// ListStatusChanges returns up to f.Limit audit rows, newest first.
func (s *Postgres) ListStatusChanges(ctx context.Context, f StatusChangeFilter) ([]StatusChange, error) {
	limit := f.Limit
	if limit <= 0 || limit > maxListLimit {
		limit = 100
	}
	var cursor *string
	if f.Cursor != "" {
		cursor = &f.Cursor
	}
	tp, _ := tenant.FromContext(ctx)
	out := []StatusChange{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+statusChangeColumns+` from inventory_transaction_status_change
			where tenant_id = $1 and ($2 = '' or transaction_id::text = $2) and ($3 = '' or command = $3) and ($4::uuid is null or id < $4::uuid)
			order by id desc limit $5`, tp.TenantID, f.TransactionID, string(f.Command), cursor, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			c, err := scanStatusChange(rows)
			if err != nil {
				return err
			}
			out = append(out, c)
		}
		return rows.Err()
	})
	return out, err
}

// GetTransaction returns one transaction with its status history, oldest
// first. An id outside the tenant or the caller's legal entities is not found.
func (s *Postgres) GetTransaction(ctx context.Context, id string) (TransactionRow, []StatusChange, error) {
	rows, err := s.ListTransactions(ctx, TransactionFilter{ID: id, Limit: 1})
	if err != nil {
		return TransactionRow{}, nil, err
	}
	if len(rows) == 0 {
		return TransactionRow{}, nil, problem(http.StatusNotFound, domain.CodeTransactionNotFound, "inventory transaction not found")
	}
	history, err := s.ListStatusChanges(ctx, StatusChangeFilter{TransactionID: id, Limit: maxListLimit})
	if err != nil {
		return TransactionRow{}, nil, err
	}
	slices.Reverse(history)
	return rows[0], history, nil
}

// checkStepFreeze applies the count freeze (INV-CNT-002) to a status change
// that moves the row's physical inventory. Steps that only touch the ordered,
// arrived or reserved buckets (for example ON_ORDER to ARRIVED) are not
// movements of counted stock and pass.
func checkStepFreeze(ctx context.Context, tx pgx.Tx, tenantID, legalEntityID string, r *row) error {
	if r.before.PhysicalInvent().Equal(r.oh.PhysicalInvent()) {
		return nil
	}
	return checkCountFreeze(ctx, tx, tenantID, legalEntityID, "", []*row{r})
}
