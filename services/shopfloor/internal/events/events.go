package events

import (
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/events"
	"github.com/BenjaminSRussell/passage/services/shopfloor/internal/domain"
)

// Context is the bounded context segment of every event type this service emits.
const Context = "shopfloor"

var (
	RegistrationPosted = events.Type(Context, "registration", "posted", 1)
	PaydayApproved     = events.Type(Context, "payday", "approved", 1)
	PaydayExported     = events.Type(Context, "payday", "exported", 1)
	AndonRaised        = events.Type(Context, "andon", "raised", 1)
	// The entity is one word: the naming rule allows only [a-z0-9] per segment.
	ProcessMessageReceived  = events.Type(Context, "processmessage", "received", 1)
	ProcessMessageDelivered = events.Type(Context, "processmessage", "delivered", 1)
	ProcessMessageDeleted   = events.Type(Context, "processmessage", "deleted", 1)
	// PRD 4.25.5 names this event passage.shopfloor.oee.bucket-updated.v1; the
	// platform naming rule allows one [a-z0-9] word per segment.
	OEEBucketUpdated = events.Type(Context, "oeebucket", "updated", 1)
	OEEBucketRemoved = events.Type(Context, "oeebucket", "removed", 1)
	// JobReported is not in the PRD event table: production (w21) needs the reported
	// quantities and the finish of an operation, quality (w33) the failed checks.
	JobReported = events.Type(Context, "job", "reported", 1)
	// JobReopened is not in the PRD event table: production (w21) and quality (w33) must learn
	// that a job they saw finished takes reports again.
	JobReopened = events.Type(Context, "job", "reopened", 1)
	// RegistrationCorrected and PaydayRevised are not in the PRD event table: payroll and
	// the w21 job-card consumer must learn that approved or posted time changed (FR-25.8, AC-25.8).
	RegistrationCorrected = events.Type(Context, "registration", "corrected", 1)
	PaydayRevised         = events.Type(Context, "payday", "revised", 1)
	// TimeProfileSaved, PayAgreementSaved and WorkerAssigned are not in the PRD event table:
	// payroll (w43) and reporting need the pay rules a worker's hours were calculated under.
	TimeProfileSaved  = events.Type(Context, "timeprofile", "saved", 1)
	PayAgreementSaved = events.Type(Context, "payagreement", "saved", 1)
	WorkerAssigned    = events.Type(Context, "worker", "assigned", 1)
	// WorkerSaved is not in the PRD event table: payroll (w43) and reporting need to know when a
	// worker's name, cost rate or active state changes, as the pay lines of earlier days keep the
	// rate they were calculated with.
	WorkerSaved = events.Type(Context, "worker", "saved", 1)
	// WorkerCostRateSet is not in the PRD event table: payroll (w43) and costing (w21, w39) learn
	// the dated hourly rate a worker's days are priced at.
	WorkerCostRateSet = events.Type(Context, "workercostrate", "set", 1)
	// AbsenceCodeSaved is not in the PRD event table: payroll (w43) needs the pay terms
	// behind the absence lines of a worker-day.
	AbsenceCodeSaved = events.Type(Context, "absencecode", "saved", 1)
	// The edge fleet events (FR-25.19) are not in the PRD event table: the config publish and
	// apply are the audit trail of what a connector ran with, and a heartbeat publishes nothing
	// (50 edges report twice a minute).
	// DowntimeReasonSaved is not in the PRD event table: OEE reporting and the andon consumers
	// need the planned or unplanned category behind a coded downtime.
	DowntimeReasonSaved = events.Type(Context, "downtimereason", "saved", 1)
	// ProcessCategorySaved is not in the PRD event table: the consuming contexts (w21, w24, w33, w35)
	// need the destinations and limits a category routes with.
	ProcessCategorySaved = events.Type(Context, "processcategory", "saved", 1)
	EdgeNodeSaved        = events.Type(Context, "edgenode", "saved", 1)
	EdgeConfigPublished  = events.Type(Context, "edgeconfig", "published", 1)
	EdgeConfigApplied    = events.Type(Context, "edgeconfig", "applied", 1)
)

