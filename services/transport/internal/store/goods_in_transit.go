package store

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/events"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/outbox"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/transport/internal/domain"
	tmsevents "github.com/BenjaminSRussell/passage/services/transport/internal/events"
)

const gitColumns = `g.id, g.voyage_id, v.reference, g.site_id, g.incoterm, g.transfer_event, g.in_transit_warehouse, g.currency,
	g.status, g.lines, g.goods_value::text, g.estimated_total::text, g.version, g.created_at, g.shipped_at, g.request_hash`

const gitFrom = ` from goods_in_transit_order g join voyage v on v.tenant_id = g.tenant_id and v.id = g.voyage_id `

// GoodsInTransitFilter narrows the goods-in-transit list. Empty fields match everything.
type GoodsInTransitFilter struct {
	VoyageID string
	Status   string
}

// CreateGoodsInTransit opens a PENDING goods-in-transit order for receipt lines
// of an ESTIMATED voyage (FR-34.12, BR-34.11). The voyage row is locked while
// the order is written, and a receipt line already on another order is refused
// with TMS-0119 (the goods_in_transit_line key holds under concurrent requests).
// A repeat with the same Idempotency-Key and body returns the first order.
func (s *Postgres) CreateGoodsInTransit(ctx context.Context, voyageID, incoterm, siteID, legalEntityID string, lines []int, idem Idem) (domain.GoodsInTransit, error) {
	p, _ := tenant.FromContext(ctx)
	var out domain.GoodsInTransit
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		replay := func() (bool, error) {
			if idem.Key == "" {
				return false, nil
			}
			g, hash, err := scanGIT(tx.QueryRow(ctx, `select `+gitColumns+gitFrom+`where g.tenant_id = $1 and g.idempotency_key = $2`, p.TenantID, idem.Key))
			if errors.Is(err, pgx.ErrNoRows) {
				return false, nil
			}
			if err != nil {
				return false, err
			}
			if hash != idem.Fingerprint {
				return false, reuse()
			}
			out = g
			return true, nil
		}
		if done, err := replay(); done || err != nil {
			return err
		}
		v, _, err := scanVoyage(tx.QueryRow(ctx, `select `+voyageColumns+` from voyage where tenant_id = $1 and id = $2 for update`, p.TenantID, voyageID))
		if errors.Is(err, pgx.ErrNoRows) {
			return NotFound("landed-cost voyage")
		}
		if err != nil {
			return err
		}
		if err := assertParentEntity(parentVoyage, legalEntityID, v.LegalEntityID); err != nil {
			return err
		}
		if v.LegalEntityID == "" {
			return httpx.NewProblem(http.StatusUnprocessableEntity, domain.CodeGITState,
				"voyage "+v.Reference+" has no legal entity; an all-entity admin must assign one before goods-in-transit orders can be opened")
		}
		// A concurrent request with the same key may have committed while this one waited.
		if done, err := replay(); done || err != nil {
			return err
		}
		g, err := domain.NewGoodsInTransit(v, incoterm, siteID, lines)
		var fields domain.FieldErrors
		if errors.As(err, &fields) {
			bad := httpx.NewProblem(http.StatusUnprocessableEntity, domain.CodeGITState, "a receipt line is not on the voyage")
			bad.Errors = fields
			return bad
		}
		if err != nil {
			return ruleProblem(err)
		}
		lineNos := make([]int, len(g.Lines))
		for i, l := range g.Lines {
			lineNos[i] = l.Line
		}
		var taken int
		var onOrder string
		err = tx.QueryRow(ctx, `select receipt_line, order_id from goods_in_transit_line where tenant_id = $1 and voyage_id = $2
			and receipt_line = any($3::int[]) order by receipt_line limit 1`, p.TenantID, voyageID, lineNos).Scan(&taken, &onOrder)
		if err == nil {
			return httpx.NewProblem(http.StatusConflict, domain.CodeGITState,
				"receipt line "+strconv.Itoa(taken)+" of voyage "+v.Reference+" is already on goods-in-transit order "+onOrder)
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		raw, err := json.Marshal(g.Lines)
		if err != nil {
			return err
		}
		id := ids.New()
		if _, err := tx.Exec(ctx, `insert into goods_in_transit_order (tenant_id, id, voyage_id, site_id, incoterm, transfer_event,
			in_transit_warehouse, currency, status, lines, goods_value, estimated_total, idempotency_key, request_hash, created_by, legal_entity_id)
			values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10::jsonb, $11::numeric, $12::numeric, $13, $14, $15, nullif($16, '')::uuid)`,
			p.TenantID, id, voyageID, g.SiteID, g.Incoterm, g.TransferEvent, g.InTransitWarehouse, g.Currency, g.Status, string(raw),
			g.GoodsValue.String(), g.EstimatedTotal.String(), nullable(idem.Key), nullable(idem.Fingerprint), p.UserID, v.LegalEntityID); err != nil {
			return err
		}
		for _, line := range lineNos {
			if _, err := tx.Exec(ctx, `insert into goods_in_transit_line (tenant_id, voyage_id, receipt_line, order_id) values ($1, $2, $3, $4)`,
				p.TenantID, voyageID, line, id); err != nil {
				if isUnique(err) {
					return httpx.NewProblem(http.StatusConflict, domain.CodeGITState,
						"receipt line "+strconv.Itoa(line)+" of voyage "+v.Reference+" is already on another goods-in-transit order")
				}
				return err
			}
		}
		out, _, err = scanGIT(tx.QueryRow(ctx, `select `+gitColumns+gitFrom+`where g.tenant_id = $1 and g.id = $2`, p.TenantID, id))
		return err
	})
	return out, err
}

