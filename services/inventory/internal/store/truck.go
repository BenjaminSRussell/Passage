package store

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"net/http"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/inventory/internal/domain"
)

// TruckRef names a truck (a warehouse of its own at a site) and the warehouse it is
// replenished from.
type TruckRef struct {
	LegalEntityID     string
	SiteID            string
	WarehouseID       string
	SourceSiteID      string
	SourceWarehouseID string
}

// TruckLevelRow is a truck's level of one product with what the truck holds and what is on
// its way (open transfer quantity not yet received).
type TruckLevelRow struct {
	TruckRef
	ProductID string
	Unit      string
	Min       decimal.Decimal
	Max       decimal.Decimal
	OnHand    decimal.Decimal
	Inbound   decimal.Decimal
}

// SetTruckLevels replaces the levels of one truck (FR-45.9). Levels of products not named
// are removed; a product named twice is refused.
func (s *Postgres) SetTruckLevels(ctx context.Context, ref TruckRef, levels []domain.TruckLevel) error {
	tp, _ := tenant.FromContext(ctx)
	seen := map[string]bool{}
	for _, l := range levels {
		if err := l.Validate(s.rules.MaxScale); err != nil {
			return problem(http.StatusBadRequest, domain.CodeTruckLevel, fmt.Sprintf("product %s: %v", l.ProductID, err))
		}
		if seen[l.ProductID] {
			return problem(http.StatusBadRequest, domain.CodeTruckLevel, fmt.Sprintf("product %s appears twice", l.ProductID))
		}
		seen[l.ProductID] = true
	}
	return dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `delete from inventory_truck_level where tenant_id = $1 and legal_entity_id = $2 and site_id = $3 and warehouse_id = $4
			and not (product_id::text = any($5))`, tp.TenantID, ref.LegalEntityID, ref.SiteID, ref.WarehouseID, productIDs(levels)); err != nil {
			return scopeProblem(err)
		}
		for _, l := range levels {
			if _, err := tx.Exec(ctx, `insert into inventory_truck_level (tenant_id, legal_entity_id, site_id, warehouse_id, product_id, unit, min_qty, max_qty,
				source_site_id, source_warehouse_id) values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
				on conflict (tenant_id, legal_entity_id, site_id, warehouse_id, product_id) do update set unit = excluded.unit, min_qty = excluded.min_qty,
					max_qty = excluded.max_qty, source_site_id = excluded.source_site_id, source_warehouse_id = excluded.source_warehouse_id, updated_at = now()`,
				tp.TenantID, ref.LegalEntityID, ref.SiteID, ref.WarehouseID, l.ProductID, l.Unit, l.Min.String(), l.Max.String(), ref.SourceSiteID, ref.SourceWarehouseID); err != nil {
				return scopeProblem(err)
			}
		}
		return nil
	})
}

func productIDs(levels []domain.TruckLevel) []string {
	out := make([]string, 0, len(levels))
	for _, l := range levels {
		out = append(out, l.ProductID)
	}
	return out
}

