package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/inventory/internal/domain"
	invevents "github.com/BenjaminSRussell/passage/services/inventory/internal/events"
)

const sourceTransferLine = "TRANSFER_LINE"

// TransferReceipt is one receipt line of a :receive call.
type TransferReceipt struct {
	LineID     string
	Qty        decimal.Decimal
	CloseShort bool
}

// InTransitRow is a transfer line with quantity shipped but not yet received
// (FR-16.16). The quantity sits at the transit warehouse dimension. Closed is
// set when the line was closed short: the remainder is in transit for
// investigation (AC-16.11).
type InTransitRow struct {
	OrderID          string
	Number           string
	LineID           string
	ProductID        string
	DimensionSet     domain.Dimensions
	Unit             string
	FromWarehouse    string
	ToWarehouse      string
	TransitWarehouse string
	ShipDate         time.Time
	InTransit        decimal.Decimal
	Closed           bool
}

// CreateTransfer stores a CREATED transfer order. A repeated Idempotency-Key
// returns the stored order with replayed=true, or idempotency_key_reuse when the
// body differs. Creating an order posts nothing; the supply and demand pair
// (ON_ORDER / ORDERED) for planning is a later slice.
func (s *Postgres) CreateTransfer(ctx context.Context, key string, o domain.TransferOrder) (domain.TransferOrder, bool, error) {
	if err := domain.ValidateNewTransfer(o, s.rules.MaxScale); err != nil {
		return domain.TransferOrder{}, false, domainProblem(err)
	}
	tp, _ := tenant.FromContext(ctx)
	var out domain.TransferOrder
	replayed := false
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if err := lockKey(ctx, tx, "transfer", tp.TenantID, key); err != nil {
			return err
		}
		var existing string
		err := tx.QueryRow(ctx, `select id::text from transfer_order where tenant_id = $1 and idempotency_key = $2`, tp.TenantID, key).Scan(&existing)
		if err == nil {
			replayed = true
			if out, err = loadTransfer(ctx, tx, existing, false); err != nil {
				return err
			}
			if !sameTransfer(out, o) {
				return problem(http.StatusUnprocessableEntity, CodeIdempotencyReuse, "Idempotency-Key was already used with a different request body")
			}
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		id := ids.New()
		number := "TO-" + strings.ToUpper(strings.ReplaceAll(id, "-", "")[20:])
		if _, err := tx.Exec(ctx, `insert into transfer_order (tenant_id, legal_entity_id, id, number, idempotency_key, status, from_warehouse,
			to_warehouse, to_site, transit_warehouse, use_transit, ship_date)
			values ($1, $2, $3, $4, $5, 'CREATED', $6, $7, $8, $9, $10, $11)`,
			tp.TenantID, o.LegalEntityID, id, number, key, o.FromWarehouse, o.ToWarehouse, optionalText(o.ToSite), optionalText(o.TransitWarehouse),
			o.UseTransit, o.ShipDate.Format(dateLayout)); err != nil {
			return scopeProblem(err)
		}
		for i, l := range o.Lines {
			hash, _, err := upsertDimensionSet(ctx, tx, tp.TenantID, o.SourceDims(l))
			if err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `insert into transfer_order_line (tenant_id, transfer_order_id, id, line_no, product_id, dimension_set_hash, unit,
				qty, under_pct, over_pct) values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
				tp.TenantID, id, ids.New(), i+1, l.ProductID, hash, l.Unit, l.Qty.String(), l.UnderPct.String(), l.OverPct.String()); err != nil {
				return err
			}
		}
		out, err = loadTransfer(ctx, tx, id, false)
		return err
	})
	return out, replayed, err
}

// sameTransfer reports whether a stored order was created from request o.
func sameTransfer(stored, o domain.TransferOrder) bool {
	if stored.LegalEntityID != o.LegalEntityID || stored.FromWarehouse != o.FromWarehouse || stored.ToWarehouse != o.ToWarehouse ||
		stored.ToSite != o.ToSite || stored.UseTransit != o.UseTransit || stored.TransitWarehouse != o.TransitWarehouse ||
		!stored.ShipDate.Equal(o.ShipDate) || len(stored.Lines) != len(o.Lines) {
		return false
	}
	for i, l := range o.Lines {
		sl := stored.Lines[i]
		want, err := o.SourceDims(l).Normalize()
		if err != nil || sl.ProductID != l.ProductID || sl.Unit != l.Unit || !sl.Qty.Equal(l.Qty) || !sl.UnderPct.Equal(l.UnderPct) ||
			!sl.OverPct.Equal(l.OverPct) || sl.Dims.Canonical() != want.Canonical() {
			return false
		}
	}
	return true
}

// GetTransfer returns one transfer order with its lines.
func (s *Postgres) GetTransfer(ctx context.Context, id string) (domain.TransferOrder, error) {
	var out domain.TransferOrder
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		out, err = loadTransfer(ctx, tx, id, false)
		return err
	})
	return out, err
}

// maxTransferList caps the transfer order list.
const maxTransferList = 200

// ListTransfers returns transfer orders with their lines, newest first,
// optionally filtered by status (at most 200).
func (s *Postgres) ListTransfers(ctx context.Context, status string) ([]domain.TransferOrder, error) {
	tp, _ := tenant.FromContext(ctx)
	out := []domain.TransferOrder{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+transferColumns+` from transfer_order where tenant_id = $1 and ($2 = '' or status = $2)
			order by created_at desc, id desc limit $3`, tp.TenantID, status, maxTransferList)
		if err != nil {
			return err
		}
		index := map[string]int{}
		for rows.Next() {
			o, err := scanTransfer(rows)
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
		lines, err := tx.Query(ctx, `select l.transfer_order_id::text, `+transferLineColumns+`
			from transfer_order_line l join dimension_set d on d.tenant_id = l.tenant_id and d.hash = l.dimension_set_hash
			where l.tenant_id = $1 and l.transfer_order_id = any($2::uuid[]) order by l.transfer_order_id, l.line_no`, tp.TenantID, orderIDs)
		if err != nil {
			return err
		}
		defer lines.Close()
		for lines.Next() {
			var orderID string
			l, err := scanTransferLine(lines, &orderID)
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

// ShipTransfer ships a CREATED order in full on shipDate. Each line moves from
// the shipping warehouse to the transit warehouse (or, for a one-step
// transfer, to the receiving warehouse) as a valued SOLD / PURCHASED pair in
// one transaction: the receipt carries the issue's cost (BR-16.3), both legs
// are on the inventory account, so there is no GL entry (AC-16.11). The
// shipping warehouse follows the negative policy neg.
func (s *Postgres) ShipTransfer(ctx context.Context, id string, version int, shipDate time.Time, neg domain.NegativePolicy) (domain.TransferOrder, error) {
	tp, _ := tenant.FromContext(ctx)
	var out domain.TransferOrder
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		o, err := lockedTransfer(ctx, tx, id, version)
		if err != nil {
			return err
		}
		if err := domain.CanShip(o); err != nil {
			return domainProblem(err)
		}
		next := domain.ApplyShip(o)
		next.ShipDate = shipDate
		if !next.UseTransit {
			next.ReceiptDate = shipDate
		}
		moves := make([]stockMove, 0, len(next.Lines))
		moved := map[string]decimal.Decimal{}
		for _, l := range next.Lines {
			to := o.TransitDims(l)
			if !o.UseTransit {
				to = o.DestinationDims(l)
			}
			moves = append(moves, stockMove{product: l.ProductID, unit: l.Unit, from: o.SourceDims(l), to: to, qty: l.Qty, neg: neg})
			moved[l.ID] = l.Qty
		}
		doc := document{legalEntityID: o.LegalEntityID, date: shipDate, sourceType: sourceTransferLine, referenceID: o.ID}
		if err := moveStockBatch(ctx, tx, tp.TenantID, doc, moves); err != nil {
			return err
		}
		if err := saveTransfer(ctx, tx, tp.TenantID, next); err != nil {
			return err
		}
		if out, err = loadTransfer(ctx, tx, id, false); err != nil {
			return err
		}
		return enqueue(ctx, tx, "transfer", o.ID, invevents.TransferShipped, tp.TenantID, transferData(out, moved, out.ReceiptDate))
	})
	return out, err
}

// ReceiveTransfer books receipts on receiptDate against a shipped order. Each
// receipt moves its quantity from the transit warehouse to the receiving
// warehouse as a valued pair; an over-delivery beyond the quantity in transit
// (within the line's tolerance) moves straight from the shipping warehouse,
// under the negative policy neg. The transit warehouse never goes negative.
func (s *Postgres) ReceiveTransfer(ctx context.Context, id string, version int, receipts []TransferReceipt, receiptDate time.Time, neg domain.NegativePolicy) (domain.TransferOrder, error) {
	tp, _ := tenant.FromContext(ctx)
	var out domain.TransferOrder
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		o, err := lockedTransfer(ctx, tx, id, version)
		if err != nil {
			return err
		}
		if err := domain.CanReceive(o); err != nil {
			return domainProblem(err)
		}
		if len(receipts) == 0 {
			return problem(http.StatusBadRequest, domain.CodeInvalid, "receipt has no lines")
		}
		index := map[string]int{}
		for i, l := range o.Lines {
			index[l.ID] = i
		}
		var moves []stockMove
		moved := map[string]decimal.Decimal{}
		for _, rc := range receipts {
			i, ok := index[rc.LineID]
			if !ok {
				return problem(http.StatusNotFound, domain.CodeTransferNotFound, "transfer line "+rc.LineID+" not found on this order")
			}
			r, err := domain.ApplyReceive(o.Lines[i], rc.Qty, rc.CloseShort)
			if err != nil {
				return domainProblem(err)
			}
			l := r.Line
			o.Lines[i] = l
			if r.FromTransit.IsPositive() {
				moves = append(moves, stockMove{product: l.ProductID, unit: l.Unit, from: o.TransitDims(l), to: o.DestinationDims(l), qty: r.FromTransit})
			}
			if r.FromSource.IsPositive() {
				moves = append(moves, stockMove{product: l.ProductID, unit: l.Unit, from: o.SourceDims(l), to: o.DestinationDims(l), qty: r.FromSource, neg: neg})
			}
			moved[l.ID] = moved[l.ID].Add(rc.Qty)
		}
		o.Status = domain.SettleTransferStatus(o)
		if o.Status == domain.TransferReceived {
			o.ReceiptDate = receiptDate
		}
		doc := document{legalEntityID: o.LegalEntityID, date: receiptDate, sourceType: sourceTransferLine, referenceID: o.ID}
		if err := moveStockBatch(ctx, tx, tp.TenantID, doc, moves); err != nil {
			return err
		}
		if err := saveTransfer(ctx, tx, tp.TenantID, o); err != nil {
			return err
		}
		if out, err = loadTransfer(ctx, tx, id, false); err != nil {
			return err
		}
		return enqueue(ctx, tx, "transfer", o.ID, invevents.TransferReceived, tp.TenantID, transferData(out, moved, receiptDate))
	})
	return out, err
}

// CancelTransfer cancels a CREATED order; nothing has moved, so nothing is
// reversed. A shipped order is INV-TRF-003 (BR-16.13).
func (s *Postgres) CancelTransfer(ctx context.Context, id string, version int) (domain.TransferOrder, error) {
	tp, _ := tenant.FromContext(ctx)
	var out domain.TransferOrder
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		o, err := lockedTransfer(ctx, tx, id, version)
		if err != nil {
			return err
		}
		if err := domain.CanCancel(o); err != nil {
			return domainProblem(err)
		}
		o.Status = domain.TransferCancelled
		if err := saveTransfer(ctx, tx, tp.TenantID, o); err != nil {
			return err
		}
		out, err = loadTransfer(ctx, tx, id, false)
		return err
	})
	return out, err
}

// ListInTransit lists transfer lines with quantity shipped but not yet
// received, oldest shipment first (FR-16.16).
func (s *Postgres) ListInTransit(ctx context.Context) ([]InTransitRow, error) {
	tp, _ := tenant.FromContext(ctx)
	out := []InTransitRow{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select o.id::text, o.number, l.id::text, l.product_id::text, d.dims::text, l.unit, o.from_warehouse, o.to_warehouse,
			coalesce(o.transit_warehouse, ''), to_char(o.ship_date, 'YYYY-MM-DD'), (l.shipped_qty - l.received_qty)::text, l.closed
			from transfer_order_line l
			join transfer_order o on o.tenant_id = l.tenant_id and o.id = l.transfer_order_id
			join dimension_set d on d.tenant_id = l.tenant_id and d.hash = l.dimension_set_hash
			where l.tenant_id = $1 and o.use_transit and l.shipped_qty > l.received_qty
			order by o.ship_date, o.number, l.line_no
			limit 1000`, tp.TenantID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var r InTransitRow
			var dimsText, ship, qty string
			if err := rows.Scan(&r.OrderID, &r.Number, &r.LineID, &r.ProductID, &dimsText, &r.Unit, &r.FromWarehouse, &r.ToWarehouse,
				&r.TransitWarehouse, &ship, &qty, &r.Closed); err != nil {
				return err
			}
			if err := json.Unmarshal([]byte(dimsText), &r.DimensionSet); err != nil {
				return err
			}
			if r.ShipDate, err = time.Parse(dateLayout, ship); err != nil {
				return err
			}
			r.InTransit = decimal.RequireFromString(qty)
			out = append(out, r)
		}
		return rows.Err()
	})
	return out, err
}

