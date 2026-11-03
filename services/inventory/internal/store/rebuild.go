package store

import (
	"context"
	"encoding/hex"
	"slices"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/inventory/internal/domain"
)

// maxRebuildDiffs caps the diff rows returned to the caller; DiffCount is exact.
const maxRebuildDiffs = 100

// RebuildDiff is one projection row whose stored buckets differ from the rebuild.
type RebuildDiff struct {
	LegalEntityID    string
	ProductID        string
	DimensionSetHash string
	Fields           []string
}

// RebuildReport is the outcome of a rebuild-and-compare (FR-16.3, AC2): the
// number of projection rows compared, how many differ, whether they were
// repaired, and up to maxRebuildDiffs of the differing rows.
type RebuildReport struct {
	Checked   int
	DiffCount int
	Repaired  bool
	Diffs     []RebuildDiff
}

// projected is one OnHand row keyed by legal entity, product and dimension set.
type projected struct {
	legalEntity string
	product     string
	hash        []byte
	currency    string
	oh          domain.OnHand
}

// projectionKey orders rows by legal entity, then the posting lock key
// (product, dimension hash), so a repair locks rows in the order postings do.
func projectionKey(legalEntity, product string, hash []byte) string {
	return legalEntity + "|" + rowKey(product, hash)
}

// rebuildSource reads, in one statement and therefore one snapshot, the stored
// projection rows ('O') and what the ledger implies for them: transaction sums
// per status ('T') and active reservation and hold sums ('R' and 'H', which move the
// reserved_physical and held_physical buckets without a transaction) and the open
// transfer order supply ('S' on_order at the shipping set, 'D' ordered at the receiving set). legal entity, product and
// hash narrow it to one row when set (repair under the row lock).
const rebuildSource = `
select 'O', o.legal_entity_id::text, o.product_id::text, o.dimension_set_hash, '', coalesce(o.currency::text, ''),
  o.posted_qty::text, o.posted_value::text, o.received::text, o.registered::text, o.arrived::text, o.ordered::text,
  o.deducted::text, o.picked::text, o.reserved_physical::text, o.held_physical::text, o.reserved_ordered::text, o.on_order::text
from on_hand o
where o.tenant_id = $1 and ($2 = '' or (o.legal_entity_id::text = $2 and o.product_id::text = $3 and o.dimension_set_hash = $4))
union all
select 'T', t.legal_entity_id::text, t.product_id::text, t.dimension_set_hash, coalesce(t.issue_status, t.receipt_status), max(t.currency::text),
  sum(t.qty)::text, sum(coalesce(t.cost_amount_posted, 0))::text, '0', '0', '0', '0', '0', '0', '0', '0', '0', '0'
from inventory_transaction t
where t.tenant_id = $1 and ($2 = '' or (t.legal_entity_id::text = $2 and t.product_id::text = $3 and t.dimension_set_hash = $4))
group by t.legal_entity_id, t.product_id, t.dimension_set_hash, coalesce(t.issue_status, t.receipt_status)
union all
select case r.kind when 'ORDERED' then 'Q' else 'R' end, r.legal_entity_id::text, r.product_id::text, r.dimension_set_hash, '', '',
  sum(r.qty)::text, '0', '0', '0', '0', '0', '0', '0', '0', '0', '0', '0'
from inventory_reservation r
where r.tenant_id = $1 and r.status = 'ACTIVE'
  and ($2 = '' or (r.legal_entity_id::text = $2 and r.product_id::text = $3 and r.dimension_set_hash = $4))
group by r.kind, r.legal_entity_id, r.product_id, r.dimension_set_hash
union all
select 'H', h.legal_entity_id::text, h.product_id::text, h.dimension_set_hash, '', '',
  sum(h.qty - h.released_qty)::text, '0', '0', '0', '0', '0', '0', '0', '0', '0', '0', '0'
from inventory_hold h
where h.tenant_id = $1 and h.state = 'ACTIVE'
  and ($2 = '' or (h.legal_entity_id::text = $2 and h.product_id::text = $3 and h.dimension_set_hash = $4))
group by h.legal_entity_id, h.product_id, h.dimension_set_hash
union all
select 'S', s.legal_entity_id::text, s.product_id::text, s.source_hash, '', '',
  sum(s.on_order_qty)::text, '0', '0', '0', '0', '0', '0', '0', '0', '0', '0', '0'
from transfer_supply s
where s.tenant_id = $1 and ($2 = '' or (s.legal_entity_id::text = $2 and s.product_id::text = $3 and s.source_hash = $4))
group by s.legal_entity_id, s.product_id, s.source_hash
union all
select 'D', s.legal_entity_id::text, s.product_id::text, s.dest_hash, '', '',
  sum(s.ordered_qty)::text, '0', '0', '0', '0', '0', '0', '0', '0', '0', '0', '0'
from transfer_supply s
where s.tenant_id = $1 and ($2 = '' or (s.legal_entity_id::text = $2 and s.product_id::text = $3 and s.dest_hash = $4))
group by s.legal_entity_id, s.product_id, s.dest_hash`

