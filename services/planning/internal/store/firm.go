package store

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/events"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/outbox"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/planning/internal/domain"
	planevents "github.com/BenjaminSRussell/passage/services/planning/internal/events"
)

const (
	// CodeAlreadyFirmed is PLAN-0002 (BR-22.9): the planned order is already firmed.
	CodeAlreadyFirmed = "PLAN-0002"
	// CodeFirmEntity is returned when one firming request names planned orders of
	// more than one legal entity: a batch belongs to exactly one.
	CodeFirmEntity = "PLAN-FIRM-ENTITY"

	firmIdempotencyConstraint = "firming_batch_idempotency_key"
)

// targetTypes maps a planned order type to the order the owning context creates.
var targetTypes = map[string]string{
	domain.OrderPurchase:   "purchase-order",
	domain.OrderProduction: "production-order",
	domain.OrderTransfer:   "transfer-order",
}

// FirmRequest is one firming command: planned orders (distinct, lowercase
// UUIDs) and the grouping of planned purchase orders into purchase orders.
type FirmRequest struct {
	IdempotencyKey string
	InputHash      string
	Grouping       string
	OrderIDs       []string
}

// FirmBatch is the result of one firming command as the API returns it.
type FirmBatch struct {
	ID       string        `json:"id"`
	Grouping string        `json:"grouping"`
	Firmed   int           `json:"firmed"`
	Groups   []FirmedGroup `json:"groups"`
}

// FirmedGroup is one target order the owning context creates; its planned
// orders carry TargetID and, for a purchase order, become its lines.
type FirmedGroup struct {
	TargetType      string   `json:"targetType"`
	TargetID        string   `json:"targetId"`
	OrderType       string   `json:"orderType"`
	SiteID          string   `json:"siteId"`
	VendorID        *string  `json:"vendorId"`
	DeliveryDate    string   `json:"deliveryDate"`
	Qty             string   `json:"qty"`
	PlannedOrderIDs []string `json:"plannedOrderIds"`
}

// firmRow is a planned order locked for firming.
type firmRow struct {
	versionID, itemID, orderDate, deliveryDate string
	status, entity                             string
	current, simulation                        bool
	cand                                       domain.FirmCandidate
}

