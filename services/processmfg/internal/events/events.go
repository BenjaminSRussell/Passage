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
	// BatchOrderStateChanged covers release, start, report-as-finished and end.
	BatchOrderStateChanged = "dev.passage.production.batchorder.statechanged.v1"
	// CoProductAllocated is written when an order ends (PRD 4.24.5 co-product.allocated).
	CoProductAllocated = "dev.passage.production.coproduct.allocated.v1"
	// BatchAttributeDefined is written when an item gets a batch attribute (FR-24.5).
	BatchAttributeDefined = "dev.passage.production.batchattribute.defined.v1"
	// BatchRegistered is written when a batch gets its quality record.
	BatchRegistered = "dev.passage.production.batch.registered.v1"
	// BatchAttributesRecorded is the PRD's batch.attributes-recorded (no hyphen).
	BatchAttributesRecorded = "dev.passage.production.batch.attributesrecorded.v1"
	// BatchDispositionChanged is written when a batch's disposition changes (AC-24.10).
	BatchDispositionChanged = "dev.passage.production.batch.dispositionchanged.v1"
	// BatchMerged is written when stored batches are merged into a new one (FR-24.16).
	BatchMerged = "dev.passage.production.batch.merged.v1"
)

// All lists every event type this service publishes.
func All() []string {
	return []string{FormulaCreated, BatchOrderCreated, BatchOrderRescaled, BatchOrderStateChanged, CoProductAllocated,
		BatchAttributeDefined, BatchRegistered, BatchAttributesRecorded, BatchDispositionChanged, BatchMerged}
}

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

// ReportedLine is the quantity reported as finished of one output line.
type ReportedLine struct {
	LineNumber int    `json:"lineNumber"`
	ItemID     string `json:"itemId"`
	Type       string `json:"type"`
	RafQty     string `json:"rafQty"`
}

// BatchOrderStateChangedData is the payload of BatchOrderStateChanged. ActualQty
// and Outputs are set when the order is reported as finished, BatchCost,
// Currency and Method when it ends.
type BatchOrderStateChangedData struct {
	OrderID       string         `json:"orderId"`
	OrderNumber   string         `json:"orderNumber"`
	LegalEntityID string         `json:"legalEntityId"`
	ItemID        string         `json:"itemId"`
	From          string         `json:"from"`
	To            string         `json:"to"`
	Version       int            `json:"version"`
	ActualQty     string         `json:"actualQty,omitempty"`
	Outputs       []ReportedLine `json:"outputs,omitempty"`
	BatchCost     string         `json:"batchCost,omitempty"`
	Currency      string         `json:"currency,omitempty"`
	Method        string         `json:"method,omitempty"`
}

// AllocationShare is one recipient of the batch cost.
type AllocationShare struct {
	Role       string `json:"role"`
	LineNumber int    `json:"lineNumber"`
	ItemID     string `json:"itemId"`
	RafQty     string `json:"rafQty"`
	Amount     string `json:"amount"`
	Percent    string `json:"percent"`
}

// CoProductAllocatedData is the payload of CoProductAllocated: the shares add
// up to BatchCost exactly.
type CoProductAllocatedData struct {
	OrderID       string            `json:"orderId"`
	OrderNumber   string            `json:"orderNumber"`
	LegalEntityID string            `json:"legalEntityId"`
	Method        string            `json:"method"`
	Currency      string            `json:"currency"`
	BatchCost     string            `json:"batchCost"`
	Shares        []AllocationShare `json:"shares"`
}

// BatchAttributeDefinedData is the payload of BatchAttributeDefined.
type BatchAttributeDefinedData struct {
	DefinitionID string   `json:"definitionId"`
	ItemID       string   `json:"itemId"`
	Name         string   `json:"name"`
	Kind         string   `json:"kind"`
	Role         string   `json:"role"`
	Target       string   `json:"target,omitempty"`
	Min          string   `json:"min,omitempty"`
	Max          string   `json:"max,omitempty"`
	Options      []string `json:"options,omitempty"`
}

// BatchRegisteredData is the payload of BatchRegistered.
type BatchRegisteredData struct {
	BatchID       string `json:"batchId"`
	LegalEntityID string `json:"legalEntityId"`
	ItemID        string `json:"itemId"`
	BatchNumber   string `json:"batchNumber"`
	BatchOrderID  string `json:"batchOrderId,omitempty"`
	Disposition   string `json:"disposition"`
}

// RecordedAttribute is one value in BatchAttributesRecordedData. Value is the
// number, text or yyyy-mm-dd date as a string; Pass is false for a numeric
// value outside its limits (an EBR exception, BR-24.15).
type RecordedAttribute struct {
	Name   string `json:"name"`
	Kind   string `json:"kind"`
	Value  string `json:"value"`
	Pass   bool   `json:"pass"`
	Detail string `json:"detail,omitempty"`
}

// BatchAttributesRecordedData is the payload of BatchAttributesRecorded.
type BatchAttributesRecordedData struct {
	BatchID       string `json:"batchId"`
	LegalEntityID string `json:"legalEntityId"`
	ItemID        string `json:"itemId"`
	BatchNumber   string `json:"batchNumber"`
	Version       int    `json:"version"`
	ExpiryDate    string `json:"expiryDate,omitempty"`
	// Potency is the value recorded for the item's potency-role attribute,
	// when this recording carries one (PRD 4.24.5).
	Potency    string              `json:"potency,omitempty"`
	Attributes []RecordedAttribute `json:"attributes"`
	Failures   int                 `json:"failures"`
}

// BatchDispositionChangedData is the payload of BatchDispositionChanged.
type BatchDispositionChangedData struct {
	BatchID       string `json:"batchId"`
	LegalEntityID string `json:"legalEntityId"`
	ItemID        string `json:"itemId"`
	BatchNumber   string `json:"batchNumber"`
	From          string `json:"from"`
	To            string `json:"to"`
	Reason        string `json:"reason"`
	// SegregationConflict: the same user recorded attributes on this batch (PRD 4.24.17).
	SegregationConflict bool `json:"segregationConflict"`
	Version             int  `json:"version"`
}

// MergedParent is one source batch of a merge.
type MergedParent struct {
	BatchID     string `json:"batchId"`
	BatchNumber string `json:"batchNumber"`
	Qty         string `json:"qty"`
	UnitCost    string `json:"unitCost"`
}

// BatchMergedData is the payload of BatchMerged: the new batch, its quantity
// and weighted-average cost per unit, the warehouse and catch-weight unit the
// parents share (where inventory posts the transfers), and the parents.
type BatchMergedData struct {
	BatchID       string         `json:"batchId"`
	LegalEntityID string         `json:"legalEntityId"`
	ItemID        string         `json:"itemId"`
	BatchNumber   string         `json:"batchNumber"`
	Disposition   string         `json:"disposition"`
	WarehouseID   string         `json:"warehouseId"`
	CWUnit        string         `json:"cwUnit,omitempty"`
	ExpiryDate    string         `json:"expiryDate,omitempty"`
	Qty           string         `json:"qty"`
	UnitCost      string         `json:"unitCost"`
	Parents       []MergedParent `json:"parents"`
}