// ListTruckLevels returns the levels of a legal entity, optionally of one truck, with the
// physical stock the truck holds and the quantity on its way.
func (s *Postgres) ListTruckLevels(ctx context.Context, legalEntityID, siteID, warehouseID string) ([]TruckLevelRow, error) {
	tp, _ := tenant.FromContext(ctx)
	var rows []TruckLevelRow
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		r, err := tx.Query(ctx, `select site_id, warehouse_id, product_id::text, unit, min_qty::text, max_qty::text, source_site_id, source_warehouse_id
			from inventory_truck_level where tenant_id = $1 and legal_entity_id = $2 and ($3 = '' or site_id = $3) and ($4 = '' or warehouse_id = $4)
			order by site_id, warehouse_id, product_id limit 5000`, tp.TenantID, legalEntityID, siteID, warehouseID)
		if err != nil {
			return err
		}
		defer r.Close()
		for r.Next() {
			row := TruckLevelRow{TruckRef: TruckRef{LegalEntityID: legalEntityID}}
			var min, max string
			if err := r.Scan(&row.SiteID, &row.WarehouseID, &row.ProductID, &row.Unit, &min, &max, &row.SourceSiteID, &row.SourceWarehouseID); err != nil {
				return err
			}
			row.Min, row.Max = decimal.RequireFromString(min), decimal.RequireFromString(max)
			rows = append(rows, row)
		}
		return r.Err()
	})
	if err != nil {
		return nil, err
	}
	type truck struct{ site, warehouse string }
	held := map[truck]map[string]decimal.Decimal{}
	inbound := map[truck]map[string]decimal.Decimal{}
	for i := range rows {
		k := truck{rows[i].SiteID, rows[i].WarehouseID}
		if held[k] == nil {
			res, err := s.ListOnHand(ctx, OnHandFilter{SiteID: k.site, WarehouseID: k.warehouse, GroupBy: []string{GroupProduct}})
			if err != nil {
				return nil, err
			}
			held[k] = map[string]decimal.Decimal{}
			for _, g := range res.Rows {
				held[k][g.Group[GroupProduct]] = held[k][g.Group[GroupProduct]].Add(g.AvailPhysical)
			}
			if inbound[k], err = s.truckInbound(ctx, legalEntityID, k.site, k.warehouse); err != nil {
				return nil, err
			}
		}
		rows[i].OnHand, rows[i].Inbound = held[k][rows[i].ProductID], inbound[k][rows[i].ProductID]
	}
	return rows, nil
}

// truckInbound is the quantity per product still to be received at the truck by open
// transfer orders (DRAFT, CREATED, SHIPPED, PARTIALLY_RECEIVED): what a new replenishment
// must not order again.
func (s *Postgres) truckInbound(ctx context.Context, legalEntityID, siteID, warehouseID string) (map[string]decimal.Decimal, error) {
	tp, _ := tenant.FromContext(ctx)
	out := map[string]decimal.Decimal{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		r, err := tx.Query(ctx, `select l.product_id::text, sum(l.qty - l.received_qty)::text
			from transfer_order o join transfer_order_line l on l.tenant_id = o.tenant_id and l.transfer_order_id = o.id
			where o.tenant_id = $1 and o.legal_entity_id = $2 and o.to_warehouse = $3 and coalesce(o.to_site, '') in ('', $4)
				and o.status in ('DRAFT', 'CREATED', 'SHIPPED', 'PARTIALLY_RECEIVED') and not l.closed
			group by 1`, tp.TenantID, legalEntityID, warehouseID, siteID)
		if err != nil {
			return err
		}
		defer r.Close()
		for r.Next() {
			var product, qty string
			if err := r.Scan(&product, &qty); err != nil {
				return err
			}
			out[product] = decimal.RequireFromString(qty)
		}
		return r.Err()
	})
	return out, err
}

// ReplenishTrucks creates a DRAFT transfer order for each truck of the legal entity (or the
// one truck named) whose stock plus open inbound transfers is below the minimum of any level:
// from the truck's source warehouse, for the quantity that brings each short product up to
// its maximum (BR-45.7). Trucks that are not short get nothing. The order is created under
// an idempotency key of the truck, the day and the lines, so a repeated run adds nothing.
func (s *Postgres) ReplenishTrucks(ctx context.Context, legalEntityID, siteID, warehouseID string, shipDate time.Time) ([]domain.TransferOrder, error) {
	all, err := s.ListTruckLevels(ctx, legalEntityID, siteID, warehouseID)
	if err != nil {
		return nil, err
	}
	type truck struct{ site, warehouse string }
	var trucks []truck
	seen := map[truck]bool{}
	for _, l := range all {
		if k := (truck{l.SiteID, l.WarehouseID}); !seen[k] {
			seen[k] = true
			trucks = append(trucks, k)
		}
	}
	var out []domain.TransferOrder
	for _, k := range trucks {
		created, err := s.replenishTruck(ctx, legalEntityID, k.site, k.warehouse, shipDate)
		out = append(out, created...)
		if err != nil {
			return out, err
		}
	}
	return out, nil
}