// All lists every event type the service publishes (used by the startup registry check).
func All() []string {
	return []string{RegistrationPosted, PaydayApproved, PaydayExported, AndonRaised, ProcessMessageReceived, ProcessMessageDelivered, ProcessMessageDeleted,
		OEEBucketUpdated, OEEBucketRemoved, JobReported, JobReopened, RegistrationCorrected, PaydayRevised, TimeProfileSaved, PayAgreementSaved, WorkerAssigned, WorkerSaved, WorkerCostRateSet, AbsenceCodeSaved,
		EdgeNodeSaved, EdgeConfigPublished, EdgeConfigApplied, DowntimeReasonSaved, ProcessCategorySaved}
}

// ProcessMessageReceivedData is the payload of dev.passage.shopfloor.processmessage.received.v1,
// published when a process message is stored, again for one destination when it is
// reprocessed, and again by the retry sweep for the destinations whose wait elapsed
// (FR-25.17). Destinations then names only those destinations. Each consuming context (w21 journal, w33 quality, w24 EBR,
// w35 webhook) acts on the destinations it owns and reports the outcome back.
// OutOfLimit tells the EBR consumer to record an exception (BR-25.13).
type ProcessMessageReceivedData struct {
	MessageID       string                  `json:"messageId"`
	Source          string                  `json:"source"`
	SourceMessageID string                  `json:"sourceMessageId"`
	CategoryCode    string                  `json:"categoryCode"`
	OrderID         string                  `json:"orderId"`
	PhasePath       string                  `json:"phasePath"`
	Destinations    []string                `json:"destinations"`
	Values          []domain.Characteristic `json:"values"`
	OutOfLimit      bool                    `json:"outOfLimit"`
	OccurredAt      time.Time               `json:"occurredAt"`
}

// ProcessMessageDeliveredData is the payload of dev.passage.shopfloor.processmessage.delivered.v1,
// published once when every destination of a message is delivered.
type ProcessMessageDeliveredData struct {
	MessageID    string `json:"messageId"`
	CategoryCode string `json:"categoryCode"`
	OrderID      string `json:"orderId"`
}

// ProcessMessageDeletedData is the payload of dev.passage.shopfloor.processmessage.deleted.v1,
// published when a supervisor deletes a message with a destination in error (FR-25.17).
// Undelivered names the destinations that never received it, so the EBR consumer can
// record the missed message as an exception (BR-24.15).
type ProcessMessageDeletedData struct {
	MessageID    string    `json:"messageId"`
	CategoryCode string    `json:"categoryCode"`
	OrderID      string    `json:"orderId"`
	Reason       string    `json:"reason"`
	Undelivered  []string  `json:"undelivered"`
	DeletedBy    string    `json:"deletedBy"`
	DeletedAt    time.Time `json:"deletedAt"`
}

// OEEBucketUpdatedData is the payload of dev.passage.shopfloor.oeebucket.updated.v1,
// published by the OEE rollup for a bucket that is new or whose values changed
// (FR-25.10). Minutes and counts travel with the ratios so a dashboard rolls
// buckets up to a shift by summing them, never by averaging ratios (BR-25.5).
type OEEBucketUpdatedData struct {
	ResourceID     string          `json:"resourceId"`
	BucketMinutes  int             `json:"bucketMinutes"`
	BucketStart    time.Time       `json:"bucketStart"`
	PlannedMinutes decimal.Decimal `json:"plannedMinutes"`
	RunMinutes     decimal.Decimal `json:"runMinutes"`
	DownMinutes    decimal.Decimal `json:"downMinutes"`
	TotalCount     int64           `json:"totalCount"`
	GoodCount      int64           `json:"goodCount"`
	Availability   decimal.Decimal `json:"availability"`
	Performance    decimal.Decimal `json:"performance"`
	Quality        decimal.Decimal `json:"quality"`
	OEE            decimal.Decimal `json:"oee"`
	OverRate       bool            `json:"overRate"`
}

