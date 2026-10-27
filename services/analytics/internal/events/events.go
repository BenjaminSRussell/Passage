// Package events holds the event types analytics publishes and their payloads.
package events

import "github.com/BenjaminSRussell/passage/platform/events"

// Context is the bounded context name used in event types.
const Context = "analytics"

// MetricReconciled is emitted when a daily reconciliation result is recorded.
var MetricReconciled = events.Type(Context, "metric", "reconciled", 1)

// MetricReconciledData is the data member of MetricReconciled (payload fields
// from PRD 9.46.5). Amounts are decimal strings, never floats.
type MetricReconciledData struct {
	MetricID      string `json:"metric_id"`
	TenantID      string `json:"tenant_id"`
	LegalEntityID string `json:"legal_entity_id"`
	Date          string `json:"date"`
	Currency      string `json:"currency"`
	SemanticValue string `json:"semantic_value"`
	SourceValue   string `json:"source_value"`
	Delta         string `json:"delta"`
	Tolerance     string `json:"tolerance"`
	Status        string `json:"status"`
	Trusted       bool   `json:"trusted"`
}