// stockMove is one valued movement of qty between two dimension sets of a
// product. neg is the negative policy at the source; the zero value forbids
// negative stock.
type stockMove struct {
	product, unit string
	from, to      domain.Dimensions
	qty           decimal.Decimal
	neg           domain.NegativePolicy
}

// moveStockBatch posts moves as valued SOLD / PURCHASED pairs in the caller's
// transaction. Every projection row involved is locked first, in ascending
// (product, dimension set) order (BR-16.4); each issue is valued at its row's
// running average and the matching receipt carries the same value (BR-16.3),
// so physical stock and value summed over the product are unchanged and there
// is no GL effect. The negative policy is checked on each source row after all
// deltas.
func moveStockBatch(ctx context.Context, tx pgx.Tx, tenantID string, doc document, moves []stockMove) error {
	rows := map[string]*row{}
	policy := map[string]domain.NegativePolicy{}
	var order []string
	key := func(product string, dims domain.Dimensions, source bool, neg domain.NegativePolicy) (string, error) {
		n, err := dims.Normalize()
		if err != nil {
			return "", domainProblem(&domain.Error{Code: domain.CodeInvalid, Detail: err.Error()})
		}
		hash := n.Hash()
		k := rowKey(product, hash)
		if _, ok := rows[k]; !ok {
			rows[k] = &row{product: product, dims: n, hash: hash}
			order = append(order, k)
			policy[k] = domain.NegativePolicy{PhysicalAllowed: true, FinancialAllowed: true}
		}
		if source {
			p := policy[k]
			policy[k] = domain.NegativePolicy{PhysicalAllowed: p.PhysicalAllowed && neg.PhysicalAllowed, FinancialAllowed: p.FinancialAllowed && neg.FinancialAllowed}
		}
		return k, nil
	}
	type leg struct{ from, to string }
	legs := make([]leg, len(moves))
	for i, m := range moves {
		from, err := key(m.product, m.from, true, m.neg)
		if err != nil {
			return err
		}
		to, err := key(m.product, m.to, false, domain.NegativePolicy{})
		if err != nil {
			return err
		}
		legs[i] = leg{from, to}
	}
	slices.Sort(order)
	for _, k := range order {
		if err := lockRow(ctx, tx, tenantID, doc.legalEntityID, "", rows[k]); err != nil {
			return err
		}
	}
	if err := checkCountFreeze(ctx, tx, tenantID, doc.legalEntityID, "", orderedRows(rows, order)); err != nil {
		return err
	}
	type posted struct {
		r  *row
		pl domain.PostedLine
	}
	var written []posted
	count := map[string]int{}
	for i, m := range moves {
		from, to := rows[legs[i].from], rows[legs[i].to]
		if from.currency == "" {
			return problem(http.StatusUnprocessableEntity, domain.CodeNegativePhysical,
				fmt.Sprintf("no stock of product %s at %s", from.product, describeDims(from.dims)))
		}
		if to.currency != "" && to.currency != from.currency {
			return problem(http.StatusUnprocessableEntity, CodeCurrencyMismatch,
				fmt.Sprintf("on-hand for product %s at %s is valued in %s, the source in %s", to.product, describeDims(to.dims), to.currency, from.currency))
		}
		value := from.oh.IssueValue(m.qty.Neg(), decimal.Zero, from.currency)
		issue := domain.PostedLine{Status: domain.StatusSold, Value: value,
			Line: domain.JournalLine{ProductID: m.product, Dims: from.dims, Qty: m.qty.Neg(), EnteredQty: m.qty.Neg(), EnteredUnit: m.unit}}
		receipt := domain.PostedLine{Status: domain.StatusPurchased, Value: value.Neg(),
			Line: domain.JournalLine{ProductID: m.product, Dims: to.dims, Qty: m.qty, EnteredQty: m.qty, EnteredUnit: m.unit}}
		if err := from.oh.Post(issue.Status, issue.Line.Qty, issue.Value, 1); err != nil {
			return err
		}
		if err := to.oh.Post(receipt.Status, receipt.Line.Qty, receipt.Value, 1); err != nil {
			return err
		}
		to.currency = from.currency
		written = append(written, posted{from, issue}, posted{to, receipt})
		count[legs[i].from]++
		count[legs[i].to]++
	}
	for _, k := range order {
		r := rows[k]
		if count[k] == 0 {
			continue
		}
		if err := checkNegative(r, policy[k]); err != nil {
			return err
		}
		if err := saveRow(ctx, tx, tenantID, doc.legalEntityID, r, count[k]); err != nil {
			return err
		}
	}
	for _, w := range written {
		d := doc
		d.currency = w.r.currency
		if _, err := writeTransaction(ctx, tx, tenantID, d, ids.New(), w.r, w.pl); err != nil {
			return err
		}
	}
	for _, k := range order {
		r := rows[k]
		if count[k] == 0 {
			continue
		}
		if err := enqueueOnHand(ctx, tx, tenantID, doc.legalEntityID, r, invevents.BucketPosted, r.oh.PostedQty.Sub(r.before.PostedQty)); err != nil {
			return err
		}
	}
	return nil
}

