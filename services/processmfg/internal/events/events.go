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
	// BatchRequirementDefined and BatchRequirementRetired cover the customer
	// batch requirements (FR-24.19): sales and picking read the rules from them.
	BatchRequirementDefined = "dev.passage.production.batchrequirement.defined.v1"
	BatchRequirementRetired = "dev.passage.production.batchrequirement.retired.v1"
	// InheritanceRuleDefined and InheritanceRuleRetired cover the attribute
	// inheritance rules (FR-24.15); BatchDerived is written when a batch's
	// attributes are derived from its senders.
	InheritanceRuleDefined = "dev.passage.production.inheritancerule.defined.v1"
	InheritanceRuleRetired = "dev.passage.production.inheritancerule.retired.v1"
	BatchDerived           = "dev.passage.production.batch.derived.v1"
	// The electronic batch record (FR-24.21): its profile, compilation, review
	// by exception, approval and seal.
	BatchRecordProfileDefined    = "dev.passage.production.batchrecordprofile.defined.v1"
	BatchRecordProfileRetired    = "dev.passage.production.batchrecordprofile.retired.v1"
	BatchRecordCompiled          = "dev.passage.production.batchrecord.compiled.v1"
	BatchRecordExceptionRaised   = "dev.passage.production.batchrecord.exceptionraised.v1"
	BatchRecordExceptionDisposed = "dev.passage.production.batchrecord.exceptiondispositioned.v1"
	BatchRecordApproved          = "dev.passage.production.batchrecord.approved.v1"
	BatchRecordSealed            = "dev.passage.production.batchrecord.sealed.v1"
)

// All lists every event type this service publishes.
func All() []string {
	return []string{FormulaCreated, BatchOrderCreated, BatchOrderRescaled, BatchOrderStateChanged, CoProductAllocated,
		BatchAttributeDefined, BatchRegistered, BatchAttributesRecorded, BatchDispositionChanged, BatchMerged,
		BatchRequirementDefined, BatchRequirementRetired, BatchRecordProfileDefined, BatchRecordProfileRetired,
		BatchRecordCompiled, BatchRecordExceptionRaised, BatchRecordExceptionDisposed, BatchRecordApproved, BatchRecordSealed,
		InheritanceRuleDefined, InheritanceRuleRetired, BatchDerived}
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

// BatchRequirementData is the payload of BatchRequirementDefined and
// BatchRequirementRetired. Level is 3 for a customer, 2 for a customer group
// and 1 for the item default (BR-24.13).
type BatchRequirementData struct {
	RequirementID   string `json:"requirementId"`
	LegalEntityID   string `json:"legalEntityId"`
	ItemID          string `json:"itemId"`
	CustomerID      string `json:"customerId,omitempty"`
	CustomerGroupID string `json:"customerGroupId,omitempty"`
	Level           int    `json:"level"`
	SellableDays    *int   `json:"sellableDays,omitempty"`
	Attribute       string `json:"attribute,omitempty"`
	Min             string `json:"min,omitempty"`
	Max             string `json:"max,omitempty"`
	SameBatch       *bool  `json:"sameBatch,omitempty"`
	ValidFrom       string `json:"validFrom,omitempty"`
	ValidTo         string `json:"validTo,omitempty"`
	Version         int    `json:"version"`
}

// BatchRecordProfileData is the payload of BatchRecordProfileDefined and
// BatchRecordProfileRetired.
type BatchRecordProfileData struct {
	ProfileID                  string `json:"profileId"`
	LegalEntityID              string `json:"legalEntityId"`
	Code                       string `json:"code"`
	ItemID                     string `json:"itemId"`
	ApprovalRequiredForRelease bool   `json:"approvalRequiredForRelease"`
	Version                    int    `json:"version"`
}

// BatchRecordData is the payload of the record events (compiled, approved,
// sealed). ContentHash is the hex SHA-256 of the sealed document and is set on
// BatchRecordSealed.
type BatchRecordData struct {
	RecordID       string `json:"recordId"`
	LegalEntityID  string `json:"legalEntityId"`
	BatchOrderID   string `json:"batchOrderId"`
	BatchID        string `json:"batchId"`
	BatchNumber    string `json:"batchNumber"`
	ItemID         string `json:"itemId"`
	Status         string `json:"status"`
	Version        int    `json:"version"`
	Exceptions     int    `json:"exceptions"`
	OpenExceptions int    `json:"openExceptions"`
	ContentHash    string `json:"contentHash,omitempty"`
}

// BatchRecordExceptionData is the payload of BatchRecordExceptionRaised and
// BatchRecordExceptionDisposed. Disposition and SignatureID are set on the
// latter.
type BatchRecordExceptionData struct {
	RecordID      string `json:"recordId"`
	ExceptionID   string `json:"exceptionId"`
	LegalEntityID string `json:"legalEntityId"`
	BatchID       string `json:"batchId"`
	SourceType    string `json:"sourceType"`
	Severity      string `json:"severity"`
	Disposition   string `json:"disposition,omitempty"`
	SignatureID   string `json:"signatureId,omitempty"`
}

// InheritanceRuleData is the payload of InheritanceRuleDefined and
// InheritanceRuleRetired. Fixed carries the fixed value of a fixed rule.
type InheritanceRuleData struct {
	RuleID        string `json:"ruleId"`
	LegalEntityID string `json:"legalEntityId"`
	ItemID        string `json:"itemId"`
	Attribute     string `json:"attribute"`
	Aggregation   string `json:"aggregation"`
	Decimals      *int   `json:"decimals,omitempty"`
	Fixed         string `json:"fixed,omitempty"`
	Version       int    `json:"version"`
}

// DerivedAttribute is one attribute a derivation filled. Recorded is false when
// no sender had a value, so the receiver attribute was left as it was.
type DerivedAttribute struct {
	Attribute   string   `json:"attribute"`
	Aggregation string   `json:"aggregation"`
	Recorded    bool     `json:"recorded"`
	Value       string   `json:"value,omitempty"`
	Missing     []string `json:"missing"`
}

// BatchDerivedData is the payload of BatchDerived: the receiver batch, its
// version after the derivation, the sender batches and what was derived.
type BatchDerivedData struct {
	BatchID       string             `json:"batchId"`
	LegalEntityID string             `json:"legalEntityId"`
	ItemID        string             `json:"itemId"`
	BatchNumber   string             `json:"batchNumber"`
	Version       int                `json:"version"`
	Senders       []string           `json:"senders"`
	Derived       []DerivedAttribute `json:"derived"`
}
