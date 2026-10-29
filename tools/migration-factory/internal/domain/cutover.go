package domain

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// Offsets are minutes from T-0 (the cutover anchor, 00:00 of cutover day).
const (
	MinutesPerHour = 60
	MinutesPerDay  = 24 * MinutesPerHour
)

// Overrun tones for the runbook (FR-49.6 / 9.49.6): amber at 110 %, red at 125 % by default.
const (
	ToneOK    = "ok"
	ToneAmber = "amber"
	ToneRed   = "red"
)

// Step is one timed runbook step (cutover_step, 9.49.4). Status is PENDING or DONE;
// Pack is non-empty for pack load steps.
type Step struct {
	Seq                int
	OffsetMin          int
	Name               string
	Owner              string
	PlannedDurationMin int
	ActualDurationMin  int
	Status             string
	GoNoGo             bool
	Pack               string
	CompletedBy        string
	CompletedAt        *time.Time
}

// Step status values.
const (
	StepPending = "PENDING"
	StepDone    = "DONE"
)

// Plan is a generated cutover_plan. Version starts at 1 and increases on every change, so a
// client that read an older plan cannot complete a step or roll back over a newer one.
type Plan struct {
	PlanID         string
	T0             time.Time
	Packs          []Pack
	Entities       []string
	AbortOffsetMin int
	Steps          []Step
	RolledBack     bool
	Approvers      []string
	Version        int
	CreatedAt      time.Time
}

// DurationOverrides maps a step name to the duration measured in the last rehearsal (minutes).
type DurationOverrides map[string]int

// PlanInput is what the planner needs.
type PlanInput struct {
	PlanID       string
	T0           time.Time
	Packs        []Pack
	Entities     []string
	AbortOffsetH int
	Rehearsal    DurationOverrides
}

// Name constants used by the timeline and by the open-access rule.
const (
	StepOpenAccess = "Open user access"
	StepReconcile  = "Reconciliation complete"
	StepGoNoGo     = "Go/no-go meeting"
	StepAbort      = "Abort deadline"
)

type template struct {
	offset int
	name   string
	owner  string
	dur    int
	goNoGo bool
}

// timeline is the FR-49.6 baseline. "T+1d first postings" is placed at T+2d because the PRD
// orders it after user access at T+30h; see NOT-IMPLEMENTED.md.
var timeline = []template{
	{-14 * MinutesPerDay, "Freeze master-data changes in legacy; final pack dry run", "Legacy data steward", 240, false},
	{-7 * MinutesPerDay, "Second rehearsal; sign off variance report", "Controller", 480, false},
	{-3 * MinutesPerDay, "Freeze configuration in Passage", "Cutover manager", 120, false},
	{-6 * MinutesPerHour, "Legacy transaction freeze (18:00)", "Legacy operations lead", 30, false},
	{-6 * MinutesPerHour, "Final extracts of master and static reference data", "Migration engineer", 120, false},
	{0, "Extract transactional packs", "Migration engineer", 180, false},
	{4 * MinutesPerHour, "Load and reconcile master data and balances", "Migration engineer", 360, false},
	{12 * MinutesPerHour, "Load open items, orders, on-hand, WIP", "Migration engineer", 720, false},
	{24 * MinutesPerHour, StepReconcile, "Controller", 240, true},
	{24 * MinutesPerHour, StepGoNoGo, "Cutover manager", 60, true},
	{30 * MinutesPerHour, StepOpenAccess, "Cutover manager", 30, false},
	{36 * MinutesPerHour, StepAbort, "Cutover manager", 0, false},
	{2 * MinutesPerDay, "First postings; hypercare starts", "Controller", 60, false},
	{7 * MinutesPerDay, "First weekly reconciliation", "Controller", 120, false},
	{30 * MinutesPerDay, "First month-end close; hypercare review", "Controller", 480, false},
}

// packLoadOffset is the timeline slot for each pack's load step (T+4h balances, T+12h transactions).
func packLoadOffset(p Pack) (int, string) {
	switch p {
	case PackGLBalances, PackBankBal, PackFixedAssets:
		return 4 * MinutesPerHour, "Load pack"
	default:
		return 12 * MinutesPerHour, "Load pack"
	}
}

