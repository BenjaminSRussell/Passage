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
	AgreementReleased    = events.Type(Context, "agreement", "released", 1)
	QuotationConverted   = events.Type(Context, "quotation", "converted", 1)
	QuotationExpired     = events.Type(Context, "quotation", "expired", 1)
	AllocationQuotaSet   = events.Type(Context, "allocationquota", "created", 1)
	AllocationQuotaMoved = events.Type(Context, "allocationquota", "changed", 1)
	AllocationConsumed   = events.Type(Context, "allocation", "consumed", 1)
	AllocationReturned   = events.Type(Context, "allocation", "returned", 1)
	ATPPromised          = events.Type(Context, "atp", "promised", 1)

	ScheduleReleaseReceived     = events.Type(Context, "schedulerelease", "received", 1)
	ScheduleReleaseResolved     = events.Type(Context, "schedulerelease", "resolved", 1)
	ScheduleReleaseRecalculated = events.Type(Context, "schedulerelease", "recalculated", 1)

	BackorderRunCommitted = events.Type(Context, "backorderrun", "committed", 1)
	ParametersChanged     = events.Type(Context, "parameters", "changed", 1)

	OrderChangeRequested = events.Type(Context, "orderchange", "requested", 1)
	OrderChangeApproved  = events.Type(Context, "orderchange", "approved", 1)
	OrderChangeRejected  = events.Type(Context, "orderchange", "rejected", 1)
	ChangePolicyChanged  = events.Type(Context, "changepolicy", "changed", 1)
)

// All lists every event type this service publishes.
func All() []string {
	return []string{OrderConfirmed, OrderHoldApplied, OrderHoldReleased, CreditCheckEvaluated, OrderChanged, OrderCancelled, AgreementReleased,
		QuotationConverted, QuotationExpired, AllocationQuotaSet, AllocationQuotaMoved, AllocationConsumed, AllocationReturned, ATPPromised,
		ScheduleReleaseReceived, ScheduleReleaseResolved, ScheduleReleaseRecalculated, BackorderRunCommitted, ParametersChanged,
		OrderChangeRequested, OrderChangeApproved, OrderChangeRejected, ChangePolicyChanged}
}

// PromisedSlice is one schedule line of an ATP promise.
type PromisedSlice struct {
	Date       string `json:"date,omitempty"`
	BucketDate string `json:"bucket_date,omitempty"`
	Quantity   string `json:"quantity"`
	Status     string `json:"status"`
}

// ATPPromisedData is the data member of atp.promised.v1: an order line was
// promised against cumulative ATP and split into schedule lines (FR-27.6).
// requested_ship_date is the line's requested date; promised_date (the
// confirmed date) is the date of the first dated slice; source is ATP, or
// ALLOCATION when the line's quota period held the promise back; each slice
// names the ATP bucket it drew on; backorder_qty is the quantity promised
// after the requested date or not at all.
type ATPPromisedData struct {
	OrderID           string          `json:"order_id"`
	LineID            string          `json:"line_id"`
	LineNo            int             `json:"line_no"`
	ProductID         string          `json:"product_id"`
	SiteID            string          `json:"site_id"`
	WarehouseID       string          `json:"warehouse_id"`
	Quantity          string          `json:"quantity"`
	RequestedShipDate string          `json:"requested_ship_date"`
	Source            string          `json:"source"`
	Complete          bool            `json:"complete"`
	PromisedDate      string          `json:"promised_date,omitempty"`
	BackorderQty      string          `json:"backorder_qty"`
	Schedule          []PromisedSlice `json:"schedule"`
	Actor             string          `json:"actor"`
	Version           int             `json:"version"`
}

// AllocationQuotaData is the data member of allocationquota.created.v1 and
// allocationquota.changed.v1 (FR-27.16). previous_qty is empty on created.
type AllocationQuotaData struct {
	QuotaID     string  `json:"quota_id"`
	ProductID   string  `json:"product_id"`
	CustomerID  *string `json:"customer_id,omitempty"`
	PeriodStart string  `json:"period_start"`
	PeriodEnd   string  `json:"period_end"`
	QuotaQty    string  `json:"quota_qty"`
	PreviousQty string  `json:"previous_qty,omitempty"`
	Consumed    string  `json:"consumed_qty"`
	Actor       string  `json:"actor"`
	Version     int     `json:"version"`
}

