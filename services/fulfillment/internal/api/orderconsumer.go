package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/eventing"
	"github.com/BenjaminSRussell/passage/platform/events"
	"github.com/BenjaminSRussell/passage/platform/ids"
	platformtenant "github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/fulfillment/internal/domain"
	"github.com/BenjaminSRussell/passage/services/fulfillment/internal/store"
)

const (
	// OrderConfirmedType is sales' order.confirmed.v1 (w27).
	OrderConfirmedType  = "dev.passage.sales.order.confirmed.v1"
	OrderConfirmedTopic = "passage.sales.order"
	// OrderHoldAppliedType and OrderHoldReleasedType are sales' orderhold events (w27), on
	// OrderHoldTopic. A post is refused while an order has an active hold (section 5.28.5).
	OrderHoldAppliedType  = "dev.passage.sales.orderhold.applied.v1"
	OrderHoldReleasedType = "dev.passage.sales.orderhold.released.v1"
	OrderHoldTopic        = "passage.sales.orderhold"
	// OrderConfirmedSource is the CloudEvents source sales writes on its events. Who may write
	// to the topic at all is the broker's ACL (only sales' outbox relay); the check keeps an
	// event another service mislabels or misroutes from deciding what ships.
	OrderConfirmedSource = "sales"
	// OrderConsumerName is the consumer (and Kafka group) that follows sales' confirmed orders,
	// and the service principal it runs as, service:fulfillment-order.
	OrderConsumerName = "fulfillment-order"
	// DutyOrderSync lets the consumer store sales' order type. Only its service principal holds it.
	DutyOrderSync = "fulfillment.order.sync"
)

type orderConfirmedData struct {
	OrderID   string `json:"order_id"`
	OrderType string `json:"order_type"`
	Version   int    `json:"version"`
}

type orderHoldData struct {
	OrderID  string `json:"order_id"`
	HoldID   string `json:"hold_id"`
	HoldType string `json:"hold_type"`
	Version  int    `json:"version"`
}

// OrderConsumerStore is the persistence the sales order consumer needs.
type OrderConsumerStore interface {
	ApplyOrderConfirmed(ctx context.Context, tx pgx.Tx, in store.OrderConfirmedIn) (bool, error)
	ApplyOrderHold(ctx context.Context, tx pgx.Tx, in store.OrderHoldIn) (bool, error)
}

// OrderConsumerHandlers handles sales' order.confirmed.v1: it stores the order type per sales
// order, which release and drop-ship creation then use in place of a caller-supplied value.
// Register it with a consumer named OrderConsumerName, which runs with the DutyOrderSync duty.
// The event's per-line atp_qty (sales slice 22) is optional and not stored.
//
// It also handles orderhold.applied.v1 and released.v1, which set a hold active or inactive.
func OrderConsumerHandlers(s OrderConsumerStore) map[string]eventing.Handler {
	hold := func(active bool) eventing.Handler {
		return eventing.HandlerFunc(func(ctx context.Context, tx pgx.Tx, evt events.Event) error {
			actor, ok := platformtenant.FromContext(ctx)
			if !ok || !actor.HasDuty("fulfillment.order.sync") {
				return eventing.Permanent(errors.New("order consumer has no principal"))
			}
			if evt.Source != OrderConfirmedSource {
				return eventing.Permanent(fmt.Errorf("%s from source %q, want %q", evt.Type, evt.Source, OrderConfirmedSource))
			}
			var d orderHoldData
			if err := json.Unmarshal(evt.Data, &d); err != nil {
				return eventing.Permanent(fmt.Errorf("%s data: %w", evt.Type, err))
			}
			if !ids.Valid(d.OrderID) || !ids.Valid(d.HoldID) || d.Version < 1 || d.HoldType == "" || len(d.HoldType) > 64 {
				return eventing.Permanent(errors.New("orderhold needs order_id, hold_id, hold_type and version"))
			}
			changed, err := s.ApplyOrderHold(ctx, tx, store.OrderHoldIn{OrderID: d.OrderID, HoldID: d.HoldID, HoldType: d.HoldType, Active: active, Version: d.Version, EventID: evt.ID})
			if err != nil {
				return err
			}
			slog.InfoContext(ctx, "sales order hold stored", "orderId", d.OrderID, "holdId", d.HoldID, "active", active, "changed", changed,
				"tenantId", evt.TenantID, "eventId", evt.ID)
			return nil
		})
	}
	return map[string]eventing.Handler{OrderHoldAppliedType: hold(true), OrderHoldReleasedType: hold(false), OrderConfirmedType: eventing.HandlerFunc(func(ctx context.Context, tx pgx.Tx, evt events.Event) error {
		actor, ok := platformtenant.FromContext(ctx)
		if !ok || !actor.HasDuty("fulfillment.order.sync") {
			return eventing.Permanent(errors.New("order consumer has no principal"))
		}
		if evt.Source != OrderConfirmedSource {
			return eventing.Permanent(fmt.Errorf("order.confirmed from source %q, want %q", evt.Source, OrderConfirmedSource))
		}
		var d orderConfirmedData
		if err := json.Unmarshal(evt.Data, &d); err != nil {
			return eventing.Permanent(fmt.Errorf("order.confirmed data: %w", err))
		}
		// order_type is required on the sales event: an empty one is malformed, never read as ORDER
		// (that would let a lost JOURNAL ship).
		if !ids.Valid(d.OrderID) || d.Version < 1 || d.OrderType == "" || !domain.ValidOrderType(d.OrderType) {
			return eventing.Permanent(errors.New("order.confirmed needs order_id, version and a known order_type"))
		}
		changed, err := s.ApplyOrderConfirmed(ctx, tx, store.OrderConfirmedIn{
			OrderID: strings.ToLower(d.OrderID), OrderType: d.OrderType, Version: d.Version, EventID: evt.ID,
		})
		if err != nil {
			return err
		}
		slog.InfoContext(ctx, "sales order type stored", "orderId", d.OrderID, "orderType", d.OrderType, "changed", changed,
			"tenantId", evt.TenantID, "eventId", evt.ID)
		return nil
	})}
}

// storedHolds returns the orders among orderIDs that have an active hold (lower case ids).
func (a *API) storedHolds(ctx context.Context, orderIDs ...string) (map[string]bool, error) {
	return a.store.HeldOrders(ctx, orderIDs)
}

// storedOrderTypes returns the order type fulfillment holds for each order, empty for an order it
// has not heard of.
func (a *API) storedOrderTypes(ctx context.Context, orderIDs ...string) (map[string]string, error) {
	return a.store.OrderTypes(ctx, orderIDs)
}
