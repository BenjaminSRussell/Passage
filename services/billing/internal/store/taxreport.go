package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/money"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/billing/internal/domain"
)

const (
	TaxReportPending  = "PENDING"
	TaxReportReported = "REPORTED"
	TaxReportParked   = "PARKED"
	taxKindInvoice    = "INVOICE"
	taxKindCreditNote = "CREDIT_NOTE"
	taxDirectionOut   = "output"
	maxTaxReportError = 1000

	CodeTaxReportNotFound = "BIL-0120"
	CodeTaxReportState    = "BIL-0121"
)

var ErrStaleTaxReport = errors.New("billing: the tax report was claimed again or finished since this attempt")

type ClaimedTaxReport struct {
	ID            string
	LegalEntityID string
	SourceRef     string
	Body          []byte
	Attempts      int
}

// TaxReportOutcome is what one attempt achieved: Status REPORTED, PENDING (try again at RetryAt) or PARKED. HTTPStatus is the
// tax service's answer, 0 when the call never got one.
type TaxReportOutcome struct {
	Status     string
	RetryAt    time.Time
	HTTPStatus int
	Error      string
}

type taxTransaction struct {
	ID        string `json:"id"`
	Code      string `json:"code"`
	Direction string `json:"direction"`
	TaxDate   string `json:"tax_date"`
	Amount    string `json:"amount"`
}

// queueTaxReport writes the PENDING report of a document just booked to the GL, inside the booking transaction: one output
// transaction per tax code with the tax the lines already carry (never re-quoted), negated on a credit note. A document without tax
// reports nothing. The unique (invoice, kind) makes a second call a no-op.
func queueTaxReport(ctx context.Context, tx pgx.Tx, id string) error {
	doc, err := scanInvoice(tx.QueryRow(ctx, `select `+invoiceColumns+` from invoice where id = $1::uuid`, id))
	if err != nil {
		return err
	}
	lines, err := loadLines(ctx, tx, id)
	if err != nil {
		return err
	}
	creditNote := doc.Type == domain.TypeCreditNote
	byCode := map[string]decimal.Decimal{}
	for _, l := range lines {
		byCode[l.TaxCode] = byCode[l.TaxCode].Add(l.Tax)
	}
	codes := make([]string, 0, len(byCode))
	for code, tax := range byCode {
		if !tax.IsZero() {
			codes = append(codes, code)
		}
	}
	if len(codes) == 0 {
		return nil
	}
	slices.Sort(codes)
	exp := money.MinorUnits(doc.Currency)
	body := struct {
		Transactions []taxTransaction `json:"transactions"`
	}{}
	for _, code := range codes {
		amount := byCode[code]
		if creditNote {
			amount = amount.Neg()
		}
		body.Transactions = append(body.Transactions, taxTransaction{ID: ids.New(), Code: code, Direction: taxDirectionOut,
			TaxDate: doc.PostingDate.Format(dateLayout), Amount: amount.StringFixed(exp)})
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return err
	}
	kind, original := taxKindInvoice, any(nil)
	if creditNote {
		kind, original = taxKindCreditNote, doc.OriginalInvoiceID
	}
	p, _ := tenant.FromContext(ctx)
	if _, err := tx.Exec(ctx, `insert into tax_transaction_report (tenant_id, id, legal_entity_id, invoice_id, original_invoice_id, kind, source_ref, body)
		values ($1, $2, $3, $4, $5, $6, $7, $8) on conflict (tenant_id, invoice_id, kind) do nothing`,
		p.TenantID, ids.New(), doc.LegalEntityID, doc.ID, original, kind, doc.Number, raw); err != nil {
		return fmt.Errorf("queue tax report: %w", err)
	}
	return nil
}

// TaxReportSystemContext is the principal the reporter reads and writes the reports of one tenant under: no user, every legal
// entity of the tenant.
func TaxReportSystemContext(ctx context.Context, tenantID string) context.Context {
	return tenant.With(ctx, tenant.Principal{TenantID: tenantID, Service: "billing", Duties: map[string]struct{}{tenant.DutyAllScope: {}}})
}

// TaxReportDueTenants lists the tenants with a report due now, across tenants (SECURITY DEFINER billing_tax_report_due_tenants()).
func (s *Postgres) TaxReportDueTenants(ctx context.Context) ([]string, error) {
	rows, err := s.pool.Query(ctx, `select t::text from billing_tax_report_due_tenants() t`)
	if err != nil {
		return nil, fmt.Errorf("billing: due tax report tenants: %w", err)
	}
	return pgx.CollectRows(rows, pgx.RowTo[string])
}