// lockedTransfer reads and locks an order for an action, checking its version
// (If-Match).
func lockedTransfer(ctx context.Context, tx pgx.Tx, id string, version int) (domain.TransferOrder, error) {
	o, err := loadTransfer(ctx, tx, id, true)
	if err != nil {
		return o, err
	}
	if o.Version != version {
		return o, problem(http.StatusPreconditionFailed, CodeConflictVersion, "transfer order changed since it was read; reread and retry")
	}
	return o, nil
}

const transferColumns = `id::text, legal_entity_id::text, number, status, from_warehouse, to_warehouse, coalesce(to_site, ''),
	coalesce(transit_warehouse, ''), use_transit, to_char(ship_date, 'YYYY-MM-DD'), to_char(receipt_date, 'YYYY-MM-DD'), version, created_at`

const transferLineColumns = `l.id::text, l.line_no, l.product_id::text, d.dims::text, l.unit, l.qty::text, l.shipped_qty::text,
	l.received_qty::text, l.under_pct::text, l.over_pct::text, l.closed`

func scanTransfer(row pgx.Row) (domain.TransferOrder, error) {
	var o domain.TransferOrder
	var ship string
	var receipt *string
	if err := row.Scan(&o.ID, &o.LegalEntityID, &o.Number, &o.Status, &o.FromWarehouse, &o.ToWarehouse, &o.ToSite, &o.TransitWarehouse,
		&o.UseTransit, &ship, &receipt, &o.Version, &o.CreatedAt); err != nil {
		return o, err
	}
	var err error
	if o.ShipDate, err = time.Parse(dateLayout, ship); err != nil {
		return o, err
	}
	if receipt != nil {
		if o.ReceiptDate, err = time.Parse(dateLayout, *receipt); err != nil {
			return o, err
		}
	}
	return o, nil
}

