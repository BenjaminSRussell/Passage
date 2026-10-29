package domain

// RollbackStep is one step of the rollback plan generated for a cutover (FR-49.9). The plan is
// derived from the cutover plan, so it is not stored: the first step is the decision point at the
// abort deadline (cutover.abort_offset_h, BR-49.9) and each later step starts when the one before
// it is planned to end.
type RollbackStep struct {
	Seq                int
	OffsetMin          int
	Name               string
	Owner              string
	PlannedDurationMin int
	DecisionPoint      bool
}

// Rollback step names, in execution order (FR-49.9, AC-49.7).
const (
	RollbackDecision       = "Decision point: go/no-go items still open at the abort deadline"
	RollbackStopAccess     = "Stop Passage user access"
	RollbackRestoreLegacy  = "Restore legacy write access"
	RollbackResumeAdapters = "Resume legacy coexistence adapters"
	RollbackExport         = "Export Passage postings since T-0 as re-keying files"
	RollbackRekey          = "Re-key the exported postings in legacy and reconcile"
)

var rollbackTimeline = []RollbackStep{
	{Name: RollbackDecision, Owner: "Cutover manager", PlannedDurationMin: 30, DecisionPoint: true},
	{Name: RollbackStopAccess, Owner: "Cutover manager", PlannedDurationMin: 15},
	{Name: RollbackRestoreLegacy, Owner: "Legacy operations lead", PlannedDurationMin: 30},
	{Name: RollbackResumeAdapters, Owner: "Migration engineer", PlannedDurationMin: 60},
	{Name: RollbackExport, Owner: "Migration engineer", PlannedDurationMin: 60},
	{Name: RollbackRekey, Owner: "Controller", PlannedDurationMin: 480},
}

// RollbackPlan returns the plan's rollback steps, numbered from 1, with offsets from T-0.
func (p *Plan) RollbackPlan() []RollbackStep {
	out := make([]RollbackStep, len(rollbackTimeline))
	at := p.AbortOffsetMin
	for i, s := range rollbackTimeline {
		s.Seq = i + 1
		s.OffsetMin = at
		at += s.PlannedDurationMin
		out[i] = s
	}
	return out
}
