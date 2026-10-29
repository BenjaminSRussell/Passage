// Package events names the CloudEvents the migration factory writes to its outbox and the
// payload of each (PRD 9.49.5). events/asyncapi.yaml documents the same contract.
package events

import "github.com/BenjaminSRussell/passage/platform/events"

// Source is the CloudEvents source of every event this service emits.
const Source = "passage/migration-factory"

var (
	// PackReconciled is written when a pack run's reconciliation step has computed its totals.
	PackReconciled = events.Type("migration", "pack", "reconciled", 1)
	// CutoverStepCompleted is written when a runbook step is marked done.
	CutoverStepCompleted = events.Type("cutover", "step", "completed", 1)
	// CutoverPlanRolledBack is written when a cutover plan's rollback is executed (FR-49.9).
	CutoverPlanRolledBack = events.Type("cutover", "plan", "rolledback", 1)
)

// All lists every event type the service publishes.
func All() []string {
	return []string{PackReconciled, CutoverStepCompleted, CutoverPlanRolledBack}
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

// CutoverStepCompletedData is the payload of dev.passage.cutover.step.completed.v1.
type CutoverStepCompletedData struct {
	PlanID            string `json:"plan_id"`
	StepID            int    `json:"step_id"`
	Name              string `json:"name"`
	ActualDurationMin int    `json:"actual_duration_min"`
	Status            string `json:"status"`
	CompletedBy       string `json:"completed_by"`
}

// CutoverPlanRolledBackData is the payload of dev.passage.cutover.plan.rolledback.v1.
type CutoverPlanRolledBackData struct {
	PlanID     string   `json:"plan_id"`
	Approvers  []string `json:"approvers"`
	ExecutedBy string   `json:"executed_by"`
}
