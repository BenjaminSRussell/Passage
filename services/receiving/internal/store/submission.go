package store

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/domain"
)

// Invoice submissions by received line (#511): the vendor-side hold on received quantities until accounts payable decides.

var ErrSubmissionNotFound = errors.New("invoice submission not found")

const submissionNumberConstraint = "receiving_invoice_submission_number_ux"

type Submission struct {
	ID            string
	LegalEntityID string
	VendorID      string
	InvoiceNumber string
	InvoiceDate   time.Time
	Currency      string
	Total         decimal.Decimal
	Note          string
	Status        string
	Lines         []domain.SubmissionLine
	CreatedBy     string
	CreatedAt     time.Time
	DecidedBy     string
	DecidedAt     *time.Time
	DecisionNote  string
	InvoiceID     string
}

type SubmissionFilter struct {
	VendorID        string
	Status          string
	CursorCreatedAt time.Time
	CursorID        string
	Limit           int
}

// InvoiceableLine is a received line a vendor can still bill: received, posted, not yet invoiced and not held by a submission.
type InvoiceableLine struct {
	ReceiptID       string
	ReceiptLineNo   int
	PackingSlip     string
	ReceiptDate     time.Time
	PurchaseOrderID string
	POLineNo        int
	ProductID       string
	ReceivedQty     decimal.Decimal
	InvoicedQty     decimal.Decimal
	ReservedQty     decimal.Decimal
	AvailableQty    decimal.Decimal
	UnitPrice       decimal.Decimal
	Currency        string
}

// SubmissionStore is the persistence of invoice submissions. Postgres implements it.
type SubmissionStore interface {
	InvoiceableLines(ctx context.Context, vendorID, purchaseOrderID string, limit int) ([]InvoiceableLine, error)
	CreateSubmission(ctx context.Context, in Submission) (Submission, error)
	GetSubmission(ctx context.Context, id string) (Submission, error)
	ListSubmissions(ctx context.Context, f SubmissionFilter) ([]Submission, error)
	WithdrawSubmission(ctx context.Context, id, vendorID string) (Submission, error)
	DecideSubmission(ctx context.Context, id, by, status, note, invoiceID string) (Submission, error)
}

var _ SubmissionStore = (*Postgres)(nil)

func submissionError(code, format string, args ...any) error {
	return &domain.Error{Code: code, Detail: fmt.Sprintf(format, args...)}
}

const invoiceableSQL = `
	SELECT r.id::text, l.line_no, r.packing_slip, r.receipt_date, r.purchase_order_id::text, COALESCE(l.po_line_no, 0), l.product_id::text,
	       g.received_qty, g.invoiced_qty,
	       COALESCE((SELECT sum(sl.qty) FROM receiving_invoice_submission_line sl JOIN receiving_invoice_submission s ON s.tenant_id = sl.tenant_id AND s.id = sl.submission_id
	                 WHERE sl.tenant_id = g.tenant_id AND sl.receipt_id = g.receipt_id AND sl.receipt_line_no = g.line_no AND s.status = 'SUBMITTED'), 0),
	       l.unit_price, g.currency
	FROM receiving_grir_open_item g
	JOIN receiving_receipt r ON r.tenant_id = g.tenant_id AND r.id = g.receipt_id
	JOIN receiving_receipt_line l ON l.tenant_id = g.tenant_id AND l.receipt_id = g.receipt_id AND l.line_no = g.line_no
	WHERE g.tenant_id = $1 AND r.vendor_id = $2::uuid AND r.state = 'POSTED' AND g.status IN ('OPEN', 'PARTIAL') AND g.received_qty > g.invoiced_qty`

func scanInvoiceable(row pgx.Row) (InvoiceableLine, error) {
	var l InvoiceableLine
	err := row.Scan(&l.ReceiptID, &l.ReceiptLineNo, &l.PackingSlip, &l.ReceiptDate, &l.PurchaseOrderID, &l.POLineNo, &l.ProductID, &l.ReceivedQty, &l.InvoicedQty, &l.ReservedQty, &l.UnitPrice, &l.Currency)
	l.AvailableQty = l.ReceivedQty.Sub(l.InvoicedQty).Sub(l.ReservedQty)
	return l, err
}

