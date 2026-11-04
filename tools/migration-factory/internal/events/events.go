// Package events names the CloudEvents the migration factory writes to its outbox and the
// payload of each (PRD 9.49.5). events/asyncapi.yaml documents the same contract.
package events

import "github.com/BenjaminSRussell/passage/platform/events"

// Source is the CloudEvents source of every event this service emits.
const Source = "passage/migration-factory"

var (
	// PackReconciled is written when a pack run's reconciliation step has computed its totals.
	PackReconciled = events.Type("migration", "pack", "reconciled", 1)
	// PackLoaded is written when the load of a pack run into its target service has finished a pass.
	PackLoaded = events.Type("migration", "pack", "loaded", 1)
	// ParallelRunDiffed is written when a parallel run's diff has classified its variances.
	ParallelRunDiffed = events.Type("migration", "parallelrun", "diffed", 1)
	// CutoverStepCompleted is written when a runbook step is marked done.
	CutoverStepCompleted = events.Type("cutover", "step", "completed", 1)
	// CutoverPlanRolledBack is written when a cutover plan's rollback is executed (FR-49.9).
	CutoverPlanRolledBack = events.Type("cutover", "plan", "rolledback", 1)
	// CutoverDecisionRecorded is written when a go/no-go decision is recorded on a plan (FR-49.6).
	CutoverDecisionRecorded = events.Type("cutover", "decision", "recorded", 1)
	// ToleranceConfigured is written when a control-total tolerance is set or changed (FR-49.2).
	ToleranceConfigured = events.Type("migration", "tolerance", "configured", 1)
)

// All lists every event type the service publishes.
func All() []string {
	return []string{PackReconciled, PackLoaded, ParallelRunDiffed, CutoverStepCompleted, CutoverPlanRolledBack, CutoverDecisionRecorded, ToleranceConfigured}
}

// PackReconciledData is the payload of dev.passage.migration.pack.reconciled.v1.
type PackReconciledData struct {
	RunID          string `json:"run_id"`
	Pack           string `json:"pack"`
	SourceSystem   string `json:"source_system"`
	SourceCompany  string `json:"source_company"`
	Status         string `json:"status"`
	TotalsMatch    int    `json:"totals_match"`
	TotalsMismatch int    `json:"totals_mismatch"`
	Quarantined    int    `json:"quarantined"`
	RecordsLoaded  int    `json:"records_loaded"`
}

// PackLoadedData is the payload of dev.passage.migration.pack.loaded.v1: how the rows of a run came out at the
// target and whether the target's totals equal what the factory sent.
type PackLoadedData struct {
	RunID          string `json:"run_id"`
	Pack           string `json:"pack"`
	Target         string `json:"target"`
	ImportSource   string `json:"import_source"`
	LegalEntityID  string `json:"legal_entity_id"`
	Status         string `json:"status"`
	Created        int    `json:"created"`
	Updated        int    `json:"updated"`
	Unchanged      int    `json:"unchanged"`
	Skipped        int    `json:"skipped"`
	Rejected       int    `json:"rejected"`
	NotAvailable   int    `json:"not_available"`
	Pending        int    `json:"pending"`
	TotalsMatch    int    `json:"totals_match"`
	TotalsMismatch int    `json:"totals_mismatch"`
}

// ParallelRunDiffedData is the payload of dev.passage.migration.parallelrun.diffed.v1. Amounts
// are decimal strings.
type ParallelRunDiffedData struct {
	ParallelRunID     string `json:"parallel_run_id"`
	Period            string `json:"period"`
	SourceSystem      string `json:"source_system"`
	VarianceCount     int    `json:"variance_count"`
	UnexplainedCount  int    `json:"unexplained_count"`
	UnexplainedAmount string `json:"unexplained_amount"`
}

// CutoverStepCompletedData is the payload of dev.passage.cutover.step.completed.v1.
type CutoverStepCompletedData struct {
	PlanID            string `json:"plan_id"`
	StepID            int    `json:"step_id"`
	Name              string `json:"name"`
	ActualDurationMin int    `json:"actual_duration_min"`
	Status            string `json:"status"`
	CompletedBy       string `json:"completed_by"`
}

// CutoverPlanRolledBackData is the payload of dev.passage.cutover.plan.rolledback.v1. The
// re-keying line counts tell the legacy side how much to re-key; the files themselves are read
// from GET /cutover/plans/{id}/rekeying/{file}.
type CutoverPlanRolledBackData struct {
	PlanID             string   `json:"plan_id"`
	Approvers          []string `json:"approvers"`
	ExecutedBy         string   `json:"executed_by"`
	RekeyJournalLines  int      `json:"rekey_journal_lines"`
	RekeyDocumentLines int      `json:"rekey_document_lines"`
}

// CutoverDecisionRecordedData is the payload of dev.passage.cutover.decision.recorded.v1.
// OpenItems lists the checklist items not met when the decision was taken (empty for a GO).
type CutoverDecisionRecordedData struct {
	PlanID    string   `json:"plan_id"`
	Decision  string   `json:"decision"`
	Approvers []string `json:"approvers"`
	Reason    string   `json:"reason"`
	OpenItems []string `json:"open_items"`
	// Waivers lists the staged-but-unposted packs both approvers accepted, with their reasons.
	Waivers   []CutoverDecisionWaiver `json:"waivers"`
	DecidedBy string                  `json:"decided_by"`
	Timestamp string                  `json:"timestamp"`
}

// CutoverDecisionWaiver is one waived pack on dev.passage.cutover.decision.recorded.v1.
type CutoverDecisionWaiver struct {
	Pack   string `json:"pack"`
	Reason string `json:"reason"`
}

// ToleranceConfiguredData is the payload of dev.passage.migration.tolerance.configured.v1. Amounts are
// decimal strings.
type ToleranceConfiguredData struct {
	Pack           string `json:"pack"`
	Total          string `json:"total"`
	Amount         string `json:"amount"`
	PreviousAmount string `json:"previous_amount"`
	Reason         string `json:"reason"`
	Version        int    `json:"version"`
	ConfiguredBy   string `json:"configured_by"`
}
