package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/eventing"
	"github.com/BenjaminSRussell/passage/platform/events"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/production/internal/store"
)

const (
	// PlannedOrderFirmedType is planning's plannedorder.firmed.v1 (w22, FR-22.10): a planned order was
	// firmed and the owning context creates the target order.
	PlannedOrderFirmedType  = "dev.passage.planning.plannedorder.firmed.v1"
	PlannedOrderFirmedTopic = "passage.planning.plannedorder"
	// PlannedOrderSource is the CloudEvents source planning writes on its events.
	PlannedOrderSource = "planning"
	// PlanningConsumerName is the consumer (and Kafka group) that follows planning's firmed planned
	// orders, and the service principal it runs as, service:production-planning.
	PlanningConsumerName = "production-planning"
	// DutyPlannedIntake lets the consumer store a firmed planned order. Only its service principal holds it.
	DutyPlannedIntake = "production.planned.intake"
)

// PlanningStore is the persistence the planning consumer needs.
type PlanningStore interface {
	RecordPlannedOrder(ctx context.Context, tx pgx.Tx, in store.PlannedIntakeIn) (bool, error)
}

type plannedOrderFirmedData struct {
	// LegalEntityID is planning's plannedorder.firmed.v1 legal_entity_id (wave 22 slice 29); the platform
	// also copies it into the CloudEvents legalentityid extension, which the consumer reads as a fallback.
	LegalEntityID      string  `json:"legal_entity_id"`
	PlannedOrderID     string  `json:"planned_order_id"`
	PlanVersionID      string  `json:"plan_version_id"`
	BatchID            string  `json:"batch_id"`
	OrderType          string  `json:"order_type"`
	TargetID           string  `json:"target_id"`
	ItemID             string  `json:"item_id"`
	WarehouseID        *string `json:"warehouse_id"`
	SiteID             string  `json:"site_id"`
	Qty                string  `json:"qty"`
	OrderDate          string  `json:"order_date"`
	DeliveryDate       string  `json:"delivery_date"`
	QuotaArrangementID *string `json:"quota_arrangement_id"`
	QuotaSourceID      *string `json:"quota_source_id"`
}

// PlanningConsumerHandlers handles planning's plannedorder.firmed.v1: a firmed production planned
// order lands in the intake (with the warehouse of a warehouse-scope order, FR-22.18) for a planner to
// convert into a production order; purchase and transfer orders belong to other contexts and are ignored.
// Register it with a consumer named PlanningConsumerName, which runs with the DutyPlannedIntake duty.
func PlanningConsumerHandlers(s PlanningStore) map[string]eventing.Handler {
	h := planningConsumer{store: s}
	return map[string]eventing.Handler{PlannedOrderFirmedType: eventing.HandlerFunc(h.plannedOrderFirmed)}
}

type planningConsumer struct{ store PlanningStore }

func principalHasIntakeDuty(ctx context.Context) bool {
	p, ok := tenant.FromContext(ctx)
	return ok && p.HasDuty("production.planned.intake")
}

func uuidOrNil(v *string) bool { return v == nil || ids.Valid(*v) }

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

func (c planningConsumer) plannedOrderFirmed(ctx context.Context, tx pgx.Tx, evt events.Event) error {
	if !principalHasIntakeDuty(ctx) {
		return eventing.Permanent(errors.New("planning consumer has no principal"))
	}
	if evt.Source != PlannedOrderSource {
		return eventing.Permanent(fmt.Errorf("plannedorder.firmed from source %q, want %q", evt.Source, PlannedOrderSource))
	}
	var d plannedOrderFirmedData
	if err := json.Unmarshal(evt.Data, &d); err != nil {
		return eventing.Permanent(fmt.Errorf("plannedorder.firmed data: %w", err))
	}
	if d.OrderType != "production" {
		return nil
	}
	qty, err := decimal.NewFromString(d.Qty)
	if err != nil || !qty.IsPositive() || qty.Exponent() < -6 || !qty.LessThan(maxQty) ||
		!ids.Valid(d.PlannedOrderID) || !ids.Valid(d.PlanVersionID) || !ids.Valid(d.BatchID) || !ids.Valid(d.TargetID) ||
		!ids.Valid(d.ItemID) || !ids.Valid(d.SiteID) || !isDate(d.OrderDate) || !isDate(d.DeliveryDate) ||
		!uuidOrNil(d.WarehouseID) || !uuidOrNil(d.QuotaArrangementID) || !uuidOrNil(d.QuotaSourceID) {
		return eventing.Permanent(errors.New("plannedorder.firmed needs planned_order_id, plan_version_id, batch_id, target_id, item_id, site_id, a positive qty and dates"))
	}
	var legalEntity *string
	if le := firstNonEmpty(d.LegalEntityID, evt.LegalEntityID); le != "" {
		if !ids.Valid(le) {
			return eventing.Permanent(errors.New("plannedorder.firmed legal entity must be a UUID"))
		}
		le = strings.ToLower(le)
		legalEntity = &le
	}
	added, err := c.store.RecordPlannedOrder(ctx, tx, store.PlannedIntakeIn{
		LegalEntityID:  legalEntity,
		PlannedOrderID: d.PlannedOrderID, PlanVersionID: d.PlanVersionID, BatchID: d.BatchID, TargetID: d.TargetID, ProductID: d.ItemID, SiteID: d.SiteID,
		WarehouseID: d.WarehouseID, QuotaArrangementID: d.QuotaArrangementID, QuotaSourceID: d.QuotaSourceID,
		Quantity: qty, OrderDate: d.OrderDate, DeliveryDate: d.DeliveryDate, EventID: evt.ID,
	})
	if err != nil {
		return err
	}
	slog.InfoContext(ctx, "planned order stored in the production intake", "plannedOrderId", d.PlannedOrderID, "added", added, "tenantId", evt.TenantID, "eventId", evt.ID)
	return nil
}
