package store

import (
	"context"
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/planning/internal/domain"
)

// DiffOrder is one planned order in a version diff.
type DiffOrder struct {
	Number string `json:"number"`
	ItemID string `json:"itemId"`
	// WarehouseID is the warehouse planning scope of the order (FR-22.18); absent for the site scope.
	WarehouseID  *string `json:"warehouseId,omitempty"`
	OrderType    string  `json:"orderType"`
	Qty          string  `json:"qty"`
	OrderDate    string  `json:"orderDate"`
	DeliveryDate string  `json:"deliveryDate"`
	VendorID     *string `json:"vendorId,omitempty"`
}

// DiffChange is a planned order present in both versions whose quantity,
// order date or vendor moved.
type DiffChange struct {
	From     DiffOrder `json:"from"`
	To       DiffOrder `json:"to"`
	QtyDelta string    `json:"qtyDelta"`
}

// VersionDiff is the answer of GET .../diff (FR-22.14): both versions, whether
// their plan hashes are equal, and the planned-order differences. The counts
// and quantity deltas cover every order; Added, Removed and Changed list at
// most the requested limit each, and Truncated says when one was cut.
type VersionDiff struct {
	From          Version           `json:"from"`
	To            Version           `json:"to"`
	Identical     bool              `json:"identical"`
	MessagesDelta int               `json:"messagesDelta"`
	AddedCount    int               `json:"addedCount"`
	RemovedCount  int               `json:"removedCount"`
	ChangedCount  int               `json:"changedCount"`
	Unchanged     int               `json:"unchanged"`
	Truncated     bool              `json:"truncated"`
	Added         []DiffOrder       `json:"added"`
	Removed       []DiffOrder       `json:"removed"`
	Changed       []DiffChange      `json:"changed"`
	QtyDelta      map[string]string `json:"qtyDeltaByOrderType"`
}

func toDiffOrder(o domain.VersionOrder) DiffOrder {
	d := DiffOrder{Number: o.Number, ItemID: o.ItemID, OrderType: o.OrderType, Qty: o.Qty.String(), OrderDate: o.OrderDate, DeliveryDate: o.DeliveryDate}
	if o.VendorID != "" {
		v := o.VendorID
		d.VendorID = &v
	}
	if o.WarehouseID != "" {
		w := o.WarehouseID
		d.WarehouseID = &w
	}
	return d
}

// DiffVersions compares two versions of a plan, current or historical, and
// lists at most limit orders in each of Added, Removed and Changed. Orders
// deleted from a version are left out of both sides.
func (s *Postgres) DiffVersions(ctx context.Context, planCode string, from, to, limit int) (VersionDiff, error) {
	p, _ := tenant.FromContext(ctx)
	var out VersionDiff
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		load := func(number int) (Version, []domain.VersionOrder, error) {
			v, err := scanVersion(tx.QueryRow(ctx, versionSelect+` where v.tenant_id = $1 and p.code = $2 and v.number = $3`, p.TenantID, planCode, number))
			if err != nil {
				return v, nil, err
			}
			rows, err := tx.Query(ctx, `select o.number, o.item_id::text, o.order_type, o.qty::text,
				to_char(o.order_date, 'YYYY-MM-DD'), to_char(o.delivery_date, 'YYYY-MM-DD'), coalesce(o.vendor_id::text, ''), coalesce(o.warehouse_id::text, '')
				from planned_order o where o.tenant_id = $1 and o.plan_version_id = $2 and o.status <> 'deleted'`, p.TenantID, v.ID)
			if err != nil {
				return v, nil, err
			}
			defer rows.Close()
			var orders []domain.VersionOrder
			for rows.Next() {
				var o domain.VersionOrder
				var qty string
				if err := rows.Scan(&o.Number, &o.ItemID, &o.OrderType, &qty, &o.OrderDate, &o.DeliveryDate, &o.VendorID, &o.WarehouseID); err != nil {
					return v, nil, err
				}
				if o.Qty, err = decimal.NewFromString(qty); err != nil {
					return v, nil, err
				}
				orders = append(orders, o)
			}
			return v, orders, rows.Err()
		}
		fv, fo, err := load(from)
		if err != nil {
			return err
		}
		tv, to2, err := load(to)
		if err != nil {
			return err
		}
		d := domain.DiffVersions(fo, to2)
		out = VersionDiff{From: fv, To: tv, Identical: fv.PlanHash == tv.PlanHash, MessagesDelta: tv.Messages - fv.Messages,
			AddedCount: len(d.Added), RemovedCount: len(d.Removed), ChangedCount: len(d.Changed), Unchanged: d.Unchanged,
			Truncated: len(d.Added) > limit || len(d.Removed) > limit || len(d.Changed) > limit,
			Added:     []DiffOrder{}, Removed: []DiffOrder{}, Changed: []DiffChange{}, QtyDelta: map[string]string{}}
		for _, o := range d.Added[:min(limit, len(d.Added))] {
			out.Added = append(out.Added, toDiffOrder(o))
		}
		for _, o := range d.Removed[:min(limit, len(d.Removed))] {
			out.Removed = append(out.Removed, toDiffOrder(o))
		}
		for _, c := range d.Changed[:min(limit, len(d.Changed))] {
			out.Changed = append(out.Changed, DiffChange{From: toDiffOrder(c.From), To: toDiffOrder(c.To), QtyDelta: c.QtyDelta.String()})
		}
		for t, q := range d.QtyDelta {
			out.QtyDelta[t] = q.String()
		}
		return nil
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return VersionDiff{}, httpx.NewProblem(http.StatusNotFound, CodeVersionNotFound, "plan version not found")
	}
	return out, err
}
