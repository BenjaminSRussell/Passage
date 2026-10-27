// Package store persists production orders in Postgres. Every write runs in a
// tenant-scoped transaction (RLS) and enqueues its event in the same transaction.
package store

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/events"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/outbox"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/production/internal/domain"
	prodevents "github.com/BenjaminSRussell/passage/services/production/internal/events"
)

// Stable error codes returned by the store.
const (
	CodeOrderNotFound    = "PRODUCTION_ORDER_NOT_FOUND"
	CodeVersionConflict  = "conflict_version"
	CodeIdempotencyReuse = "idempotency_key_reuse"
	eventSource          = "production"
	aggregateType        = "production_order"
)

// Order is the order view served by the API: header, exploded BOM, route,
// planned cost. Decimals serialize as strings.
type Order struct {
	ID            string             `json:"id"`
	LegalEntityID string             `json:"legalEntityId"`
	OrderNumber   string             `json:"orderNumber"`
	ProductID     string             `json:"productId"`
	SiteID        string             `json:"siteId"`
	WarehouseID   string             `json:"warehouseId"`
	Currency      string             `json:"currency"`
	Quantity      decimal.Decimal    `json:"quantity"`
	QtyRAF        decimal.Decimal    `json:"qtyRaf"`
	QtyError      decimal.Decimal    `json:"qtyError"`
	Status        domain.Status      `json:"status"`
	DeliveryDate  *string            `json:"deliveryDate,omitempty"`
	OriginType    string             `json:"originType"`
	OriginID      *string            `json:"originId,omitempty"`
	Version       int                `json:"version"`
	Lines         []domain.OrderLine `json:"lines"`
	Operations    []domain.Operation `json:"operations"`
	Costs         []domain.CostRow   `json:"costs"`
	CreatedAt     time.Time          `json:"createdAt"`
	UpdatedAt     time.Time          `json:"updatedAt"`
}

// OrderSummary is one row of the order list (PRD 4.21.14 "Production orders list").
type OrderSummary struct {
	ID            string          `json:"id"`
	LegalEntityID string          `json:"legalEntityId"`
	OrderNumber   string          `json:"orderNumber"`
	ProductID     string          `json:"productId"`
	SiteID        string          `json:"siteId"`
	WarehouseID   string          `json:"warehouseId"`
	Currency      string          `json:"currency"`
	Quantity      decimal.Decimal `json:"quantity"`
	QtyRAF        decimal.Decimal `json:"qtyRaf"`
	QtyError      decimal.Decimal `json:"qtyError"`
	Remaining     decimal.Decimal `json:"remaining"`
	Status        domain.Status   `json:"status"`
	DeliveryDate  *string         `json:"deliveryDate,omitempty"`
	PlannedCost   decimal.Decimal `json:"plannedCost"`
	Version       int             `json:"version"`
	CreatedAt     time.Time       `json:"createdAt"`
}

// OrderPage is a keyset page of the order list, newest first.
type OrderPage struct {
	Items      []OrderSummary `json:"items"`
	NextCursor string         `json:"nextCursor,omitempty"`
}

// ListFilter narrows the order list. Empty fields do not filter. Cursor is the
// id of the last order of the previous page.
type ListFilter struct {
	Status        domain.Status
	LegalEntityID string
	SiteID        string
	ProductID     string
	Cursor        string
	Limit         int
}

// StatusLogEntry is one applied command (FR-21.2): actor, time and trace id.
type StatusLogEntry struct {
	Command    domain.Command `json:"command"`
	From       domain.Status  `json:"from"`
	To         domain.Status  `json:"to"`
	Actor      string         `json:"actor"`
	TraceID    string         `json:"traceId"`
	OccurredAt time.Time      `json:"occurredAt"`
}

// NewOrder is a validated order to create, with its exploded snapshot.
// RequestHash identifies the request body for Idempotency-Key replays.
type NewOrder struct {
	LegalEntityID  string
	ProductID      string
	SiteID         string
	WarehouseID    string
	Currency       string
	Quantity       decimal.Decimal
	DeliveryDate   *string
	OriginType     string
	OriginID       *string
	Lines          []domain.OrderLine
	Operations     []domain.Operation
	IdempotencyKey string
	RequestHash    string
}