// InvoiceableLines lists the vendor's received lines with a quantity left to bill, newest receipt first. A vendor sees only the
// receipts of its own purchase orders: the vendor is the receipt's, never a parameter of the caller.
func (s *Postgres) InvoiceableLines(ctx context.Context, vendorID, purchaseOrderID string, limit int) ([]InvoiceableLine, error) {
	tenantID, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	limit = min(max(limit, 1), maxListLimit)
	out := []InvoiceableLine{}
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, invoiceableSQL+` AND ($3 = '' OR r.purchase_order_id = NULLIF($3, '')::uuid) ORDER BY r.receipt_date DESC, r.id, l.line_no LIMIT $4`, tenantID, vendorID, purchaseOrderID, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			l, err := scanInvoiceable(rows)
			if err != nil {
				return err
			}
			if l.AvailableQty.IsPositive() {
				out = append(out, l)
			}
		}
		return rows.Err()
	})
	return out, err
}

// CreateSubmission records a submission after checking, under row locks on the received lines, that every line is the vendor's,
// posted, in the invoice's currency, and that the quantities fit what is left once invoiced quantities and other open submissions
// are counted. The unit price is the vendor's claim; accounts payable matches it when it keys the invoice.
func (s *Postgres) CreateSubmission(ctx context.Context, in Submission) (Submission, error) {
	tenantID, err := tenantOf(ctx)
	if err != nil {
		return Submission{}, err
	}
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		keys := slices.Clone(in.Lines)
		slices.SortFunc(keys, func(a, b domain.SubmissionLine) int {
			return cmp.Or(strings.Compare(a.ReceiptID, b.ReceiptID), cmp.Compare(a.ReceiptLineNo, b.ReceiptLineNo))
		})
		for _, k := range keys {
			// The same answer for a line that does not exist, belongs to another vendor or is not posted: a vendor learns nothing about them.
			if !ids.Valid(k.ReceiptID) {
				return submissionError(domain.CodeSubmissionLine, "receipt %s line %d is not a received line of yours that can be invoiced", k.ReceiptID, k.ReceiptLineNo)
			}
			var vendor, state, currency string
			var received, invoiced decimal.Decimal
			err := tx.QueryRow(ctx, `SELECT r.vendor_id::text, r.state, g.currency, g.received_qty, g.invoiced_qty
				FROM receiving_grir_open_item g JOIN receiving_receipt r ON r.tenant_id = g.tenant_id AND r.id = g.receipt_id
				WHERE g.tenant_id = $1 AND g.receipt_id = $2::uuid AND g.line_no = $3 FOR UPDATE OF g`, tenantID, k.ReceiptID, k.ReceiptLineNo).Scan(&vendor, &state, &currency, &received, &invoiced)
			if errors.Is(err, pgx.ErrNoRows) || (err == nil && (!strings.EqualFold(vendor, in.VendorID) || state != "POSTED")) {
				return submissionError(domain.CodeSubmissionLine, "receipt %s line %d is not a received line of yours that can be invoiced", k.ReceiptID, k.ReceiptLineNo)
			}
			if err != nil {
				return err
			}
			if currency != in.Currency {
				return submissionError(domain.CodeSubmissionLine, "receipt %s line %d is in %s, the invoice is in %s", k.ReceiptID, k.ReceiptLineNo, currency, in.Currency)
			}
			var reserved decimal.Decimal
			if err := tx.QueryRow(ctx, `SELECT COALESCE(sum(sl.qty), 0) FROM receiving_invoice_submission_line sl JOIN receiving_invoice_submission s ON s.tenant_id = sl.tenant_id AND s.id = sl.submission_id
				WHERE sl.tenant_id = $1 AND sl.receipt_id = $2::uuid AND sl.receipt_line_no = $3 AND s.status = 'SUBMITTED'`, tenantID, k.ReceiptID, k.ReceiptLineNo).Scan(&reserved); err != nil {
				return err
			}
			if left := received.Sub(invoiced).Sub(reserved); k.Qty.GreaterThan(left) {
				return submissionError(domain.CodeSubmissionOverQty, "receipt %s line %d has %s left to invoice (received %s, invoiced %s, in other submissions %s)", k.ReceiptID, k.ReceiptLineNo, decimal.Max(left, decimal.Zero), received, invoiced, reserved)
			}
		}
		if _, err := tx.Exec(ctx, `INSERT INTO receiving_invoice_submission (tenant_id, id, legal_entity_id, vendor_id, invoice_number, invoice_date, currency, total, note, status, created_by)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, 'SUBMITTED', $10)`,
			tenantID, in.ID, in.LegalEntityID, in.VendorID, in.InvoiceNumber, in.InvoiceDate, in.Currency, in.Total, in.Note, in.CreatedBy); err != nil {
			return err
		}
		rows := make([][]any, len(in.Lines))
		for i, l := range in.Lines {
			rows[i] = []any{tenantID, in.ID, i + 1, in.LegalEntityID, l.ReceiptID, l.ReceiptLineNo, l.Qty, l.UnitPrice, l.Amount}
		}
		return execBatch(ctx, tx, `INSERT INTO receiving_invoice_submission_line (tenant_id, submission_id, line_no, legal_entity_id, receipt_id, receipt_line_no, qty, unit_price, amount)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`, rows)
	})
	if isUnique(err, submissionNumberConstraint) {
		return Submission{}, submissionError(domain.CodeSubmissionDuplicate, "invoice %q was already submitted", in.InvoiceNumber)
	}
	if err != nil {
		return Submission{}, err
	}
	return s.GetSubmission(ctx, in.ID)
}

