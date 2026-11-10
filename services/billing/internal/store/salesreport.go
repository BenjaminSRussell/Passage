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
	"github.com/BenjaminSRussell/passage/platform/tenant"
)

const (
	SalesReportPending  = "PENDING"
	SalesReportReported = "REPORTED"
	SalesReportParked   = "PARKED"
	// SalesReportKeyPrefix starts the Idempotency-Key of every report: the key is the prefix, the invoice id and the order id,
	// so it is stable per (invoice, order) and never collides with another caller's keys in sales.
	SalesReportKeyPrefix = "billing-invoice:"
	salesReportKind      = "INVOICED"
	maxSalesReportError  = 1000
)

type ClaimedSalesReport struct {
	ID             string
	LegalEntityID  string
	OrderID        string
	SourceRef      string
	IdempotencyKey string
	Body           []byte
	Attempts       int
}

// SalesReportOutcome is what one attempt achieved: Status REPORTED, PENDING (try again at RetryAt) or PARKED. HTTPStatus is
// sales' answer, 0 when the call never got one.
type SalesReportOutcome struct {
	Status     string
	RetryAt    time.Time
	HTTPStatus int
	Error      string
}

var ErrStaleSalesReport = errors.New("billing: the sales report was claimed again or finished since this attempt")

type salesReportBody struct {
	Kind      string            `json:"kind"`
	SourceRef string            `json:"sourceRef"`
	Lines     []salesReportLine `json:"lines"`
}

type salesReportLine struct {
	LineID   string `json:"lineId"`
	Quantity string `json:"quantity"`
}

// queueSalesReports writes the PENDING report of every order an invoice just posted bills lines of, inside the post
// transaction: the body is frozen now. Two invoice lines of one order line are summed, as sales takes a line once per report.
// An invoice without order-sourced lines (free text, subscription, milestone) reports nothing.
func queueSalesReports(ctx context.Context, tx pgx.Tx, tenantID string, inv Invoice) error {
	byOrder := map[string]map[string]decimal.Decimal{}
	for _, l := range inv.Lines {
		if l.Source.OrderID == "" || l.Source.OrderLineID == "" {
			continue
		}
		if byOrder[l.Source.OrderID] == nil {
			byOrder[l.Source.OrderID] = map[string]decimal.Decimal{}
		}
		byOrder[l.Source.OrderID][l.Source.OrderLineID] = byOrder[l.Source.OrderID][l.Source.OrderLineID].Add(l.Qty)
	}
	orders := make([]string, 0, len(byOrder))
	for o := range byOrder {
		orders = append(orders, o)
	}
	slices.Sort(orders)
	for _, orderID := range orders {
		lineIDs := make([]string, 0, len(byOrder[orderID]))
		for id := range byOrder[orderID] {
			lineIDs = append(lineIDs, id)
		}
		slices.Sort(lineIDs)
		lines := make([]salesReportLine, len(lineIDs))
		for i, id := range lineIDs {
			lines[i] = salesReportLine{LineID: id, Quantity: byOrder[orderID][id].String()}
		}
		body, err := json.Marshal(salesReportBody{Kind: salesReportKind, SourceRef: inv.Number, Lines: lines})
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `insert into sales_invoice_report (tenant_id, id, legal_entity_id, invoice_id, source_ref, order_id, idempotency_key, body)
			values ($1, $2, $3, $4, $5, $6, $7, $8)`,
			tenantID, ids.New(), inv.LegalEntityID, inv.ID, inv.Number, orderID, SalesReportKeyPrefix+inv.ID+":"+orderID, body); err != nil {
			return fmt.Errorf("queue sales report: %w", err)
		}
	}
	return nil
}

// SalesReportSystemContext is the principal the reporter reads and writes the reports of one tenant under: no user, every
// legal entity of the tenant.
func SalesReportSystemContext(ctx context.Context, tenantID string) context.Context {
	return tenant.With(ctx, tenant.Principal{TenantID: tenantID, Service: "billing", Duties: map[string]struct{}{tenant.DutyAllScope: {}}})
}

// SalesReportDueTenants lists the tenants with a report due now, across tenants (SECURITY DEFINER billing_sales_report_due_tenants()).
func (s *Postgres) SalesReportDueTenants(ctx context.Context) ([]string, error) {
	rows, err := s.pool.Query(ctx, `select t::text from billing_sales_report_due_tenants() t`)
	if err != nil {
		return nil, fmt.Errorf("billing: due sales report tenants: %w", err)
	}
	return pgx.CollectRows(rows, pgx.RowTo[string])
}