// replenishTruck replenishes one truck while holding that truck's advisory lock. Without it, two runs that read the
// truck at different moments (a completed work order, the nightly run, a buyer's click) see different shortfalls, get
// different idempotency keys and both create a DRAFT; under the lock the second run reads the first one's DRAFT as inbound
// stock and finds nothing short.
func (s *Postgres) replenishTruck(ctx context.Context, legalEntityID, siteID, warehouseID string, shipDate time.Time) ([]domain.TransferOrder, error) {
	unlock, err := s.lockTruck(ctx, legalEntityID, siteID, warehouseID)
	if err != nil {
		return nil, err
	}
	defer unlock()
	levels, err := s.ListTruckLevels(ctx, legalEntityID, siteID, warehouseID)
	if err != nil {
		return nil, err
	}
	type truck struct{ site, warehouse, sourceSite, sourceWarehouse string }
	lines := map[truck][]domain.TransferLine{}
	var order []truck
	for _, l := range levels {
		qty := domain.TruckLevel{Min: l.Min, Max: l.Max}.ReplenishQty(l.OnHand, l.Inbound)
		if !qty.IsPositive() {
			continue
		}
		k := truck{l.SiteID, l.WarehouseID, l.SourceSiteID, l.SourceWarehouseID}
		if _, ok := lines[k]; !ok {
			order = append(order, k)
		}
		lines[k] = append(lines[k], domain.TransferLine{ProductID: l.ProductID, Unit: l.Unit, Qty: qty, Dims: domain.Dimensions{domain.DimSite: l.SourceSiteID}})
	}
	var out []domain.TransferOrder
	for _, k := range order {
		ls := lines[k]
		sort.Slice(ls, func(i, j int) bool { return ls[i].ProductID < ls[j].ProductID })
		h := sha256.New()
		fmt.Fprintf(h, "%s|%s|%s|%s|%s|%s", legalEntityID, k.site, k.warehouse, k.sourceSite, k.sourceWarehouse, shipDate.Format(dateLayout))
		for i := range ls {
			ls[i].LineNo = i + 1
			fmt.Fprintf(h, "|%s=%s", ls[i].ProductID, ls[i].Qty)
		}
		o := domain.TransferOrder{LegalEntityID: legalEntityID, Status: domain.TransferDraft, FromWarehouse: k.sourceWarehouse, ToWarehouse: k.warehouse,
			ShipDate: shipDate, Lines: ls}
		if k.site != k.sourceSite {
			o.ToSite = k.site
		}
		created, _, err := s.CreateTransfer(ctx, "truck-replenish:"+hex.EncodeToString(h.Sum(nil)[:16]), o)
		if err != nil {
			return out, fmt.Errorf("replenish truck %s/%s: %w", k.site, k.warehouse, err)
		}
		out = append(out, created)
	}
	return out, nil
}

// lockTruck takes a session advisory lock named after the tenant and the truck on a connection of its own and returns the
// function that releases it. A run that does not get the lock gives its connection back and tries again shortly, so a crowd
// of waiting runs never holds every pooled connection while the holder needs one; it gives up when its context ends.
func (s *Postgres) lockTruck(ctx context.Context, legalEntityID, siteID, warehouseID string) (func(), error) {
	tp, _ := tenant.FromContext(ctx)
	sum := sha256.Sum256([]byte("truck-replenish|" + tp.TenantID + "|" + legalEntityID + "|" + siteID + "|" + warehouseID))
	key := int64(binary.BigEndian.Uint64(sum[:8]))
	for {
		conn, err := s.pool.Acquire(ctx)
		if err != nil {
			return nil, err
		}
		var got bool
		if err := conn.QueryRow(ctx, `select pg_try_advisory_lock($1)`, key).Scan(&got); err != nil {
			conn.Release()
			return nil, err
		}
		if got {
			return func() {
				// A cancelled request must still give the lock back; if the unlock fails the connection is closed, which releases it.
				if _, err := conn.Exec(context.WithoutCancel(ctx), `select pg_advisory_unlock($1)`, key); err != nil {
					_ = conn.Conn().Close(context.WithoutCancel(ctx))
				}
				conn.Release()
			}, nil
		}
		conn.Release()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(20 * time.Millisecond):
		}
	}
}