const submissionColumns = `s.id::text, s.legal_entity_id::text, s.vendor_id::text, s.invoice_number, s.invoice_date, s.currency, s.total, s.note, s.status, s.created_by, s.created_at,
	COALESCE(s.decided_by, ''), s.decided_at, s.decision_note, COALESCE(s.invoice_id::text, '')`

func scanSubmission(row pgx.Row) (Submission, error) {
	var x Submission
	err := row.Scan(&x.ID, &x.LegalEntityID, &x.VendorID, &x.InvoiceNumber, &x.InvoiceDate, &x.Currency, &x.Total, &x.Note, &x.Status, &x.CreatedBy, &x.CreatedAt, &x.DecidedBy, &x.DecidedAt, &x.DecisionNote, &x.InvoiceID)
	return x, err
}

func submissionLines(ctx context.Context, tx pgx.Tx, tenantID string, submissionIDs []string) (map[string][]domain.SubmissionLine, error) {
	out := map[string][]domain.SubmissionLine{}
	rows, err := tx.Query(ctx, `SELECT submission_id::text, receipt_id::text, receipt_line_no, qty, unit_price, amount FROM receiving_invoice_submission_line
		WHERE tenant_id = $1 AND submission_id = ANY($2::uuid[]) ORDER BY submission_id, line_no`, tenantID, submissionIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var l domain.SubmissionLine
		if err := rows.Scan(&id, &l.ReceiptID, &l.ReceiptLineNo, &l.Qty, &l.UnitPrice, &l.Amount); err != nil {
			return nil, err
		}
		out[id] = append(out[id], l)
	}
	return out, rows.Err()
}

func (s *Postgres) GetSubmission(ctx context.Context, id string) (Submission, error) {
	if !ids.Valid(id) {
		return Submission{}, ErrSubmissionNotFound
	}
	tenantID, err := tenantOf(ctx)
	if err != nil {
		return Submission{}, err
	}
	var out Submission
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		if out, err = scanSubmission(tx.QueryRow(ctx, `SELECT `+submissionColumns+` FROM receiving_invoice_submission s WHERE s.tenant_id = $1 AND s.id = $2::uuid`, tenantID, id)); err != nil {
			return err
		}
		lines, err := submissionLines(ctx, tx, tenantID, []string{out.ID})
		out.Lines = lines[out.ID]
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return Submission{}, ErrSubmissionNotFound
	}
	return out, err
}

