// Package events holds the event types and payloads this service publishes.
// Schemas are documented in events/asyncapi.yaml.
package events

import (
	"github.com/BenjaminSRussell/passage/platform/events"
	"github.com/BenjaminSRussell/passage/services/fieldservice/internal/domain"
)

// Context is the bounded-context segment of every event type.
const Context = "service"

// Event types published by the field-service slice.
var (
	CaseOpened      = events.Type(Context, "case", "opened", 1)
	WorkOrderPosted = events.Type(Context, "workorder", "posted", 1)
	// WorkOrderCompleted is dev.passage.service.workorder.completed.v1 (PRD 8.45.5): the
	// work order reached COMPLETED, by the technician's complete action or by hand.
	WorkOrderCompleted = events.Type(Context, "workorder", "completed", 1)
	SLAWarning         = events.Type(Context, "sla", "warning", 1)
	SLABreached        = events.Type(Context, "sla", "breached", 1)

	// AgreementChanged is dev.passage.service.agreement.changed.v1: an agreement
	// moved between DRAFT, ACTIVE, SUSPENDED, CANCELED and EXPIRED (FR-45.3).
	AgreementChanged = events.Type(Context, "agreement", "changed", 1)
	// AgreementVisitRecorded is dev.passage.service.agreementvisit.recorded.v1: an
	// included visit of an agreement line was consumed (FR-45.3).
	AgreementVisitRecorded = events.Type(Context, "agreementvisit", "recorded", 1)

	// AssignmentChanged is dev.passage.service.assignment.changed.v1: a work order was
	// given to a technician at a time, or taken off the schedule (FR-45.7).
	AssignmentChanged = events.Type(Context, "assignment", "changed", 1)
	// DispatchPublished is dev.passage.service.dispatch.published.v1 (PRD 8.45.5): the
	// SCHEDULED work orders of a territory-day were dispatched to their technicians.
	DispatchPublished = events.Type(Context, "dispatch", "published", 1)

	// ArticlePublished is dev.passage.knowledge.article.published.v1 (PRD 8.45.5):
	// the knowledge base is its own context in the event catalog.
	ArticlePublished = events.Type(KnowledgeContext, "article", "published", 1)

	// CaseStageChanged is dev.passage.case.stage.changed.v1 (PRD 8.45.5): stage
	// moves of cases in any category belong to the case context of the catalog.
	CaseStageChanged = events.Type(CaseContext, "stage", "changed", 1)
)

// CaseContext is the bounded-context segment of the generic case events (FR-45.12).
const CaseContext = "case"

// CaseStageChangedData is the payload of dev.passage.case.stage.changed.v1 (PRD
// 8.45.5: caseId, categoryId, from, to, at, actor). Reason is empty on a forward move.
type CaseStageChangedData struct {
	CaseID     string `json:"caseId"`
	Number     string `json:"number"`
	CategoryID string `json:"categoryId"`
	From       string `json:"from"`
	To         string `json:"to"`
	At         string `json:"at"`
	Actor      string `json:"actor"`
	Reason     string `json:"reason"`
}

// KnowledgeContext is the bounded-context segment of the knowledge-base events.
const KnowledgeContext = "knowledge"

// ArticlePublishedData is the payload of dev.passage.knowledge.article.published.v1
// (PRD 8.45.5: articleId, version, language, applicability). RetiredArticleID is
// the version this publish retired, if any, so the search index can drop it.
type ArticlePublishedData struct {
	ArticleID        string               `json:"articleId"`
	Number           string               `json:"number"`
	Version          int                  `json:"version"`
	Language         string               `json:"language"`
	Title            string               `json:"title"`
	Applicability    domain.Applicability `json:"applicability"`
	PublishedAt      string               `json:"publishedAt"`
	RetiredArticleID *string              `json:"retiredArticleId"`
}

// SLAWarningData is the payload of dev.passage.service.sla.warning.v1: a case
// clock reached the warning threshold (BR-45.2, message SVC-SLA-001). SLAType is
// RESPONSE or RESOLUTION; times are RFC 3339 UTC.
type SLAWarningData struct {
	CaseID           string `json:"caseId"`
	Number           string `json:"number"`
	SLAType          string `json:"slaType"`
	Priority         int    `json:"priority"`
	DueAt            string `json:"dueAt"`
	WarnedAt         string `json:"warnedAt"`
	ThresholdPercent int    `json:"thresholdPercent"`
}

