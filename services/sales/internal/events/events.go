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
	OrderChanged         = events.Type(Context, "order", "changed", 1)
	OrderCancelled       = events.Type(Context, "order", "cancelled", 1)
)

// All lists every event type this service publishes.
func All() []string {
	return []string{OrderConfirmed, OrderHoldApplied, OrderHoldReleased, CreditCheckEvaluated, OrderChanged, OrderCancelled}
}

// FieldChange is one entry of order.changed's diff. line_no is 0 for a header
// field.
type FieldChange struct {
	LineNo int    `json:"line_no"`
	Field  string `json:"field"`
	Old    string `json:"old"`
	New    string `json:"new"`
}

// DiffOf converts a domain diff to the event's snake_case shape.
func DiffOf(diff []domain.FieldChange) []FieldChange {
	out := make([]FieldChange, 0, len(diff))
	for _, d := range diff {
		out = append(out, FieldChange{LineNo: d.LineNo, Field: d.Field, Old: d.Old, New: d.New})
	}
	return out
}

// OrderChangedData is the data member of order.changed.v1: the version reached,
// the field-level diff that produced it and the lines whose promise was re-run.
type OrderChangedData struct {
	OrderID         string        `json:"order_id"`
	Version         int           `json:"version"`
	Reason          string        `json:"reason"`
	Actor           string        `json:"actor"`
	Diff            []FieldChange `json:"diff"`
	RepromisedLines []int         `json:"repromised_lines"`
}

// OrderCancelledData is the data member of order.cancelled.v1.
type OrderCancelledData struct {
	OrderID string   `json:"order_id"`
	Version int      `json:"version"`
	LineIDs []string `json:"line_ids"`
	Reason  string   `json:"reason"`
	Actor   string   `json:"actor"`
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
