// Package events holds the CloudEvent type names and payloads the pricing
// service publishes. Contract: events/asyncapi.yaml.
package events

// Event types. The kernel regex allows three segments after the namespace, so the
// catalog's condition-record.activated collapses to conditionrecord.activated (as org does).
const (
	ConditionRecordActivated = "dev.passage.pricing.conditionrecord.activated.v1"
	ConditionRecordWithdrawn = "dev.passage.pricing.conditionrecord.withdrawn.v1"
)

// ConditionRecordData is the payload of conditionrecord.activated. Keys are
// carried, never customer names or personal data.
type ConditionRecordData struct {
	ID            string `json:"id"`
	LegalEntityID string `json:"legal_entity_id"`
	Type          string `json:"type"`
	CustomerKind  string `json:"customer_kind"`
	CustomerKey   string `json:"customer_key"`
	ProductKind   string `json:"product_kind"`
	ProductKey    string `json:"product_key"`
	Variant       string `json:"variant"`
	Currency      string `json:"currency"`
	Unit          string `json:"unit"`
	FromQuantity  string `json:"from_quantity"`
	ValidFrom     string `json:"valid_from"`
	ValidTo       string `json:"valid_to"`
	Version       int    `json:"version"`
	// Retroactive is true when valid_from is before the activation day (BR-26.7).
	Retroactive bool `json:"retroactive"`
}

// ConditionRecordWithdrawnData is the payload of conditionrecord.withdrawn.
type ConditionRecordWithdrawnData struct {
	ID      string `json:"id"`
	Version int    `json:"version"`
	Reason  string `json:"reason"`
}
