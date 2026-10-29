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
	SLAWarning      = events.Type(Context, "sla", "warning", 1)
	SLABreached     = events.Type(Context, "sla", "breached", 1)

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
