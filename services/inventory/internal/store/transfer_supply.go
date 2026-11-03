package store

import (
	"context"
	"encoding/json"
	"slices"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/tenant"

	"github.com/BenjaminSRussell/passage/services/inventory/internal/domain"
	invevents "github.com/BenjaminSRussell/passage/services/inventory/internal/events"
)

// supplyDelta is the change to one projection row's ON_ORDER and ORDERED buckets.
type supplyDelta struct {
	r                *row
	onOrder, ordered decimal.Decimal
}

// syncTransferSupply books the supply and demand pair of order o and announces
// it: the whole of bookTransferSupply and announceTransferSupply for a mutation
// that moves no stock (create, confirm, edit, cancel, delete).
func syncTransferSupply(ctx context.Context, tx pgx.Tx, tenantID string, o domain.TransferOrder) error {
	moved, err := bookTransferSupply(ctx, tx, tenantID, o, nil)
	if err != nil {
		return err
	}
	return announceTransferSupply(ctx, tx, tenantID, o.LegalEntityID, moved)
}

// bookTransferSupply books the supply and demand pair of order o (BR-16.13):
// per line, ON_ORDER at the shipping dimension set for what has not shipped
// and ORDERED at the receiving dimension set for what has not been received.
// It compares what the order booked before (transfer_supply) with what o holds
// now, applies only the difference to the projection rows and rewrites the
// order's transfer_supply rows. Every transfer mutation calls it in its own
// transaction, with the order as it will be saved: a DRAFT, cancelled, deleted
// or received order holds nothing, so the same call books a new order, adjusts
// an edit, and releases the rest. Booking supply does not check the negative
// policy: a transfer order is a plan.
//
// Lock order (BR-16.4): the caller holds the transfer order row. The rows of
// moves (the ship or receive about to run through moveStockBatch) are locked
// here together with the supply rows, in one ascending rowKey pass after the
// dimension sets, so moveStockBatch only re-reads rows this transaction already
// holds and never takes a lower row after a higher one. A ship or receive books
// before it moves stock, so the shipped quantity no longer counts as demand when
// the negative check runs on the shipping row.
//
// It returns the rows whose buckets moved; announceTransferSupply emits their
// onhand.changed once the caller's stock moves are done, so every event of the
// transaction carries the row's final availability.
func bookTransferSupply(ctx context.Context, tx pgx.Tx, tenantID string, o domain.TransferOrder, moves []stockMove) ([]*supplyDelta, error) {
	type booked struct {
		product       string
		src, dst      domain.Dimensions
		onOrder, ordr decimal.Decimal
	}
	rows, err := tx.Query(ctx, `select s.product_id::text, sd.dims::text, dd.dims::text, s.on_order_qty::text, s.ordered_qty::text
		from transfer_supply s
		join dimension_set sd on sd.tenant_id = s.tenant_id and sd.hash = s.source_hash
		join dimension_set dd on dd.tenant_id = s.tenant_id and dd.hash = s.dest_hash
		where s.tenant_id = $1 and s.transfer_order_id = $2`, tenantID, o.ID)
	if err != nil {
		return nil, err
	}
	var old []booked
	for rows.Next() {
		var b booked
		var sdims, ddims, oo, od string
		if err := rows.Scan(&b.product, &sdims, &ddims, &oo, &od); err != nil {
			rows.Close()
			return nil, err
		}
		if err := json.Unmarshal([]byte(sdims), &b.src); err != nil {
			rows.Close()
			return nil, err
		}
		if err := json.Unmarshal([]byte(ddims), &b.dst); err != nil {
			rows.Close()
			return nil, err
		}
		b.onOrder, b.ordr = decimal.RequireFromString(oo), decimal.RequireFromString(od)
		old = append(old, b)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	deltas := map[string]*supplyDelta{}
	var order []string
	at := func(product string, dims domain.Dimensions) (*supplyDelta, error) {
		n, err := dims.Normalize()
		if err != nil {
			return nil, domainProblem(&domain.Error{Code: domain.CodeInvalid, Detail: err.Error()})
		}
		hash := n.Hash()
		k := rowKey(product, hash)
		d, ok := deltas[k]
		if !ok {
			d = &supplyDelta{r: &row{product: product, dims: n, hash: hash}}
			deltas[k] = d
			order = append(order, k)
		}
		return d, nil
	}
	for _, b := range old {
		s, err := at(b.product, b.src)
		if err != nil {
			return nil, err
		}
		s.onOrder = s.onOrder.Sub(b.onOrder)
		d, err := at(b.product, b.dst)
		if err != nil {
			return nil, err
		}
		d.ordered = d.ordered.Sub(b.ordr)
	}
	type want struct {
		line       domain.TransferLine
		supply     domain.TransferSupply
		srcH, dstH []byte
	}
	var wants []want
	for _, l := range o.Lines {
		sp := o.Supply(l)
		if sp.OnOrder.IsZero() && sp.Ordered.IsZero() {
			continue
		}
		s, err := at(l.ProductID, o.SourceDims(l))
		if err != nil {
			return nil, err
		}
		s.onOrder = s.onOrder.Add(sp.OnOrder)
		d, err := at(l.ProductID, o.DestinationDims(l))
		if err != nil {
			return nil, err
		}
		d.ordered = d.ordered.Add(sp.Ordered)
		wants = append(wants, want{l, sp, s.r.hash, d.r.hash})
	}
	// The rows the caller's stock moves will lock, so that every row lock of the
	// transaction is taken here, in one ascending pass.
	moveRows := map[string]bool{}
	for _, m := range moves {
		for _, dims := range []domain.Dimensions{m.from, m.to} {
			d, err := at(m.product, dims)
			if err != nil {
				return nil, err
			}
			moveRows[rowKey(d.r.product, d.r.hash)] = true
		}
	}
	slices.Sort(order)
	var locks []string
	sets := make([]domain.Dimensions, 0, len(order))
	for _, k := range order {
		d := deltas[k]
		if d.onOrder.IsZero() && d.ordered.IsZero() && !moveRows[k] {
			continue // nothing to book here and no stock move to lock it for
		}
		locks = append(locks, k)
		sets = append(sets, d.r.dims)
	}
	if err := upsertDimensionSets(ctx, tx, tenantID, sets...); err != nil {
		return nil, err
	}
	var moved []*supplyDelta
	for _, k := range locks {
		d := deltas[k]
		if err := lockRow(ctx, tx, tenantID, o.LegalEntityID, "", d.r); err != nil {
			return nil, err
		}
		if d.onOrder.IsZero() && d.ordered.IsZero() {
			continue
		}
		d.r.oh.OnOrder = d.r.oh.OnOrder.Add(d.onOrder)
		d.r.oh.Ordered = d.r.oh.Ordered.Add(d.ordered)
		if err := saveRow(ctx, tx, tenantID, o.LegalEntityID, d.r, 0); err != nil {
			return nil, err
		}
		moved = append(moved, d)
	}
	if _, err := tx.Exec(ctx, `delete from transfer_supply where tenant_id = $1 and transfer_order_id = $2`, tenantID, o.ID); err != nil {
		return nil, err
	}
	for _, w := range wants {
		if _, err := tx.Exec(ctx, `insert into transfer_supply (tenant_id, legal_entity_id, transfer_order_id, line_id, product_id, source_hash, dest_hash,
			on_order_qty, ordered_qty) values ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
			tenantID, o.LegalEntityID, o.ID, w.line.ID, w.line.ProductID, w.srcH, w.dstH, w.supply.OnOrder.String(), w.supply.Ordered.String()); err != nil {
			return nil, scopeProblem(err)
		}
	}
	return moved, nil
}

// announceTransferSupply emits onhand.changed (deltaBucket ON_ORDER or ORDERED)
// per bucket bookTransferSupply moved, with each row's availability as it
// stands now: the caller runs it after its stock moves, so no event shows the
// intermediate state between releasing the supply and moving the stock (a
// receipt's ORDERED release before its quantity arrives). The rows are already
// locked by this transaction; reading them again does not wait.
func announceTransferSupply(ctx context.Context, tx pgx.Tx, tenantID, legalEntityID string, moved []*supplyDelta) error {
	for _, d := range moved {
		if err := lockRow(ctx, tx, tenantID, legalEntityID, "", d.r); err != nil {
			return err
		}
		if !d.onOrder.IsZero() {
			if err := enqueueOnHand(ctx, tx, tenantID, legalEntityID, d.r, invevents.BucketOnOrder, d.onOrder); err != nil {
				return err
			}
		}
		if !d.ordered.IsZero() {
			if err := enqueueOnHand(ctx, tx, tenantID, legalEntityID, d.r, invevents.BucketOrdered, d.ordered); err != nil {
				return err
			}
		}
	}
	return nil
}

// ownDemand is the ON_ORDER demand order o itself still holds, by projection
// row (rowKey of the shipping dimension set). A ship or receive of o excludes
// it from the FR-16.4 financial-negative check on those rows, so a transfer can
// always move stock against its own booked demand while every other issue still
// sees that demand in availOrdered.
func ownDemand(o domain.TransferOrder) (map[string]decimal.Decimal, error) {
	own := map[string]decimal.Decimal{}
	for _, l := range o.Lines {
		sp := o.Supply(l)
		if !sp.OnOrder.IsPositive() {
			continue
		}
		n, err := o.SourceDims(l).Normalize()
		if err != nil {
			return nil, domainProblem(&domain.Error{Code: domain.CodeInvalid, Detail: err.Error()})
		}
		k := rowKey(l.ProductID, n.Hash())
		own[k] = own[k].Add(sp.OnOrder)
	}
	return own, nil
}

// withoutOwnDemand is r with own taken out of its ON_ORDER, before and after
// (a copy: the row that is saved keeps the demand).
func withoutOwnDemand(r *row, own decimal.Decimal) *row {
	if own.IsZero() {
		return r
	}
	c := *r
	c.oh.OnOrder = c.oh.OnOrder.Sub(own)
	c.before.OnOrder = c.before.OnOrder.Sub(own)
	return &c
}

// SupplyBackfillReport is the outcome of one BackfillTransferSupply batch: the
// open orders examined and those that had pair rows to book. More is true when
// further orders follow Next (the last order id examined); the caller passes
// Next as after to continue.
type SupplyBackfillReport struct {
	Checked int
	Booked  int
	More    bool
	Next    string
}

// BackfillTransferSupply books the ON_ORDER / ORDERED pair for the transfer
// orders that are open (CREATED, SHIPPED or PARTIALLY_RECEIVED) and hold no
// transfer_supply rows: the orders already open when migration 0025 ran, whose
// dimension-set hashes SQL cannot compute. It examines at most limit orders
// with an id above after (empty: from the start), in id order, so a large
// tenant is worked through in bounded calls. Each order runs in its own
// transaction under its order row lock (taken before the supply rows are read
// again, so an edit, ship or receive committed in the meantime is seen and
// only the difference is booked) and the projection lock order of every
// transfer mutation, and announces onhand.changed per moved row. The booking
// is a difference against transfer_supply, so a re-run (or an order that
// changed or was booked in the meantime) books nothing twice. It runs per
// tenant, over the legal entities the caller can see.
func (s *Postgres) BackfillTransferSupply(ctx context.Context, after string, limit int) (SupplyBackfillReport, error) {
	if limit <= 0 || limit > maxListLimit {
		limit = 200
	}
	tp, _ := tenant.FromContext(ctx)
	var rep SupplyBackfillReport
	var open []string
	if err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select o.id::text from transfer_order o
			where o.tenant_id = $1 and o.status in ('CREATED', 'SHIPPED', 'PARTIALLY_RECEIVED')
			and ($2 = '' or o.id > $2::uuid)
			and not exists (select 1 from transfer_supply s where s.tenant_id = o.tenant_id and s.transfer_order_id = o.id)
			order by o.id limit $3`, tp.TenantID, after, limit+1)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				return err
			}
			open = append(open, id)
		}
		return rows.Err()
	}); err != nil {
		return rep, err
	}
	if len(open) > limit {
		open, rep.More = open[:limit], true
	}
	for _, id := range open {
		booked := false
		err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
			o, err := loadTransfer(ctx, tx, id, true)
			if err != nil {
				return err
			}
			moved, err := bookTransferSupply(ctx, tx, tp.TenantID, o, nil)
			if err != nil {
				return err
			}
			if err := announceTransferSupply(ctx, tx, tp.TenantID, o.LegalEntityID, moved); err != nil {
				return err
			}
			booked = len(moved) > 0
			return nil
		})
		if err != nil {
			return rep, err
		}
		rep.Checked++
		if booked {
			rep.Booked++
		}
		rep.Next = id
	}
	if !rep.More {
		rep.Next = ""
	}
	return rep, nil
}
