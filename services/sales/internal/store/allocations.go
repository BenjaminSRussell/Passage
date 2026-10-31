package store

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/sales/internal/domain"
	salesevents "github.com/BenjaminSRussell/passage/services/sales/internal/events"
)

// Problem codes of the allocation quotas (FR-27.16, BR-27.15).
const (
	CodeAllocationExhausted = "SLS-0013"
	CodeQuotaOverlap        = "SLS-0035"
	CodeQuotaBelowConsumed  = "SLS-0036"
	CodeQuotaNotFound       = "SLS-0108"

	// allocationSource marks a line confirmed in a later period than requested.
	allocationSource = "ALLOCATION"
	// allocationAggregate routes allocation.consumed and allocation.returned to
	// passage.sales.allocation, keyed by the quota so a consumer sees one
	// quota's draws and returns in order.
	allocationAggregate = "allocation"
)

// NewAllocationQuota is a validated quota for one product, for one customer
// (or every customer when CustomerID is nil) over PeriodStart..PeriodEnd.
type NewAllocationQuota struct {
	LegalEntityID  string
	ProductID      string
	CustomerID     *string
	PeriodStart    string
	PeriodEnd      string
	Quantity       decimal.Decimal
	IdempotencyKey string
	RequestHash    string
	Actor          string
}

// AllocationQuota is a quota with what is consumed and what remains.
type AllocationQuota struct {
	ID            string          `json:"id"`
	LegalEntityID string          `json:"legalEntityId"`
	ProductID     string          `json:"productId"`
	CustomerID    *string         `json:"customerId,omitempty"`
	PeriodStart   string          `json:"periodStart"`
	PeriodEnd     string          `json:"periodEnd"`
	QuotaQty      decimal.Decimal `json:"quotaQty"`
	ConsumedQty   decimal.Decimal `json:"consumedQty"`
	RemainingQty  decimal.Decimal `json:"remainingQty"`
	Version       int             `json:"version"`
	CreatedBy     string          `json:"createdBy"`
	CreatedAt     time.Time       `json:"createdAt"`
	UpdatedAt     time.Time       `json:"updatedAt"`
	// Consumptions is the draw history, newest first; filled only on a single read.
	Consumptions []AllocationConsumption `json:"consumptions,omitempty"`
}

// AllocationConsumption is one draw of an order line against a quota.
type AllocationConsumption struct {
	ID         string          `json:"id"`
	OrderID    string          `json:"orderId"`
	LineID     string          `json:"lineId"`
	LineNo     int             `json:"lineNo"`
	Quantity   decimal.Decimal `json:"quantity"`
	ConsumedAt time.Time       `json:"consumedAt"`
	ReturnedAt *time.Time      `json:"returnedAt,omitempty"`
}

// AllocationFilter narrows the quota inquiry. Empty fields match everything;
// an empty LegalEntityID means every legal entity in the caller's scope.
// OnDate keeps quotas whose period contains that day.
type AllocationFilter struct {
	LegalEntityID string
	ProductID     string
	CustomerID    string
	OnDate        string
	Limit         int
}

const quotaColumns = `id::text, legal_entity_id::text, product_id::text, customer_id::text, period_start::text, period_end::text,
	quota_qty::text, consumed_qty::text, version, created_by, created_at, updated_at`

func scanQuota(row pgx.Row) (AllocationQuota, error) {
	var q AllocationQuota
	var qty, used string
	if err := row.Scan(&q.ID, &q.LegalEntityID, &q.ProductID, &q.CustomerID, &q.PeriodStart, &q.PeriodEnd, &qty, &used, &q.Version,
		&q.CreatedBy, &q.CreatedAt, &q.UpdatedAt); err != nil {
		return AllocationQuota{}, err
	}
	q.QuotaQty = decimal.RequireFromString(qty)
	q.ConsumedQty = decimal.RequireFromString(used)
	q.RemainingQty = q.QuotaQty.Sub(q.ConsumedQty)
	return q, nil
}

func quotaNotFound() error {
	return problem(http.StatusNotFound, CodeQuotaNotFound, "allocation quota not found")
}