// ClaimSalesReports leases up to limit due PENDING reports of the tenant in ctx: each one's attempts grows by one and its next
// attempt moves past the lease, so another replica skips it (FOR UPDATE SKIP LOCKED now, the lease after commit) and a reporter
// that dies mid-call gives it back when the lease runs out.
func (s *Postgres) ClaimSalesReports(ctx context.Context, limit int, lease time.Duration) ([]ClaimedSalesReport, error) {
	var out []ClaimedSalesReport
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `with claimed as (
				select id as claimed_id from sales_invoice_report where status = 'PENDING' and next_attempt_at <= now()
				order by next_attempt_at, id limit $1 for update skip locked)
			update sales_invoice_report r set attempts = r.attempts + 1, next_attempt_at = now() + make_interval(secs => $2), updated_at = now()
			from claimed where r.id = claimed.claimed_id
			returning r.id::text, r.legal_entity_id::text, r.order_id::text, r.source_ref, r.idempotency_key, r.body::text, r.attempts`,
			limit, lease.Seconds())
		if err != nil {
			return err
		}
		out, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (ClaimedSalesReport, error) {
			var c ClaimedSalesReport
			var body string
			err := r.Scan(&c.ID, &c.LegalEntityID, &c.OrderID, &c.SourceRef, &c.IdempotencyKey, &body, &c.Attempts)
			c.Body = []byte(body)
			return c, err
		})
		return err
	})
	return out, err
}

// FinishSalesReport records the outcome of the attempt numbered attempts. A report claimed again since (its lease ran out) is
// not touched and ErrStaleSalesReport is returned.
func (s *Postgres) FinishSalesReport(ctx context.Context, id string, attempts int, o SalesReportOutcome) error {
	var retry, httpStatus, lastError any
	if o.Status == SalesReportPending {
		retry = o.RetryAt
	}
	if o.HTTPStatus != 0 {
		httpStatus = o.HTTPStatus
	}
	if o.Error != "" {
		lastError = o.Error[:min(len(o.Error), maxSalesReportError)]
	}
	return dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `update sales_invoice_report set status = $3, next_attempt_at = coalesce($4, next_attempt_at),
				last_status = $5, last_error = $6, reported_at = case when $3 = 'REPORTED' then now() else null end, updated_at = now()
			where id = $1 and attempts = $2 and status = 'PENDING'`,
			id, attempts, o.Status, retry, httpStatus, lastError)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return ErrStaleSalesReport
		}
		return nil
	})
}

const (
	CodeSalesReportNotFound = "BIL-0110"
	CodeSalesReportState    = "BIL-0111"
)

type SalesReport struct {
	ID            string     `json:"id"`
	LegalEntityID string     `json:"legalEntityId"`
	InvoiceID     string     `json:"invoiceId"`
	SourceRef     string     `json:"sourceRef"`
	OrderID       string     `json:"orderId"`
	Status        string     `json:"status"`
	Attempts      int        `json:"attempts"`
	NextAttemptAt *time.Time `json:"nextAttemptAt,omitempty"`
	LastStatus    *int       `json:"lastStatus,omitempty"`
	LastError     *string    `json:"lastError,omitempty"`
	ReportedAt    *time.Time `json:"reportedAt,omitempty"`
	CreatedAt     time.Time  `json:"createdAt"`
}

const salesReportCols = `id::text, legal_entity_id::text, invoice_id::text, source_ref, order_id::text, status, attempts,
	case when status = 'PENDING' then next_attempt_at end, last_status, last_error, reported_at, created_at`

func scanSalesReport(row pgx.Row) (SalesReport, error) {
	var r SalesReport
	err := row.Scan(&r.ID, &r.LegalEntityID, &r.InvoiceID, &r.SourceRef, &r.OrderID, &r.Status, &r.Attempts, &r.NextAttemptAt, &r.LastStatus, &r.LastError, &r.ReportedAt, &r.CreatedAt)
	return r, err
}

// ListSalesReports lists the reports newest first; an empty status does not filter.
func (s *Postgres) ListSalesReports(ctx context.Context, status string, limit int) ([]SalesReport, error) {
	out := []SalesReport{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+salesReportCols+` from sales_invoice_report where ($1 = '' or status = $1) order by created_at desc, id limit $2`, status, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			r, err := scanSalesReport(rows)
			if err != nil {
				return err
			}
			out = append(out, r)
		}
		return rows.Err()
	})
	return out, err
}

// RetrySalesReport puts a PARKED report back in the queue, due now with its attempts counted from zero; a PENDING one is left
// as it is. A REPORTED one is refused (BIL-0111). Body and key do not change, so sales answers a report it holds from its copy.
func (s *Postgres) RetrySalesReport(ctx context.Context, id string) (SalesReport, error) {
	var out SalesReport
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		r, err := scanSalesReport(tx.QueryRow(ctx, `select `+salesReportCols+` from sales_invoice_report where id = $1 for update`, id))
		if errors.Is(err, pgx.ErrNoRows) {
			return httpx.NewProblem(http.StatusNotFound, CodeSalesReportNotFound, "sales report not found")
		}
		if err != nil {
			return err
		}
		switch r.Status {
		case SalesReportPending:
			out = r
			return nil
		case SalesReportParked:
			out, err = scanSalesReport(tx.QueryRow(ctx, `update sales_invoice_report set status = 'PENDING', attempts = 0, next_attempt_at = now(), updated_at = now()
				where id = $1 returning `+salesReportCols, id))
			return err
		}
		return httpx.NewProblem(http.StatusConflict, CodeSalesReportState, "the sales report is "+r.Status+"; only a PARKED report is retried")
	})
	return out, err
}
