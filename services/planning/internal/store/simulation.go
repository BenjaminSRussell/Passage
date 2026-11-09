package store

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/planning/internal/domain"
	planevents "github.com/BenjaminSRussell/passage/services/planning/internal/events"
)

// planKindSimulation is the kind of a version written by a scenario run (FR-22.15).
const planKindSimulation = "simulation"

// SimulationRequest is one persisted scenario run. Result is the engine's
// output for the snapshot with the scenario applied.
type SimulationRequest struct {
	ScenarioCode   string
	LegalEntityID  string
	SiteID         string
	AsOf           time.Time
	ItemsPlanned   int
	DurationMs     int64
	InputHash      string
	IdempotencyKey string
	Result         domain.Result
}

func simulationNotFirmable(orderID string) error {
	msg := "planned orders of a simulation cannot be firmed or changed; copy the quantities to the operational plan"
	if orderID != "" {
		msg = "planned order " + orderID + " belongs to a simulation and cannot be firmed; copy the quantities to the operational plan"
	}
	return httpx.NewProblem(http.StatusConflict, CodeSimulationNotFirmable, msg)
}

// RunSimulation stores the result of a scenario run as a new simulation
// version of the scenario's plan (FR-22.15, BR-22.14). It writes only its own
// version: the plan's current version is not demoted, nothing is auto-firmed,
// and no plan.started.v1, plan.completed.v1 or message.raised.v1 is published
// (so purchasing, sales and the operational consumers never see it). One
// scenario.simulated.v1 goes to the outbox in the same transaction. A repeated
// Idempotency-Key returns the stored version. The plan must already have an
// operational run (404 PLAN-NOT-FOUND otherwise).
func (s *Postgres) RunSimulation(ctx context.Context, req SimulationRequest) (Version, bool, error) {
	p, _ := tenant.FromContext(ctx)
	var v Version
	replayed := false
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if req.IdempotencyKey != "" {
			existing, err := scanVersion(tx.QueryRow(ctx, versionSelect+` where v.tenant_id = $1 and v.idempotency_key = $2`, p.TenantID, req.IdempotencyKey))
			if err == nil {
				if existing.inputHash != req.InputHash {
					return idempotencyReuse()
				}
				v, replayed = existing, true
				return nil
			}
			if !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
		}
		var scenarioID, planCode string
		var active bool
		err := tx.QueryRow(ctx, `select id::text, plan_code, active from planning_scenario where tenant_id = $1 and code = $2`, p.TenantID, req.ScenarioCode).
			Scan(&scenarioID, &planCode, &active)
		if errors.Is(err, pgx.ErrNoRows) {
			return scenarioNotFound()
		}
		if err != nil {
			return err
		}
		if !active {
			return httpx.NewProblem(http.StatusConflict, CodeScenarioInactive, "scenario "+req.ScenarioCode+" is not active")
		}
		var exists bool
		if err := tx.QueryRow(ctx, `select exists (select 1 from plan where tenant_id = $1 and code = $2)`, p.TenantID, planCode).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return httpx.NewProblem(http.StatusNotFound, CodePlanNotFound, "plan "+planCode+" has no operational run to simulate against")
		}
		planID, planEntity, err := lockPlan(ctx, tx, p.TenantID, planCode, req.LegalEntityID)
		if err != nil {
			return err
		}
		var number int
		if err := tx.QueryRow(ctx, `select coalesce(max(number), 0) + 1 from plan_version where tenant_id = $1 and plan_id = $2`, p.TenantID, planID).Scan(&number); err != nil {
			return err
		}
		versionID := ids.New()
		var key *string
		if req.IdempotencyKey != "" {
			key = &req.IdempotencyKey
		}
		asOf := req.AsOf.Format(dateLayout)
		r := req.Result
		if _, err := tx.Exec(ctx, `insert into plan_version (tenant_id, id, plan_id, number, kind, scenario_id, site_id, as_of, status, items_planned, planned_orders_count,
			messages_count, duration_ms, plan_hash, input_hash, idempotency_key, legal_entity_id)
			values ($1, $2, $3, $4, 'simulation', $5, $6, $7::date, 'complete', $8, $9, $10, $11, $12, $13, $14, nullif($15, '')::uuid)`,
			p.TenantID, versionID, planID, number, scenarioID, req.SiteID, asOf, req.ItemsPlanned, len(r.Orders), len(r.Messages), req.DurationMs, r.Hash, req.InputHash, key, planEntity); err != nil {
			return err
		}
		batch := &pgx.Batch{}
		counts := planevents.PlannedCounts{}
		for _, o := range r.Orders {
			var futures *string
			if o.FuturesDate != nil {
				f := dayDate(req.AsOf, *o.FuturesDate)
				futures = &f
			}
			switch o.OrderType {
			case domain.OrderPurchase:
				counts.Purchase++
			case domain.OrderProduction:
				counts.Production++
			case domain.OrderTransfer:
				counts.Transfer++
			}
			batch.Queue(`insert into planned_order (tenant_id, id, plan_version_id, number, item_id, order_type, qty, order_date, delivery_date, futures_date, delay_days, lot_rule,
				planning_priority, buffer_zone, warehouse_id)
				values ($1, $2, $3, $4, $5, $6, $7::numeric, $8::date, $9::date, $10::date, $11, $12, $13::numeric, nullif($14, ''), nullif($15, '')::uuid)`,
				p.TenantID, ids.New(), versionID, o.Number, o.ItemID, o.OrderType, o.Qty.String(), dayDate(req.AsOf, o.OrderDate), dayDate(req.AsOf, o.DeliveryDate), futures, o.DelayDays, o.LotRule,
				priorityText(o.PlanningPriority), o.BufferZone, o.WarehouseID)
		}
		for _, pg := range r.Pegging {
			batch.Queue(`insert into pegging (tenant_id, id, plan_version_id, item_id, supply_ref, demand_ref, demand_ref_type, demand_date, qty, warehouse_id)
				values ($1, $2, $3, $4, $5, $6, $7, $8::date, $9::numeric, nullif($10, '')::uuid)`,
				p.TenantID, ids.New(), versionID, pg.ItemID, pg.SupplyRef, pg.DemandRef, pg.DemandRefType, dayDate(req.AsOf, pg.DemandDate), pg.Qty.String(), pg.WarehouseID)
		}
		for _, m := range r.Messages {
			batch.Queue(`insert into action_message (tenant_id, id, plan_version_id, item_id, action, supply_ref, demand_ref, suggested_date, delay_days, qty, warehouse_id, parent_action)
				values ($1, $2, $3, $4, $5, $6, $7, $8::date, $9, $10::numeric, nullif($11, '')::uuid, $12)`,
				p.TenantID, ids.New(), versionID, m.ItemID, m.Action, m.SupplyRef, m.DemandRef, dayDate(req.AsOf, m.SuggestedDate), m.DelayDays, m.Qty.String(), m.WarehouseID, m.ParentAction)
		}
		for _, n := range r.Net {
			batch.Queue(`insert into net_requirement (tenant_id, id, plan_version_id, item_id, day, gross_requirements, scheduled_receipts, planned_receipts, pab, net_requirement, warehouse_id)
				values ($1, $2, $3, $4, $5::date, $6::numeric, $7::numeric, $8::numeric, $9::numeric, $10::numeric, nullif($11, '')::uuid)`,
				p.TenantID, ids.New(), versionID, n.ItemID, dayDate(req.AsOf, n.Day), n.Gross.String(), n.ScheduledReceipts.String(),
				n.PlannedReceipts.String(), n.PAB.String(), n.Net.String(), n.WarehouseID)
		}
		if err := tx.SendBatch(ctx, batch).Close(); err != nil {
			return err
		}
		if err := purgeVersions(ctx, tx, p.TenantID, planID, number-s.maxVersions); err != nil {
			return err
		}
		v = Version{ID: versionID, PlanCode: planCode, Number: number, Status: "complete", AsOf: asOf, ItemsPlanned: req.ItemsPlanned,
			PlannedOrders: len(r.Orders), Messages: len(r.Messages), DurationMs: req.DurationMs, PlanHash: r.Hash,
			Kind: planKindSimulation, ScenarioCode: req.ScenarioCode, inputHash: req.InputHash}
		return enqueue(ctx, tx, planevents.ScenarioSimulated, planID, p.TenantID, versionID, planevents.ScenarioSimulatedData{
			PlanID: planID, PlanCode: planCode, ScenarioID: scenarioID, ScenarioCode: req.ScenarioCode, VersionID: versionID, VersionNumber: number,
			SiteID: req.SiteID, AsOf: asOf, ItemsPlanned: req.ItemsPlanned, PlannedOrders: counts, Messages: len(r.Messages),
			DurationMs: req.DurationMs, PlanHash: r.Hash,
		})
	})
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation && pgErr.ConstraintName == idempotencyConstraint {
		return Version{}, false, idempotencyReuse()
	}
	return v, replayed, err
}