// AllocationData is the data member of allocation.consumed.v1 and
// allocation.returned.v1: one order line drew quantity from a quota at
// confirmation, or gave it back (BR-27.15). confirmed_ship_date is set on
// consumed and is the first day of the period when the line moved to a later
// one (source ALLOCATION).
type AllocationData struct {
	QuotaID           string `json:"quota_id"`
	OrderID           string `json:"order_id"`
	LineID            string `json:"line_id"`
	ProductID         string `json:"product_id"`
	Quantity          string `json:"quantity"`
	Consumed          string `json:"consumed_qty"`
	Remaining         string `json:"remaining_qty"`
	ConfirmedShipDate string `json:"confirmed_ship_date,omitempty"`
	Source            string `json:"source,omitempty"`
}

// QuotationConvertedData is the data member of quotation.converted.v1: the
// lines of one quotation (by quotation line number) that became the lines of
// a new DRAFT sales order (FR-27.1).
type QuotationConvertedData struct {
	QuotationID     string `json:"quotation_id"`
	QuotationNumber string `json:"quotation_number"`
	CustomerID      string `json:"customer_id"`
	OrderID         string `json:"order_id"`
	OrderNumber     string `json:"order_number"`
	Lines           []int  `json:"lines"`
	Actor           string `json:"actor"`
	Version         int    `json:"version"`
}

// QuotationExpiredData is the data member of quotation.expired.v1: a DRAFT or
// SENT quotation whose validity had passed was moved to EXPIRED by the expiry
// run (FR-27.1). net_total is a decimal string in currency.
type QuotationExpiredData struct {
	QuotationID     string `json:"quotation_id"`
	QuotationNumber string `json:"quotation_number"`
	LegalEntityID   string `json:"legal_entity_id"`
	CustomerID      string `json:"customer_id"`
	ValidUntil      string `json:"valid_until"`
	PreviousStatus  string `json:"previous_status"`
	ProbabilityPct  int    `json:"probability_pct"`
	NetTotal        string `json:"net_total"`
	Currency        string `json:"currency"`
	Actor           string `json:"actor"`
	Version         int    `json:"version"`
}

