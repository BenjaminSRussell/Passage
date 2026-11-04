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
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	fulevents "github.com/BenjaminSRussell/passage/services/fulfillment/internal/events"
)

// CodeToleranceVersion is the code of a tolerance saved from a stale version.
const CodeToleranceVersion = CodeSettingVersion

// DeliveryToleranceStore is the over-delivery tolerance per product and
// customer (BR-28.7, overdelivery_pct of section 5.27): how far above the
// planned quantity a pick may go before it is FUL-0005.
type DeliveryToleranceStore interface {
	ListDeliveryTolerances(ctx context.Context, productID, customerID string, limit int) ([]DeliveryTolerance, error)
	SaveDeliveryTolerance(ctx context.Context, in DeliveryToleranceInput, version int, idem Idem) (DeliveryTolerance, error)
}

// DeliveryToleranceInput is the tolerance saved for a scope. A nil product or
// customer widens the scope; both nil is the general tolerance.
type DeliveryToleranceInput struct {
	ProductID       *string
	CustomerID      *string
	OverdeliveryPct decimal.Decimal
}

// DeliveryTolerance is the saved over-delivery tolerance of one scope.
type DeliveryTolerance struct {
	ID              string          `json:"id"`
	ProductID       *string         `json:"productId"`
	CustomerID      *string         `json:"customerId"`
	OverdeliveryPct decimal.Decimal `json:"overdeliveryPct"`
	Version         int             `json:"version"`
	UpdatedAt       time.Time       `json:"updatedAt"`
}

