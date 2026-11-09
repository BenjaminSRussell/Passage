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
	OrderHoldRejected    = events.Type(Context, "orderhold", "rejected", 1)
	CreditCheckEvaluated = events.Type(Context, "creditcheck", "evaluated", 1)
	OrderChanged         = events.Type(Context, "order", "changed", 1)
	OrderCancelled       = events.Type(Context, "order", "cancelled", 1)
	AgreementReleased    = events.Type(Context, "agreement", "released", 1)
	AgreementChanged     = events.Type(Context, "agreement", "changed", 1)
	AgreementClosed      = events.Type(Context, "agreement", "closed", 1)
	AgreementReturned    = events.Type(Context, "agreement", "returned", 1)
	AgreementDrawn       = events.Type(Context, "agreement", "drawn", 1)
	AgreementRepriced    = events.Type(Context, "agreement", "repriced", 1)
	QuotationConverted   = events.Type(Context, "quotation", "converted", 1)
	QuotationExpired     = events.Type(Context, "quotation", "expired", 1)
	QuotationChanged     = events.Type(Context, "quotation", "changed", 1)
	QuotationCancelled   = events.Type(Context, "quotation", "cancelled", 1)
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
	IntakeErrorRecorded   = events.Type(Context, "intakeerror", "recorded", 1)
	IntakeErrorResolved   = events.Type(Context, "intakeerror", "resolved", 1)

	OrderChangeRequested = events.Type(Context, "orderchange", "requested", 1)
	OrderChangeApproved  = events.Type(Context, "orderchange", "approved", 1)
	OrderChangeRejected  = events.Type(Context, "orderchange", "rejected", 1)
	OrderChangeWithdrawn = events.Type(Context, "orderchange", "withdrawn", 1)
	ChangePolicyChanged  = events.Type(Context, "changepolicy", "changed", 1)

	DeliveryTermsCreated = events.Type(Context, "deliveryterms", "created", 1)
	DeliveryTermsChanged = events.Type(Context, "deliveryterms", "changed", 1)

	DeliveryToleranceCreated = events.Type(Context, "deliverytolerance", "created", 1)
	DeliveryToleranceChanged = events.Type(Context, "deliverytolerance", "changed", 1)
	DeliveryToleranceRemoved = events.Type(Context, "deliverytolerance", "removed", 1)

	OrderProgressRecorded = events.Type(Context, "orderprogress", "recorded", 1)

	OrderRemainderCancelled = events.Type(Context, "order", "remaindercancelled", 1)
	OrderClosed             = events.Type(Context, "order", "closed", 1)

	ReturnOrderAuthorized    = events.Type(Context, "returnorder", "authorized", 1)
	ReturnOrderReceived      = events.Type(Context, "returnorder", "received", 1)
	ReturnOrderInspected     = events.Type(Context, "returnorder", "inspected", 1)
	ReturnOrderDispositioned = events.Type(Context, "returnorder", "dispositioned", 1)
	ReturnOrderCredited      = events.Type(Context, "returnorder", "credited", 1)
)

