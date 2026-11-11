package store

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
)

// Payment status for the supplier portal (#511). The vendor reads its own invoices as accounts payable holds them: when they are
// due, whether a payment run has them, and when they were paid. A payment hold, a vendor block and the proposal's contents stay
// internal.

// Vendor payment statuses, in the vendor's words.
const (
	PaymentOpen          = "OPEN"
	PaymentScheduled     = "SCHEDULED"
	PaymentPartiallyPaid = "PARTIALLY_PAID"
	PaymentPaid          = "PAID"
	PaymentCancelled     = "CANCELLED"
)

// VendorPayment is one invoice of a vendor with its payment status.
type VendorPayment struct {
	InvoiceID     string          `json:"invoiceId"`
	InvoiceNumber string          `json:"invoiceNumber"`
	InvoiceDate   string          `json:"invoiceDate"`
	DueDate       string          `json:"dueDate"`
	Currency      string          `json:"currency"`
	Amount        decimal.Decimal `json:"amount"`
	PaidAmount    decimal.Decimal `json:"paidAmount"`
	OpenAmount    decimal.Decimal `json:"openAmount"`
	Status        string          `json:"status"`
	// ScheduledFor is the run date of the approved payment proposal that holds the invoice, while it is scheduled.
	ScheduledFor string `json:"scheduledFor,omitempty"`
	// PaidAt is the date the payment run settled the invoice, or the date of the last settlement.
	PaidAt string `json:"paidAt,omitempty"`

	cursorID string
}

// VendorPaymentFilter narrows a vendor's payment list. Status is one of the vendor statuses or empty.
type VendorPaymentFilter struct {
	Status string
	// Cursor is the pair returned as NextCursor (invoice date and open item id).
	CursorDate string
	CursorID   string
	Limit      int
}

// VendorPaymentStore reads the payment status of one vendor's invoices.
type VendorPaymentStore interface {
	VendorPayments(ctx context.Context, vendorID string, f VendorPaymentFilter) ([]VendorPayment, error)
}

var _ VendorPaymentStore = (*Postgres)(nil)

// Cursor is what to pass back as CursorDate and CursorID to continue after p.
func (p VendorPayment) Cursor() (date, id string) { return p.InvoiceDate, p.cursorID }

// vendorPaymentsSQL maps an open item to the vendor's status in SQL so a status filter and the page size agree: settled is PAID,
// partly settled PARTIALLY_PAID, reversed CANCELLED, and an open item is SCHEDULED while an approved or running payment proposal
// holds it, otherwise OPEN.
const vendorPaymentsSQL = `
	SELECT x.item_id, x.source_invoice_id, x.invoice_number, x.invoice_date, x.due_date, x.currency, x.amount, x.open_amount, x.vstatus, x.scheduled_for, x.settled_at, x.updated_at
	FROM (
	  SELECT o.id::text AS item_id, o.id AS item_uuid, o.invoice_date AS inv_date, o.source_invoice_id::text AS source_invoice_id, o.invoice_number,
	         to_char(o.invoice_date, 'YYYY-MM-DD') AS invoice_date, to_char(o.due_date, 'YYYY-MM-DD') AS due_date, o.currency, o.amount, o.open_amount, o.updated_at,
	         (SELECT to_char(p.run_date, 'YYYY-MM-DD') FROM ap_payment_proposal_line pl JOIN ap_payment_proposal p ON p.tenant_id = pl.tenant_id AND p.id = pl.proposal_id
	           WHERE pl.tenant_id = o.tenant_id AND pl.open_item_id = o.id AND p.status IN ('APPROVED', 'EXECUTING', 'EXECUTED') LIMIT 1) AS scheduled_for,
	         (SELECT r.settled_at FROM ap_payment_proposal_line pl JOIN ap_payment_run r ON r.tenant_id = pl.tenant_id AND r.proposal_id = pl.proposal_id
	           WHERE pl.tenant_id = o.tenant_id AND pl.open_item_id = o.id AND r.settled_at IS NOT NULL LIMIT 1) AS settled_at,
	         CASE o.status WHEN 'SETTLED' THEN 'PAID' WHEN 'PARTIALLY_SETTLED' THEN 'PARTIALLY_PAID' WHEN 'REVERSED' THEN 'CANCELLED'
	           ELSE CASE WHEN EXISTS (SELECT 1 FROM ap_payment_proposal_line pl JOIN ap_payment_proposal p ON p.tenant_id = pl.tenant_id AND p.id = pl.proposal_id
	                                  WHERE pl.tenant_id = o.tenant_id AND pl.open_item_id = o.id AND p.status IN ('APPROVED', 'EXECUTING', 'EXECUTED')) THEN 'SCHEDULED' ELSE 'OPEN' END END AS vstatus
	  FROM ap_open_item o
	  WHERE o.tenant_id = $6 AND o.vendor_id = $1::uuid AND ($2::date IS NULL OR (o.invoice_date, o.id) < ($2::date, $3::uuid))
	) x
	WHERE $4 = '' OR x.vstatus = $4
	ORDER BY x.inv_date DESC, x.item_uuid DESC LIMIT $5`

// VendorPayments lists a vendor's invoices newest first, one more than the limit so the caller can tell there is a next page. The
// vendor is the caller's verified one; the tenant and legal entity are the token's through row security.
func (s *Postgres) VendorPayments(ctx context.Context, vendorID string, f VendorPaymentFilter) ([]VendorPayment, error) {
	limit := f.Limit
	if limit < 1 || limit > maxLimit {
		limit = defaultLimit
	}
	var cursorDate, cursorID any
	if f.CursorDate != "" {
		cursorDate, cursorID = f.CursorDate, f.CursorID
	}
	out := []VendorPayment{}
	if !ids.Valid(vendorID) {
		return out, nil
	}
	p, _ := tenant.FromContext(ctx)
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, vendorPaymentsSQL, vendorID, cursorDate, cursorID, f.Status, limit+1, p.TenantID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var p VendorPayment
			var scheduledFor *string
			var settledAt *time.Time
			var updatedAt time.Time
			if err := rows.Scan(&p.cursorID, &p.InvoiceID, &p.InvoiceNumber, &p.InvoiceDate, &p.DueDate, &p.Currency, &p.Amount, &p.OpenAmount, &p.Status, &scheduledFor, &settledAt, &updatedAt); err != nil {
				return err
			}
			p.PaidAmount = p.Amount.Sub(p.OpenAmount)
			if scheduledFor != nil && p.Status == PaymentScheduled {
				p.ScheduledFor = *scheduledFor
			}
			if p.Status == PaymentPaid || p.Status == PaymentPartiallyPaid {
				paid := updatedAt
				if settledAt != nil {
					paid = *settledAt
				}
				p.PaidAt = paid.UTC().Format("2006-01-02")
			}
			out = append(out, p)
		}
		return rows.Err()
	})
	return out, err
}