// FirmPlannedOrders firms planned orders of current plan versions in one
// transaction (FR-22.10, BR-22.9): every order is locked and checked, the
// orders are grouped, each is marked firmed with the id of its target order,
// and one plannedorder.firmed.v1 per order goes to the outbox in the same
// transaction. Creating the target orders is the owning contexts' half of the
// saga. A repeated Idempotency-Key returns the stored batch (replayed true);
// the same key with another request is 422.
func (s *Postgres) FirmPlannedOrders(ctx context.Context, req FirmRequest) (FirmBatch, bool, error) {
	p, _ := tenant.FromContext(ctx)
	var out FirmBatch
	replayed := false
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		// Lock first, then look the key up: a retry that waited on the locks of
		// the same firming in flight sees its committed batch and replays it
		// instead of reporting its own orders as already firmed.
		rows, err := lockForFirming(ctx, tx, p.TenantID, req.OrderIDs)
		if err != nil {
			return err
		}
		var raw []byte
		var hash string
		err = tx.QueryRow(ctx, `select response, input_hash from firming_batch where tenant_id = $1 and idempotency_key = $2`,
			p.TenantID, req.IdempotencyKey).Scan(&raw, &hash)
		if err == nil {
			if hash != req.InputHash {
				return idempotencyReuse()
			}
			replayed = true
			return json.Unmarshal(raw, &out)
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		cands := make([]domain.FirmCandidate, 0, len(req.OrderIDs))
		entity := ""
		for i, id := range req.OrderIDs {
			o, ok := rows[id]
			switch {
			case !ok:
				return httpx.NewProblem(http.StatusNotFound, CodeOrderNotFound, "planned order "+id+" not found")
			case o.status == "firmed":
				return httpx.NewProblem(http.StatusConflict, CodeAlreadyFirmed, "planned order "+id+" is already firmed")
			case o.status == "deleted":
				return httpx.NewProblem(http.StatusConflict, CodeOrderNotEditable, "planned order "+id+" is deleted")
			case o.simulation:
				return simulationNotFirmable(id)
			case !o.current:
				return httpx.NewProblem(http.StatusConflict, CodeOrderNotEditable, "planned order "+id+" belongs to a version that is no longer current")
			}
			if i == 0 {
				entity = o.entity
			} else if o.entity != entity {
				return httpx.NewProblem(http.StatusUnprocessableEntity, CodeFirmEntity, "planned orders of more than one legal entity cannot be firmed in one batch")
			}
			cands = append(cands, o.cand)
		}
		groups, err := domain.GroupForFirming(cands, req.Grouping)
		if err != nil {
			return err
		}

		// The whole batch is decided before the first write, so the batch row
		// the planned orders reference can be inserted first.
		batchID := ids.New()
		out = FirmBatch{ID: batchID, Grouping: req.Grouping, Firmed: len(cands), Groups: make([]FirmedGroup, 0, len(groups))}
		for _, g := range groups {
			out.Groups = append(out.Groups, FirmedGroup{
				TargetType: targetTypes[g.OrderType], TargetID: ids.New(), OrderType: g.OrderType, SiteID: g.SiteID,
				VendorID: optional(g.VendorID), DeliveryDate: epochDate(g.DeliveryDay), Qty: g.Qty.String(), PlannedOrderIDs: g.IDs,
			})
		}
		body, err := json.Marshal(out)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `insert into firming_batch (tenant_id, id, idempotency_key, input_hash, grouping, firmed_count, response, legal_entity_id)
			values ($1, $2, $3, $4, $5, $6, $7, nullif($8, '')::uuid)`, p.TenantID, batchID, req.IdempotencyKey, req.InputHash, req.Grouping, len(cands), body, entity); err != nil {
			return err
		}
		quotaRefs, err := bookQuota(ctx, tx, p.TenantID, req.OrderIDs)
		if err != nil {
			return err
		}
		for _, g := range out.Groups {
			for _, id := range g.PlannedOrderIDs {
				if _, err := tx.Exec(ctx, `update planned_order set status = 'firmed', firmed_batch_id = $3, firmed_target_type = $4,
					firmed_target_id = $5, version = version + 1 where tenant_id = $1 and id = $2`,
					p.TenantID, id, batchID, g.TargetType, g.TargetID); err != nil {
					return err
				}
				o := rows[id]
				evt, err := events.New(planevents.PlannedOrderFirmed, eventSource, id, p.TenantID, planevents.PlannedOrderFirmedData{
					PlannedOrderID: id, PlanVersionID: o.versionID, BatchID: batchID, OrderType: g.OrderType,
					TargetType: g.TargetType, TargetID: g.TargetID, ItemID: o.itemID, SiteID: o.cand.SiteID,
					VendorID: optional(o.cand.VendorID), Qty: o.cand.Qty.String(), OrderDate: o.orderDate, DeliveryDate: o.deliveryDate,
					QuotaArrangementID: quotaRefs[id].arrangement(), QuotaSourceID: quotaRefs[id].source(),
				})
				if err != nil {
					return err
				}
				if err := outbox.Enqueue(ctx, tx, "planned-order", id, evt); err != nil {
					return err
				}
			}
		}
		return nil
	})
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation && pgErr.ConstraintName == firmIdempotencyConstraint {
		// A concurrent request with the same key committed first.
		return FirmBatch{}, false, idempotencyReuse()
	}
	return out, replayed, err
}