func quotaEvent(q AllocationQuota, previous, actor string) salesevents.AllocationQuotaData {
	return salesevents.AllocationQuotaData{QuotaID: q.ID, ProductID: q.ProductID, CustomerID: q.CustomerID, PeriodStart: q.PeriodStart,
		PeriodEnd: q.PeriodEnd, QuotaQty: q.QuotaQty.String(), PreviousQty: previous, Consumed: q.ConsumedQty.String(), Actor: actor, Version: q.Version}
}

// CreateAllocationQuota stores a quota with consumed quantity 0. A repeat of
// the same Idempotency-Key with the same body returns the stored quota with
// created=false; the key with another body is refused. A quota whose period
// overlaps another quota of the same product and customer scope is 409
// SLS-0035, so a day is never covered by two quotas.
func (s *Postgres) CreateAllocationQuota(ctx context.Context, in NewAllocationQuota) (AllocationQuota, bool, error) {
	p, _ := tenant.FromContext(ctx)
	var out AllocationQuota
	created := false
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		// Serialise creates for one product of a legal entity, so two concurrent
		// requests with overlapping periods cannot both pass the overlap check.
		if _, err := tx.Exec(ctx, `select pg_advisory_xact_lock(hashtextextended('sales.allocation_quota:' || $1 || ':' || $2 || ':' || $3, 0))`,
			p.TenantID, in.LegalEntityID, in.ProductID); err != nil {
			return err
		}
		var existing, hash string
		err := tx.QueryRow(ctx, `select id::text, request_hash from allocation_quota where tenant_id = $1 and idempotency_key = $2`,
			p.TenantID, in.IdempotencyKey).Scan(&existing, &hash)
		if err == nil {
			if hash != in.RequestHash {
				return problem(http.StatusUnprocessableEntity, CodeIdempotencyReuse, "Idempotency-Key was already used with a different request body")
			}
			out, err = loadQuota(ctx, tx, p.TenantID, existing, false)
			return err
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		var overlap int
		if err := tx.QueryRow(ctx, `select count(*) from allocation_quota
			where tenant_id = $1 and legal_entity_id = $2 and product_id = $3 and customer_id is not distinct from $4::uuid
			  and period_start <= $6::date and period_end >= $5::date`,
			p.TenantID, in.LegalEntityID, in.ProductID, in.CustomerID, in.PeriodStart, in.PeriodEnd).Scan(&overlap); err != nil {
			return err
		}
		if overlap > 0 {
			return problem(http.StatusConflict, CodeQuotaOverlap, "an allocation quota for this product and customer already covers part of the period")
		}
		id := ids.New()
		if _, err := tx.Exec(ctx, `insert into allocation_quota (tenant_id, id, legal_entity_id, product_id, customer_id, period_start, period_end,
				quota_qty, idempotency_key, request_hash, created_by)
			values ($1, $2, $3, $4, $5, $6::date, $7::date, $8::numeric, $9, $10, $11)`,
			p.TenantID, id, in.LegalEntityID, in.ProductID, in.CustomerID, in.PeriodStart, in.PeriodEnd, in.Quantity.String(),
			in.IdempotencyKey, in.RequestHash, in.Actor); err != nil {
			return err
		}
		created = true
		out, err = loadQuota(ctx, tx, p.TenantID, id, false)
		if err != nil {
			return err
		}
		return enqueue(ctx, tx, "allocationquota", id, salesevents.AllocationQuotaSet, p.TenantID, quotaEvent(out, "", in.Actor))
	})
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation {
		return AllocationQuota{}, false, problem(http.StatusConflict, CodeQuotaOverlap, "an allocation quota with this period start exists for the product and customer, or a concurrent request stored it first")
	}
	if err != nil {
		return AllocationQuota{}, false, err
	}
	return out, created, nil
}