// scanTransferLine scans transferLineColumns, preceded by the columns scanned
// into prefix.
func scanTransferLine(rows pgx.Rows, prefix ...any) (domain.TransferLine, error) {
	var l domain.TransferLine
	var dimsText, qty, shipped, received, under, over string
	dest := append(prefix, &l.ID, &l.LineNo, &l.ProductID, &dimsText, &l.Unit, &qty, &shipped, &received, &under, &over, &l.Closed)
	if err := rows.Scan(dest...); err != nil {
		return l, err
	}
	if err := json.Unmarshal([]byte(dimsText), &l.Dims); err != nil {
		return l, err
	}
	l.Qty = decimal.RequireFromString(qty)
	l.ShippedQty = decimal.RequireFromString(shipped)
	l.ReceivedQty = decimal.RequireFromString(received)
	l.UnderPct = decimal.RequireFromString(under)
	l.OverPct = decimal.RequireFromString(over)
	return l, nil
}

// loadTransfer reads an order and its lines. lock takes a row lock on the header
// for the rest of the transaction.
func loadTransfer(ctx context.Context, tx pgx.Tx, id string, lock bool) (domain.TransferOrder, error) {
	tp, _ := tenant.FromContext(ctx)
	q := `select ` + transferColumns + ` from transfer_order where tenant_id = $1 and id = $2`
	if lock {
		q += " for update"
	}
	o, err := scanTransfer(tx.QueryRow(ctx, q, tp.TenantID, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return o, problem(http.StatusNotFound, domain.CodeTransferNotFound, "transfer order not found")
	}
	if err != nil {
		return o, err
	}
	rows, err := tx.Query(ctx, `select `+transferLineColumns+`
		from transfer_order_line l join dimension_set d on d.tenant_id = l.tenant_id and d.hash = l.dimension_set_hash
		where l.tenant_id = $1 and l.transfer_order_id = $2 order by l.line_no`, tp.TenantID, id)
	if err != nil {
		return o, err
	}
	defer rows.Close()
	for rows.Next() {
		l, err := scanTransferLine(rows)
		if err != nil {
			return o, err
		}
		o.Lines = append(o.Lines, l)
	}
	return o, rows.Err()
}

// saveTransfer writes the header status and dates (version bumped) and each
// line's shipped, received and closed state.
func saveTransfer(ctx context.Context, tx pgx.Tx, tenantID string, o domain.TransferOrder) error {
	var receipt *string
	if !o.ReceiptDate.IsZero() {
		s := o.ReceiptDate.Format(dateLayout)
		receipt = &s
	}
	if _, err := tx.Exec(ctx, `update transfer_order set status = $3, ship_date = $4, receipt_date = $5, version = version + 1, updated_at = now()
		where tenant_id = $1 and id = $2`, tenantID, o.ID, o.Status, o.ShipDate.Format(dateLayout), receipt); err != nil {
		return err
	}
	for _, l := range o.Lines {
		if _, err := tx.Exec(ctx, `update transfer_order_line set shipped_qty = $3, received_qty = $4, closed = $5
			where tenant_id = $1 and id = $2`, tenantID, l.ID, l.ShippedQty.String(), l.ReceivedQty.String(), l.Closed); err != nil {
			return err
		}
	}
	return nil
}

// transferData builds a transfer event: the lines in moved, with the quantity
// the event moved on each.
func transferData(o domain.TransferOrder, moved map[string]decimal.Decimal, receiptDate time.Time) invevents.TransferData {
	out := invevents.TransferData{
		TransferOrderID: o.ID, LegalEntityID: o.LegalEntityID, Number: o.Number, Status: o.Status, FromWarehouse: o.FromWarehouse,
		ToWarehouse: o.ToWarehouse, TransitWarehouse: optionalText(o.TransitWarehouse), ShipDate: o.ShipDate.Format(dateLayout),
		Lines: []invevents.TransferLineData{},
	}
	if !o.UseTransit {
		out.TransitWarehouse = nil
	}
	if !receiptDate.IsZero() {
		out.ReceiptDate = optionalText(receiptDate.Format(dateLayout))
	}
	for _, l := range o.Lines {
		qty, ok := moved[l.ID]
		if !ok {
			continue
		}
		out.Lines = append(out.Lines, invevents.TransferLineData{
			LineID: l.ID, ProductID: l.ProductID, DimensionSet: map[string]string(l.Dims), Unit: l.Unit, Qty: qty.String(),
			ShippedQty: l.ShippedQty.String(), ReceivedQty: l.ReceivedQty.String(), Closed: l.Closed,
		})
	}
	return out
}

// TransferLineFilter narrows the transfer order line inquiry. Empty fields do
// not filter; From and To (inclusive, nil for none) bound the planned or actual
// ship date. LateOnly keeps the lines late as of AsOf
// (domain.TransferLineLateness with LateReceiptDays).
type TransferLineFilter struct {
	LegalEntityID   string
	Status          string
	ProductID       string
	FromWarehouse   string
	ToWarehouse     string
	From            *time.Time
	To              *time.Time
	LateOnly        bool
	AsOf            time.Time
	LateReceiptDays int
}

// TransferLineRow is one transfer order line with its order header.
type TransferLineRow struct {
	OrderID       string
	LegalEntityID string
	Number        string
	Status        string
	FromWarehouse string
	ToWarehouse   string
	ShipDate      time.Time
	Line          domain.TransferLine
}

// maxTransferLineList caps the transfer order line inquiry.
const maxTransferLineList = 1000

// ListTransferLines is the cross-order transfer line inquiry (FR-16.15): lines
// of every order in the caller's legal entities matching the filter, oldest
// planned or actual ship date first. It returns at most maxTransferLineList
// lines and reports whether more matched. The late filter is the SQL form of
// domain.TransferLineLateness.
func (s *Postgres) ListTransferLines(ctx context.Context, f TransferLineFilter) ([]TransferLineRow, bool, error) {
	tp, _ := tenant.FromContext(ctx)
	out := []TransferLineRow{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select o.id::text, o.legal_entity_id::text, o.number, o.status, o.from_warehouse, o.to_warehouse,
			to_char(o.ship_date, 'YYYY-MM-DD'), `+transferLineColumns+`
			from transfer_order_line l
			join transfer_order o on o.tenant_id = l.tenant_id and o.id = l.transfer_order_id
			join dimension_set d on d.tenant_id = l.tenant_id and d.hash = l.dimension_set_hash
			where l.tenant_id = $1
			  and ($2 = '' or o.legal_entity_id::text = $2)
			  and ($3 = '' or o.status = $3)
			  and ($4 = '' or l.product_id::text = $4)
			  and ($5 = '' or o.from_warehouse = $5)
			  and ($6 = '' or o.to_warehouse = $6)
			  and ($7::date is null or o.ship_date >= $7::date)
			  and ($8::date is null or o.ship_date <= $8::date)
			  and (not $9 or (o.status = 'CREATED' and o.ship_date < $10::date)
			       or (o.status in ('SHIPPED', 'PARTIALLY_RECEIVED') and not l.closed and l.shipped_qty > l.received_qty
			           and o.ship_date < $10::date - $11::int))
			order by o.ship_date, o.number, l.line_no
			limit $12`, tp.TenantID, f.LegalEntityID, f.Status, f.ProductID, f.FromWarehouse, f.ToWarehouse, f.From,
			f.To, f.LateOnly, f.AsOf, f.LateReceiptDays, maxTransferLineList+1)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var r TransferLineRow
			var ship string
			if r.Line, err = scanTransferLine(rows, &r.OrderID, &r.LegalEntityID, &r.Number, &r.Status, &r.FromWarehouse, &r.ToWarehouse, &ship); err != nil {
				return err
			}
			if r.ShipDate, err = time.Parse(dateLayout, ship); err != nil {
				return err
			}
			out = append(out, r)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, false, err
	}
	if len(out) > maxTransferLineList {
		return out[:maxTransferLineList], true, nil
	}
	return out, false, nil
}