// readProjection returns the stored and the rebuilt projection, keyed by
// projectionKey. A transaction status sum is applied through OnHand.Post, the
// same arithmetic as a live posting, so the rebuild cannot drift from it.
func readProjection(ctx context.Context, tx pgx.Tx, tenantID, legalEntity, product string, hash []byte) (map[string]*projected, map[string]*projected, error) {
	rows, err := tx.Query(ctx, rebuildSource, tenantID, legalEntity, product, hash)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	stored, rebuilt := map[string]*projected{}, map[string]*projected{}
	for rows.Next() {
		var kind, le, prod, status, currency string
		var h []byte
		var b [12]string
		dest := []any{&kind, &le, &prod, &h, &status, &currency}
		for i := range b {
			dest = append(dest, &b[i])
		}
		if err := rows.Scan(dest...); err != nil {
			return nil, nil, err
		}
		k := projectionKey(le, prod, h)
		into := rebuilt
		if kind == "O" {
			into = stored
		}
		p := into[k]
		if p == nil {
			p = &projected{legalEntity: le, product: prod, hash: h}
			into[k] = p
		}
		switch kind {
		case "O":
			p.oh, p.currency = onHandFrom(b), currency
		case "T":
			if err := p.oh.Post(domain.Status(status), decimal.RequireFromString(b[0]), decimal.RequireFromString(b[1]), 1); err != nil {
				return nil, nil, err
			}
			if p.currency == "" {
				p.currency = currency
			}
		case "S":
			p.oh.OnOrder = p.oh.OnOrder.Add(decimal.RequireFromString(b[0]))
		case "D":
			p.oh.Ordered = p.oh.Ordered.Add(decimal.RequireFromString(b[0]))
		case "H":
			p.oh.HeldPhysical = p.oh.HeldPhysical.Add(decimal.RequireFromString(b[0]))
		case "Q":
			p.oh.ReservedOrdered = p.oh.ReservedOrdered.Add(decimal.RequireFromString(b[0]))
		default: // "R"
			p.oh.ReservedPhysical = p.oh.ReservedPhysical.Add(decimal.RequireFromString(b[0]))
		}
	}
	return stored, rebuilt, rows.Err()
}