func loadQuota(ctx context.Context, tx pgx.Tx, tenantID, id string, lock bool) (AllocationQuota, error) {
	q := `select ` + quotaColumns + ` from allocation_quota where tenant_id = $1 and id = $2`
	if lock {
		q += " for update"
	}
	out, err := scanQuota(tx.QueryRow(ctx, q, tenantID, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return AllocationQuota{}, quotaNotFound()
	}
	return out, err
}

// ListAllocationQuotas is the quota inquiry per period: quota, consumed and
// remaining, ordered by product and period.
func (s *Postgres) ListAllocationQuotas(ctx context.Context, f AllocationFilter) ([]AllocationQuota, error) {
	p, _ := tenant.FromContext(ctx)
	out := []AllocationQuota{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+quotaColumns+` from allocation_quota
			where tenant_id = $1 and ($2 = '' or legal_entity_id::text = $2) and ($3 = '' or product_id::text = $3)
			  and ($4 = '' or customer_id::text = $4) and ($5 = '' or ($5::date between period_start and period_end))
			order by product_id, period_start desc, id limit $6`, p.TenantID, f.LegalEntityID, f.ProductID, f.CustomerID, f.OnDate, f.Limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			q, err := scanQuota(rows)
			if err != nil {
				return err
			}
			out = append(out, q)
		}
		return rows.Err()
	})
	return out, err
}

// GetAllocationQuota returns one quota with its draw history.
func (s *Postgres) GetAllocationQuota(ctx context.Context, id string) (AllocationQuota, error) {
	p, _ := tenant.FromContext(ctx)
	var q AllocationQuota
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		if q, err = loadQuota(ctx, tx, p.TenantID, id, false); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `select id::text, order_id::text, line_id::text, line_no, quantity::text, consumed_at, returned_at
			from allocation_consumption where tenant_id = $1 and quota_id = $2 order by consumed_at desc, id desc limit 200`, p.TenantID, id)
		if err != nil {
			return err
		}
		defer rows.Close()
		q.Consumptions = []AllocationConsumption{}
		for rows.Next() {
			var c AllocationConsumption
			var qty string
			if err := rows.Scan(&c.ID, &c.OrderID, &c.LineID, &c.LineNo, &qty, &c.ConsumedAt, &c.ReturnedAt); err != nil {
				return err
			}
			c.Quantity = decimal.RequireFromString(qty)
			q.Consumptions = append(q.Consumptions, c)
		}
		return rows.Err()
	})
	return q, err
}

// ChangeAllocationQuota sets a new quota quantity under If-Match. A quantity
// below the consumed quantity is 409 SLS-0036 (BR-27.15).
func (s *Postgres) ChangeAllocationQuota(ctx context.Context, id string, version int, qty decimal.Decimal, actor string, now time.Time) (AllocationQuota, error) {
	p, _ := tenant.FromContext(ctx)
	var out AllocationQuota
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		q, err := loadQuota(ctx, tx, p.TenantID, id, true)
		if err != nil {
			return err
		}
		if q.Version != version {
			return versionConflict()
		}
		if err := domain.ValidateQuotaChange(qty, q.ConsumedQty); err != nil {
			if errors.Is(err, domain.ErrQuotaBelowConsumed) {
				return problem(http.StatusConflict, CodeQuotaBelowConsumed, err.Error())
			}
			return problem(http.StatusUnprocessableEntity, CodeInvalidChange, err.Error())
		}
		if qty.Equal(q.QuotaQty) {
			out = q
			return nil
		}
		if _, err := tx.Exec(ctx, `update allocation_quota set quota_qty = $3::numeric, version = version + 1, updated_at = $4
			where tenant_id = $1 and id = $2`, p.TenantID, id, qty.String(), now); err != nil {
			return err
		}
		out, err = loadQuota(ctx, tx, p.TenantID, id, false)
		if err != nil {
			return err
		}
		return enqueue(ctx, tx, "allocationquota", id, salesevents.AllocationQuotaMoved, p.TenantID, quotaEvent(out, q.QuotaQty.String(), actor))
	})
	return out, err
}

// allocation is what allocateOrderTx did to the lines it considered: an
// SLS-0013 notice and the new confirmed ship date of each line it moved to a
// later period.
type allocation struct {
	Notices []Notice
	Moved   map[int]string
}

// lockOrderQuotasTx locks, in id order, every quota that can govern a line of
// o, so two transactions that draw on or return to the same quotas take the
// row locks in the same order and cannot deadlock.
func lockOrderQuotasTx(ctx context.Context, tx pgx.Tx, tenantID string, o Order) error {
	products := make([]string, 0, len(o.Lines))
	for _, l := range o.Lines {
		products = append(products, l.ProductID)
	}
	_, err := tx.Exec(ctx, `select id from allocation_quota
		where tenant_id = $1 and legal_entity_id = $2 and product_id = any($3::uuid[]) and (customer_id = $4 or customer_id is null)
		order by id for update`, tenantID, o.LegalEntityID, products, o.CustomerID)
	return err
}

