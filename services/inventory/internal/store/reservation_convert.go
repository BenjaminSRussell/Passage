package store

import (
	"context"
	"slices"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"
	invevents "github.com/BenjaminSRussell/passage/services/inventory/internal/events"

	"github.com/BenjaminSRussell/passage/services/inventory/internal/domain"
)

// convertOrderedReservations turns reservations against the expected receipt of a dimension
// set into physical reservations once received of it has arrived (FR-16.7). r is the
// projection row, locked by the caller and already holding the arrived stock; received is the
// quantity that just became physical there. Only reserved quantity converts: min(received,
// reserved_ordered), taken from the ACTIVE ORDERED reservations oldest first. A reservation
// that arrived whole changes kind in place; one that arrived in part is split, the arrived part
// becoming a new PHYSICAL allocation of the same request and demand. reserved_ordered falls and
// reserved_physical rises by the same quantity, so availOrdered does not move and availPhysical
// is lowered by what is now held for the demand. Each conversion writes
// reservation.converted.v1; the row's two buckets are announced once with onhand.changed.
// The caller's transaction carries all of it.
func convertOrderedReservations(ctx context.Context, tx pgx.Tx, tenantID, legalEntityID string, r *row, received decimal.Decimal) error {
	if !received.IsPositive() || !r.oh.ReservedOrdered.IsPositive() {
		return nil
	}
	active, err := queryReservations(ctx, tx, `r.tenant_id = $1 and r.legal_entity_id = $2 and r.product_id = $3 and r.dimension_set_hash = $4
		and r.status = 'ACTIVE' and r.kind = 'ORDERED' order by r.created_at, r.id for update of r`, tenantID, legalEntityID, r.product, r.hash)
	if err != nil {
		return err
	}
	list := make([]domain.Reservation, len(active))
	byID := make(map[string]reservationRow, len(active))
	for i, a := range active {
		list[i] = a.Reservation
		byID[a.ID] = a
	}
	steps := domain.PlanConversion(list, decimal.Min(received, r.oh.ReservedOrdered))
	total := decimal.Zero
	for _, st := range steps {
		cur := byID[st.ReservationID]
		converted := cur.Reservation
		data := invevents.ReservationData{}
		if st.Whole {
			if _, err := tx.Exec(ctx, `update inventory_reservation set kind = 'PHYSICAL', converted_at = now() where tenant_id = $1 and id = $2`,
				tenantID, cur.ID); err != nil {
				return err
			}
			converted.Kind = domain.ReserveKindPhysical
			data = reservationData(converted)
		} else {
			if _, err := tx.Exec(ctx, `update inventory_reservation set qty = qty - $3 where tenant_id = $1 and id = $2`,
				tenantID, cur.ID, st.Qty.String()); err != nil {
				return err
			}
			converted.ID, converted.Qty, converted.Kind = ids.New(), st.Qty, domain.ReserveKindPhysical
			if err := tx.QueryRow(ctx, `insert into inventory_reservation (tenant_id, legal_entity_id, id, idempotency_key, allocation_no,
				product_id, dimension_set_hash, qty, requested_qty, demand_type, demand_id, policy, status, created_by, created_at, demand_segment,
				expires_at, kind, converted_at)
				select tenant_id, legal_entity_id, $3, idempotency_key,
					(select max(allocation_no) + 1 from inventory_reservation m where m.tenant_id = s.tenant_id and m.idempotency_key = s.idempotency_key),
					product_id, dimension_set_hash, $4, requested_qty, demand_type, demand_id, policy, 'ACTIVE', created_by, created_at, demand_segment,
					expires_at, 'PHYSICAL', now()
				from inventory_reservation s where tenant_id = $1 and id = $2
				returning allocation_no, converted_at`, tenantID, cur.ID, converted.ID, st.Qty.String()).Scan(&converted.AllocationNo, &converted.ConvertedAt); err != nil {
				return scopeProblem(err)
			}
			data = reservationData(converted)
			data.ConvertedFromReservationID = cur.ID
		}
		total = total.Add(st.Qty)
		if err := enqueue(ctx, tx, "reservation", converted.ID, invevents.ReservationConverted, tenantID, data); err != nil {
			return err
		}
	}
	if total.IsZero() {
		return nil
	}
	r.oh.ReservedOrdered = r.oh.ReservedOrdered.Sub(total)
	r.oh.ReservedPhysical = r.oh.ReservedPhysical.Add(total)
	if err := saveRow(ctx, tx, tenantID, legalEntityID, r, 0); err != nil {
		return err
	}
	if err := enqueueOnHand(ctx, tx, tenantID, legalEntityID, r, invevents.BucketReservedOrdered, total.Neg()); err != nil {
		return err
	}
	return enqueueOnHand(ctx, tx, tenantID, legalEntityID, r, invevents.BucketReservedPhysical, total)
}

// convertReceivedReservations converts, at every receiving row of a transfer receipt, the
// reservations made against that receipt. The rows are already locked by the transaction
// (bookTransferSupply took them with the receipt's moves), so re-reading them does not wait;
// they are visited in ascending rowKey order like every other row pass.
func convertReceivedReservations(ctx context.Context, tx pgx.Tx, tenantID, legalEntityID string, moves []stockMove) error {
	arrived := map[string]*row{}
	qty := map[string]decimal.Decimal{}
	var keys []string
	for _, m := range moves {
		n, err := m.to.Normalize()
		if err != nil {
			return domainProblem(&domain.Error{Code: domain.CodeInvalid, Detail: err.Error()})
		}
		h := n.Hash()
		k := rowKey(m.product, h)
		if _, ok := arrived[k]; !ok {
			arrived[k] = &row{product: m.product, dims: n, hash: h}
			keys = append(keys, k)
		}
		qty[k] = qty[k].Add(m.qty)
	}
	slices.Sort(keys)
	for _, k := range keys {
		r := arrived[k]
		if err := lockRow(ctx, tx, tenantID, legalEntityID, "", r); err != nil {
			return err
		}
		if err := convertOrderedReservations(ctx, tx, tenantID, legalEntityID, r, qty[k]); err != nil {
			return err
		}
	}
	return nil
}
