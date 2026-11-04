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
	platformtenant "github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/purchasing/internal/domain"
	"github.com/BenjaminSRussell/passage/services/purchasing/internal/store"
)

const (
	// DropShipClosedType is fulfillment's dropship.closed.v1 (wave 28), on DropShipTopic (the drop-ship
	// channel of fulfillment's AsyncAPI, keyed by the drop-ship, so the closings of one drop-ship arrive in order).
	DropShipClosedType = "dev.passage.fulfillment.dropship.closed.v1"
	// DropShipCancelledType is fulfillment's dropship.cancelled.v1 on the same topic (slice 19).
	DropShipCancelledType = "dev.passage.fulfillment.dropship.cancelled.v1"
	DropShipTopic         = "passage.fulfillment.dropship"
	// DropShipSource is the CloudEvents source fulfillment writes. Who may write to the topic at all is
	// the broker's ACL (only fulfillment's outbox relay); the check keeps an event another service
	// mislabels or misroutes from changing a purchase order.
	DropShipSource = "fulfillment"
	// MessageAppliedType is planning's message.applied.v1 (wave 22), on MessageAppliedTopic. Planning writes
	// its message events with aggregate type "plan", so they travel on the plan channel of its AsyncAPI
	// (passage.planning.plan, keyed by the plan) beside plan.started, plan.completed, message.raised and
	// message.dismissed, which the consumer skips.
	MessageAppliedType  = "dev.passage.planning.message.applied.v1"
	MessageAppliedTopic = "passage.planning.plan"
	// MessageSource is the CloudEvents source planning writes.
	MessageSource = "planning"
	// InboundConsumerName is the consumer (and Kafka group) that follows the two topics, and the service
	// principal it runs as, service:purchasing-inbound.
	InboundConsumerName = "purchasing-inbound"
	// DutyInboundApply lets the consumer change purchase orders on behalf of another service's event.
	// Only its service principal holds it.
	DutyInboundApply = "purchasing.inbound.apply"
)

// dropShipClosedData is the part of dropship.closed.v1 the consumer reads.
type dropShipClosedData struct {
	DropShipID          string `json:"drop_ship_id"`
	DropShipNumber      string `json:"drop_ship_number"`
	LegalEntityID       string `json:"legal_entity_id"`
	PurchaseOrderID     string `json:"purchase_order_id"`
	PurchaseOrderLineID string `json:"purchase_order_line_id"`
	QtyClosed           string `json:"qty_closed"`
}

// dropShipCancelledData is the part of dropship.cancelled.v1 the consumer reads.
type dropShipCancelledData struct {
	DropShipID          string `json:"drop_ship_id"`
	DropShipNumber      string `json:"drop_ship_number"`
	LegalEntityID       string `json:"legal_entity_id"`
	PurchaseOrderID     string `json:"purchase_order_id"`
	PurchaseOrderLineID string `json:"purchase_order_line_id"`
	Qty                 string `json:"qty"`
	QtyConfirmed        string `json:"qty_confirmed"`
}

// messageAppliedData is the part of message.applied.v1 the consumer reads. legal_entity_id is null for a
// plan with none, and warehouse_id for the site scope.
type messageAppliedData struct {
	MessageID     string  `json:"message_id"`
	LegalEntityID *string `json:"legal_entity_id"`
	Action        string  `json:"action"`
	ItemID        string  `json:"item_id"`
	WarehouseID   *string `json:"warehouse_id"`
	SupplyRef     string  `json:"supply_ref"`
	SuggestedDate string  `json:"suggested_date"`
	Qty           string  `json:"qty"`
	DecidedBy     string  `json:"decided_by"`
	Note          string  `json:"note"`
}

// InboundConsumerStore is the persistence the inbound consumer needs.
type InboundConsumerStore interface {
	ApplyDropShipClosed(ctx context.Context, tx pgx.Tx, in store.DropShipClosedIn) (store.InboundChange, bool, error)
	ApplyDropShipCancelled(ctx context.Context, tx pgx.Tx, in store.DropShipCancelledIn) (store.InboundChange, bool, error)
	ApplyPlanningMessage(ctx context.Context, tx pgx.Tx, in store.PlanningMessageIn) (store.InboundChange, bool, error)
}