// JobReportedData is the payload of dev.passage.shopfloor.job.reported.v1, published
// once per stored operator feedback on a job (FR-25.4, FR-25.5); a replayed feedbackId
// publishes nothing. QtyGood and QtyScrap are this posting's increments, GoodQuantity
// and ScrapQuantity the job's totals after it. FailedChecks lists the checks whose
// latest result fails; OverrideReason is set when a supervisor finished the job over
// failed mandatory checks (SF-0005).
type JobReportedData struct {
	JobID          string          `json:"jobId"`
	FeedbackID     string          `json:"feedbackId"`
	OrderRef       string          `json:"orderRef"`
	OperationRef   string          `json:"operationRef"`
	ResourceID     string          `json:"resourceId"`
	QtyGood        decimal.Decimal `json:"qtyGood"`
	QtyScrap       decimal.Decimal `json:"qtyScrap"`
	ScrapReasonID  *string         `json:"scrapReasonId"`
	GoodQuantity   decimal.Decimal `json:"goodQuantity"`
	ScrapQuantity  decimal.Decimal `json:"scrapQuantity"`
	Finished       bool            `json:"finished"`
	FailedChecks   []string        `json:"failedChecks"`
	OverrideReason *string         `json:"overrideReason"`
	RecordedBy     string          `json:"recordedBy"`
	RecordedAt     time.Time       `json:"recordedAt"`
}

// JobReopenedData is the payload of dev.passage.shopfloor.job.reopened.v1, published when a
// supervisor reopens a finished job. GoodQuantity and ScrapQuantity are the job's totals at the
// reopening; PreviousFinishedAt is the finish that no longer stands.
type JobReopenedData struct {
	JobID              string          `json:"jobId"`
	ReopeningID        string          `json:"reopeningId"`
	OrderRef           string          `json:"orderRef"`
	OperationRef       string          `json:"operationRef"`
	ResourceID         string          `json:"resourceId"`
	GoodQuantity       decimal.Decimal `json:"goodQuantity"`
	ScrapQuantity      decimal.Decimal `json:"scrapQuantity"`
	Reason             string          `json:"reason"`
	ReopenedBy         string          `json:"reopenedBy"`
	ReopenedAt         time.Time       `json:"reopenedAt"`
	PreviousFinishedAt time.Time       `json:"previousFinishedAt"`
}

// OEEBucketRemovedData is the payload of dev.passage.shopfloor.oeebucket.removed.v1,
// published when a rollup finds that a stored bucket no longer has planned time
// (for example its downtime was coded as planned afterwards).
type OEEBucketRemovedData struct {
	ResourceID    string    `json:"resourceId"`
	BucketMinutes int       `json:"bucketMinutes"`
	BucketStart   time.Time `json:"bucketStart"`
}

// PaydayExportedData is the payload of dev.passage.shopfloor.payday.exported.v1:
// one payroll export batch with every pay line it carries (FR-25.8, w43).
type PaydayExportedData struct {
	BatchID   string             `json:"batchId"`
	FromDate  string             `json:"fromDate"`
	ToDate    string             `json:"toDate"`
	LineCount int                `json:"lineCount"`
	CreatedBy string             `json:"createdBy"`
	Lines     []ExportedLineData `json:"lines"`
}

// ExportedLineData is one approved pay line inside an export batch.
type ExportedLineData struct {
	WorkerID   string          `json:"workerId"`
	WorkDate   string          `json:"workDate"`
	Type       string          `json:"type"`
	Hours      decimal.Decimal `json:"hours"`
	Multiplier decimal.Decimal `json:"multiplier"`
	Amount     decimal.Decimal `json:"amount"`
	Currency   string          `json:"currency"`
}