// SLABreachedData is the payload of dev.passage.service.sla.breached.v1 (PRD
// 8.0: caseId, slaType, dueAt, breachedAt; message SVC-SLA-002). BreachedAt is
// when the service detected the breach: the monitor pass, the late response or
// the transition that found the clock past due.
type SLABreachedData struct {
	CaseID     string `json:"caseId"`
	Number     string `json:"number"`
	SLAType    string `json:"slaType"`
	Priority   int    `json:"priority"`
	DueAt      string `json:"dueAt"`
	BreachedAt string `json:"breachedAt"`
}

// CaseOpenedData is the payload of dev.passage.service.case.opened.v1.
type CaseOpenedData struct {
	CaseID          string  `json:"caseId"`
	Number          string  `json:"number"`
	CustomerPartyID string  `json:"customerId"`
	Priority        int     `json:"priority"`
	AgreementID     *string `json:"agreementId"`
	ResponseDue     string  `json:"responseDue"`
	ResolutionDue   string  `json:"resolutionDue"`
}

// WorkOrderPostedData is the payload of dev.passage.service.workorder.posted.v1.
// It carries what wave-29 invoicing needs to bill the BILLABLE lines (customer,
// legal entity, lines) and what the GL needs for warranty cost. Amounts are
// decimal strings in Currency; they are never floats.
type WorkOrderPostedData struct {
	WorkOrderID         string       `json:"workOrderId"`
	Number              string       `json:"number"`
	CustomerPartyID     string       `json:"customerId"`
	LegalEntityID       string       `json:"legalEntityId"`
	CaseID              *string      `json:"caseId"`
	AgreementID         *string      `json:"agreementId"`
	Currency            string       `json:"currency"`
	BillableTotal       string       `json:"billableTotal"`
	WarrantyCost        string       `json:"warrantyCost"`
	EntitlementConsumed string       `json:"entitlementConsumed"`
	Lines               []PostedLine `json:"lines"`
}

// PostedLine is one posted piece of a work-order line with its coverage.
type PostedLine struct {
	LineID         string `json:"lineId"`
	Kind           string `json:"kind"`
	Coverage       string `json:"coverage"`
	Qty            string `json:"qty"`
	Amount         string `json:"amount"`
	WarrantyCost   string `json:"warrantyCost"`
	AgreementHours string `json:"agreementHours"`
}

// SupplierClaimSettled is dev.passage.service.supplierclaim.settled.v1: a vendor
// credit note was matched to an accepted supplier warranty claim (FR-45.15,
// BR-45.12). The PRD writes the entity as "supplier-claim"; the kernel's event type
// rule allows no hyphen in a segment, so it is "supplierclaim".
var SupplierClaimSettled = events.Type(Context, "supplierclaim", "settled", 1)

// SupplierClaimSettledData is the payload of SupplierClaimSettled (PRD 8.45.5:
// claimId, vendorId, claimedAmount, recoveredAmount). The GL warranty recovery
// credits the warranty expense account of LegalEntityID with RecoveredAmount.
// Amounts are decimal strings in Currency.
type SupplierClaimSettledData struct {
	ClaimID         string `json:"claimId"`
	Number          string `json:"number"`
	VendorID        string `json:"vendorId"`
	LegalEntityID   string `json:"legalEntityId"`
	Currency        string `json:"currency"`
	ClaimedAmount   string `json:"claimedAmount"`
	RecoveredAmount string `json:"recoveredAmount"`
	CreditNoteID    string `json:"creditNoteId"`
	VendorReference string `json:"vendorReference"`
	SettledAt       string `json:"settledAt"`
}

// AgreementChangedData is the payload of AgreementChanged. Reason is empty when
// the caller gave none (an activation, or the expiry pass, which writes "validTo passed").
type AgreementChangedData struct {
	AgreementID     string  `json:"agreementId"`
	Number          string  `json:"number"`
	CustomerPartyID string  `json:"customerId"`
	LegalEntityID   string  `json:"legalEntityId"`
	BillingModel    string  `json:"billingModel"`
	From            string  `json:"from"`
	To              string  `json:"to"`
	Reason          string  `json:"reason"`
	ValidTo         *string `json:"validTo"`
	At              string  `json:"at"`
	Actor           string  `json:"actor"`
}