// InboundConsumerHandlers handles fulfillment's dropship.closed.v1 and dropship.cancelled.v1 and planning's
// message.applied.v1. A cancelled drop-ship takes its quantity (less what was confirmed or closed) off its
// PO line the way a closing does.
// Register it with a consumer named InboundConsumerName, which runs with the DutyInboundApply duty.
//
// A closed drop-ship lowers the PO line it bought against by what the closing adds to qty_closed (the
// drop-ship's running total). An applied planning message
// postpones or advances, decreases or increases, or cancels the line of its item on the purchase order its
// supply_ref names; a supply_ref that is no purchase order is skipped without a trace. Both go through
// the PO change management path (FR-17.8) and leave one row for a buyer, applied or refused. A malformed
// event is permanent (dead-lettered); a purchasing rule that stops a change is a REFUSED row, never a retry.
func InboundConsumerHandlers(s InboundConsumerStore) map[string]eventing.Handler {
	return map[string]eventing.Handler{
		DropShipClosedType: eventing.HandlerFunc(func(ctx context.Context, tx pgx.Tx, evt events.Event) error {
			if err := checkInboundEvent(ctx, evt, DropShipSource); err != nil {
				return err
			}
			var d dropShipClosedData
			if err := json.Unmarshal(evt.Data, &d); err != nil {
				return eventing.Permanent(fmt.Errorf("%s data: %w", evt.Type, err))
			}
			qty, err := decimal.NewFromString(d.QtyClosed)
			if !ids.Valid(d.DropShipID) || !ids.Valid(d.LegalEntityID) || !ids.Valid(d.PurchaseOrderID) || !ids.Valid(d.PurchaseOrderLineID) ||
				err != nil || qty.IsNegative() {
				return eventing.Permanent(errors.New("dropship.closed needs drop_ship_id, legal_entity_id, purchase_order_id, purchase_order_line_id and a qty_closed of 0 or more"))
			}
			change, recorded, err := s.ApplyDropShipClosed(ctx, tx, store.DropShipClosedIn{EventID: evt.ID, LegalEntityID: strings.ToLower(d.LegalEntityID),
				DropShipID: strings.ToLower(d.DropShipID), DropShipNumber: d.DropShipNumber, PurchaseOrderID: strings.ToLower(d.PurchaseOrderID),
				PurchaseOrderLineID: strings.ToLower(d.PurchaseOrderLineID), QtyClosed: qty})
			if err != nil {
				return err
			}
			slog.InfoContext(ctx, "drop-ship closing followed", "dropShipId", d.DropShipID, "recorded", recorded, "status", change.Status,
				"reasonCode", change.ReasonCode, "tenantId", evt.TenantID, "eventId", evt.ID)
			return nil
		}),
		DropShipCancelledType: eventing.HandlerFunc(func(ctx context.Context, tx pgx.Tx, evt events.Event) error {
			if err := checkInboundEvent(ctx, evt, DropShipSource); err != nil {
				return err
			}
			var d dropShipCancelledData
			if err := json.Unmarshal(evt.Data, &d); err != nil {
				return eventing.Permanent(fmt.Errorf("%s data: %w", evt.Type, err))
			}
			qty, err := decimal.NewFromString(d.Qty)
			confirmed := decimal.Zero
			if err == nil && d.QtyConfirmed != "" {
				confirmed, err = decimal.NewFromString(d.QtyConfirmed)
			}
			if !ids.Valid(d.DropShipID) || !ids.Valid(d.LegalEntityID) || !ids.Valid(d.PurchaseOrderID) || !ids.Valid(d.PurchaseOrderLineID) ||
				err != nil || qty.IsNegative() || confirmed.IsNegative() {
				return eventing.Permanent(errors.New("dropship.cancelled needs drop_ship_id, legal_entity_id, purchase_order_id, purchase_order_line_id and a qty and qty_confirmed of 0 or more"))
			}
			change, recorded, err := s.ApplyDropShipCancelled(ctx, tx, store.DropShipCancelledIn{EventID: evt.ID, LegalEntityID: strings.ToLower(d.LegalEntityID),
				DropShipID: strings.ToLower(d.DropShipID), DropShipNumber: d.DropShipNumber, PurchaseOrderID: strings.ToLower(d.PurchaseOrderID),
				PurchaseOrderLineID: strings.ToLower(d.PurchaseOrderLineID), Qty: qty, QtyConfirmed: confirmed})
			if err != nil {
				return err
			}
			slog.InfoContext(ctx, "drop-ship cancellation followed", "dropShipId", d.DropShipID, "recorded", recorded, "status", change.Status,
				"reasonCode", change.ReasonCode, "tenantId", evt.TenantID, "eventId", evt.ID)
			return nil
		}),
		MessageAppliedType: eventing.HandlerFunc(func(ctx context.Context, tx pgx.Tx, evt events.Event) error {
			if err := checkInboundEvent(ctx, evt, MessageSource); err != nil {
				return err
			}
			var d messageAppliedData
			if err := json.Unmarshal(evt.Data, &d); err != nil {
				return eventing.Permanent(fmt.Errorf("%s data: %w", evt.Type, err))
			}
			in, err := planningMessageIn(evt.ID, d)
			if err != nil {
				return eventing.Permanent(err)
			}
			if in.LegalEntityID == "" {
				// Only a PO of a legal entity can be changed; a plan with none has no purchase orders.
				slog.InfoContext(ctx, "planning message of a plan with no legal entity skipped", "messageId", d.MessageID, "tenantId", evt.TenantID, "eventId", evt.ID)
				return nil
			}
			if d.SupplyRef == "" || !appliesToPurchaseOrder(d.Action) {
				return nil
			}
			change, recorded, err := s.ApplyPlanningMessage(ctx, tx, in)
			if err != nil {
				return err
			}
			slog.InfoContext(ctx, "planning message followed", "messageId", d.MessageID, "action", d.Action, "recorded", recorded, "status", change.Status,
				"reasonCode", change.ReasonCode, "tenantId", evt.TenantID, "eventId", evt.ID)
			return nil
		}),
	}
}

