// Package events holds the event types analytics publishes and their payloads.
package events

import "github.com/BenjaminSRussell/passage/platform/events"

// Context is the bounded context name used in event types.
const Context = "analytics"

// MetricReconciled is emitted when a daily reconciliation result is recorded.
var MetricReconciled = events.Type(Context, "metric", "reconciled", 1)

// DQScored is emitted for every data-quality evaluation (PRD 9.46.5 names it in
// the catalog context: dev.passage.catalog.dq.scored.v1).
var DQScored = events.Type("catalog", "dq", "scored", 1)

// ArchivePartitionMoved is emitted when the archive job verified a partition
// (dev.passage.archive.partition.moved.v1, evidence for control CC6.5).
var ArchivePartitionMoved = events.Type("archive", "partition", "moved", 1)

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

// DQScoredData is the data member of DQScored. NotifyOwner asks the owner's
// inbox for the day's task on the first unmuted below-threshold evaluation of
// the rule that day, with TaskKey as the inbox's dedupe key. EscalateSteward
// is set once per below-threshold run, when it reaches three consecutive
// evaluations (BR-46.15). A muted rule sets neither. Percentages are decimal
// strings.
type DQScoredData struct {
	AssetFQN        string `json:"asset_fqn"`
	RuleID          string `json:"rule_id"`
	Date            string `json:"date"`
	PassPct         string `json:"pass_pct"`
	ThresholdPct    string `json:"threshold_pct"`
	FailedRows      int64  `json:"failed_rows"`
	TotalRows       int64  `json:"total_rows"`
	Severity        string `json:"severity"`
	OwnerUserID     string `json:"owner_user_id"`
	Below           bool   `json:"below"`
	BelowStreak     int    `json:"below_streak"`
	Muted           bool   `json:"muted"`
	NotifyOwner     bool   `json:"notify_owner"`
	EscalateSteward bool   `json:"escalate_steward"`
	TaskKey         string `json:"task_key,omitempty"`
}

// ArchivePartitionMovedData is the data member of ArchivePartitionMoved.
type ArchivePartitionMovedData struct {
	PartitionID string `json:"partition_id"`
	Domain      string `json:"domain"`
	Table       string `json:"table"`
	Period      string `json:"period"`
	Rows        int64  `json:"rows"`
	ContentHash string `json:"content_hash"`
	MovedAt     string `json:"moved_at"`
}

// ReportDelivered is emitted when a scheduled report reached its destination
// (FR-46.10, AC-46.10). Consumer: audit.
var ReportDelivered = events.Type(Context, "report", "delivered", 1)

// ReportDeliveredData is the data member of ReportDelivered. RunID is the same
// for every attempt of one scheduled run. Destination is the destination type
// (EMAIL, SFTP or INBOX): recipient addresses stay in the schedule and never
// travel in the event. The report body never leaves the destination adapter.
type ReportDeliveredData struct {
	ScheduleID  string `json:"schedule_id"`
	RunID       string `json:"run_id"`
	Attempt     int    `json:"attempt"`
	Format      string `json:"format"`
	Destination string `json:"destination"`
	RowCount    int64  `json:"row_count"`
	Bytes       int64  `json:"bytes"`
	DeliveredAt string `json:"delivered_at"`
}