// All lists every event type this service publishes.
func All() []string {
	return []string{OrderConfirmed, OrderHoldApplied, OrderHoldReleased, OrderHoldRejected, CreditCheckEvaluated, OrderChanged, OrderCancelled, AgreementReleased,
		AgreementChanged, AgreementClosed, AgreementReturned, AgreementDrawn, AgreementRepriced,
		QuotationConverted, QuotationExpired, QuotationChanged, QuotationCancelled, AllocationQuotaSet, AllocationQuotaMoved, AllocationConsumed, AllocationReturned, ATPPromised,
		ScheduleReleaseReceived, ScheduleReleaseResolved, ScheduleReleaseRecalculated, BackorderRunCommitted, ParametersChanged, IntakeErrorRecorded, IntakeErrorResolved,
		OrderChangeRequested, OrderChangeApproved, OrderChangeRejected, OrderChangeWithdrawn, ChangePolicyChanged,
		DeliveryTermsCreated, DeliveryTermsChanged, DeliveryToleranceCreated, DeliveryToleranceChanged, DeliveryToleranceRemoved, OrderProgressRecorded, OrderRemainderCancelled, OrderClosed,
		ReturnOrderAuthorized, ReturnOrderReceived, ReturnOrderInspected, ReturnOrderDispositioned, ReturnOrderCredited}
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

// QuotationChangedData is the data member of quotation.changed.v1: a DRAFT or
// SENT quotation took a new validity, probability or line set (FR-27.1).
// changed names what the call changed (valid_until, probability_pct, lines);
// net_total is a decimal string in currency.
type QuotationChangedData struct {
	QuotationID     string   `json:"quotation_id"`
	QuotationNumber string   `json:"quotation_number"`
	LegalEntityID   string   `json:"legal_entity_id"`
	CustomerID      string   `json:"customer_id"`
	Status          string   `json:"status"`
	Changed         []string `json:"changed"`
	ValidUntil      string   `json:"valid_until"`
	ProbabilityPct  int      `json:"probability_pct"`
	LineCount       int      `json:"line_count"`
	NetTotal        string   `json:"net_total"`
	Currency        string   `json:"currency"`
	Actor           string   `json:"actor"`
	Version         int      `json:"version"`
}

// QuotationCancelledData is the data member of quotation.cancelled.v1: a DRAFT
// or SENT quotation was withdrawn by a user, with the reason (FR-27.1).
type QuotationCancelledData struct {
	QuotationID     string `json:"quotation_id"`
	QuotationNumber string `json:"quotation_number"`
	LegalEntityID   string `json:"legal_entity_id"`
	CustomerID      string `json:"customer_id"`
	PreviousStatus  string `json:"previous_status"`
	Reason          string `json:"reason"`
	NetTotal        string `json:"net_total"`
	Currency        string `json:"currency"`
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
	// CommitmentType is QUANTITY or AMOUNT; DrawnUnits is what the release drew
	// from the commitment in its unit (the quantity, or the line net value).
	CommitmentType string `json:"commitment_type"`
	DrawnUnits     string `json:"drawn_units"`
	Released       string `json:"released_qty"`
	Remaining      string `json:"remaining_qty"`
	Status         string `json:"status"`
	Version        int    `json:"version"`
	Actor          string `json:"actor"`
}

// AgreementLifecycleData is the data member of agreement.changed.v1,
// .closed.v1, .returned.v1 and .drawn.v1 (FR-27.10). diff lists the changed
// fields of a revision (changed) or the released quantity (returned, drawn);
// reason is the close reason, the cancellation or line change that returned
// quantity, or the raise that drew it; sales_order_id and returned_qty are set
// on returned, sales_order_id and drawn_qty on drawn. remaining_qty is what can
// still be released: the commitment with overdelivery_pct, less released_qty.
type AgreementLifecycleData struct {
	AgreementID     string `json:"agreement_id"`
	AgreementNumber string `json:"agreement_number"`
	LegalEntityID   string `json:"legal_entity_id"`
	CustomerID      string `json:"customer_id"`
	ProductID       string `json:"product_id"`
	// CommitmentType is QUANTITY or AMOUNT; the commitment, released, remaining,
	// returned and drawn figures are in that unit (quantity or currency amount).
	CommitmentType  string        `json:"commitment_type"`
	Commitment      string        `json:"commitment_qty"`
	OverdeliveryPct string        `json:"overdelivery_pct"`
	Released        string        `json:"released_qty"`
	Remaining       string        `json:"remaining_qty"`
	UnitPrice       string        `json:"unit_price"`
	Status          string        `json:"status"`
	Diff            []FieldChange `json:"diff"`
	Reason          string        `json:"reason,omitempty"`
	SalesOrderID    string        `json:"sales_order_id,omitempty"`
	ReturnedQty     string        `json:"returned_qty,omitempty"`
	DrawnQty        string        `json:"drawn_qty,omitempty"`
	// RepricedOrders is set on repriced only: the released orders whose release
	// line took the agreement price.
	RepricedOrders []RepricedOrder `json:"repriced_orders,omitempty"`
	Version        int             `json:"version"`
	Actor          string          `json:"actor"`
}

// RepricedOrder is one entry of agreement.repriced.v1's repriced_orders.
type RepricedOrder struct {
	SalesOrderID string `json:"sales_order_id"`
	OldPrice     string `json:"old_price"`
	NewPrice     string `json:"new_price"`
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

// RemainderLine is one line closed by order.remaindercancelled.v1: the quantity
// the line gave up and the quantity it keeps (zero when the line was cancelled).
type RemainderLine struct {
	LineID       string `json:"line_id"`
	LineNo       int    `json:"line_no"`
	ProductID    string `json:"product_id"`
	CancelledQty string `json:"cancelled_qty"`
	KeptQty      string `json:"kept_qty"`
	LineStatus   string `json:"line_status"`
}

// OrderRemainderCancelledData is the data member of order.remaindercancelled.v1:
// the undelivered remainder of a partly shipped order, or of one of its lines,
// was closed. Status is SHIPPED or INVOICED, or PARTIALLY_SHIPPED after a
// one-line close while another line still has quantity to deliver, or
// CONFIRMED when a PATCH removed lines of a confirmed order (each kept at 0) or
// lowered, raised or reopened a line (kept at the new quantity; cancelled 0 for
// a raise or reopen), and CANCELLED when a confirmed order was cancelled whole
// (every line kept at 0). KeptQty is always the line's whole current ceiling.
type OrderRemainderCancelledData struct {
	OrderID       string          `json:"order_id"`
	LegalEntityID string          `json:"legal_entity_id"`
	Version       int             `json:"version"`
	Status        string          `json:"status"`
	Lines         []RemainderLine `json:"lines"`
	Reason        string          `json:"reason"`
	Actor         string          `json:"actor"`
}

// ConfirmedLine is one line of order.confirmed.
type ConfirmedLine struct {
	LineID            string `json:"line_id"`
	ProductID         string `json:"product_id"`
	Quantity          string `json:"quantity"`
	SiteID            string `json:"site_id"`
	WarehouseID       string `json:"warehouse_id"`
	ConfirmedShipDate string `json:"confirmed_ship_date"`
	// ConfirmedReceiptDate is the ship date plus the customer's transport days
	// rolled to its next receiving day (BR-27.2).
	ConfirmedReceiptDate string `json:"confirmed_receipt_date,omitempty"`
	// AtpQty is the quantity of the line whose schedule lines are CONFIRMED
	// (promised by the requested date). A release caller sends it as the
	// fulfillment release line's atpQty.
	AtpQty string `json:"atp_qty"`
	// ConfigurationID is the saved configuration of a configured line; absent otherwise.
	ConfigurationID *string `json:"configuration_id,omitempty"`
	// CatchWeight is the per-case weight spec of a catch-weight line (FR-24.7);
	// Quantity is then a count of cases, not weight. Absent otherwise.
	CatchWeight *CatchWeightSpec `json:"catch_weight,omitempty"`
}

// CatchWeightSpec is a catch-weight line's nominal, minimum and maximum
// weight per case, decimal strings in the product's inventory weight unit.
type CatchWeightSpec struct {
	Nominal string `json:"nominal"`
	Min     string `json:"min"`
	Max     string `json:"max"`
}

// OrderConfirmedData is the data member of order.confirmed.v1.
type OrderConfirmedData struct {
	OrderID    string `json:"order_id"`
	CustomerID string `json:"customer_id"`
	// OrderType lets consumers honour BR-27.7: a JOURNAL order never
	// reserves or consumes ATP, so inventory and planning skip its lines.
	OrderType string          `json:"order_type"`
	Currency  string          `json:"currency"`
	Lines     []ConfirmedLine `json:"lines"`
	NetTotal  string          `json:"net_total"`
	Version   int             `json:"version"`
	// OneTime marks a one-time customer order (FR-27.22); absent otherwise.
	OneTime *OneTimeRef `json:"one_time,omitempty"`
}

// OneTimeRef is the one_time member of order.confirmed. The buyer's name,
// address and tax id are personal data kept sealed in sales and are not put on
// the bus: the invoice and open item read them from GET /v1/sales/orders/{id}.
type OneTimeRef struct {
	Country string `json:"country"`
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

// OrderHoldRejectedData is the data member of orderhold.rejected.v1: a credit
// manager rejected a CREDIT hold with a comment, and the unconfirmed order was
// cancelled in the same transaction (order.cancelled.v1 follows on the same
// order). Version is the order's version after the cancellation.
type OrderHoldRejectedData struct {
	OrderID    string `json:"order_id"`
	HoldID     string `json:"hold_id"`
	HoldType   string `json:"hold_type"`
	ReasonCode string `json:"reason_code"`
	Comment    string `json:"comment"`
	Actor      string `json:"actor"`
	Version    int    `json:"version"`
}

// CreditCheckData is the data member of creditcheck.evaluated.v1.
type CreditCheckData struct {
	CustomerID string              `json:"customer_id"`
	OrderID    string              `json:"order_id"`
	Rules      []domain.RuleResult `json:"rules"`
	Outcome    string              `json:"outcome"`
	// Point is the credit_check_points value the check ran at: entry,
	// confirm or release.
	Point string `json:"point"`
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
	// OneTimeOrderLimit and its currency are absent when the defaults apply.
	OneTimeOrderLimit         *string  `json:"one_time_order_limit,omitempty"`
	OneTimeOrderLimitCurrency *string  `json:"one_time_order_limit_currency,omitempty"`
	CreditCheckPoints         []string `json:"credit_check_points"`
	IntakeDedupWindowDays     int      `json:"intake_dedup_window_days"`
	// DeliveryDateControlDefault and SalesLeadTimeDays are the FR-27.3 settings.
	DeliveryDateControlDefault string `json:"delivery_date_control_default"`
	SalesLeadTimeDays          int    `json:"sales_lead_time_days"`
	// PartialDeliveryDefault is partial_delivery_default (FR-27.6).
	PartialDeliveryDefault string `json:"partial_delivery_default"`
	Actor                  string `json:"actor"`
	Version                int    `json:"version"`
}

// OrderChangeRequestedData is the data member of orderchange.requested.v1: a
// change of a confirmed order was stored for approval (FR-27.9).
type OrderChangeRequestedData struct {
	RequestID     string        `json:"request_id"`
	LegalEntityID string        `json:"legal_entity_id"`
	OrderID       string        `json:"order_id"`
	CustomerID    string        `json:"customer_id"`
	Kind          string        `json:"kind"`
	BaseVersion   int           `json:"base_version"`
	Reason        string        `json:"reason"`
	Actor         string        `json:"actor"`
	Diff          []FieldChange `json:"diff"`
}

// OrderChangeDecidedData is the data member of orderchange.approved.v1,
// orderchange.rejected.v1 and orderchange.withdrawn.v1. applied_version is
// the order version an approval produced and is absent on a rejection or a
// withdrawal; on a withdrawal actor is the requester.
type OrderChangeDecidedData struct {
	RequestID      string `json:"request_id"`
	LegalEntityID  string `json:"legal_entity_id"`
	OrderID        string `json:"order_id"`
	Kind           string `json:"kind"`
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
	// ApprovalThreshold is the amount below which a change needs no approval,
	// empty when every change does; ThresholdCurrency is its currency.
	ApprovalThreshold string   `json:"approval_threshold,omitempty"`
	ThresholdCurrency string   `json:"threshold_currency,omitempty"`
	ApproverIDs       []string `json:"approver_ids"`
	Actor             string   `json:"actor"`
	Version           int      `json:"version"`
}

// IntakeErrorData is the data member of intakeerror.recorded.v1 and
// intakeerror.resolved.v1: a channel order was refused (recorded, once per
// attempt) or an operator closed the failure (resolved). The payload is never
// carried, only its hash.
type IntakeErrorData struct {
	IntakeErrorID string `json:"intake_error_id"`
	LegalEntityID string `json:"legal_entity_id"`
	Channel       string `json:"channel"`
	ExternalRef   string `json:"external_ref,omitempty"`
	RequestHash   string `json:"request_hash"`
	ErrorCode     string `json:"error_code"`
	Attempts      int    `json:"attempts"`
	Actor         string `json:"actor,omitempty"`
	Version       int    `json:"version"`
}

// DeliveryTermsData is the data member of deliveryterms.created.v1 and
// deliveryterms.changed.v1: a customer's transport days and receiving weekdays
// (ISO 1 Monday to 7 Sunday) were set (FR-27.3, BR-27.2). previous_* are empty
// on created.
type DeliveryTermsData struct {
	TermsID               string `json:"terms_id"`
	LegalEntityID         string `json:"legal_entity_id"`
	CustomerID            string `json:"customer_id"`
	TransportDays         int    `json:"transport_days"`
	ReceivingDays         []int  `json:"receiving_days"`
	PreviousTransportDays *int   `json:"previous_transport_days,omitempty"`
	PreviousReceivingDays []int  `json:"previous_receiving_days,omitempty"`
	Actor                 string `json:"actor"`
	Version               int    `json:"version"`
}

// DeliveryToleranceData is the data member of deliverytolerance.created.v1,
// deliverytolerance.changed.v1 and deliverytolerance.removed.v1: the
// over-delivery and under-delivery tolerance (percent of the ordered quantity)
// and the partial delivery policy of a product for one customer, or for every
// customer when customer_id is absent (BR-27.9, BR-28.7). previous_* are empty
// on created; on removed the figures are those the rule had.
type DeliveryToleranceData struct {
	ToleranceID              string  `json:"tolerance_id"`
	LegalEntityID            string  `json:"legal_entity_id"`
	ProductID                string  `json:"product_id"`
	CustomerID               *string `json:"customer_id,omitempty"`
	OverdeliveryPct          string  `json:"overdelivery_pct"`
	UnderdeliveryPct         string  `json:"underdelivery_pct"`
	PartialDelivery          string  `json:"partial_delivery"`
	PreviousOverdeliveryPct  string  `json:"previous_overdelivery_pct,omitempty"`
	PreviousUnderdeliveryPct string  `json:"previous_underdelivery_pct,omitempty"`
	PreviousPartialDelivery  string  `json:"previous_partial_delivery,omitempty"`
	Actor                    string  `json:"actor"`
	Version                  int     `json:"version"`
}

// ProgressLine is one line touched by an order progress record.
type ProgressLine struct {
	LineID       string `json:"line_id"`
	LineNo       int    `json:"line_no"`
	Quantity     string `json:"quantity"`
	DeliveredQty string `json:"delivered_qty"`
	InvoicedQty  string `json:"invoiced_qty"`
	LineStatus   string `json:"line_status"`
	// LineQuantity is the line's ordered quantity after the report, set only
	// where the delivery tolerance acted (it is then the delivered quantity):
	// Tolerance is OVER (the line grew to the quantity delivered) or UNDER (the
	// remainder was closed at the quantity delivered, see order.remaindercancelled).
	LineQuantity string `json:"line_quantity,omitempty"`
	Tolerance    string `json:"tolerance,omitempty"`
	// Catch-weight lines only (FR-24.7): on a DELIVERED report, the weight it
	// delivered (the reported actualWeight, else cases x nominal) and the
	// line's delivered weight so far. Quantities stay in cases.
	ReportedWeight  string `json:"reported_weight,omitempty"`
	DeliveredWeight string `json:"delivered_weight,omitempty"`
	// ProductID, Unit, SiteID and WarehouseID are the line's (#480), so a
	// consumer that books the delivery in inventory needs no other read.
	ProductID   string `json:"product_id,omitempty"`
	Unit        string `json:"unit,omitempty"`
	SiteID      string `json:"site_id,omitempty"`
	WarehouseID string `json:"warehouse_id,omitempty"`
	// Tracking is what a DELIVERED report took from each batch or serial
	// (#480), as the reporter named it; absent for an untracked delivery.
	// Quantities add up to Quantity.
	Tracking []ProgressTracking `json:"tracking,omitempty"`
}

// ProgressTracking is one batch or serial of a delivered line: quantity in
// the line's unit (cases on a catch-weight line) and, on a catch-weight line
// split over several entries, reported_weight, the entry's weight.
type ProgressTracking struct {
	Batch          string `json:"batch,omitempty"`
	Serial         string `json:"serial,omitempty"`
	Quantity       string `json:"quantity"`
	ReportedWeight string `json:"reported_weight,omitempty"`
}

// OrderProgressData is the data member of orderprogress.recorded.v1.
type OrderProgressData struct {
	ProgressID     string         `json:"progress_id"`
	OrderID        string         `json:"order_id"`
	LegalEntityID  string         `json:"legal_entity_id"`
	Currency       string         `json:"currency,omitempty"`
	Kind           string         `json:"kind"`
	SourceRef      string         `json:"source_ref"`
	Lines          []ProgressLine `json:"lines"`
	PreviousStatus string         `json:"previous_status"`
	OrderStatus    string         `json:"order_status"`
	Actor          string         `json:"actor"`
	Version        int            `json:"version"`
}

// OrderClosedData is the data member of order.closed.v1: an INVOICED order was
// moved to CLOSED by the nightly auto-close (BR-27.5).
type OrderClosedData struct {
	OrderID        string `json:"order_id"`
	LegalEntityID  string `json:"legal_entity_id"`
	Version        int    `json:"version"`
	PreviousStatus string `json:"previous_status"`
	GraceDays      int    `json:"grace_days"`
	Actor          string `json:"actor"`
}

// ReturnOrderLine is one line on a return-order event.
type ReturnOrderLine struct {
	LineID             string  `json:"line_id"`
	LineNo             int     `json:"line_no"`
	ProductID          string  `json:"product_id"`
	Disposition        string  `json:"disposition,omitempty"`
	QuantityExpected   string  `json:"quantity_expected"`
	QuantityReceived   string  `json:"quantity_received"`
	UnitPrice          string  `json:"unit_price"`
	Restocking         string  `json:"restocking"`
	InventoryReceiptID *string `json:"inventory_receipt_id,omitempty"`
}

// ReturnOrderData is the data member of returnorder.*.v1 (FR-30.11).
type ReturnOrderData struct {
	ReturnOrderID    string            `json:"return_order_id"`
	RMANumber        string            `json:"rma_number"`
	LegalEntityID    string            `json:"legal_entity_id"`
	CustomerID       string            `json:"customer_id"`
	Status           string            `json:"status"`
	Currency         string            `json:"currency"`
	Gross            string            `json:"gross"`
	Restocking       string            `json:"restocking"`
	Net              string            `json:"net"`
	CreditNoteID     *string           `json:"credit_note_id,omitempty"`
	JournalPostingID *string           `json:"journal_posting_id,omitempty"`
	Lines            []ReturnOrderLine `json:"lines"`
	Actor            string            `json:"actor"`
	Version          int               `json:"version"`
}
