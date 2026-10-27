// Package events holds the sales event types and payloads (see events/asyncapi.yaml).
package events

import (
	"github.com/BenjaminSRussell/passage/platform/events"
	"github.com/BenjaminSRussell/passage/services/sales/internal/domain"
)

const Context = "sales"

var (
	OrderConfirmed       = events.Type(Context, "order", "confirmed", 1)
	OrderHoldApplied     = events.Type(Context, "orderhold", "applied", 1)
	OrderHoldReleased    = events.Type(Context, "orderhold", "released", 1)
	CreditCheckEvaluated = events.Type(Context, "creditcheck", "evaluated", 1)
)

// All lists every event type this service publishes.
func All() []string {
	return []string{OrderConfirmed, OrderHoldApplied, OrderHoldReleased, CreditCheckEvaluated}
}

// ConfirmedLine is one line of order.confirmed.
type ConfirmedLine struct {
	LineID            string `json:"line_id"`
	ProductID         string `json:"product_id"`
	Quantity          string `json:"quantity"`
	SiteID            string `json:"site_id"`
	WarehouseID       string `json:"warehouse_id"`
	ConfirmedShipDate string `json:"confirmed_ship_date"`
}

// OrderConfirmedData is the data member of order.confirmed.v1.
type OrderConfirmedData struct {
	OrderID    string          `json:"order_id"`
	CustomerID string          `json:"customer_id"`
	Currency   string          `json:"currency"`
	Lines      []ConfirmedLine `json:"lines"`
	NetTotal   string          `json:"net_total"`
	Version    int             `json:"version"`
}

// OrderHoldData is the data member of orderhold.applied.v1 and released.v1.
type OrderHoldData struct {
	OrderID    string `json:"order_id"`
	HoldID     string `json:"hold_id"`
	HoldType   string `json:"hold_type"`
	ReasonCode string `json:"reason_code"`
	Actor      string `json:"actor"`
	Version    int    `json:"version"`
}

// CreditCheckData is the data member of creditcheck.evaluated.v1.
type CreditCheckData struct {
	CustomerID string              `json:"customer_id"`
	OrderID    string              `json:"order_id"`
	Rules      []domain.RuleResult `json:"rules"`
	Outcome    string              `json:"outcome"`
}