// RebuildOnHand recomputes every on-hand row the caller can see from the
// inventory transactions and active reservations and compares it with the
// stored row (FR-16.3 rebuild-and-compare, AC2). The comparison reads both
// sides in one snapshot. With repair, each differing row is locked in posting
// lock order, recomputed again under the lock (a posting in flight has then
// committed or not started) and overwritten, so a repair never writes a stale
// value over a concurrent posting. A missing row is created, an orphan row (no
// transaction or reservation) is zeroed. Re-running is safe: the result is a
// function of the ledger.
func (s *Postgres) RebuildOnHand(ctx context.Context, repair bool) (RebuildReport, error) {
	tp, _ := tenant.FromContext(ctx)
	rep := RebuildReport{Repaired: repair, Diffs: []RebuildDiff{}}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		stored, rebuilt, err := readProjection(ctx, tx, tp.TenantID, "", "", nil)
		if err != nil {
			return err
		}
		keys := make([]string, 0, len(stored)+len(rebuilt))
		for k := range stored {
			keys = append(keys, k)
		}
		for k := range rebuilt {
			if _, ok := stored[k]; !ok {
				keys = append(keys, k)
			}
		}
		slices.Sort(keys)
		var diffs []*projected
		for _, k := range keys {
			rep.Checked++
			st, rb := stored[k], rebuilt[k]
			src := st
			var have, want domain.OnHand
			if st != nil {
				have = st.oh
			}
			if rb != nil {
				want = rb.oh
				if src == nil {
					src = rb
				}
			}
			fields := domain.Diff(have, want)
			if len(fields) == 0 {
				continue
			}
			rep.DiffCount++
			diffs = append(diffs, src)
			if len(rep.Diffs) < maxRebuildDiffs {
				rep.Diffs = append(rep.Diffs, RebuildDiff{LegalEntityID: src.legalEntity, ProductID: src.product,
					DimensionSetHash: hex.EncodeToString(src.hash), Fields: fields})
			}
		}
		if !repair {
			return nil
		}
		for _, p := range diffs {
			if err := repairRow(ctx, tx, tp.TenantID, p); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return RebuildReport{}, err
	}
	return rep, nil
}

// repairRow locks one projection row (creating it when missing), rebuilds it
// under the lock and writes the rebuilt buckets. last_txn_seq is left alone:
// it counts postings applied, which a repair does not add.
func repairRow(ctx context.Context, tx pgx.Tx, tenantID string, p *projected) error {
	if _, err := tx.Exec(ctx, `insert into on_hand (tenant_id, legal_entity_id, product_id, dimension_set_hash) values ($1, $2, $3, $4)
		on conflict do nothing`, tenantID, p.legalEntity, p.product, p.hash); err != nil {
		return scopeProblem(err)
	}
	if _, err := tx.Exec(ctx, `select 1 from on_hand where tenant_id = $1 and legal_entity_id = $2 and product_id = $3 and dimension_set_hash = $4
		for update`, tenantID, p.legalEntity, p.product, p.hash); err != nil {
		return err
	}
	_, rebuilt, err := readProjection(ctx, tx, tenantID, p.legalEntity, p.product, p.hash)
	if err != nil {
		return err
	}
	var o domain.OnHand
	var currency *string
	if rb := rebuilt[projectionKey(p.legalEntity, p.product, p.hash)]; rb != nil {
		o = rb.oh
		if rb.currency != "" {
			currency = &rb.currency
		}
	}
	_, err = tx.Exec(ctx, `update on_hand set posted_qty = $5, posted_value = $6, received = $7, registered = $8, arrived = $9, ordered = $10,
		deducted = $11, picked = $12, reserved_physical = $13, held_physical = $14, reserved_ordered = $15, on_order = $16, currency = coalesce(currency, $17), updated_at = now()
		where tenant_id = $1 and legal_entity_id = $2 and product_id = $3 and dimension_set_hash = $4`,
		tenantID, p.legalEntity, p.product, p.hash, o.PostedQty.String(), o.PostedValue.String(), o.Received.String(), o.Registered.String(),
		o.Arrived.String(), o.Ordered.String(), o.Deducted.String(), o.Picked.String(), o.ReservedPhysical.String(), o.HeldPhysical.String(),
		o.ReservedOrdered.String(), o.OnOrder.String(), currency)
	return err
}