// OrderUpdate is a PATCH of the header. A nil Quantity leaves it; SetDeliveryDate
// with a nil DeliveryDate clears the date.
type OrderUpdate struct {
	OrderID         string
	IfMatch         int
	Quantity        *decimal.Decimal
	SetDeliveryDate bool
	DeliveryDate    *string
	Actor           string
}

// Transition is one `:command` request. ResetTo is the target of
// `:reset-status` (empty for the previous status). Good and Error are the
// quantities of `:report-finished`; RAFTolerancePct bounds them (FR-21.7).
type Transition struct {
	OrderID         string
	IfMatch         int
	Command         domain.Command
	ResetTo         domain.Status
	Good            decimal.Decimal
	Error           decimal.Decimal
	RAFTolerancePct decimal.Decimal
	ZeroOutputDuty  bool
	Actor           string
	TraceID         string
	IdempotencyKey  string
	RequestHash     string
}

// Store is what the API needs from persistence.
type Store interface {
	Ping(ctx context.Context) error
	CreateOrder(ctx context.Context, in NewOrder) (Order, bool, error)
	GetOrder(ctx context.Context, id string) (Order, error)
	ListOrders(ctx context.Context, f ListFilter) (OrderPage, error)
	StatusLog(ctx context.Context, orderID string) ([]StatusLogEntry, error)
	UpdateOrder(ctx context.Context, u OrderUpdate) (Order, error)
	Transition(ctx context.Context, t Transition) (Order, bool, error)
}

// Postgres is the production store.
type Postgres struct {
	pool *pgxpool.Pool
}

var _ Store = (*Postgres)(nil)

// New returns a store over pool.
func New(pool *pgxpool.Pool) *Postgres { return &Postgres{pool: pool} }

// Ping checks the database for readiness.
func (s *Postgres) Ping(ctx context.Context) error { return s.pool.Ping(ctx) }

// querier is satisfied by both pgx.Tx and the pool, so loaders run in or out of a transaction.
type querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// dec parses a numeric value selected as text. Database numerics are always well formed.
func dec(s string) decimal.Decimal {
	v, _ := decimal.NewFromString(s)
	return v
}

func notFound() error {
	return httpx.NewProblem(http.StatusNotFound, CodeOrderNotFound, "production order not found")
}

func enqueue(ctx context.Context, tx pgx.Tx, orderID, eventType, tenantID, actor string, data any) error {
	evt, err := events.New(eventType, eventSource, orderID, tenantID, data)
	if err != nil {
		return err
	}
	evt.Actor = actor
	return outbox.Enqueue(ctx, tx, aggregateType, orderID, evt)
}

// CreateOrder inserts an order with its exploded lines and operations and
// allocates its number from the legal entity's sequence. A replay with the same
// Idempotency-Key and body returns the stored order and created=false; the same
// key with another body is refused.
func (s *Postgres) CreateOrder(ctx context.Context, in NewOrder) (Order, bool, error) {
	p, _ := tenant.FromContext(ctx)
	var out Order
	created := false
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var replayID, replayHash string
		err := tx.QueryRow(ctx, `select id::text, request_hash from production_order where tenant_id = $1 and idempotency_key = $2`,
			p.TenantID, in.IdempotencyKey).Scan(&replayID, &replayHash)
		if err == nil {
			if replayHash != in.RequestHash {
				return httpx.NewProblem(http.StatusUnprocessableEntity, CodeIdempotencyReuse, "Idempotency-Key was already used with a different request body")
			}
			out, err = loadOrder(ctx, tx, p.TenantID, replayID)
			return err
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		var seq int64
		if err := tx.QueryRow(ctx, `insert into production_order_sequence (tenant_id, legal_entity_id, next_value) values ($1, $2, 2)
			on conflict (tenant_id, legal_entity_id) do update set next_value = production_order_sequence.next_value + 1
			returning next_value - 1`, p.TenantID, in.LegalEntityID).Scan(&seq); err != nil {
			return err
		}
		id := ids.New()
		number := fmt.Sprintf("PO-%06d", seq)
		if _, err := tx.Exec(ctx, `insert into production_order (tenant_id, id, legal_entity_id, order_number, product_id, site_id, warehouse_id, currency,
			quantity, delivery_date, origin_type, origin_id, idempotency_key, request_hash)
			values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10::date, $11, $12, $13, $14)`,
			p.TenantID, id, in.LegalEntityID, number, in.ProductID, in.SiteID, in.WarehouseID, in.Currency,
			in.Quantity.String(), in.DeliveryDate, in.OriginType, in.OriginID, in.IdempotencyKey, in.RequestHash); err != nil {
			return err
		}
		if err := insertLines(ctx, tx, p.TenantID, id, in.Lines); err != nil {
			return err
		}
		if err := insertOperations(ctx, tx, p.TenantID, id, in.Operations); err != nil {
			return err
		}
		out, err = loadOrder(ctx, tx, p.TenantID, id)
		if err != nil {
			return err
		}
		created = true
		return enqueue(ctx, tx, id, prodevents.OrderCreated, p.TenantID, p.UserID, prodevents.OrderCreatedData{
			OrderID: id, OrderNumber: number, ProductID: in.ProductID, Quantity: in.Quantity.String(), SiteID: in.SiteID,
			DeliveryDate: in.DeliveryDate, OriginType: in.OriginType, OriginID: in.OriginID,
		})
	})
	return out, created, err
}