// AndonRaisedData is the payload of dev.passage.shopfloor.andon.raised.v1.
type AndonRaisedData struct {
	AlertID    string  `json:"alertId"`
	ResourceID string  `json:"resourceId"`
	Kind       string  `json:"kind"`
	Severity   string  `json:"severity"`
	JobID      *string `json:"jobId"`
	StartedAt  string  `json:"startedAt"`
}

// RegistrationPostedData is the payload of dev.passage.shopfloor.registration.posted.v1.
type RegistrationPostedData struct {
	RegistrationID string  `json:"registrationId"`
	WorkerID       string  `json:"workerId"`
	Kind           string  `json:"kind"`
	JobID          *string `json:"jobId"`
	BundleID       *string `json:"bundleId"`
	// AbsenceCodeID is the absence code of an absence registration, otherwise null.
	AbsenceCodeID *string          `json:"absenceCodeId"`
	Hours         *decimal.Decimal `json:"hours"`
	QtyGood       decimal.Decimal  `json:"qtyGood"`
	QtyScrap      decimal.Decimal  `json:"qtyScrap"`
	Offline       bool             `json:"offline"`
	// Flags are the registration's review flags: job-finished-conflict on a
	// job-start (BR-25.7), auto-stopped and auto-stop-capped on a job-stop the
	// server wrote when the worker started another job (FR-25.3), auto-clock-out on
	// a clock-out the auto clock-out sweep wrote (D2h), production-finished on a job-stop the server
	// wrote when production finished the job's operation (FR-25.3).
	Flags []string `json:"flags"`
	// OrderRef and OperationRef are the job's production order id and operation
	// number (text, as stored) when the registration names a known job.
	OrderRef     string `json:"orderRef,omitempty"`
	OperationRef string `json:"operationRef,omitempty"`
	// CostRate is the worker's hourly rate when it is greater than zero; the
	// w21 job-card consumer uses it over the resource labor rate (BR-25.9).
	CostRate *decimal.Decimal `json:"costRate,omitempty"`
	// CostRateCurrency is the worker's currency, the currency of CostRate.
	CostRateCurrency string `json:"costRateCurrency,omitempty"`
}

// PayLineData is one pay line inside a payday approval.
type PayLineData struct {
	Type       string          `json:"type"`
	Hours      decimal.Decimal `json:"hours"`
	Multiplier decimal.Decimal `json:"multiplier"`
	Amount     decimal.Decimal `json:"amount"`
	Currency   string          `json:"currency"`
}

// PaydayApprovedData is the payload of dev.passage.shopfloor.payday.approved.v1.
type PaydayApprovedData struct {
	WorkerID   string        `json:"workerId"`
	WorkDate   string        `json:"workDate"`
	PayLines   []PayLineData `json:"payLines"`
	ApprovedBy string        `json:"approvedBy"`
}

// RegistrationCorrectedData is the payload of dev.passage.shopfloor.registration.corrected.v1,
// published when a supervisor changes the times of a registration with a reason (FR-25.8).
// Hours is the new elapsed time of a closed registration; consumers that posted the old
// hours (w21 job card) replace them.
type RegistrationCorrectedData struct {
	RegistrationID string           `json:"registrationId"`
	WorkerID       string           `json:"workerId"`
	Kind           string           `json:"kind"`
	JobID          *string          `json:"jobId"`
	OldStartedAt   time.Time        `json:"oldStartedAt"`
	OldStoppedAt   *time.Time       `json:"oldStoppedAt"`
	NewStartedAt   time.Time        `json:"newStartedAt"`
	NewStoppedAt   *time.Time       `json:"newStoppedAt"`
	Hours          *decimal.Decimal `json:"hours"`
	Reason         string           `json:"reason"`
	CorrectedBy    string           `json:"correctedBy"`
}

