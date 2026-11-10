package store

import (
	"cmp"
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

// Sales delivery report problem codes (#496).
const (
	CodeSalesReportNotFound = "FUL-0080"
	// CodeSalesReportState is a retry of a report that is not PARKED or PENDING:
	// it was reported, or its slip was cancelled before it was.
	CodeSalesReportState = "FUL-0081"
)

// Sales delivery report statuses (#496).
const (
	SalesReportPending   = "PENDING"
	SalesReportReported  = "REPORTED"
	SalesReportParked    = "PARKED"
	SalesReportWithdrawn = "WITHDRAWN"
)

// SalesReportKeyPrefix starts the Idempotency-Key of every report: the key is
// the prefix, the slip id and the order id, so it is stable per (slip, order)
// and never collides with another caller's keys in sales.
const SalesReportKeyPrefix = "fulfillment-packing-slip:"

// SalesReport is one report of a posted packing slip to sales'
// :record-progress (kind DELIVERED), as the API lists it.
type SalesReport struct {
	ID            string     `json:"id"`
	LegalEntityID string     `json:"legalEntityId"`
	PackingSlipID string     `json:"packingSlipId"`
	SourceRef     string     `json:"sourceRef"`
	OrderID       string     `json:"orderId"`
	Status        string     `json:"status"`
	Attempts      int        `json:"attempts"`
	NextAttemptAt *time.Time `json:"nextAttemptAt,omitempty"`
	LastStatus    *int       `json:"lastStatus,omitempty"`
	LastError     *string    `json:"lastError,omitempty"`
	ReportedAt    *time.Time `json:"reportedAt,omitempty"`
	CancelledAt   *time.Time `json:"cancelledAt,omitempty"`
	CreatedAt     time.Time  `json:"createdAt"`
}

// SalesReportFilter narrows the list; empty fields do not filter.
type SalesReportFilter struct {
	Status        string
	PackingSlipID string
	Limit         int
}

// ClaimedSalesReport is a report the reporter leased: Attempts is this
// attempt's number and the lease token FinishSalesReport checks.
type ClaimedSalesReport struct {
	ID             string
	LegalEntityID  string
	OrderID        string
	SourceRef      string
	IdempotencyKey string
	Body           []byte
	Attempts       int
}

// SalesReportOutcome is what one attempt achieved: Status REPORTED, PENDING
// (try again at RetryAt) or PARKED. HTTPStatus is sales' answer, 0 when the
// call never got one.
type SalesReportOutcome struct {
	Status     string
	RetryAt    time.Time
	HTTPStatus int
	Error      string
}

// SalesReportStore is the API's view of the reports: list them and put a
// parked one back in the queue.
type SalesReportStore interface {
	ListSalesReports(ctx context.Context, f SalesReportFilter) ([]SalesReport, error)
	RetrySalesReport(ctx context.Context, id string, idem Idem) (SalesReport, error)
}

// salesReportBody is the :record-progress body (sales' recordProgressRequest).
// Lines name the order line by id: fulfillment knows sales' line ids
// (order.confirmed line_id, the release line's orderLineId), not their numbers.
type salesReportBody struct {
	Kind      string            `json:"kind"`
	SourceRef string            `json:"sourceRef"`
	Lines     []salesReportLine `json:"lines"`
}

type salesReportLine struct {
	LineID       string               `json:"lineId"`
	Quantity     string               `json:"quantity"`
	ActualWeight *string              `json:"actualWeight,omitempty"`
	Tracking     []salesReportTracked `json:"tracking,omitempty"`
}

type salesReportTracked struct {
	Batch        string  `json:"batch,omitempty"`
	Serial       string  `json:"serial,omitempty"`
	Quantity     string  `json:"quantity"`
	ActualWeight *string `json:"actualWeight,omitempty"`
}

// salesReportLines builds the report lines of a slip: one per order line (two
// shipment lines of one order line are summed, as sales takes a line once per
// report), the slip's quantity, the catch weight as actualWeight, and the
// batch and serial entries stamped with the slip as tracking. Inventory books
// no location, so entries of one batch picked from two locations become one
// entry. A catch-weight line with one entry gives the entry the line's weight;
// with several, the entry weights are left out (fulfillment does not weigh per
// entry).
func salesReportLines(lines []PackingSlipLine, tracked map[string][]TrackingEntry) []salesReportLine {
	type acc struct {
		qty     decimal.Decimal
		weight  *decimal.Decimal
		entries []salesReportTracked
		qtys    []decimal.Decimal
		index   map[[2]string]int
	}
	var order []string
	byLine := map[string]*acc{}
	for _, l := range lines {
		a := byLine[l.OrderLineID]
		if a == nil {
			a = &acc{index: map[[2]string]int{}}
			byLine[l.OrderLineID] = a
			order = append(order, l.OrderLineID)
		}
		a.qty = a.qty.Add(l.Qty)
		if l.Weight != nil {
			w := *l.Weight
			if a.weight != nil {
				w = w.Add(*a.weight)
			}
			a.weight = &w
		}
		for _, e := range tracked[l.ShipmentLineID] {
			k := [2]string{strVal(e.BatchNo), strVal(e.SerialNo)}
			if i, ok := a.index[k]; ok {
				a.qtys[i] = a.qtys[i].Add(e.Qty)
				continue
			}
			a.index[k] = len(a.entries)
			a.entries = append(a.entries, salesReportTracked{Batch: k[0], Serial: k[1]})
			a.qtys = append(a.qtys, e.Qty)
		}
	}
	out := make([]salesReportLine, 0, len(order))
	for _, id := range order {
		a := byLine[id]
		line := salesReportLine{LineID: id, Quantity: a.qty.String()}
		if a.weight != nil {
			w := a.weight.String()
			line.ActualWeight = &w
		}
		for i := range a.entries {
			a.entries[i].Quantity = a.qtys[i].String()
		}
		// Entries captured together share a timestamp, so their read order is not
		// stable; the body is ordered by batch and serial instead.
		slices.SortFunc(a.entries, func(x, y salesReportTracked) int {
			return cmp.Or(cmp.Compare(x.Batch, y.Batch), cmp.Compare(x.Serial, y.Serial))
		})
		if len(a.entries) == 1 && line.ActualWeight != nil {
			w := *line.ActualWeight
			a.entries[0].ActualWeight = &w
		}
		if len(a.entries) > 0 {
			line.Tracking = a.entries
		}
		out = append(out, line)
	}
	return out
}

// queueSalesReport records the PENDING report of a slip just posted, inside
// the post transaction: the body is frozen now, so every attempt sends the
// same body under the same key. A slip belongs to one shipment and so to one
// order, which makes this one report per (slip, order).
func queueSalesReport(ctx context.Context, tx pgx.Tx, tenantID, legalEntityID string, slip PackingSlip, tracked map[string][]TrackingEntry) error {
	body, err := json.Marshal(salesReportBody{Kind: "DELIVERED", SourceRef: slip.Number, Lines: salesReportLines(slip.Lines, tracked)})
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `insert into sales_delivery_report (tenant_id, id, legal_entity_id, packing_slip_id, source_ref, order_id, idempotency_key, body)
		values ($1, $2, $3, $4, $5, $6, $7, $8)`,
		tenantID, ids.New(), legalEntityID, slip.ID, slip.Number, slip.OrderID, SalesReportKeyPrefix+slip.ID+":"+slip.OrderID, body)
	return err
}