// GetGoodsInTransit returns one order or TMS-0100.
func (s *Postgres) GetGoodsInTransit(ctx context.Context, id string) (domain.GoodsInTransit, error) {
	p, _ := tenant.FromContext(ctx)
	var g domain.GoodsInTransit
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		g, _, err = scanGIT(tx.QueryRow(ctx, `select `+gitColumns+gitFrom+`where g.tenant_id = $1 and g.id = $2`, p.TenantID, id))
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.GoodsInTransit{}, NotFound("goods-in-transit order")
	}
	return g, err
}

// ListGoodsInTransit returns the newest orders first, at most ListLimit.
func (s *Postgres) ListGoodsInTransit(ctx context.Context, f GoodsInTransitFilter) ([]domain.GoodsInTransit, error) {
	p, _ := tenant.FromContext(ctx)
	out := []domain.GoodsInTransit{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+gitColumns+gitFrom+`where g.tenant_id = $1 and ($2::uuid is null or g.voyage_id = $2)
			and ($3::text = '' or g.status = $3) order by g.created_at desc, g.id limit $4`, p.TenantID, nullable(f.VoyageID), f.Status, ListLimit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			g, _, err := scanGIT(rows)
			if err != nil {
				return err
			}
			out = append(out, g)
		}
		return rows.Err()
	})
	return out, err
}

// ShipGoodsInTransit moves a PENDING order to IN_TRANSIT at the transfer event
// under a row lock and writes goodsintransit.shipped in the same transaction
// (BR-34.11, TMS-0121). An IN_TRANSIT order is returned unchanged and nothing is
// written, so a retry is safe.
func (s *Postgres) ShipGoodsInTransit(ctx context.Context, id string, at time.Time) (domain.GoodsInTransit, error) {
	p, _ := tenant.FromContext(ctx)
	var out domain.GoodsInTransit
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		g, _, err := scanGIT(tx.QueryRow(ctx, `select `+gitColumns+gitFrom+`where g.tenant_id = $1 and g.id = $2 for update of g`, p.TenantID, id))
		if errors.Is(err, pgx.ErrNoRows) {
			return NotFound("goods-in-transit order")
		}
		if err != nil {
			return err
		}
		if g.Status == domain.GITInTransit {
			out = g
			return nil
		}
		next, err := g.Ship(at)
		if err != nil {
			return ruleProblem(err)
		}
		if _, err := tx.Exec(ctx, `update goods_in_transit_order set status = $3, shipped_at = $4, shipped_by = $5,
			version = version + 1, updated_at = now() where tenant_id = $1 and id = $2`,
			p.TenantID, id, next.Status, *next.ShippedAt, p.UserID); err != nil {
			return err
		}
		out, _, err = scanGIT(tx.QueryRow(ctx, `select `+gitColumns+gitFrom+`where g.tenant_id = $1 and g.id = $2`, p.TenantID, id))
		if err != nil {
			return err
		}
		data := tmsevents.GoodsInTransitShippedPayload{
			OrderID: out.ID, VoyageID: out.VoyageID, SiteID: out.SiteID, Incoterm: out.Incoterm, TransferEvent: out.TransferEvent,
			InTransitWarehouse: out.InTransitWarehouse, Currency: out.Currency, ShippedAt: out.ShippedAt.Format(time.RFC3339Nano),
			GoodsValue: out.GoodsValue.StringFixed(2), EstimatedTotal: out.EstimatedTotal.StringFixed(2),
			Lines: make([]tmsevents.GITLinePayload, 0, len(out.Lines)),
		}
		for _, l := range out.Lines {
			line := tmsevents.GITLinePayload{Line: l.Line, Value: l.Value.StringFixed(2), Quantity: l.Quantity.String(),
				Estimated: l.Estimated.StringFixed(2), Costs: make([]tmsevents.GITCostPayload, 0, len(l.Costs))}
			for _, c := range l.Costs {
				line.Costs = append(line.Costs, tmsevents.GITCostPayload{CostType: c.CostType, Amount: c.Amount.StringFixed(2)})
			}
			data.Lines = append(data.Lines, line)
		}
		evt, err := events.New(tmsevents.GoodsInTransitShipped, eventSource, id, p.TenantID, data)
		if err != nil {
			return err
		}
		return outbox.Enqueue(ctx, tx, "goodsintransit", id, evt)
	})
	return out, err
}

func scanGIT(row pgx.Row) (domain.GoodsInTransit, string, error) {
	var g domain.GoodsInTransit
	var lines []byte
	var value, total string
	var hash *string
	if err := row.Scan(&g.ID, &g.VoyageID, &g.VoyageReference, &g.SiteID, &g.Incoterm, &g.TransferEvent, &g.InTransitWarehouse,
		&g.Currency, &g.Status, &lines, &value, &total, &g.Version, &g.CreatedAt, &g.ShippedAt, &hash); err != nil {
		return domain.GoodsInTransit{}, "", err
	}
	if err := json.Unmarshal(lines, &g.Lines); err != nil {
		return domain.GoodsInTransit{}, "", err
	}
	g.GoodsValue, g.EstimatedTotal = decimal.RequireFromString(value), decimal.RequireFromString(total)
	g.CreatedAt = g.CreatedAt.UTC()
	if g.ShippedAt != nil {
		t := g.ShippedAt.UTC()
		g.ShippedAt = &t
		g.Code = domain.CodeOwnershipMoved
	}
	return g, deref(hash), nil
}