// PaydayRevisedData is the payload of dev.passage.shopfloor.payday.revised.v1, published
// when a supervisor revises an approved worker-day (BR-25.8, AC-25.8). ReversedLines
// carry negative hours and amounts and cancel lines that were approved (and possibly
// exported); AddedLines are the new pay lines. Revision counts the day's revisions.
type PaydayRevisedData struct {
	WorkerID      string        `json:"workerId"`
	WorkDate      string        `json:"workDate"`
	Revision      int           `json:"revision"`
	Reason        string        `json:"reason"`
	ReversedLines []PayLineData `json:"reversedLines"`
	AddedLines    []PayLineData `json:"addedLines"`
	RevisedBy     string        `json:"revisedBy"`
}

// TimeProfileSavedData is the payload of dev.passage.shopfloor.timeprofile.saved.v1,
// published when a time profile is created or changed. Version is 1 for a new profile.
type TimeProfileSavedData struct {
	ProfileID string `json:"profileId"`
	Code      string `json:"code"`
	Name      string `json:"name"`
	Active    bool   `json:"active"`
	Version   int    `json:"version"`
	SavedBy   string `json:"savedBy"`
}

// PayAgreementSavedData is the payload of dev.passage.shopfloor.payagreement.saved.v1,
// published when a pay agreement is created or changed. Decimals are strings.
type PayAgreementSavedData struct {
	AgreementID        string `json:"agreementId"`
	Code               string `json:"code"`
	Name               string `json:"name"`
	DailyThreshold     string `json:"dailyThreshold"`
	WeeklyThreshold    string `json:"weeklyThreshold"`
	OvertimeMultiplier string `json:"overtimeMultiplier"`
	SundayMultiplier   string `json:"sundayMultiplier"`
	WeekStart          int    `json:"weekStart"`
	// ShiftPremiums are the premium windows; empty when the agreement has none.
	ShiftPremiums []domain.ShiftPremiumInput `json:"shiftPremiums"`
	Active        bool                       `json:"active"`
	Version       int                        `json:"version"`
	SavedBy       string                     `json:"savedBy"`
}

// WorkerAssignedData is the payload of dev.passage.shopfloor.worker.assigned.v1,
// published when a worker's time profile, pay agreement or home resource changes. A nil
// profile or agreement id means the worker is calculated with the defaults; a nil
// defaultResourceId means the worker has no home resource. Days already calculated keep their pay lines
// until a supervisor recalculates them.
type WorkerAssignedData struct {
	WorkerID       string  `json:"workerId"`
	ProfileID      *string `json:"profileId"`
	PayAgreementID *string `json:"payAgreementId"`
	// DefaultResourceID is the resource whose jobs the worker's dispatch list shows.
	DefaultResourceID *string `json:"defaultResourceId"`
	AssignedBy        string  `json:"assignedBy"`
}

// WorkerSavedData is the payload of dev.passage.shopfloor.worker.saved.v1, published when a
// worker's own fields change or the worker is deactivated or reactivated. Change is
// "updated", "deactivated" or "reactivated"; Reason is set for a deactivation. The badge is a
// personal identifier and is not published. CostRate is a decimal string. Days already calculated
// keep the rate they were calculated with until a supervisor recalculates them.
type WorkerSavedData struct {
	WorkerID string  `json:"workerId"`
	Name     string  `json:"name"`
	UserID   *string `json:"userId"`
	CostRate string  `json:"costRate"`
	Currency string  `json:"currency"`
	Active   bool    `json:"active"`
	Version  int     `json:"version"`
	Change   string  `json:"change"`
	Reason   *string `json:"reason"`
	SavedBy  string  `json:"savedBy"`
}

// WorkerCostRateSetData is the payload of dev.passage.shopfloor.workercostrate.set.v1, published when
// a dated cost rate is added to a worker. CostRate is a decimal string per hour in Currency, and holds
// for days from EffectiveFrom (YYYY-MM-DD) until the next dated rate. Days already calculated keep
// the rate they were calculated with until a supervisor recalculates or revises them.
type WorkerCostRateSetData struct {
	WorkerID      string `json:"workerId"`
	CostRateID    string `json:"costRateId"`
	EffectiveFrom string `json:"effectiveFrom"`
	CostRate      string `json:"costRate"`
	Currency      string `json:"currency"`
	SetBy         string `json:"setBy"`
}