// GeneratePlan builds the timed runbook from the timeline baseline, the selected packs and the
// rehearsal durations. Steps are sorted by offset and numbered from 1.
func GeneratePlan(in PlanInput) (Plan, error) {
	if in.T0.IsZero() {
		return Plan{}, fmt.Errorf("%w: cutover.t0 is required", ErrInvalid)
	}
	if len(in.Packs) == 0 {
		return Plan{}, fmt.Errorf("%w: at least one pack is required", ErrInvalid)
	}
	if len(in.Entities) == 0 {
		return Plan{}, fmt.Errorf("%w: at least one legal entity is required", ErrInvalid)
	}
	seenPack := map[Pack]bool{}
	for _, p := range in.Packs {
		if seenPack[p] {
			return Plan{}, fmt.Errorf("%w: pack %s is selected twice", ErrInvalid, p)
		}
		seenPack[p] = true
	}
	seenEntity := map[string]bool{}
	for _, e := range in.Entities {
		if strings.TrimSpace(e) == "" || seenEntity[e] {
			return Plan{}, fmt.Errorf("%w: legal entities must be distinct and not blank", ErrInvalid)
		}
		seenEntity[e] = true
	}
	for name, d := range in.Rehearsal {
		if d < 0 {
			return Plan{}, fmt.Errorf("%w: rehearsal duration for %q must be >= 0", ErrInvalid, name)
		}
	}
	abortH := in.AbortOffsetH
	if abortH == 0 {
		abortH = 36
	}
	if abortH < 12 || abortH > 72 {
		return Plan{}, fmt.Errorf("%w: cutover.abort_offset_h must be 12-72", ErrInvalid)
	}
	var steps []Step
	dur := func(name string, def int) int {
		if d, ok := in.Rehearsal[name]; ok {
			return d
		}
		return def
	}
	for _, t := range timeline {
		if t.name == StepAbort {
			// BR-49.9: the decision point sits at cutover.abort_offset_h, not at the 36 h default.
			t.offset = abortH * MinutesPerHour
		}
		steps = append(steps, Step{OffsetMin: t.offset, Name: t.name, Owner: t.owner, PlannedDurationMin: dur(t.name, t.dur), Status: StepPending, GoNoGo: t.goNoGo})
	}
	for _, p := range in.Packs {
		off, prefix := packLoadOffset(p)
		name := fmt.Sprintf("%s %s", prefix, p)
		steps = append(steps, Step{OffsetMin: off, Name: name, Owner: "Migration engineer", PlannedDurationMin: dur(name, 60), Status: StepPending, Pack: string(p)})
	}
	sort.SliceStable(steps, func(i, j int) bool { return steps[i].OffsetMin < steps[j].OffsetMin })
	for i := range steps {
		steps[i].Seq = i + 1
	}
	return Plan{PlanID: in.PlanID, T0: in.T0, Packs: in.Packs, Entities: in.Entities, AbortOffsetMin: abortH * MinutesPerHour, Steps: steps, Version: 1}, nil
}

// StepByName returns the step with the given name.
func (p *Plan) StepByName(name string) (*Step, bool) {
	for i := range p.Steps {
		if p.Steps[i].Name == name {
			return &p.Steps[i], true
		}
	}
	return nil, false
}

// OpenGoNoGo returns the names of go/no-go steps not yet DONE.
func (p *Plan) OpenGoNoGo() []string {
	var open []string
	for _, s := range p.Steps {
		if s.GoNoGo && s.Status != StepDone {
			open = append(open, s.Name)
		}
	}
	return open
}

// Complete marks a step done. A go/no-go step may complete only after the earlier go/no-go
// steps are done; the open-access step may start only when every go/no-go item is done (9.49.4).
func (p *Plan) Complete(seq, actualMin int) error {
	idx := -1
	for i, s := range p.Steps {
		if s.Seq == seq {
			idx = i
			break
		}
	}
	if idx < 0 {
		return fmt.Errorf("%w: step %d does not exist", ErrInvalid, seq)
	}
	if p.RolledBack {
		return fmt.Errorf("%w: plan was rolled back; its steps cannot be completed", ErrTransition)
	}
	if actualMin < 0 {
		return fmt.Errorf("%w: actual duration must be >= 0 minutes", ErrInvalid)
	}
	s := &p.Steps[idx]
	if s.Status == StepDone {
		return fmt.Errorf("%w: step %d is already complete", ErrInvalid, seq)
	}
	if s.GoNoGo {
		for _, earlier := range p.Steps[:idx] {
			if earlier.GoNoGo && earlier.Status != StepDone {
				return fmt.Errorf("%w: CUT-1601 step %q cannot start: go/no-go items %q are open", ErrInvalid, s.Name, earlier.Name)
			}
		}
	}
	if s.Name == StepOpenAccess {
		if open := p.OpenGoNoGo(); len(open) > 0 {
			return fmt.Errorf("%w: CUT-1601 step %q cannot start: go/no-go items %v are open", ErrInvalid, s.Name, open)
		}
	}
	s.Status = StepDone
	s.ActualDurationMin = actualMin
	p.Version++
	return nil
}

// AbortDue reports the BR-49.9 state at minute offset now (from T-0): mustRollback when the
// abort deadline has passed with go/no-go items open. warn is set in the hour before the deadline.
func (p *Plan) AbortDue(now int) (mustRollback bool, warn string) {
	open := p.OpenGoNoGo()
	left := p.AbortOffsetMin - now
	if left <= MinutesPerHour && len(open) > 0 && left > 0 {
		warn = fmt.Sprintf("CUT-1604 Abort deadline T+%dh in %d min with open go/no-go items.", p.AbortOffsetMin/MinutesPerHour, left)
	}
	return now >= p.AbortOffsetMin && len(open) > 0, warn
}

// Rollback records the rollback execution. It needs two distinct approvers (CUT-1603) and
// runs once: a plan already rolled back is rejected as a state transition.
func (p *Plan) Rollback(approvers ...string) error {
	if p.RolledBack {
		return fmt.Errorf("%w: plan is already rolled back", ErrTransition)
	}
	distinct := map[string]struct{}{}
	var ordered []string
	for _, a := range approvers {
		a = strings.TrimSpace(a)
		if _, dup := distinct[a]; a != "" && !dup {
			distinct[a] = struct{}{}
			ordered = append(ordered, a)
		}
	}
	if len(distinct) < 2 {
		return fmt.Errorf("%w: CUT-1603 rollback requires two distinct approvers", ErrInvalid)
	}
	p.RolledBack = true
	p.Approvers = ordered
	p.Version++
	return nil
}

// OverrunTone classifies elapsed against planned using the amber (110) and red (125) percentages.
func OverrunTone(plannedMin, actualMin, amberPct, redPct int) string {
	if plannedMin <= 0 {
		return ToneOK
	}
	pct := actualMin * 100 / plannedMin
	switch {
	case pct >= redPct:
		return ToneRed
	case pct >= amberPct:
		return ToneAmber
	}
	return ToneOK
}