// lockForFirming locks the requested planned orders in id order (so two
// overlapping firming requests cannot deadlock) and reads what firming needs.
// Orders that do not exist for the tenant are absent from the map.
func lockForFirming(ctx context.Context, tx pgx.Tx, tenantID string, orderIDs []string) (map[string]firmRow, error) {
	rows, err := tx.Query(ctx, `select o.id::text, o.plan_version_id::text, o.item_id::text, v.site_id::text, o.order_type,
		coalesce(o.vendor_id::text, ''), o.qty::text, to_char(o.order_date, 'YYYY-MM-DD'), to_char(o.delivery_date, 'YYYY-MM-DD'),
		(o.delivery_date - date '1970-01-01'), o.status, v.status = 'current', v.kind = 'simulation', coalesce(v.legal_entity_id::text, '')
		from planned_order o join plan_version v on v.tenant_id = o.tenant_id and v.id = o.plan_version_id
		where o.tenant_id = $1 and o.id = any($2::uuid[]) order by o.id for update of o`, tenantID, orderIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string]firmRow, len(orderIDs))
	for rows.Next() {
		var r firmRow
		var id, qty string
		if err := rows.Scan(&id, &r.versionID, &r.itemID, &r.cand.SiteID, &r.cand.OrderType, &r.cand.VendorID, &qty,
			&r.orderDate, &r.deliveryDate, &r.cand.DeliveryDay, &r.status, &r.current, &r.simulation, &r.entity); err != nil {
			return nil, err
		}
		if r.cand.Qty, err = decimal.NewFromString(qty); err != nil {
			return nil, err
		}
		r.cand.ID = id
		out[id] = r
	}
	return out, rows.Err()
}

// epochDate renders days since 1970-01-01 as YYYY-MM-DD.
func epochDate(day int) string {
	return time.Unix(int64(day)*86400, 0).UTC().Format(dateLayout)
}

func optional(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// autoFirmKeyPrefix keys the firming batch a run writes for its auto-firmed
// orders (BR-22.4): "plan-run:<version id>".
const autoFirmKeyPrefix = "plan-run:"

// autoFirm is the firming a run does for its orders inside the firming fence.
type autoFirm struct {
	batch   FirmBatch
	byOrder map[string]FirmedGroup
}

// groupOf returns the target group of an auto-firmed order; a nil autoFirm
// firms nothing.
func (a *autoFirm) groupOf(orderID string) (FirmedGroup, bool) {
	if a == nil {
		return FirmedGroup{}, false
	}
	g, ok := a.byOrder[orderID]
	return g, ok
}

// autoFirmBatch groups the run's auto-firmed orders (orderIDs[i] is the id of
// req.Result.Orders[i]) into target orders as a planner's firming would with
// the default grouping vendor-date (4.22.13), and returns nil when the run
// firms nothing. A planned order allocated by a quota arrangement carries its
// source's vendor; purchases without one merge per delivery date.
func autoFirmBatch(req RunRequest, versionID string, orderIDs []string) (*autoFirm, error) {
	epoch := int(req.AsOf.Unix() / 86400)
	var cands []domain.FirmCandidate
	for i, o := range req.Result.Orders {
		if o.AutoFirmed {
			cands = append(cands, domain.FirmCandidate{ID: orderIDs[i], OrderType: o.OrderType, SiteID: req.SiteID, VendorID: o.VendorID,
				DeliveryDay: epoch + o.DeliveryDate, Qty: o.Qty})
		}
	}
	if len(cands) == 0 {
		return nil, nil
	}
	groups, err := domain.GroupForFirming(cands, domain.DefaultGrouping)
	if err != nil {
		return nil, err
	}
	out := &autoFirm{
		batch:   FirmBatch{ID: ids.New(), Grouping: domain.DefaultGrouping, Firmed: len(cands), Groups: make([]FirmedGroup, 0, len(groups))},
		byOrder: make(map[string]FirmedGroup, len(cands)),
	}
	for _, g := range groups {
		fg := FirmedGroup{TargetType: targetTypes[g.OrderType], TargetID: ids.New(), OrderType: g.OrderType, SiteID: g.SiteID,
			DeliveryDate: epochDate(g.DeliveryDay), Qty: g.Qty.String(), PlannedOrderIDs: g.IDs}
		out.batch.Groups = append(out.batch.Groups, fg)
		for _, id := range g.IDs {
			out.byOrder[id] = fg
		}
	}
	return out, nil
}
