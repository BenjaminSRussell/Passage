package store

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/money"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/inventory/internal/domain"
	invevents "github.com/BenjaminSRussell/passage/services/inventory/internal/events"
)

// ConsignmentFilter selects replenishment orders; empty fields do not filter.
type ConsignmentFilter struct {
	Status   string
	VendorID string
}

// ConsignmentStockRow is vendor-owned stock at one dimension set (the
// consignment stock report of PRD 3.16.14, SAP MB54). Price is the consignment
// price of the vendor's latest replenishment line for the product and Value the
// statistical value qty x price in Currency (not posted: vendor-owned stock is
// never valued); both are empty when no order carries a price. OldestReceipt
// is the first vendor-owned receipt at the dimension set, AgeDays its age.
type ConsignmentStockRow struct {
	LegalEntityID string
	VendorID      string
	ProductID     string
	DimensionSet  domain.Dimensions
	Qty           decimal.Decimal
	Price         *decimal.Decimal
	Currency      string
	Value         *decimal.Decimal
	OldestReceipt *time.Time
}

// maxConsignmentList caps the replenishment order list and the stock report.
const maxConsignmentList = 500

// CreateConsignment stores a CREATED replenishment order. Creating it posts
// nothing; stock arrives with :receive. A repeated Idempotency-Key returns the
// stored order with replayed=true, or idempotency_key_reuse when the body
// differs.
func (s *Postgres) CreateConsignment(ctx context.Context, key string, o domain.ConsignmentOrder) (domain.ConsignmentOrder, bool, error) {
	if err := domain.ValidateNewConsignment(o, s.rules.MaxScale); err != nil {
		return domain.ConsignmentOrder{}, false, domainProblem(err)
	}
	tp, _ := tenant.FromContext(ctx)
	var out domain.ConsignmentOrder
	replayed := false
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if err := lockKey(ctx, tx, "consignment", tp.TenantID, key); err != nil {
			return err
		}
		var existing string
		err := tx.QueryRow(ctx, `select id::text from consignment_order where tenant_id = $1 and idempotency_key = $2`, tp.TenantID, key).Scan(&existing)
		if err == nil {
			replayed = true
			if out, err = loadConsignment(ctx, tx, existing, false); err != nil {
				return err
			}
			if !sameConsignment(out, o) {
				return problem(http.StatusUnprocessableEntity, CodeIdempotencyReuse, "Idempotency-Key was already used with a different request body")
			}
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		id := ids.New()
		number := "CRO-" + strings.ToUpper(strings.ReplaceAll(id, "-", "")[20:])
		if _, err := tx.Exec(ctx, `insert into consignment_order (tenant_id, legal_entity_id, id, number, idempotency_key, vendor_id, site, warehouse,
			requested_date, currency, status) values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, 'CREATED')`,
			tp.TenantID, o.LegalEntityID, id, number, key, strings.TrimSpace(o.VendorID), strings.TrimSpace(o.Site), strings.TrimSpace(o.Warehouse),
			o.RequestedDate.Format(dateLayout), o.Currency); err != nil {
			return scopeProblem(err)
		}
		for i, l := range o.Lines {
			hash, _, err := upsertDimensionSet(ctx, tx, tp.TenantID, o.LineDims(l))
			if err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `insert into consignment_order_line (tenant_id, consignment_order_id, id, line_no, product_id, dimension_set_hash,
				unit, qty, price) values ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
				tp.TenantID, id, ids.New(), i+1, l.ProductID, hash, l.Unit, l.Qty.String(), l.Price.String()); err != nil {
				return err
			}
		}
		out, err = loadConsignment(ctx, tx, id, false)
		return err
	})
	return out, replayed, err
}

// sameConsignment reports whether a stored order was created from request o.
func sameConsignment(stored, o domain.ConsignmentOrder) bool {
	if stored.LegalEntityID != o.LegalEntityID || stored.VendorID != strings.TrimSpace(o.VendorID) || stored.Site != strings.TrimSpace(o.Site) ||
		stored.Warehouse != strings.TrimSpace(o.Warehouse) || stored.Currency != o.Currency || !stored.RequestedDate.Equal(o.RequestedDate) ||
		len(stored.Lines) != len(o.Lines) {
		return false
	}
	for i, l := range o.Lines {
		sl := stored.Lines[i]
		want, err := o.LineDims(l).Normalize()
		if err != nil || sl.ProductID != l.ProductID || sl.Unit != l.Unit || !sl.Qty.Equal(l.Qty) || !sl.Price.Equal(l.Price) ||
			sl.Dims.Canonical() != want.Canonical() {
			return false
		}
	}
	return true
}

// GetConsignment returns one replenishment order with its lines.
func (s *Postgres) GetConsignment(ctx context.Context, id string) (domain.ConsignmentOrder, error) {
	var out domain.ConsignmentOrder
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		out, err = loadConsignment(ctx, tx, id, false)
		return err
	})
	return out, err
}

// ListConsignments returns replenishment orders with their lines, newest
// first (at most 500).
func (s *Postgres) ListConsignments(ctx context.Context, f ConsignmentFilter) ([]domain.ConsignmentOrder, error) {
	tp, _ := tenant.FromContext(ctx)
	out := []domain.ConsignmentOrder{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+consignmentColumns+` from consignment_order
			where tenant_id = $1 and ($2 = '' or status = $2) and ($3 = '' or vendor_id = $3)
			order by created_at desc, id desc limit $4`, tp.TenantID, f.Status, f.VendorID, maxConsignmentList)
		if err != nil {
			return err
		}
		index := map[string]int{}
		for rows.Next() {
			o, err := scanConsignment(rows)
			if err != nil {
				rows.Close()
				return err
			}
			index[o.ID] = len(out)
			out = append(out, o)
		}
		rows.Close()
		if err := rows.Err(); err != nil || len(out) == 0 {
			return err
		}
		orderIDs := make([]string, 0, len(out))
		for _, o := range out {
			orderIDs = append(orderIDs, o.ID)
		}
		lines, err := tx.Query(ctx, `select l.consignment_order_id::text, `+consignmentLineColumns+`
			from consignment_order_line l join dimension_set d on d.tenant_id = l.tenant_id and d.hash = l.dimension_set_hash
			where l.tenant_id = $1 and l.consignment_order_id = any($2::uuid[]) order by l.consignment_order_id, l.line_no`, tp.TenantID, orderIDs)
		if err != nil {
			return err
		}
		defer lines.Close()
		for lines.Next() {
			var orderID string
			l, err := scanConsignmentLine(lines, &orderID)
			if err != nil {
				return err
			}
			i := index[orderID]
			out[i].Lines = append(out[i].Lines, l)
		}
		return lines.Err()
	})
	return out, err
}

// ReceiveConsignment books receipts against a replenishment order on
// receiptDate (FR-16.17). Each receipt is a RECEIVED transaction of
// vendor-owned stock (owner = the order's vendor) with no value, no financial
// date and no GL entry (BR-16.14); origin CONSIGNMENT_LINE, reference = the
// order id. Projection rows are locked in ascending (product, dimension set)
// order (BR-16.4) and a serial is received at most once across the legal
// entity (BR-16.9). The version must match (If-Match).
func (s *Postgres) ReceiveConsignment(ctx context.Context, id string, version int, receipts []domain.ConsignmentReceipt, receiptDate time.Time) (domain.ConsignmentOrder, error) {
	tp, _ := tenant.FromContext(ctx)
	var out domain.ConsignmentOrder
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		o, err := loadConsignment(ctx, tx, id, true)
		if err != nil {
			return err
		}
		if o.Version != version {
			return problem(http.StatusPreconditionFailed, CodeConflictVersion, "replenishment order changed since it was read; reread and retry")
		}
		next, plan, err := domain.ApplyConsignmentReceipts(o, receipts)
		if err != nil {
			return domainProblem(err)
		}
		rows, order, err := groupRows(plan)
		if err != nil {
			return err
		}
		serials := receiptSerials(plan)
		if err := lockSerials(ctx, tx, tp.TenantID, o.LegalEntityID, serials); err != nil {
			return err
		}
		doc := document{legalEntityID: o.LegalEntityID, currency: o.Currency, date: receiptDate, sourceType: sourceConsignmentLine, referenceID: o.ID}
		for _, k := range order {
			r := rows[k]
			if err := lockRow(ctx, tx, tp.TenantID, o.LegalEntityID, "", r); err != nil {
				return err
			}
			for _, i := range r.lines {
				if err := r.oh.Post(plan[i].Status, plan[i].Line.Qty, plan[i].Value, 1); err != nil {
					return err
				}
			}
			if err := checkVendorOwned(r); err != nil {
				return err
			}
			if err := saveRow(ctx, tx, tp.TenantID, o.LegalEntityID, r, len(r.lines)); err != nil {
				return err
			}
		}
		if err := checkSerials(ctx, tx, tp.TenantID, o.LegalEntityID, serials); err != nil {
			return err
		}
		for _, k := range order {
			r := rows[k]
			for _, i := range r.lines {
				if _, err := writeTransaction(ctx, tx, tp.TenantID, doc, ids.New(), r, plan[i]); err != nil {
					return err
				}
			}
			if err := enqueueOnHand(ctx, tx, tp.TenantID, o.LegalEntityID, r, invevents.BucketReceived, r.oh.Received.Sub(r.before.Received)); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `update consignment_order set status = $3, version = version + 1, updated_at = now()
			where tenant_id = $1 and id = $2`, tp.TenantID, o.ID, next.Status); err != nil {
			return err
		}
		for _, l := range next.Lines {
			if _, err := tx.Exec(ctx, `update consignment_order_line set received_qty = $3 where tenant_id = $1 and id = $2`,
				tp.TenantID, l.ID, l.ReceivedQty.String()); err != nil {
				return err
			}
		}
		out, err = loadConsignment(ctx, tx, id, false)
		return err
	})
	return out, err
}

// ListConsignmentStock is the consignment stock report: vendor-owned physical
// stock by vendor and dimension set, oldest receipt first (at most 500 rows).
func (s *Postgres) ListConsignmentStock(ctx context.Context, vendor string) ([]ConsignmentStockRow, error) {
	tp, _ := tenant.FromContext(ctx)
	out := []ConsignmentStockRow{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select o.legal_entity_id::text, d.dims->>'owner', o.product_id::text, d.dims::text,
			(o.posted_qty + o.received + o.registered - o.deducted - o.picked)::text,
			(select min(t.physical_date) from inventory_transaction t
			  where t.tenant_id = o.tenant_id and t.legal_entity_id = o.legal_entity_id and t.product_id = o.product_id
			    and t.dimension_set_hash = o.dimension_set_hash and t.receipt_status is not null),
			p.price::text, coalesce(p.currency::text, '')
			from on_hand o
			join dimension_set d on d.tenant_id = o.tenant_id and d.hash = o.dimension_set_hash
			left join lateral (
			  select l.price, c.currency from consignment_order_line l
			  join consignment_order c on c.tenant_id = l.tenant_id and c.id = l.consignment_order_id
			  where l.tenant_id = o.tenant_id and c.legal_entity_id = o.legal_entity_id and c.vendor_id = d.dims->>'owner'
			    and l.product_id = o.product_id and l.price > 0
			  order by c.created_at desc, l.line_no limit 1) p on true
			where o.tenant_id = $1 and d.dims->>'owner' is not null and ($2 = '' or d.dims->>'owner' = $2)
			  and o.posted_qty + o.received + o.registered - o.deducted - o.picked <> 0
			order by 6 nulls last, 2, 3, o.dimension_set_hash
			limit $3`, tp.TenantID, vendor, maxConsignmentList)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var r ConsignmentStockRow
			var dimsText, qty string
			var price *string
			if err := rows.Scan(&r.LegalEntityID, &r.VendorID, &r.ProductID, &dimsText, &qty, &r.OldestReceipt, &price, &r.Currency); err != nil {
				return err
			}
			if err := json.Unmarshal([]byte(dimsText), &r.DimensionSet); err != nil {
				return err
			}
			r.Qty = decimal.RequireFromString(qty)
			if price != nil {
				p := decimal.RequireFromString(*price)
				v := money.Amount{Value: r.Qty.Mul(p), Currency: r.Currency}.Round().Value
				r.Price, r.Value = &p, &v
			}
			out = append(out, r)
		}
		return rows.Err()
	})
	return out, err
}

