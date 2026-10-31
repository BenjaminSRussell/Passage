package store

import (
	"context"
	"encoding/json"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/events"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/outbox"
	"github.com/BenjaminSRussell/passage/services/planning/internal/domain"
	planevents "github.com/BenjaminSRussell/passage/services/planning/internal/events"
)

// approvedFirmKeyPrefix keys the firming batch a run writes for the orders a
// planner approved in the previous version (FR-22.11): "plan-run-approved:<version id>".
const approvedFirmKeyPrefix = "plan-run-approved:"

type approvedOrder struct {
	id, itemID, siteID, orderType, vendorID, orderDate, deliveryDate string
	deliveryDay                                                      int
	qty                                                              decimal.Decimal
}

// firmApprovedOrders firms, inside the run transaction, the planned orders of
// the plan's previous current version that a planner approved (FR-22.11: an
// approved order becomes firmed on the next run). They are grouped like a
// planner's firming with the default grouping vendor-date (4.22.13), stored in
// one firming batch, marked firmed with the id of their target order, and one
// plannedorder.firmed.v1 per order goes to the outbox. Firmed orders stay in
// the version they were approved in, where carryForward reads them as supply.
// It returns how many orders it firmed.
func firmApprovedOrders(ctx context.Context, tx pgx.Tx, tenantID, planID, planEntity, newVersionID, runHash string) (int, error) {
	rows, err := tx.Query(ctx, `select o.id::text, o.plan_version_id::text, o.item_id::text, v.site_id::text, o.order_type,
		coalesce(o.vendor_id::text, ''), o.qty::text, to_char(o.order_date, 'YYYY-MM-DD'), to_char(o.delivery_date, 'YYYY-MM-DD'),
		(o.delivery_date - date '1970-01-01')
		from planned_order o join plan_version v on v.tenant_id = o.tenant_id and v.id = o.plan_version_id
		where o.tenant_id = $1 and v.plan_id = $2 and v.status = 'current' and v.kind <> 'simulation' and o.status = 'approved'
		order by o.id for update of o`, tenantID, planID)
	if err != nil {
		return 0, err
	}
	var orders []approvedOrder
	versionIDs := map[string]string{}
	for rows.Next() {
		var o approvedOrder
		var versionID, qty string
		if err := rows.Scan(&o.id, &versionID, &o.itemID, &o.siteID, &o.orderType, &o.vendorID, &qty, &o.orderDate, &o.deliveryDate, &o.deliveryDay); err != nil {
			rows.Close()
			return 0, err
		}
		if o.qty, err = decimal.NewFromString(qty); err != nil {
			rows.Close()
			return 0, err
		}
		versionIDs[o.id] = versionID
		orders = append(orders, o)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	if len(orders) == 0 {
		return 0, nil
	}
	cands := make([]domain.FirmCandidate, len(orders))
	byID := make(map[string]approvedOrder, len(orders))
	for i, o := range orders {
		cands[i] = domain.FirmCandidate{ID: o.id, OrderType: o.orderType, SiteID: o.siteID, VendorID: o.vendorID, DeliveryDay: o.deliveryDay, Qty: o.qty}
		byID[o.id] = o
	}
	groups, err := domain.GroupForFirming(cands, domain.DefaultGrouping)
	if err != nil {
		return 0, err
	}
	batch := FirmBatch{ID: ids.New(), Grouping: domain.DefaultGrouping, Firmed: len(orders), Groups: make([]FirmedGroup, 0, len(groups))}
	for _, g := range groups {
		batch.Groups = append(batch.Groups, FirmedGroup{TargetType: targetTypes[g.OrderType], TargetID: ids.New(), OrderType: g.OrderType,
			SiteID: g.SiteID, VendorID: optional(g.VendorID), DeliveryDate: epochDate(g.DeliveryDay), Qty: g.Qty.String(), PlannedOrderIDs: g.IDs})
	}
	body, err := json.Marshal(batch)
	if err != nil {
		return 0, err
	}
	if _, err := tx.Exec(ctx, `insert into firming_batch (tenant_id, id, idempotency_key, input_hash, grouping, firmed_count, response, legal_entity_id)
		values ($1, $2, $3, $4, $5, $6, $7, nullif($8, '')::uuid)`, tenantID, batch.ID, approvedFirmKeyPrefix+newVersionID, runHash,
		batch.Grouping, batch.Firmed, body, planEntity); err != nil {
		return 0, err
	}
	for _, g := range batch.Groups {
		for _, id := range g.PlannedOrderIDs {
			if _, err := tx.Exec(ctx, `update planned_order set status = 'firmed', firmed_batch_id = $3, firmed_target_type = $4,
				firmed_target_id = $5, version = version + 1 where tenant_id = $1 and id = $2`, tenantID, id, batch.ID, g.TargetType, g.TargetID); err != nil {
				return 0, err
			}
			o := byID[id]
			evt, err := events.New(planevents.PlannedOrderFirmed, eventSource, id, tenantID, planevents.PlannedOrderFirmedData{
				PlannedOrderID: id, PlanVersionID: versionIDs[id], BatchID: batch.ID, OrderType: g.OrderType,
				TargetType: g.TargetType, TargetID: g.TargetID, ItemID: o.itemID, SiteID: o.siteID,
				VendorID: optional(o.vendorID), Qty: o.qty.String(), OrderDate: o.orderDate, DeliveryDate: o.deliveryDate,
			})
			if err != nil {
				return 0, err
			}
			if err := outbox.Enqueue(ctx, tx, "planned-order", id, evt); err != nil {
				return 0, err
			}
		}
	}
	return len(orders), nil
}