func insertLines(ctx context.Context, tx pgx.Tx, tenantID, orderID string, lines []domain.OrderLine) error {
	for i, l := range lines {
		if _, err := tx.Exec(ctx, `insert into production_bom_line (tenant_id, order_id, line_number, line_seq, item_id, qty_per, series_size, scrap_pct, round_up, qty_required, unit_cost_est)
			values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`,
			tenantID, orderID, l.LineNo, i+1, l.ItemID, l.QtyPer.String(), l.SeriesSize.String(), l.ScrapPct.String(), l.RoundUp, l.QtyRequired.String(), l.UnitCost.String()); err != nil {
			return err
		}
	}
	return nil
}

func insertOperations(ctx context.Context, tx pgx.Tx, tenantID, orderID string, ops []domain.Operation) error {
	for _, op := range ops {
		if _, err := tx.Exec(ctx, `insert into production_operation (tenant_id, order_id, oper_number, resource_id, setup_time_h, run_time_h_per, process_qty, scrap_pct,
			labor_rate, overhead_rate, subcontract, subcontract_price)
			values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)`,
			tenantID, orderID, op.OperNumber, op.ResourceID, op.SetupH.String(), op.RunHPer.String(), op.ProcessQty.String(), op.ScrapPct.String(),
			op.LaborRate.String(), op.OverheadRate.String(), op.Subcontract, op.SubcontractPrice.String()); err != nil {
			return err
		}
	}
	return nil
}

// GetOrder returns one order with its lines, operations and planned cost.
func (s *Postgres) GetOrder(ctx context.Context, id string) (Order, error) {
	p, _ := tenant.FromContext(ctx)
	var out Order
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) (err error) {
		out, err = loadOrder(ctx, tx, p.TenantID, id)
		return err
	})
	return out, err
}