// withdrawSalesReport runs in the cancel transaction: a report not yet made
// (PENDING or PARKED) is WITHDRAWN and never sent; one already REPORTED keeps
// its status and records the cancellation, as sales cannot reverse a delivery.
// A report the reporter is sending right now is withdrawn too; if that send
// succeeds, FinishSalesReport records REPORTED, which is what sales holds.
func withdrawSalesReport(ctx context.Context, tx pgx.Tx, tenantID, slipID string) error {
	_, err := tx.Exec(ctx, `update sales_delivery_report set cancelled_at = now(), updated_at = now(),
			status = case when status in ('PENDING', 'PARKED') then 'WITHDRAWN' else status end
		where tenant_id = $1 and packing_slip_id = $2 and cancelled_at is null`, tenantID, slipID)
	return err
}

// SalesReportSystemContext is the principal the reporter reads and writes the
// reports of one tenant under: no user, every legal entity of the tenant.
func SalesReportSystemContext(ctx context.Context, tenantID string) context.Context {
	return tenant.With(ctx, tenant.Principal{TenantID: tenantID, Service: "fulfillment", Duties: map[string]struct{}{tenant.DutyAllScope: {}}})
}

// SalesReportDueTenants lists the tenants with a report due now, across
// tenants (SECURITY DEFINER fulfillment_sales_report_due_tenants()).
func (s *Postgres) SalesReportDueTenants(ctx context.Context) ([]string, error) {
	rows, err := s.pool.Query(ctx, `select t::text from fulfillment_sales_report_due_tenants() t`)
	if err != nil {
		return nil, fmt.Errorf("fulfillment: due sales report tenants: %w", err)
	}
	return pgx.CollectRows(rows, pgx.RowTo[string])
}