// AgreementVisitRecordedData is the payload of AgreementVisitRecorded. Wave 29
// bills PER_VISIT agreements from it; VisitsLeft is what remains on the line.
type AgreementVisitRecordedData struct {
	VisitID         string  `json:"visitId"`
	AgreementID     string  `json:"agreementId"`
	Number          string  `json:"number"`
	CustomerPartyID string  `json:"customerId"`
	LegalEntityID   string  `json:"legalEntityId"`
	BillingModel    string  `json:"billingModel"`
	LineIndex       int     `json:"lineIndex"`
	Kind            string  `json:"kind"`
	VisitDate       string  `json:"visitDate"`
	WorkOrderID     *string `json:"workOrderId"`
	VisitsLeft      int     `json:"visitsLeft"`
}

// AssignmentChangedData is the payload of AssignmentChanged. Action is ASSIGNED (also
// a move to another technician or time) or UNASSIGNED; the technician and times of an
// UNASSIGNED event are the ones the order lost. PreviousTechnicianID is set when an
// assignment replaced another technician's.
type AssignmentChangedData struct {
	WorkOrderID          string  `json:"workOrderId"`
	Number               string  `json:"number"`
	LegalEntityID        string  `json:"legalEntityId"`
	TerritoryID          string  `json:"territoryId"`
	Action               string  `json:"action"`
	TechnicianID         *string `json:"technicianId"`
	PreviousTechnicianID *string `json:"previousTechnicianId"`
	Start                *string `json:"start"`
	End                  *string `json:"end"`
	Pinned               bool    `json:"pinned"`
	At                   string  `json:"at"`
	Actor                string  `json:"actor"`
}

// DispatchedOrder is one work order of a published territory-day, in the technician's
// route order.
type DispatchedOrder struct {
	WorkOrderID  string `json:"workOrderId"`
	Number       string `json:"number"`
	TechnicianID string `json:"technicianId"`
	Sequence     int    `json:"sequence"`
	Start        string `json:"start"`
	End          string `json:"end"`
}

// DispatchPublishedData is the payload of DispatchPublished: the technicians' mobile
// apps and customer notifications consume it.
type DispatchPublishedData struct {
	RunID         string            `json:"runId"`
	LegalEntityID string            `json:"legalEntityId"`
	TerritoryID   string            `json:"territoryId"`
	Date          string            `json:"date"`
	TimeZone      string            `json:"timeZone"`
	PublishedAt   string            `json:"publishedAt"`
	Actor         string            `json:"actor"`
	Assignments   []DispatchedOrder `json:"assignments"`
}

// WorkOrderCompletedData is the payload of dev.passage.service.workorder.completed.v1
// (PRD 8.45.5: workOrderId, technicianId, lines[], firstTimeFix, durationMinutes).
// Quantities are decimal strings. Inventory issues the PART lines' ConsumedQty from the
// technician's truck; DurationMinutes is the time IN_PROGRESS by device time (0 when
// the work order was completed by hand without an action log).
type WorkOrderCompletedData struct {
	WorkOrderID     string  `json:"workOrderId"`
	Number          string  `json:"number"`
	CustomerPartyID string  `json:"customerId"`
	LegalEntityID   string  `json:"legalEntityId"`
	CaseID          *string `json:"caseId"`
	TechnicianID    *string `json:"technicianId"`
	FirstTimeFix    bool    `json:"firstTimeFix"`
	DurationMinutes int     `json:"durationMinutes"`
	CompletedAt     string  `json:"completedAt"`
	// Truck is where the technician's truck stock sits in inventory; absent when the work
	// order has no technician or the technician has no truck (BR-45.7).
	Truck *CompletedTruck `json:"truck"`
	Lines []CompletedLine `json:"lines"`
}

// CompletedTruck addresses the truck's stock by the inventory dimensions site, warehouse
// and (optionally) location.
type CompletedTruck struct {
	SiteID      string  `json:"siteId"`
	WarehouseID string  `json:"warehouseId"`
	LocationID  *string `json:"locationId"`
}

// CompletedLine is one work-order line at completion; ConsumedQty is what the technician
// reported using (PART lines), "0" for the other kinds.
type CompletedLine struct {
	LineID      string `json:"lineId"`
	Kind        string `json:"kind"`
	Qty         string `json:"qty"`
	ConsumedQty string `json:"consumedQty"`
	// ProductID and Unit name the stock item of a PART line and the unit ConsumedQty is in;
	// empty for a line that moves no stock.
	ProductID string `json:"productId"`
	Unit      string `json:"unit"`
}