// ListOrders returns a page of orders, newest first (ids are UUIDv7, so id
// order is creation order). RLS limits the rows to the caller's legal entities.
func (s *Postgres) ListOrders(ctx context.Context, f ListFilter) (OrderPage, error) {
	p, _ := tenant.FromContext(ctx)
	if f.Limit <= 0 {
		f.Limit = 50
	}
	page := OrderPage{Items: []OrderSummary{}}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select o.id::text, o.legal_entity_id::text, o.order_number, o.product_id::text, o.site_id::text,
				o.warehouse_id::text, o.currency::text, o.quantity::text, o.qty_raf::text, o.qty_error::text, o.status,
				o.delivery_date::text, o.version, o.created_at,
				coalesce((select sum(c.planned_amount) from production_cost_estimate c
				          where c.tenant_id = o.tenant_id and c.order_id = o.id), 0)::text
			from production_order o
			where o.tenant_id = $1
			  and ($2 = '' or o.status = $2)
			  and ($3 = '' or o.legal_entity_id = nullif($3, '')::uuid)
			  and ($4 = '' or o.site_id = nullif($4, '')::uuid)
			  and ($5 = '' or o.product_id = nullif($5, '')::uuid)
			  and ($6 = '' or o.id < nullif($6, '')::uuid)
			order by o.id desc
			limit $7`,
			p.TenantID, string(f.Status), f.LegalEntityID, f.SiteID, f.ProductID, f.Cursor, f.Limit+1)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var o OrderSummary
			var status, qty, raf, qerr, planned string
			if err := rows.Scan(&o.ID, &o.LegalEntityID, &o.OrderNumber, &o.ProductID, &o.SiteID, &o.WarehouseID, &o.Currency,
				&qty, &raf, &qerr, &status, &o.DeliveryDate, &o.Version, &o.CreatedAt, &planned); err != nil {
				return err
			}
			o.Status = domain.Status(status)
			o.Quantity, o.QtyRAF, o.QtyError = dec(qty), dec(raf), dec(qerr)
			o.Remaining = decimal.Max(o.Quantity.Sub(o.QtyRAF).Sub(o.QtyError), decimal.Zero)
			o.PlannedCost = dec(planned).Round(2)
			page.Items = append(page.Items, o)
		}
		return rows.Err()
	})
	if err != nil {
		return OrderPage{}, err
	}
	if len(page.Items) > f.Limit {
		page.Items = page.Items[:f.Limit]
		page.NextCursor = page.Items[f.Limit-1].ID
	}
	return page, nil
}

// StatusLog returns the applied commands of an order, oldest first.
func (s *Postgres) StatusLog(ctx context.Context, orderID string) ([]StatusLogEntry, error) {
	p, _ := tenant.FromContext(ctx)
	out := []StatusLogEntry{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var exists bool
		if err := tx.QueryRow(ctx, `select exists(select 1 from production_order where tenant_id = $1 and id = $2)`, p.TenantID, orderID).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return notFound()
		}
		rows, err := tx.Query(ctx, `select command, from_status, to_status, actor, trace_id, occurred_at from production_status_log
			where tenant_id = $1 and order_id = $2 order by occurred_at, id`, p.TenantID, orderID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var e StatusLogEntry
			var cmd, from, to string
			if err := rows.Scan(&cmd, &from, &to, &e.Actor, &e.TraceID, &e.OccurredAt); err != nil {
				return err
			}
			e.Command, e.From, e.To = domain.Command(cmd), domain.Status(from), domain.Status(to)
			out = append(out, e)
		}
		return rows.Err()
	})
	return out, err
}

// orderHead is the locked header row a command or edit works on.
type orderHead struct {
	status        domain.Status
	version       int
	quantity      decimal.Decimal
	qtyRAF        decimal.Decimal
	qtyError      decimal.Decimal
	legalEntityID string
}

// lockOrder takes the row lock that serializes commands on one order
// (BR-21.4) and checks If-Match against the version.
func lockOrder(ctx context.Context, tx pgx.Tx, tenantID, orderID string, ifMatch int) (orderHead, error) {
	var h orderHead
	var status, qty, raf, qerr string
	err := tx.QueryRow(ctx, `select status, version, quantity::text, qty_raf::text, qty_error::text, legal_entity_id::text from production_order
		where tenant_id = $1 and id = $2 for update`, tenantID, orderID).Scan(&status, &h.version, &qty, &raf, &qerr, &h.legalEntityID)
	if errors.Is(err, pgx.ErrNoRows) {
		return h, notFound()
	}
	if err != nil {
		return h, err
	}
	if h.version != ifMatch {
		return h, httpx.NewProblem(http.StatusPreconditionFailed, CodeVersionConflict, fmt.Sprintf("order was changed by another user; current version is %d", h.version))
	}
	h.status = domain.Status(status)
	h.quantity, h.qtyRAF, h.qtyError = dec(qty), dec(raf), dec(qerr)
	return h, nil
}

// UpdateOrder applies a PATCH of the header under If-Match. A quantity change
// re-explodes the lines and re-plans the operations from the stored snapshot,
// and re-estimates an Estimated order, all in one transaction.
func (s *Postgres) UpdateOrder(ctx context.Context, u OrderUpdate) (Order, error) {
	p, _ := tenant.FromContext(ctx)
	var out Order
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		h, err := lockOrder(ctx, tx, p.TenantID, u.OrderID, u.IfMatch)
		if err != nil {
			return err
		}
		if err := domain.CheckEdit(h.status, u.Quantity != nil, u.SetDeliveryDate); err != nil {
			return domainProblem(err)
		}
		qty := h.quantity
		if u.Quantity != nil {
			qty = *u.Quantity
			if err := requantify(ctx, tx, p.TenantID, u.OrderID, qty); err != nil {
				return err
			}
			if h.status == domain.Estimated {
				if err := replaceEstimate(ctx, tx, p.TenantID, u.OrderID); err != nil {
					return err
				}
			}
		}
		if _, err := tx.Exec(ctx, `update production_order set quantity = $3,
				delivery_date = case when $4 then $5::date else delivery_date end,
				version = version + 1, updated_at = now()
			where tenant_id = $1 and id = $2`, p.TenantID, u.OrderID, qty.String(), u.SetDeliveryDate, u.DeliveryDate); err != nil {
			return err
		}
		out, err = loadOrder(ctx, tx, p.TenantID, u.OrderID)
		if err != nil {
			return err
		}
		return enqueue(ctx, tx, u.OrderID, prodevents.OrderUpdated, p.TenantID, u.Actor, prodevents.OrderUpdatedData{
			OrderID: u.OrderID, Quantity: out.Quantity.String(), DeliveryDate: out.DeliveryDate, Status: string(out.Status),
			Version: out.Version, Actor: u.Actor,
		})
	})
	return out, err
}

// requantify rewrites the required line quantities and operation process
// quantities for a new order quantity.
func requantify(ctx context.Context, tx pgx.Tx, tenantID, orderID string, qty decimal.Decimal) error {
	lines, err := loadLines(ctx, tx, tenantID, orderID)
	if err != nil {
		return err
	}
	if lines, err = domain.Requantify(lines, qty); err != nil {
		return domainProblem(err)
	}
	ops, err := loadOperations(ctx, tx, tenantID, orderID)
	if err != nil {
		return err
	}
	if ops, err = domain.PlanOperations(ops, qty); err != nil {
		return domainProblem(err)
	}
	for _, l := range lines {
		if _, err := tx.Exec(ctx, `update production_bom_line set qty_required = $4 where tenant_id = $1 and order_id = $2 and line_number = $3`,
			tenantID, orderID, l.LineNo, l.QtyRequired.String()); err != nil {
			return err
		}
	}
	for _, op := range ops {
		if _, err := tx.Exec(ctx, `update production_operation set process_qty = $4 where tenant_id = $1 and order_id = $2 and oper_number = $3`,
			tenantID, orderID, op.OperNumber, op.ProcessQty.String()); err != nil {
			return err
		}
	}
	return nil
}

// Transition applies a `:command` to an order. It locks the order row, checks
// If-Match against the version, applies the state machine and its guards,
// re-estimates on :estimate (replacing the cost rows atomically), books the
// reported quantities on :report-finished, undoes them and the estimate on a
// reset past them, writes the status log and the event, and bumps the version.
// A replay of the same Idempotency-Key and request returns the current order
// with replayed=true and changes nothing.
func (s *Postgres) Transition(ctx context.Context, t Transition) (Order, bool, error) {
	p, _ := tenant.FromContext(ctx)
	var out Order
	replayed := false
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var loggedOrder, loggedHash string
		err := tx.QueryRow(ctx, `select order_id::text, request_hash from production_status_log where tenant_id = $1 and idempotency_key = $2`,
			p.TenantID, t.IdempotencyKey).Scan(&loggedOrder, &loggedHash)
		if err == nil {
			if loggedOrder != t.OrderID || loggedHash != t.RequestHash {
				return httpx.NewProblem(http.StatusUnprocessableEntity, CodeIdempotencyReuse, "Idempotency-Key was already used for another order or command")
			}
			replayed = true
			out, err = loadOrder(ctx, tx, p.TenantID, t.OrderID)
			return err
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}

		h, err := lockOrder(ctx, tx, p.TenantID, t.OrderID, t.IfMatch)
		if err != nil {
			return err
		}
		from := h.status
		var to domain.Status
		if t.Command == domain.CmdResetStatus && t.ResetTo != "" {
			to, err = domain.ResetTo(from, t.ResetTo)
		} else {
			to, err = domain.Transition(from, t.Command)
		}
		if err != nil {
			return domainProblem(err)
		}
		raf, qerr := h.qtyRAF, h.qtyError
		switch t.Command {
		case domain.CmdEnd:
			if err := domain.CheckEnd(from, raf, t.ZeroOutputDuty); err != nil {
				return domainProblem(err)
			}
		case domain.CmdReportFinished:
			if err := domain.CheckReportFinished(h.quantity, raf, qerr, t.Good, t.Error, t.RAFTolerancePct); err != nil {
				return domainProblem(err)
			}
			raf, qerr = raf.Add(t.Good), qerr.Add(t.Error)
		case domain.CmdEstimate:
			if err := replaceEstimate(ctx, tx, p.TenantID, t.OrderID); err != nil {
				return err
			}
		case domain.CmdResetStatus:
			// FR-21.9: a reset reverses what the statuses it leaves booked.
			if from == domain.ReportedAsFinished {
				raf, qerr = decimal.Zero, decimal.Zero
			}
			if to == domain.Created {
				if _, err := tx.Exec(ctx, `delete from production_cost_estimate where tenant_id = $1 and order_id = $2`, p.TenantID, t.OrderID); err != nil {
					return err
				}
			}
		}
		if _, err := tx.Exec(ctx, `update production_order set status = $3, qty_raf = $4, qty_error = $5, version = version + 1, updated_at = now()
			where tenant_id = $1 and id = $2`, p.TenantID, t.OrderID, string(to), raf.String(), qerr.String()); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `insert into production_status_log (tenant_id, id, legal_entity_id, order_id, command, from_status, to_status, actor, trace_id, idempotency_key, request_hash)
			values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`, p.TenantID, ids.New(), h.legalEntityID, t.OrderID, string(t.Command),
			string(from), string(to), t.Actor, t.TraceID, t.IdempotencyKey, t.RequestHash); err != nil {
			return err
		}
		out, err = loadOrder(ctx, tx, p.TenantID, t.OrderID)
		if err != nil {
			return err
		}
		if from == to && t.Command != domain.CmdReportFinished {
			return nil // a re-estimate changes cost rows, not the status
		}
		return enqueue(ctx, tx, t.OrderID, prodevents.OrderStatusChanged, p.TenantID, t.Actor, prodevents.OrderStatusChangedData{
			OrderID: t.OrderID, From: string(from), To: string(to), Quantity: out.Quantity.String(), QtyRAF: out.QtyRAF.String(),
			QtyError: out.QtyError.String(), Actor: t.Actor, OccurredAt: out.UpdatedAt.UTC().Format(time.RFC3339Nano),
		})
	})
	return out, replayed, err
}

// replaceEstimate recomputes planned cost from the stored lines and operations
// and replaces the cost rows of the order.
func replaceEstimate(ctx context.Context, tx pgx.Tx, tenantID, orderID string) error {
	lines, err := loadLines(ctx, tx, tenantID, orderID)
	if err != nil {
		return err
	}
	ops, err := loadOperations(ctx, tx, tenantID, orderID)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `delete from production_cost_estimate where tenant_id = $1 and order_id = $2`, tenantID, orderID); err != nil {
		return err
	}
	for _, c := range domain.Estimate(lines, ops) {
		if _, err := tx.Exec(ctx, `insert into production_cost_estimate (tenant_id, order_id, cost_group, resource_key, planned_qty, planned_amount, fixed_amount)
			values ($1, $2, $3, $4, $5, $6, $7)`, tenantID, orderID, string(c.Group), c.Key, c.Qty.String(), c.Amount.String(), c.Fixed.String()); err != nil {
			return err
		}
	}
	return nil
}

// domainProblem maps a domain rule violation to a problem: an illegal
// transition or a locked field is a 409 conflict with the order's state, the
// other rules are 422.
func domainProblem(err error) error {
	var de *domain.Error
	if !errors.As(err, &de) {
		return err
	}
	status := http.StatusUnprocessableEntity
	if de.Code == domain.CodeIllegalTransition || de.Code == domain.CodeNotEditable {
		status = http.StatusConflict
	}
	return httpx.NewProblem(status, de.Code, de.Detail)
}

func loadOrder(ctx context.Context, q querier, tenantID, id string) (Order, error) {
	var o Order
	var status, qty, raf, qerr string
	err := q.QueryRow(ctx, `select id::text, legal_entity_id::text, order_number, product_id::text, site_id::text, warehouse_id::text, currency::text,
		quantity::text, qty_raf::text, qty_error::text, status, delivery_date::text, origin_type, origin_id::text, version, created_at, updated_at
		from production_order where tenant_id = $1 and id = $2`, tenantID, id).Scan(&o.ID, &o.LegalEntityID, &o.OrderNumber, &o.ProductID,
		&o.SiteID, &o.WarehouseID, &o.Currency, &qty, &raf, &qerr, &status, &o.DeliveryDate, &o.OriginType, &o.OriginID, &o.Version,
		&o.CreatedAt, &o.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Order{}, notFound()
	}
	if err != nil {
		return Order{}, err
	}
	o.Status = domain.Status(status)
	o.Quantity, o.QtyRAF, o.QtyError = dec(qty), dec(raf), dec(qerr)
	if o.Lines, err = loadLines(ctx, q, tenantID, id); err != nil {
		return Order{}, err
	}
	if o.Operations, err = loadOperations(ctx, q, tenantID, id); err != nil {
		return Order{}, err
	}
	o.Costs, err = loadCosts(ctx, q, tenantID, id)
	return o, err
}

// loadLines returns the BOM lines in explosion order (the BOM's line order,
// phantom children in place of their parent).
func loadLines(ctx context.Context, q querier, tenantID, orderID string) ([]domain.OrderLine, error) {
	rows, err := q.Query(ctx, `select line_number, item_id::text, qty_per::text, series_size::text, scrap_pct::text, round_up, qty_required::text, unit_cost_est::text
		from production_bom_line where tenant_id = $1 and order_id = $2 order by line_seq`, tenantID, orderID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.OrderLine{}
	for rows.Next() {
		var l domain.OrderLine
		var qtyPer, series, scrap, req, cost string
		if err := rows.Scan(&l.LineNo, &l.ItemID, &qtyPer, &series, &scrap, &l.RoundUp, &req, &cost); err != nil {
			return nil, err
		}
		l.QtyPer, l.SeriesSize, l.ScrapPct, l.QtyRequired, l.UnitCost = dec(qtyPer), dec(series), dec(scrap), dec(req), dec(cost)
		out = append(out, l)
	}
	return out, rows.Err()
}

func loadOperations(ctx context.Context, q querier, tenantID, orderID string) ([]domain.Operation, error) {
	rows, err := q.Query(ctx, `select oper_number, resource_id::text, setup_time_h::text, run_time_h_per::text, process_qty::text, scrap_pct::text,
		labor_rate::text, overhead_rate::text, subcontract, subcontract_price::text
		from production_operation where tenant_id = $1 and order_id = $2 order by oper_number`, tenantID, orderID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.Operation{}
	for rows.Next() {
		var op domain.Operation
		var setup, run, proc, scrap, labor, oh, sub string
		if err := rows.Scan(&op.OperNumber, &op.ResourceID, &setup, &run, &proc, &scrap, &labor, &oh, &op.Subcontract, &sub); err != nil {
			return nil, err
		}
		op.SetupH, op.RunHPer, op.ProcessQty, op.ScrapPct = dec(setup), dec(run), dec(proc), dec(scrap)
		op.LaborRate, op.OverheadRate, op.SubcontractPrice = dec(labor), dec(oh), dec(sub)
		out = append(out, op)
	}
	return out, rows.Err()
}

func loadCosts(ctx context.Context, q querier, tenantID, orderID string) ([]domain.CostRow, error) {
	rows, err := q.Query(ctx, `select cost_group, resource_key, planned_qty::text, planned_amount::text, fixed_amount::text
		from production_cost_estimate where tenant_id = $1 and order_id = $2 order by cost_group, resource_key`, tenantID, orderID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.CostRow{}
	for rows.Next() {
		var c domain.CostRow
		var g, qty, amt, fixed string
		if err := rows.Scan(&g, &c.Key, &qty, &amt, &fixed); err != nil {
			return nil, err
		}
		c.Group, c.Qty, c.Amount, c.Fixed = domain.CostGroup(g), dec(qty), dec(amt), dec(fixed)
		out = append(out, c)
	}
	return out, rows.Err()
}