// AbsenceCodeSavedData is the payload of dev.passage.shopfloor.absencecode.saved.v1,
// published when an absence code is created or changed. PayMultiplier is a decimal string.
type AbsenceCodeSavedData struct {
	AbsenceCodeID string `json:"absenceCodeId"`
	Code          string `json:"code"`
	Name          string `json:"name"`
	Paid          bool   `json:"paid"`
	PayMultiplier string `json:"payMultiplier"`
	Active        bool   `json:"active"`
	Version       int    `json:"version"`
	SavedBy       string `json:"savedBy"`
}

// DowntimeReasonSavedData is the payload of dev.passage.shopfloor.downtimereason.saved.v1,
// published when a downtime reason code is created or changed.
type DowntimeReasonSavedData struct {
	ReasonID string `json:"reasonId"`
	Code     string `json:"code"`
	Name     string `json:"name"`
	Category string `json:"category"`
	Active   bool   `json:"active"`
	Version  int    `json:"version"`
	SavedBy  string `json:"savedBy"`
}

// ProcessCategorySavedData is the payload of dev.passage.shopfloor.processcategory.saved.v1,
// published when a process message category is created or changed.
type ProcessCategorySavedData struct {
	CategoryID       string                     `json:"categoryId"`
	Code             string                     `json:"code"`
	Name             string                     `json:"name"`
	Destinations     []string                   `json:"destinations"`
	RejectOutOfLimit bool                       `json:"rejectOutOfLimit"`
	Characteristics  []ProcessCategoryCharacter `json:"characteristics"`
	Active           bool                       `json:"active"`
	Version          int                        `json:"version"`
	SavedBy          string                     `json:"savedBy"`
}

// ProcessCategoryCharacter is one characteristic definition of a saved category.
type ProcessCategoryCharacter struct {
	Name     string  `json:"name"`
	Type     string  `json:"type"`
	Unit     *string `json:"unit,omitempty"`
	Min      *string `json:"min,omitempty"`
	Max      *string `json:"max,omitempty"`
	Required bool    `json:"required"`
}

// EdgeNodeSavedData is the payload of dev.passage.shopfloor.edgenode.saved.v1, published when
// an edge node is registered or its name, site, ring, connector identity or status is changed.
type EdgeNodeSavedData struct {
	EdgeID   string  `json:"edgeId"`
	SiteID   *string `json:"siteId"`
	Name     string  `json:"name"`
	Platform string  `json:"platform"`
	Ring     string  `json:"ring"`
	// ConnectorSubject is the identity bound to the node; only it may pull the configuration
	// and report heartbeats. Null leaves the node unbound.
	ConnectorSubject *string `json:"connectorSubject"`
	Active           bool    `json:"active"`
	Version          int     `json:"version"`
	SavedBy          string  `json:"savedBy"`
}

// EdgeConfigPublishedData is the payload of dev.passage.shopfloor.edgeconfig.published.v1,
// published when a new configuration version is stored for an edge node. It is the audit
// record of the publish: who, the SHA-256 of the stored document and the write allowlist nodes.
type EdgeConfigPublishedData struct {
	EdgeID         string   `json:"edgeId"`
	Version        int      `json:"version"`
	DocumentSHA256 string   `json:"documentSha256"`
	WriteRuleNodes []string `json:"writeRuleNodes"`
	PublishedBy    string   `json:"publishedBy"`
}

// EdgeConfigAppliedData is the payload of dev.passage.shopfloor.edgeconfig.applied.v1,
// published when a heartbeat reports a configuration version other than the one applied before.
// PreviousVersion is null for the first one.
type EdgeConfigAppliedData struct {
	EdgeID          string `json:"edgeId"`
	Version         int    `json:"version"`
	PreviousVersion *int   `json:"previousVersion"`
}
