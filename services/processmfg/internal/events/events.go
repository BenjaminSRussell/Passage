// Package events holds the event types and payloads the processmfg service
// publishes through its outbox. Types follow the kernel rule
// dev.passage.<context>.<entity>.<verb>.v<n>; the context is "production",
// as in PRD 4.24.5, and the entity drops the PRD's hyphen (batch-order →
// batchorder) because the kernel rule allows none.
package events

// Source is the CloudEvents source of every event this service writes.
const Source = "processmfg"

// Event types.
const (
	FormulaCreated     = "dev.passage.production.formula.created.v1"
	BatchOrderCreated  = "dev.passage.production.batchorder.created.v1"
	BatchOrderRescaled = "dev.passage.production.batchorder.rescaled.v1"
)

// All lists every event type this service publishes.
func All() []string { return []string{FormulaCreated, BatchOrderCreated, BatchOrderRescaled} }

// FormulaCreatedData is the payload of FormulaCreated.
type FormulaCreatedData struct {
	FormulaID string `json:"formulaId"`
	Code      string `json:"code"`
	ItemID    string `json:"itemId"`
	Size      string `json:"size"`
	Unit      string `json:"unit"`
	Lines     int    `json:"lines"`
}

// OutputLine is a co- or by-product line of a batch order at its batch size.
type OutputLine struct {
	LineNumber int    `json:"lineNumber"`
	ItemID     string `json:"itemId"`
	Type       string `json:"type"`
	Qty        string `json:"qty"`
}

// BatchOrderCreatedData is the payload of BatchOrderCreated (PRD 4.24.5:
// orderId, formulaVersionId, batchSize, scaleFactor, coProducts[]).
type BatchOrderCreatedData struct {
	OrderID        string       `json:"orderId"`
	OrderNumber    string       `json:"orderNumber"`
	LegalEntityID  string       `json:"legalEntityId"`
	FormulaID      string       `json:"formulaId"`
	FormulaVersion int          `json:"formulaVersion"`
	ItemID         string       `json:"itemId"`
	BatchSize      string       `json:"batchSize"`
	ScaleFactor    string       `json:"scaleFactor"`
	CoProducts     []OutputLine `json:"coProducts"`
}

// BatchOrderRescaledData is the payload of BatchOrderRescaled.
type BatchOrderRescaledData struct {
	OrderID           string       `json:"orderId"`
	PreviousBatchSize string       `json:"previousBatchSize"`
	BatchSize         string       `json:"batchSize"`
	ScaleFactor       string       `json:"scaleFactor"`
	Version           int          `json:"version"`
	CoProducts        []OutputLine `json:"coProducts"`
}