// ListSubmissions lists newest first, one more than the limit so the caller can tell there is a next page.
func (s *Postgres) ListSubmissions(ctx context.Context, f SubmissionFilter) ([]Submission, error) {
	tenantID, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	limit := min(max(f.Limit, 1), maxListLimit)
	var cursorID any
	if !f.CursorCreatedAt.IsZero() {
		cursorID = f.CursorID
	}
	out := []Submission{}
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT `+submissionColumns+` FROM receiving_invoice_submission s
			WHERE s.tenant_id = $1 AND ($2 = '' OR s.vendor_id = NULLIF($2, '')::uuid) AND ($3 = '' OR s.status = $3)
			AND ($4::timestamptz IS NULL OR (s.created_at, s.id) < ($4, $5::uuid))
			ORDER BY s.created_at DESC, s.id DESC LIMIT $6`, tenantID, f.VendorID, f.Status, nullTime(f.CursorCreatedAt), cursorID, limit+1)
		if err != nil {
			return err
		}
		defer rows.Close()
		var ids []string
		for rows.Next() {
			x, err := scanSubmission(rows)
			if err != nil {
				return err
			}
			out = append(out, x)
			ids = append(ids, x.ID)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		rows.Close()
		lines, err := submissionLines(ctx, tx, tenantID, ids)
		for i := range out {
			out[i].Lines = lines[out[i].ID]
		}
		return err
	})
	return out, err
}

// moveSubmission changes the status of a SUBMITTED submission; vendorID, when set, must own it.
func (s *Postgres) moveSubmission(ctx context.Context, id, vendorID, status, by, note, invoiceID string) (Submission, error) {
	if !ids.Valid(id) {
		return Submission{}, ErrSubmissionNotFound
	}
	if invoiceID != "" && !ids.Valid(invoiceID) {
		return Submission{}, submissionError(domain.CodeSubmissionInvalid, "invoiceId is not a posted invoice")
	}
	tenantID, err := tenantOf(ctx)
	if err != nil {
		return Submission{}, err
	}
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		cur, err := scanSubmission(tx.QueryRow(ctx, `SELECT `+submissionColumns+` FROM receiving_invoice_submission s WHERE s.tenant_id = $1 AND s.id = $2::uuid FOR UPDATE`, tenantID, id))
		if err != nil {
			return err
		}
		if vendorID != "" && !strings.EqualFold(cur.VendorID, vendorID) {
			return pgx.ErrNoRows
		}
		if cur.Status != domain.SubmissionSubmitted {
			return submissionError(domain.CodeSubmissionState, "the submission is %s", cur.Status)
		}
		if invoiceID != "" {
			var vendor string
			if err := tx.QueryRow(ctx, `SELECT vendor_id::text FROM receiving_vendor_invoice WHERE tenant_id = $1 AND id = $2::uuid`, tenantID, invoiceID).Scan(&vendor); err != nil {
				if errors.Is(err, pgx.ErrNoRows) {
					return submissionError(domain.CodeSubmissionInvalid, "invoiceId is not a posted invoice")
				}
				return err
			}
			if !strings.EqualFold(vendor, cur.VendorID) {
				return submissionError(domain.CodeSubmissionInvalid, "the invoice is another vendor's")
			}
		}
		_, err = tx.Exec(ctx, `UPDATE receiving_invoice_submission SET status = $3, decided_by = NULLIF($4, ''), decided_at = CASE WHEN $3 IN ('REJECTED', 'POSTED') THEN now() END,
			decision_note = $5, invoice_id = NULLIF($6, '')::uuid WHERE tenant_id = $1 AND id = $2::uuid`, tenantID, id, status, by, note, invoiceID)
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return Submission{}, ErrSubmissionNotFound
	}
	if err != nil {
		return Submission{}, err
	}
	return s.GetSubmission(ctx, id)
}

func (s *Postgres) WithdrawSubmission(ctx context.Context, id, vendorID string) (Submission, error) {
	return s.moveSubmission(ctx, id, vendorID, domain.SubmissionWithdrawn, "", "", "")
}

// DecideSubmission closes a submission for accounts payable: REJECTED with a note, or POSTED with the invoice that was keyed.
func (s *Postgres) DecideSubmission(ctx context.Context, id, by, status, note, invoiceID string) (Submission, error) {
	return s.moveSubmission(ctx, id, "", status, by, note, invoiceID)
}