// AgreementReleasedData is the data member of agreement.released.v1: one
// release drew quantity against the agreement commitment and created the DRAFT
// order sales_order_id (FR-27.10).
type AgreementReleasedData struct {
	AgreementID     string `json:"agreement_id"`
	AgreementNumber string `json:"agreement_number"`
	CustomerID      string `json:"customer_id"`
	ProductID       string `json:"product_id"`
	SalesOrderID    string `json:"sales_order_id"`
	Quantity        string `json:"quantity"`
	Released        string `json:"released_qty"`
	Remaining       string `json:"remaining_qty"`
	Status          string `json:"status"`
	Version         int    `json:"version"`
	Actor           string `json:"actor"`
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
	// ConfigurationID is the saved configuration of a configured line; absent otherwise.
	ConfigurationID *string `json:"configuration_id,omitempty"`
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

// ScheduleReleaseLine is one open schedule line of a scheduling-agreement release.
type ScheduleReleaseLine struct {
	Date     string `json:"date"`
	Quantity string `json:"quantity"`
}

// ScheduleReleaseData is the data member of schedulerelease.received.v1,
// .resolved.v1 and .recalculated.v1 (FR-27.20, BR-27.12). status is ACCEPTED
// (the open lines replaced the previous release's), HELD (CUM_QTY_MISMATCH,
// nothing changed) or REJECTED; decision and reason are set on resolved only;
// superseded_release_id names the release whose open lines were replaced.
type ScheduleReleaseData struct {
	ReleaseID           string                `json:"release_id"`
	ReleaseNo           string                `json:"release_no"`
	ReleaseType         string                `json:"release_type"`
	LegalEntityID       string                `json:"legal_entity_id"`
	AgreementID         string                `json:"agreement_id"`
	AgreementNumber     string                `json:"agreement_number"`
	CustomerID          string                `json:"customer_id"`
	ProductID           string                `json:"product_id"`
	Status              string                `json:"status"`
	CumReceived         string                `json:"cum_received"`
	CumShipped          string                `json:"cum_shipped"`
	Difference          string                `json:"difference"`
	OpenQty             string                `json:"open_qty"`
	Lines               []ScheduleReleaseLine `json:"lines"`
	SupersededReleaseID string                `json:"superseded_release_id,omitempty"`
	Decision            string                `json:"decision,omitempty"`
	Reason              string                `json:"reason,omitempty"`
	Actor               string                `json:"actor"`
}

// BackorderRunLineData is one changed line of a committed backorder run.
type BackorderRunLineData struct {
	OrderID     string `json:"order_id"`
	LineID      string `json:"line_id"`
	LineNo      int    `json:"line_no"`
	Before      string `json:"before_qty"`
	After       string `json:"after_qty"`
	Deconfirmed bool   `json:"deconfirmed"`
}

// BackorderRunCommittedData is the data member of backorderrun.committed.v1: a
// backorder run was applied to the schedule lines of its changed lines
// (FR-27.17). atp.promised.v1 is written per changed line in the same
// transaction.
type BackorderRunCommittedData struct {
	RunID            string                 `json:"run_id"`
	LegalEntityID    string                 `json:"legal_entity_id"`
	ProductID        string                 `json:"product_id"`
	SiteID           string                 `json:"site_id"`
	WarehouseID      string                 `json:"warehouse_id"`
	Strategy         string                 `json:"strategy"`
	Supply           string                 `json:"supply"`
	Allocated        string                 `json:"allocated"`
	DeconfirmedCount int                    `json:"deconfirmed_count"`
	Lines            []BackorderRunLineData `json:"lines"`
	Actor            string                 `json:"actor"`
	Version          int                    `json:"version"`
}

// ParametersChangedData is the data member of parameters.changed.v1: the
// sales parameters of one legal entity were created or replaced.
type ParametersChangedData struct {
	LegalEntityID               string `json:"legal_entity_id"`
	QuotationExpiryDays         int    `json:"quotation_expiry_days"`
	QuotationProbabilityDefault int    `json:"quotation_probability_default"`
	CreditReleaseValidityDays   int    `json:"credit_release_validity_days"`
	CreditHoldSLABusinessDays   int    `json:"credit_hold_sla_business_days"`
	DefaultOrderType            string `json:"default_order_type"`
	Actor                       string `json:"actor"`
	Version                     int    `json:"version"`
}

// OrderChangeRequestedData is the data member of orderchange.requested.v1: a
// change of a confirmed order was stored for approval (FR-27.9).
type OrderChangeRequestedData struct {
	RequestID     string        `json:"request_id"`
	LegalEntityID string        `json:"legal_entity_id"`
	OrderID       string        `json:"order_id"`
	CustomerID    string        `json:"customer_id"`
	BaseVersion   int           `json:"base_version"`
	Reason        string        `json:"reason"`
	Actor         string        `json:"actor"`
	Diff          []FieldChange `json:"diff"`
}

// OrderChangeDecidedData is the data member of orderchange.approved.v1 and
// orderchange.rejected.v1. applied_version is the order version an approval
// produced and is absent on a rejection.
type OrderChangeDecidedData struct {
	RequestID      string `json:"request_id"`
	LegalEntityID  string `json:"legal_entity_id"`
	OrderID        string `json:"order_id"`
	RequestedBy    string `json:"requested_by"`
	Actor          string `json:"actor"`
	Comment        string `json:"comment"`
	AppliedVersion *int   `json:"applied_version,omitempty"`
}

// ChangePolicyChangedData is the data member of changepolicy.changed.v1.
type ChangePolicyChangedData struct {
	LegalEntityID    string `json:"legal_entity_id"`
	CustomerID       string `json:"customer_id"`
	ApprovalRequired bool   `json:"approval_required"`
	Actor            string `json:"actor"`
	Version          int    `json:"version"`
}