const consignmentColumns = `id::text, legal_entity_id::text, number, vendor_id, site, warehouse, to_char(requested_date, 'YYYY-MM-DD'),
	currency::text, status, version, created_at`

const consignmentLineColumns = `l.id::text, l.line_no, l.product_id::text, d.dims::text, l.unit, l.qty::text, l.received_qty::text, l.price::text`

func scanConsignment(row pgx.Row) (domain.ConsignmentOrder, error) {
	var o domain.ConsignmentOrder
	var requested string
	if err := row.Scan(&o.ID, &o.LegalEntityID, &o.Number, &o.VendorID, &o.Site, &o.Warehouse, &requested, &o.Currency, &o.Status,
		&o.Version, &o.CreatedAt); err != nil {
		return o, err
	}
	var err error
	o.RequestedDate, err = time.Parse(dateLayout, requested)
	return o, err
}

// scanConsignmentLine scans consignmentLineColumns, preceded by the order id
// when orderID is not nil.
func scanConsignmentLine(rows pgx.Rows, orderID *string) (domain.ConsignmentLine, error) {
	var l domain.ConsignmentLine
	var dimsText, qty, received, price string
	dest := []any{&l.ID, &l.LineNo, &l.ProductID, &dimsText, &l.Unit, &qty, &received, &price}
	if orderID != nil {
		dest = append([]any{orderID}, dest...)
	}
	if err := rows.Scan(dest...); err != nil {
		return l, err
	}
	if err := json.Unmarshal([]byte(dimsText), &l.Dims); err != nil {
		return l, err
	}
	l.Qty, l.ReceivedQty, l.Price = decimal.RequireFromString(qty), decimal.RequireFromString(received), decimal.RequireFromString(price)
	return l, nil
}

// loadConsignment reads an order and its lines. lock takes a row lock on the
// header for the rest of the transaction.
func loadConsignment(ctx context.Context, tx pgx.Tx, id string, lock bool) (domain.ConsignmentOrder, error) {
	tp, _ := tenant.FromContext(ctx)
	q := `select ` + consignmentColumns + ` from consignment_order where tenant_id = $1 and id = $2`
	if lock {
		q += " for update"
	}
	o, err := scanConsignment(tx.QueryRow(ctx, q, tp.TenantID, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return o, problem(http.StatusNotFound, domain.CodeConsignmentNotFound, "replenishment order not found")
	}
	if err != nil {
		return o, err
	}
	rows, err := tx.Query(ctx, `select `+consignmentLineColumns+`
		from consignment_order_line l join dimension_set d on d.tenant_id = l.tenant_id and d.hash = l.dimension_set_hash
		where l.tenant_id = $1 and l.consignment_order_id = $2 order by l.line_no`, tp.TenantID, id)
	if err != nil {
		return o, err
	}
	defer rows.Close()
	for rows.Next() {
		l, err := scanConsignmentLine(rows, nil)
		if err != nil {
			return o, err
		}
		o.Lines = append(o.Lines, l)
	}
	return o, rows.Err()
}
