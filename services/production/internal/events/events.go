// Package events holds the production event types and payloads.
package events

import "github.com/BenjaminSRussell/passage/platform/events"

// Context is the bounded context name used in event types.
const Context = "production"

// Event types. The kernel naming rule allows no hyphen, so the PRD's
// order.status-changed is order.statuschanged (see NOT-IMPLEMENTED.md).
var (
	OrderCreated       = events.Type(Context, "order", "created", 1)
	OrderUpdated       = events.Type(Context, "order", "updated", 1)
	OrderStatusChanged = events.Type(Context, "order", "statuschanged", 1)
)

// All lists every event type this service publishes.
func All() []string { return []string{OrderCreated, OrderUpdated, OrderStatusChanged} }

// OrderCreatedData is the payload of order.created.v1.
type OrderCreatedData struct {
	OrderID      string  `json:"orderId"`
	OrderNumber  string  `json:"orderNumber"`
	ProductID    string  `json:"productId"`
	Quantity     string  `json:"quantity"`
	SiteID       string  `json:"siteId"`
	DeliveryDate *string `json:"deliveryDate"`
	OriginType   string  `json:"originType"`
	OriginID     *string `json:"originId"`
}

// OrderUpdatedData is the payload of order.updated.v1: the header after a PATCH.
type OrderUpdatedData struct {
	OrderID      string  `json:"orderId"`
	Quantity     string  `json:"quantity"`
	DeliveryDate *string `json:"deliveryDate"`
	Status       string  `json:"status"`
	Version      int     `json:"version"`
	Actor        string  `json:"actor"`
}

// OrderStatusChangedData is the payload of order.statuschanged.v1.
type OrderStatusChangedData struct {
	OrderID    string `json:"orderId"`
	From       string `json:"from"`
	To         string `json:"to"`
	Quantity   string `json:"quantity"`
	QtyRAF     string `json:"qtyRaf"`
	QtyError   string `json:"qtyError"`
	Actor      string `json:"actor"`
	OccurredAt string `json:"occurredAt"`
}