// allocateOrderTx draws quota for the lines of a confirming order, in
// requested-period order (BR-27.15), and reports the lines it moved to a later
// period with an SLS-0013 notice each. lineNos limits the lines considered;
// nil means every line that is not cancelled. A line with no quota covering
// its requested date is not constrained. When no period has enough left the
// confirmation is refused with 422 SLS-0013; the caller's transaction must
// then roll back, because lines drawn before the refused one stay drawn.
func allocateOrderTx(ctx context.Context, tx pgx.Tx, tenantID string, o Order, lineNos map[int]bool) (allocation, error) {
	out := allocation{Moved: map[int]string{}}
	lines := make([]Line, 0, len(o.Lines))
	for _, l := range o.Lines {
		if l.LineStatus == domain.LineCancelled || (lineNos != nil && !lineNos[l.LineNo]) {
			continue
		}
		lines = append(lines, l)
	}
	if len(lines) == 0 {
		return out, nil
	}
	if err := lockOrderQuotasTx(ctx, tx, tenantID, o); err != nil {
		return out, err
	}
	sort.SliceStable(lines, func(i, j int) bool {
		if lines[i].RequestedShipDate != lines[j].RequestedShipDate {
			return lines[i].RequestedShipDate < lines[j].RequestedShipDate
		}
		return lines[i].LineNo < lines[j].LineNo
	})
	for _, l := range lines {
		requested, err := time.Parse(dayLayout, l.RequestedShipDate)
		if err != nil {
			return out, err
		}
		periods, err := quotaPeriodsTx(ctx, tx, tenantID, o.LegalEntityID, l.ProductID, o.CustomerID)
		if err != nil {
			return out, err
		}
		plan := domain.PlanAllocation(periods, requested, l.Quantity)
		switch plan.Outcome {
		case domain.AllocationUnconstrained:
			continue
		case domain.AllocationExhausted:
			return out, problem(http.StatusUnprocessableEntity, CodeAllocationExhausted,
				fmt.Sprintf("Allocation for line %d (product %s) from %s is exhausted and no later period has %s left; request a quota change",
					l.LineNo, l.ProductID, l.RequestedShipDate, l.Quantity.String()))
		}
		if _, err := tx.Exec(ctx, `update allocation_quota set consumed_qty = consumed_qty + $3::numeric, version = version + 1, updated_at = now()
			where tenant_id = $1 and id = $2`, tenantID, plan.QuotaID, l.Quantity.String()); err != nil {
			return out, err
		}
		if _, err := tx.Exec(ctx, `insert into allocation_consumption (tenant_id, id, legal_entity_id, quota_id, order_id, line_id, line_no, quantity)
			values ($1, $2, $3, $4, $5, $6, $7, $8::numeric)`,
			tenantID, ids.New(), o.LegalEntityID, plan.QuotaID, o.ID, l.ID, l.LineNo, l.Quantity.String()); err != nil {
			return out, err
		}
		q, err := loadQuota(ctx, tx, tenantID, plan.QuotaID, false)
		if err != nil {
			return out, err
		}
		confirm := plan.ConfirmDate.Format(dayLayout)
		data := salesevents.AllocationData{QuotaID: q.ID, OrderID: o.ID, LineID: l.ID, ProductID: l.ProductID, Quantity: l.Quantity.String(),
			Consumed: q.ConsumedQty.String(), Remaining: q.RemainingQty.String(), ConfirmedShipDate: confirm}
		if plan.Outcome == domain.AllocationNextPeriod {
			data.Source = allocationSource
			if _, err := tx.Exec(ctx, `update sales_order_line set confirmed_ship_date = $4::date where tenant_id = $1 and order_id = $2 and id = $3`,
				tenantID, o.ID, l.ID, confirm); err != nil {
				return out, err
			}
			out.Moved[l.LineNo] = confirm
			out.Notices = append(out.Notices, Notice{Code: CodeAllocationExhausted, LineNo: l.LineNo,
				Message: fmt.Sprintf("Allocation for product %s in the period of %s is exhausted; line %d is promised for %s (source %s)",
					l.ProductID, l.RequestedShipDate, l.LineNo, confirm, allocationSource)})
		}
		if err := enqueue(ctx, tx, allocationAggregate, q.ID, salesevents.AllocationConsumed, tenantID, data); err != nil {
			return out, err
		}
	}
	return out, nil
}