// ClaimSalesReports leases up to limit due PENDING reports of the tenant in
// ctx: each one's attempts grows by one and its next attempt moves past the
// lease, so another replica skips it (FOR UPDATE SKIP LOCKED now, the lease
// after commit) and a reporter that dies mid-call gives it back when the
// lease runs out.
func (s *Postgres) ClaimSalesReports(ctx context.Context, limit int, lease time.Duration) ([]ClaimedSalesReport, error) {
	var out []ClaimedSalesReport
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `with claimed as (
				select id as claimed_id from sales_delivery_report where status = 'PENDING' and next_attempt_at <= now()
				order by next_attempt_at, id limit $1 for update skip locked)
			update sales_delivery_report r set attempts = r.attempts + 1, next_attempt_at = now() + make_interval(secs => $2), updated_at = now()
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

// FinishSalesReport records the outcome of the attempt numbered attempts. A
// report claimed again since (its lease ran out) is not touched and
// ErrStaleSalesReport is returned. A report whose slip was cancelled while it
// was being sent ends REPORTED when sales took it, and WITHDRAWN otherwise.
func (s *Postgres) FinishSalesReport(ctx context.Context, id string, attempts int, o SalesReportOutcome) error {
	var retry any
	if o.Status == SalesReportPending {
		retry = o.RetryAt
	}
	var httpStatus any
	if o.HTTPStatus != 0 {
		httpStatus = o.HTTPStatus
	}
	errText := o.Error
	if len(errText) > 1000 {
		errText = errText[:1000]
	}
	var lastError any
	if errText != "" {
		lastError = errText
	}
	return dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `update sales_delivery_report set
				status = case when $3 <> 'REPORTED' and cancelled_at is not null then 'WITHDRAWN' else $3 end,
				next_attempt_at = coalesce($4, next_attempt_at), last_status = $5, last_error = $6,
				reported_at = case when $3 = 'REPORTED' then now() else null end, updated_at = now()
			where id = $1 and attempts = $2 and status in ('PENDING', 'WITHDRAWN') and reported_at is null`,
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

// ErrStaleSalesReport is a finish for an attempt that no longer holds the
// report's lease.
var ErrStaleSalesReport = errors.New("fulfillment: the sales report was claimed again or finished since this attempt")

const salesReportCols = `id::text, legal_entity_id::text, packing_slip_id::text, source_ref, order_id::text, status, attempts,
	case when status = 'PENDING' then next_attempt_at end, last_status, last_error, reported_at, cancelled_at, created_at`

func scanSalesReport(row pgx.Row) (SalesReport, error) {
	var r SalesReport
	err := row.Scan(&r.ID, &r.LegalEntityID, &r.PackingSlipID, &r.SourceRef, &r.OrderID, &r.Status, &r.Attempts, &r.NextAttemptAt,
		&r.LastStatus, &r.LastError, &r.ReportedAt, &r.CancelledAt, &r.CreatedAt)
	return r, err
}

// ListSalesReports lists the reports newest first.
func (s *Postgres) ListSalesReports(ctx context.Context, f SalesReportFilter) ([]SalesReport, error) {
	out := []SalesReport{}
	limit := f.Limit
	if limit < 1 || limit > 500 {
		limit = 100
	}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+salesReportCols+` from sales_delivery_report
			where ($1 = '' or status = $1) and ($2 = '' or packing_slip_id::text = $2) order by created_at desc, id limit $3`,
			f.Status, f.PackingSlipID, limit)
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

// RetrySalesReport puts a PARKED report back in the queue, due now and with
// its attempts counted from zero; a PENDING one is left as it is. A REPORTED
// or WITHDRAWN report is refused (FUL-0081). The body and key do not change,
// so sales answers a report it already holds from its stored copy.
func (s *Postgres) RetrySalesReport(ctx context.Context, id string, idem Idem) (SalesReport, error) {
	p, _ := tenant.FromContext(ctx)
	var out SalesReport
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		res, err := runIdempotent(ctx, tx, p.TenantID, idem, func() (SalesReport, error) {
			r, err := scanSalesReport(tx.QueryRow(ctx, `select `+salesReportCols+` from sales_delivery_report where id = $1 for update`, id))
			if errors.Is(err, pgx.ErrNoRows) {
				return r, httpx.NewProblem(http.StatusNotFound, CodeSalesReportNotFound, "sales delivery report not found")
			}
			if err != nil {
				return r, err
			}
			switch r.Status {
			case SalesReportPending:
				return r, nil
			case SalesReportParked:
			default:
				return r, httpx.NewProblem(http.StatusConflict, CodeSalesReportState,
					fmt.Sprintf("sales delivery report is %s; only a PARKED report is retried", r.Status))
			}
			return scanSalesReport(tx.QueryRow(ctx, `update sales_delivery_report set status = 'PENDING', attempts = 0, next_attempt_at = now(), updated_at = now()
				where id = $1 returning `+salesReportCols, id))
		})
		out = res
		return err
	})
	return out, err
}