// ListDeliveryTolerances lists the saved tolerances, optionally of one product
// or one customer, the most specific scopes first.
func (s *Postgres) ListDeliveryTolerances(ctx context.Context, productID, customerID string, limit int) ([]DeliveryTolerance, error) {
	out := []DeliveryTolerance{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select id, product_id::text, customer_id::text, overdelivery_pct::text, version, updated_at
			from delivery_tolerance where ($1 = '' or product_id::text = $1) and ($2 = '' or customer_id::text = $2)
			order by (product_id is not null) desc, (customer_id is not null) desc, product_id, customer_id limit $3`, productID, customerID, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var t DeliveryTolerance
			var pct string
			if err := rows.Scan(&t.ID, &t.ProductID, &t.CustomerID, &pct, &t.Version, &t.UpdatedAt); err != nil {
				return err
			}
			t.OverdeliveryPct = dec(pct)
			out = append(out, t)
		}
		return rows.Err()
	})
	return out, err
}

// SaveDeliveryTolerance sets the tolerance of a product and customer scope.
// version is the one the caller read, 0 when the scope has no tolerance yet; a
// stale version is refused with 412. deliverytolerance.changed is written in
// the same transaction.
func (s *Postgres) SaveDeliveryTolerance(ctx context.Context, in DeliveryToleranceInput, version int, idem Idem) (DeliveryTolerance, error) {
	p, _ := tenant.FromContext(ctx)
	var out DeliveryTolerance
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		res, err := runIdempotent(ctx, tx, p.TenantID, idem, func() (DeliveryTolerance, error) {
			zero := "00000000-0000-0000-0000-000000000000"
			scope := func(v *string) string {
				if v == nil {
					return zero
				}
				return *v
			}
			// Serialise the first save of a scope, which has no row to lock yet.
			if _, err := tx.Exec(ctx, `select pg_advisory_xact_lock(hashtextextended($1 || '/delivery-tolerance/' || $2 || '/' || $3, 0))`,
				p.TenantID, scope(in.ProductID), scope(in.CustomerID)); err != nil {
				return DeliveryTolerance{}, err
			}
			var curID, curPct string
			cur := 0
			err := tx.QueryRow(ctx, `select id::text, overdelivery_pct::text, version from delivery_tolerance
				where coalesce(product_id, $1::uuid) = $2::uuid and coalesce(customer_id, $1::uuid) = $3::uuid for update`,
				zero, scope(in.ProductID), scope(in.CustomerID)).Scan(&curID, &curPct, &cur)
			exists := !errors.Is(err, pgx.ErrNoRows)
			if err != nil && exists {
				return DeliveryTolerance{}, err
			}
			if cur != version {
				return DeliveryTolerance{}, httpx.NewProblem(http.StatusPreconditionFailed, CodeToleranceVersion, "delivery tolerance changed since it was read; reload and retry")
			}
			t := DeliveryTolerance{ProductID: in.ProductID, CustomerID: in.CustomerID, OverdeliveryPct: in.OverdeliveryPct}
			var previous *string
			if exists {
				t.ID = curID
				prev := dec(curPct).String()
				previous = &prev
				err = tx.QueryRow(ctx, `update delivery_tolerance set overdelivery_pct = $3, version = version + 1, updated_at = now()
					where tenant_id = $1 and id = $2 returning version, updated_at`, p.TenantID, curID, in.OverdeliveryPct).Scan(&t.Version, &t.UpdatedAt)
			} else {
				t.ID = ids.New()
				err = tx.QueryRow(ctx, `insert into delivery_tolerance (tenant_id, id, product_id, customer_id, overdelivery_pct)
					values ($1, $2, $3, $4, $5) returning version, updated_at`, p.TenantID, t.ID, in.ProductID, in.CustomerID, in.OverdeliveryPct).Scan(&t.Version, &t.UpdatedAt)
			}
			if err != nil {
				return DeliveryTolerance{}, err
			}
			data := fulevents.DeliveryToleranceChangedData{ToleranceID: t.ID, OverdeliveryPct: t.OverdeliveryPct.String(), PreviousPct: previous, Version: t.Version}
			if in.ProductID != nil {
				data.ProductID = *in.ProductID
			}
			if in.CustomerID != nil {
				data.CustomerID = *in.CustomerID
			}
			return t, enqueueFor(ctx, tx, "delivery_tolerance", t.ID, fulevents.DeliveryToleranceChanged, p.TenantID, data)
		})
		out = res
		return err
	})
	return out, err
}

// resolveTolerance is the over-delivery tolerance in percent for a product
// shipped to a customer: the most specific saved scope wins (product and
// customer, product, customer, general); 0 when none is saved.
func resolveTolerance(ctx context.Context, tx pgx.Tx, productID, customerID string) (decimal.Decimal, error) {
	var pct string
	err := tx.QueryRow(ctx, `select overdelivery_pct::text from delivery_tolerance
		where (product_id is null or product_id::text = $1) and (customer_id is null or customer_id::text = $2)
		order by (product_id is not null) desc, (customer_id is not null) desc limit 1`, productID, customerID).Scan(&pct)
	if errors.Is(err, pgx.ErrNoRows) {
		return decimal.Zero, nil
	}
	return dec(pct), err
}

// checkOverPickKept refuses a pick above the planned quantity of line when sales
// cancelled the remainder of its order line (an order_line_kept row) and the
// pick would take the order line beyond the quantity it keeps (FUL-0066): the
// line's other live shipment lines count with the larger of their planned and
// picked quantities, an active drop-ship with its open quantity. The post would
// refuse the same quantity (checkKeptQty); refusing it at pick saves an unpack.
// A line sales never cut back has no row and only the tolerance limits it.
func checkOverPickKept(ctx context.Context, tx pgx.Tx, line ShipmentLine, picked decimal.Decimal) error {
	var kept, others decimal.Decimal
	err := tx.QueryRow(ctx, `select k.kept_qty,
			coalesce((select sum(greatest(sl.qty_planned, sl.qty_picked)) from shipment_line sl join shipment s on s.tenant_id = sl.tenant_id and s.id = sl.shipment_id
				where sl.tenant_id = k.tenant_id and sl.order_line_id = k.order_line_id and s.status <> 'CANCELLED' and sl.id <> $2::uuid), 0)
			+ coalesce((select sum(d.qty - d.qty_closed) from drop_ship d where d.tenant_id = k.tenant_id and d.order_line_id = k.order_line_id and d.status <> 'CANCELLED'), 0)
		from order_line_kept k where k.order_line_id = $1::uuid`, line.OrderLineID, line.ID).Scan(&kept, &others)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if left := kept.Sub(others); picked.GreaterThan(left) {
		return httpx.NewProblem(http.StatusUnprocessableEntity, CodeKeptExceeded,
			fmt.Sprintf("FUL-0066: sales cancelled the remainder of the order line; it keeps %s and its other shipments carry %s, so this line may pick at most %s",
				kept, others, decimal.Max(line.Planned, left)))
	}
	return nil
}