// ClaimTaxReports leases up to limit due PENDING reports of the tenant in ctx: each one's attempts grows by one and its next
// attempt moves past the lease, so another replica skips it and a reporter that dies mid-call gives it back when the lease ends.
func (s *Postgres) ClaimTaxReports(ctx context.Context, limit int, lease time.Duration) ([]ClaimedTaxReport, error) {
	var out []ClaimedTaxReport
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `with claimed as (
				select id as claimed_id from tax_transaction_report where status = 'PENDING' and next_attempt_at <= now()
				order by next_attempt_at, id limit $1 for update skip locked)
			update tax_transaction_report r set attempts = r.attempts + 1, next_attempt_at = now() + make_interval(secs => $2), updated_at = now()
			from claimed where r.id = claimed.claimed_id
			returning r.id::text, r.legal_entity_id::text, r.source_ref, r.body::text, r.attempts`,
			limit, lease.Seconds())
		if err != nil {
			return err
		}
		out, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (ClaimedTaxReport, error) {
			var c ClaimedTaxReport
			var body string
			err := r.Scan(&c.ID, &c.LegalEntityID, &c.SourceRef, &body, &c.Attempts)
			c.Body = []byte(body)
			return c, err
		})
		return err
	})
	return out, err
}

// FinishTaxReport records the outcome of the attempt numbered attempts. A report claimed again since is not touched and
// ErrStaleTaxReport is returned.
func (s *Postgres) FinishTaxReport(ctx context.Context, id string, attempts int, o TaxReportOutcome) error {
	var retry, httpStatus, lastError any
	if o.Status == TaxReportPending {
		retry = o.RetryAt
	}
	if o.HTTPStatus != 0 {
		httpStatus = o.HTTPStatus
	}
	if o.Error != "" {
		lastError = o.Error[:min(len(o.Error), maxTaxReportError)]
	}
	return dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `update tax_transaction_report set status = $3, next_attempt_at = coalesce($4, next_attempt_at),
				last_status = $5, last_error = $6, reported_at = case when $3 = 'REPORTED' then now() else null end, updated_at = now()
			where id = $1 and attempts = $2 and status = 'PENDING'`,
			id, attempts, o.Status, retry, httpStatus, lastError)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return ErrStaleTaxReport
		}
		return nil
	})
}

type TaxReport struct {
	ID                string     `json:"id"`
	LegalEntityID     string     `json:"legalEntityId"`
	InvoiceID         string     `json:"invoiceId"`
	OriginalInvoiceID *string    `json:"originalInvoiceId,omitempty"`
	Kind              string     `json:"kind"`
	SourceRef         string     `json:"sourceRef"`
	Status            string     `json:"status"`
	Attempts          int        `json:"attempts"`
	NextAttemptAt     *time.Time `json:"nextAttemptAt,omitempty"`
	LastStatus        *int       `json:"lastStatus,omitempty"`
	LastError         *string    `json:"lastError,omitempty"`
	ReportedAt        *time.Time `json:"reportedAt,omitempty"`
	CreatedAt         time.Time  `json:"createdAt"`
}

const taxReportCols = `id::text, legal_entity_id::text, invoice_id::text, original_invoice_id::text, kind, source_ref, status, attempts,
	case when status = 'PENDING' then next_attempt_at end, last_status, last_error, reported_at, created_at`

func scanTaxReport(row pgx.Row) (TaxReport, error) {
	var r TaxReport
	err := row.Scan(&r.ID, &r.LegalEntityID, &r.InvoiceID, &r.OriginalInvoiceID, &r.Kind, &r.SourceRef, &r.Status, &r.Attempts, &r.NextAttemptAt, &r.LastStatus, &r.LastError, &r.ReportedAt, &r.CreatedAt)
	return r, err
}

// ListTaxReports lists the reports newest first; an empty status does not filter.
func (s *Postgres) ListTaxReports(ctx context.Context, status string, limit int) ([]TaxReport, error) {
	out := []TaxReport{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+taxReportCols+` from tax_transaction_report where ($1 = '' or status = $1) order by created_at desc, id limit $2`, status, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			r, err := scanTaxReport(rows)
			if err != nil {
				return err
			}
			out = append(out, r)
		}
		return rows.Err()
	})
	return out, err
}

// RetryTaxReport puts a PARKED report back in the queue, due now with its attempts counted from zero; a PENDING one is left as it
// is. A REPORTED one is refused (BIL-0121). Body does not change, so the tax service answers a repeat from its stored transactions.
func (s *Postgres) RetryTaxReport(ctx context.Context, id string) (TaxReport, error) {
	var out TaxReport
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		r, err := scanTaxReport(tx.QueryRow(ctx, `select `+taxReportCols+` from tax_transaction_report where id = $1 for update`, id))
		if errors.Is(err, pgx.ErrNoRows) {
			return httpx.NewProblem(http.StatusNotFound, CodeTaxReportNotFound, "tax report not found")
		}
		if err != nil {
			return err
		}
		switch r.Status {
		case TaxReportPending:
			out = r
			return nil
		case TaxReportParked:
			out, err = scanTaxReport(tx.QueryRow(ctx, `update tax_transaction_report set status = 'PENDING', attempts = 0, next_attempt_at = now(), updated_at = now()
				where id = $1 returning `+taxReportCols, id))
			return err
		}
		return httpx.NewProblem(http.StatusConflict, CodeTaxReportState, "the tax report is "+r.Status+"; only a PARKED report is retried")
	})
	return out, err
}
