package store

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/planning/internal/domain"
)

// OrderPatch is a planner's change to a planned order (FR-22.11). Nil fields
// are unchanged; Approve moves an unprocessed order to approved.
type OrderPatch struct {
	Qty          *decimal.Decimal
	OrderDate    *string
	DeliveryDate *string
	VendorID     *string
	Approve      bool
}

func versionConflict(what string, current int) error {
	return httpx.NewProblem(http.StatusPreconditionFailed, CodeVersionConflict,
		fmt.Sprintf("the %s was changed by another user; current version is %d", what, current))
}

// editableOrder is the locked state of a planned order that maintenance checks.
type editableOrder struct {
	version                 int
	orderType               string
	orderDate, deliveryDate time.Time
}

// lockEditableOrder locks the order row and checks it can be maintained: it
// exists (404), it is open and belongs to the current version (409
// PLAN-ORDER-NOT-EDITABLE) and expectedVersion is its version (412).
func lockEditableOrder(ctx context.Context, tx pgx.Tx, tenantID, id string, expectedVersion int) (editableOrder, error) {
	var o editableOrder
	var status, versionStatus, kind string
	err := tx.QueryRow(ctx, `select o.status, v.status, v.kind, o.version, o.order_type, o.order_date, o.delivery_date from planned_order o
		join plan_version v on v.tenant_id = o.tenant_id and v.id = o.plan_version_id
		where o.tenant_id = $1 and o.id = $2 for update of o`, tenantID, id).
		Scan(&status, &versionStatus, &kind, &o.version, &o.orderType, &o.orderDate, &o.deliveryDate)
	if errors.Is(err, pgx.ErrNoRows) {
		return o, httpx.NewProblem(http.StatusNotFound, CodeOrderNotFound, "planned order not found")
	}
	if err != nil {
		return o, err
	}
	switch {
	case kind == "simulation":
		return o, simulationNotFirmable("")
	case status == "firmed":
		return o, httpx.NewProblem(http.StatusConflict, CodeOrderNotEditable, "the planned order is firmed")
	case status == "deleted":
		return o, httpx.NewProblem(http.StatusConflict, CodeOrderNotEditable, "the planned order is deleted")
	case versionStatus != "current":
		return o, httpx.NewProblem(http.StatusConflict, CodeOrderNotEditable, "the planned order belongs to a version that is no longer current")
	}
	if o.version != expectedVersion {
		return o, versionConflict("planned order", o.version)
	}
	return o, nil
}

// UpdatePlannedOrder changes quantity, dates or vendor of an open planned order
// of the current version, or approves it, under If-Match (FR-22.11). The
// order date may not fall after the delivery date, and a vendor is only set on
// a planned purchase order. A new vendor on an order a run allocated by quota
// moves it to that vendor's source of the same arrangement, or out of the
// arrangement when the vendor is not one of its sources, so firming books the
// quantity on the vendor that gets the order (FR-22.17).
func (s *Postgres) UpdatePlannedOrder(ctx context.Context, id string, expectedVersion int, patch OrderPatch) (PlannedOrder, error) {
	p, _ := tenant.FromContext(ctx)
	var out PlannedOrder
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		cur, err := lockEditableOrder(ctx, tx, p.TenantID, id, expectedVersion)
		if err != nil {
			return err
		}
		fields := map[string]string{}
		orderDate, deliveryDate := cur.orderDate, cur.deliveryDate
		if patch.OrderDate != nil {
			if orderDate, err = time.Parse(dateLayout, *patch.OrderDate); err != nil {
				fields["orderDate"] = "must be a date as YYYY-MM-DD"
			}
		}
		if patch.DeliveryDate != nil {
			if deliveryDate, err = time.Parse(dateLayout, *patch.DeliveryDate); err != nil {
				fields["deliveryDate"] = "must be a date as YYYY-MM-DD"
			}
		}
		if len(fields) == 0 && orderDate.After(deliveryDate) {
			fields["orderDate"] = "must not be after the delivery date"
		}
		if patch.VendorID != nil && cur.orderType != domain.OrderPurchase {
			fields["vendorId"] = "can only be set on a planned purchase order"
		}
		if len(fields) > 0 {
			prob := httpx.NewProblem(http.StatusUnprocessableEntity, CodeOrderInvalid, "the change is not valid for this planned order")
			prob.Errors = fields
			return prob
		}
		var qty *string
		if patch.Qty != nil {
			q := patch.Qty.String()
			qty = &q
		}
		if _, err := tx.Exec(ctx, `update planned_order set
			qty = coalesce($3::numeric, qty),
			order_date = $4::date,
			delivery_date = $5::date,
			vendor_id = coalesce($6::uuid, vendor_id),
			quota_source_id = case when $6::uuid is null or $6::uuid is not distinct from vendor_id then quota_source_id
				else (select s.id from quota_source s where s.tenant_id = $1 and s.arrangement_id = planned_order.quota_arrangement_id
					and s.source_type = 'vendor' and s.vendor_id = $6::uuid) end,
			quota_arrangement_id = case when $6::uuid is null or $6::uuid is not distinct from vendor_id then quota_arrangement_id
				else (select s.arrangement_id from quota_source s where s.tenant_id = $1 and s.arrangement_id = planned_order.quota_arrangement_id
					and s.source_type = 'vendor' and s.vendor_id = $6::uuid) end,
			status = case when $7::boolean and status = 'unprocessed' then 'approved' else status end,
			version = version + 1
			where tenant_id = $1 and id = $2`,
			p.TenantID, id, qty, orderDate.Format(dateLayout), deliveryDate.Format(dateLayout), patch.VendorID, patch.Approve); err != nil {
			return err
		}
		out, err = scanOrder(tx.QueryRow(ctx, orderSelect+` where o.tenant_id = $1 and o.id = $2`, p.TenantID, id))
		return err
	})
	return out, err
}

// DeletePlannedOrder deletes an open planned order of the current version
// under If-Match (FR-22.11). The row is kept with status deleted so the
// version's pegging still resolves; lists leave deleted orders out.
func (s *Postgres) DeletePlannedOrder(ctx context.Context, id string, expectedVersion int) error {
	p, _ := tenant.FromContext(ctx)
	return dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := lockEditableOrder(ctx, tx, p.TenantID, id, expectedVersion); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `update planned_order set status = 'deleted', version = version + 1 where tenant_id = $1 and id = $2`, p.TenantID, id)
		return err
	})
}