// quotaPeriodsTx loads, with a row lock, the quotas that govern a customer's
// orders of a product: the customer's own quotas when it has any, otherwise
// the all-customer quotas. They are returned by period start.
func quotaPeriodsTx(ctx context.Context, tx pgx.Tx, tenantID, legalEntityID, productID, customerID string) ([]domain.QuotaPeriod, error) {
	rows, err := tx.Query(ctx, `select id::text, customer_id is not null, period_start, period_end, quota_qty::text, consumed_qty::text
		from allocation_quota
		where tenant_id = $1 and legal_entity_id = $2 and product_id = $3 and (customer_id = $4 or customer_id is null)
		order by period_start, id for update`, tenantID, legalEntityID, productID, customerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var own, all []domain.QuotaPeriod
	for rows.Next() {
		var qp domain.QuotaPeriod
		var isOwn bool
		var qty, used string
		if err := rows.Scan(&qp.ID, &isOwn, &qp.Start, &qp.End, &qty, &used); err != nil {
			return nil, err
		}
		qp.Quantity, qp.Consumed = decimal.RequireFromString(qty), decimal.RequireFromString(used)
		if isOwn {
			own = append(own, qp)
		} else {
			all = append(all, qp)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(own) > 0 {
		return own, nil
	}
	return all, nil
}

// returnAllocationTx gives back what the order's lines consumed, to the period
// it was drawn from (BR-27.15). lineNos limits the lines; nil means every line
// of the order. It writes allocation.returned.v1 per consumption.
func returnAllocationTx(ctx context.Context, tx pgx.Tx, tenantID string, o Order, lineNos map[int]bool, now time.Time) error {
	rows, err := tx.Query(ctx, `select c.id::text, c.quota_id::text, c.line_id::text, c.line_no, c.quantity::text
		from allocation_consumption c where c.tenant_id = $1 and c.order_id = $2 and c.returned_at is null order by c.quota_id, c.line_no`, tenantID, o.ID)
	if err != nil {
		return err
	}
	type back struct {
		id, quotaID, lineID string
		lineNo              int
		qty                 decimal.Decimal
	}
	var pending []back
	for rows.Next() {
		var b back
		var qty string
		if err := rows.Scan(&b.id, &b.quotaID, &b.lineID, &b.lineNo, &qty); err != nil {
			rows.Close()
			return err
		}
		b.qty = decimal.RequireFromString(qty)
		if lineNos == nil || lineNos[b.lineNo] {
			pending = append(pending, b)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	if len(pending) == 0 {
		return nil
	}
	if err := lockOrderQuotasTx(ctx, tx, tenantID, o); err != nil {
		return err
	}
	products := map[string]string{}
	for _, l := range o.Lines {
		products[l.ID] = l.ProductID
	}
	for _, b := range pending {
		if _, err := tx.Exec(ctx, `update allocation_quota set consumed_qty = consumed_qty - $3::numeric, version = version + 1, updated_at = $4
			where tenant_id = $1 and id = $2`, tenantID, b.quotaID, b.qty.String(), now); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `update allocation_consumption set returned_at = $3 where tenant_id = $1 and id = $2`, tenantID, b.id, now); err != nil {
			return err
		}
		q, err := loadQuota(ctx, tx, tenantID, b.quotaID, false)
		if err != nil {
			return err
		}
		if err := enqueue(ctx, tx, allocationAggregate, q.ID, salesevents.AllocationReturned, tenantID, salesevents.AllocationData{
			QuotaID: q.ID, OrderID: o.ID, LineID: b.lineID, ProductID: products[b.lineID], Quantity: b.qty.String(),
			Consumed: q.ConsumedQty.String(), Remaining: q.RemainingQty.String()}); err != nil {
			return err
		}
	}
	return nil
}