func appliesToPurchaseOrder(action string) bool {
	switch action {
	case domain.ActionPostpone, domain.ActionAdvance, domain.ActionCancel, domain.ActionDecrease, domain.ActionIncrease:
		return true
	}
	return false
}

func checkInboundEvent(ctx context.Context, evt events.Event, source string) error {
	actor, ok := platformtenant.FromContext(ctx)
	if !ok || !actor.HasDuty(DutyInboundApply) {
		return eventing.Permanent(errors.New("inbound consumer has no principal"))
	}
	if evt.Source != source {
		return eventing.Permanent(fmt.Errorf("%s from source %q, want %q", evt.Type, evt.Source, source))
	}
	return nil
}

// planningMessageIn validates the data of message.applied.v1 and turns it into the store input.
func planningMessageIn(eventID string, d messageAppliedData) (store.PlanningMessageIn, error) {
	in := store.PlanningMessageIn{EventID: eventID, MessageID: strings.ToLower(d.MessageID), Action: d.Action, SupplyRef: d.SupplyRef,
		ItemID: strings.ToLower(d.ItemID), DecidedBy: d.DecidedBy, Note: d.Note}
	if !ids.Valid(d.MessageID) || !ids.Valid(d.ItemID) || d.Action == "" {
		return in, errors.New("message.applied needs message_id, item_id and action")
	}
	if d.LegalEntityID != nil {
		if !ids.Valid(*d.LegalEntityID) {
			return in, errors.New("message.applied legal_entity_id is not a UUID")
		}
		in.LegalEntityID = strings.ToLower(*d.LegalEntityID)
	}
	if d.WarehouseID != nil {
		if !ids.Valid(*d.WarehouseID) {
			return in, errors.New("message.applied warehouse_id is not a UUID")
		}
		in.WarehouseID = strings.ToLower(*d.WarehouseID)
	}
	if d.Qty != "" {
		qty, err := decimal.NewFromString(d.Qty)
		if err != nil || qty.IsNegative() {
			return in, errors.New("message.applied qty is not a decimal of 0 or more")
		}
		in.Qty = qty
	}
	if d.SuggestedDate != "" {
		day, err := domain.ParseDay(d.SuggestedDate)
		if err != nil {
			return in, errors.New("message.applied suggested_date is not a date")
		}
		in.SuggestedDate = day
	}
	return in, nil
}
