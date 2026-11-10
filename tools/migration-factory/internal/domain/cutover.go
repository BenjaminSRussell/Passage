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
	// Entity is the legal entity of a per-entity step (a plan generated with ExpandByEntity); empty
	// for the steps that cover the whole cutover.
	Entity      string
	CompletedBy string
	CompletedAt *time.Time
	// StartedAt is set when the step is started; the runbook timer runs from it (FR-49.6).
	StartedAt *time.Time
	StartedBy string
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
	// Rekey counts the re-keying lines the rollback exported (FR-49.9); zero until rolled back.
	Rekey RekeySummary
	// RollbackPackRuns counts the pack runs the rollback moved to ROLLED_BACK (BR-49.1) and
	// RollbackCoexistence the coexistence flags it handed back to the legacy system (FR-49.7).
	RollbackPackRuns    int
	RollbackCoexistence int
	Version             int
	// LatestDecision is the most recent go/no-go decision (nil until one is recorded); the full
	// history is read with the readiness view.
	LatestDecision *Decision
	// PendingRollback is the first approval of a rollback in the current go/no-go decision round,
	// waiting for a second, different migration.cutover.manage (CUT-1603); nil when none.
	PendingRollback *RollbackApproval
	// RehearsalPlanID is the earlier plan whose measured durations this plan was generated from
	// (FR-49.6 "durations from the last rehearsal"); empty when none. RehearsalCarried counts the
	// steps whose planned duration came from a rehearsal, from that plan or the request.
	RehearsalPlanID  string
	RehearsalCarried int
	CreatedAt        time.Time
}

// MeasuredDurations returns the durations measured on this plan, by step name: the done steps with a
// recorded duration. A rehearsal plan's measurements become the next plan's planned durations.
func (p *Plan) MeasuredDurations() DurationOverrides {
	out := DurationOverrides{}
	for _, s := range p.Steps {
		if s.Status == StepDone && s.ActualDurationMin > 0 {
			out[s.Name] = s.ActualDurationMin
		}
	}
	return out
}

// RollbackApproval is the first approval of a rollback (CUT-1603, CUT-1605). It belongs to the
// go/no-go decision round it was given in, so a decision recorded in between voids it.
type RollbackApproval struct {
	Round       int       `json:"round"`
	Approver    string    `json:"approver"`
	PlanVersion int       `json:"planVersion"`
	ApprovedAt  time.Time `json:"approvedAt"`
}

// PrepareRollback applies the approval rules: approvers are authenticated people, never services and
// never names in a request. With no approval pending the caller's is the first and complete is
// false; a different caller's confirms it and both become the plan's approvers (first approver
// first). The same user cannot approve twice. Nothing is approved on a plan already rolled back.
func (p *Plan) PrepareRollback(by string, pending *RollbackApproval) (approvers []string, complete bool, err error) {
	if p.RolledBack {
		return nil, false, fmt.Errorf("%w: plan is already rolled back", ErrTransition)
	}
	by = strings.TrimSpace(by)
	if by == "" || strings.HasPrefix(by, "service:") {
		return nil, false, fmt.Errorf("%w: CUT-1605 a rollback must be approved by an identified person, not a service", ErrInvalid)
	}
	if pending == nil {
		return []string{by}, false, nil
	}
	if pending.Approver == by {
		return nil, false, fmt.Errorf("%w: CUT-1603 you have already approved this rollback; it needs a second approval by a different migration.cutover.manage", ErrTransition)
	}
	return []string{pending.Approver, by}, true, nil
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
	// RehearsalPlanID names the plan the Rehearsal durations were carried from, for the record.
	RehearsalPlanID string
	// ExpandByEntity turns each pack load into one load and one reconcile step per legal entity and
	// adds the per-entity freeze, extract, reconcile, sign-off and hypercare steps (D5a, the
	// `fx-cutover-plan` runbook: 3 entities and 9 packs give 84 steps).
	ExpandByEntity bool
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
	if in.ExpandByEntity && len(in.Entities) > MaxExpandedEntities {
		return Plan{}, fmt.Errorf("%w: a per-entity runbook covers at most %d legal entities", ErrInvalid, MaxExpandedEntities)
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
	if in.ExpandByEntity {
		steps = append(steps, entitySteps(in.Packs, in.Entities, dur)...)
	} else {
		for _, p := range in.Packs {
			off, prefix := packLoadOffset(p)
			name := fmt.Sprintf("%s %s", prefix, p)
			steps = append(steps, Step{OffsetMin: off, Name: name, Owner: "Migration engineer", PlannedDurationMin: dur(name, 60), Status: StepPending, Pack: string(p)})
		}
	}
	sort.SliceStable(steps, func(i, j int) bool { return steps[i].OffsetMin < steps[j].OffsetMin })
	for i := range steps {
		steps[i].Seq = i + 1
	}
	carried := 0
	for _, s := range steps {
		if _, ok := in.Rehearsal[s.Name]; ok {
			carried++
		}
	}
	return Plan{PlanID: in.PlanID, T0: in.T0, Packs: in.Packs, Entities: in.Entities, AbortOffsetMin: abortH * MinutesPerHour, Steps: steps, Version: 1,
		RehearsalPlanID: in.RehearsalPlanID, RehearsalCarried: carried}, nil
}

// MaxExpandedEntities caps the legal entities of a per-entity runbook: each entity adds 5 steps
// plus 2 per pack, and the plan is written and gated as one document.
const MaxExpandedEntities = 50

// Names of the per-entity steps. The generator writes them and the gate reads them back, so a
// stored plan keeps its dependencies without a separate column.
func entityFreezeName(e string) string  { return "Freeze legacy company " + e }
func entityExtractName(e string) string { return "Extract entity " + e }
func entityLoadName(p, e string) string { return fmt.Sprintf("Load pack %s for %s", p, e) }
func entityPackReconcileName(p, e string) string {
	return fmt.Sprintf("Reconcile pack %s for %s", p, e)
}
func entityReconcileName(e string) string { return "Reconcile entity " + e }
func entitySignOffName(e string) string   { return "Controller sign-off " + e }
func entityHypercareName(e string) string { return "Hypercare check " + e }

// entitySteps is the per-entity part of an expanded plan: for each legal entity the legacy freeze
// and the extract, then for each pack its load and its reconcile, then the entity's reconciliation,
// the controller sign-off and the hypercare check. None of them is a go/no-go step; the gate
// (prerequisites) orders them within the entity and holds the plan-wide go/no-go steps until every
// entity is signed off.
func entitySteps(packs []Pack, entities []string, dur func(string, int) int) []Step {
	var out []Step
	add := func(off int, name, owner string, def int, pack, entity string) {
		out = append(out, Step{OffsetMin: off, Name: name, Owner: owner, PlannedDurationMin: dur(name, def), Status: StepPending, Pack: pack, Entity: entity})
	}
	for _, e := range entities {
		add(-6*MinutesPerHour, entityFreezeName(e), "Legacy operations lead", 15, "", e)
		add(0, entityExtractName(e), "Migration engineer", 60, "", e)
		for _, p := range packs {
			off, _ := packLoadOffset(p)
			add(off, entityLoadName(string(p), e), "Migration engineer", 60, string(p), e)
			add(off+8*MinutesPerHour, entityPackReconcileName(string(p), e), "Controller", 30, string(p), e)
		}
		add(22*MinutesPerHour, entityReconcileName(e), "Controller", 60, "", e)
		add(23*MinutesPerHour, entitySignOffName(e), "Controller", 30, "", e)
		add(2*MinutesPerDay, entityHypercareName(e), "Controller", 60, "", e)
	}
	return out
}

// expanded reports whether the plan was generated with ExpandByEntity (it has per-entity steps).
func (p *Plan) expanded() bool {
	for _, s := range p.Steps {
		if s.Entity != "" {
			return true
		}
	}
	return false
}

// prerequisites names the steps that must be DONE before s can start or complete in a per-entity
// runbook (D5a): an entity's extract waits for its freeze; each pack load waits for the entity's
// freeze and extract; a pack reconcile waits for that pack's load; the entity reconciliation waits
// for every pack reconcile of the entity; the sign-off waits for the entity reconciliation; the
// hypercare check waits for the sign-off; and every plan-wide go/no-go step waits for the sign-off
// of every entity. A plan-wide runbook has none.
func (p *Plan) prerequisites(s Step) []string {
	e := s.Entity
	switch {
	case s.GoNoGo && e == "" && p.expanded():
		out := make([]string, 0, len(p.Entities))
		for _, ent := range p.Entities {
			out = append(out, entitySignOffName(ent))
		}
		return out
	case e == "":
		return nil
	case s.Name == entityExtractName(e):
		return []string{entityFreezeName(e)}
	case s.Pack != "" && s.Name == entityLoadName(s.Pack, e):
		return []string{entityFreezeName(e), entityExtractName(e)}
	case s.Pack != "" && s.Name == entityPackReconcileName(s.Pack, e):
		return []string{entityLoadName(s.Pack, e)}
	case s.Name == entityReconcileName(e):
		out := make([]string, 0, len(p.Packs))
		for _, pk := range p.Packs {
			out = append(out, entityPackReconcileName(string(pk), e))
		}
		return out
	case s.Name == entitySignOffName(e):
		return []string{entityReconcileName(e)}
	case s.Name == entityHypercareName(e):
		return []string{entitySignOffName(e)}
	}
	return nil
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

// indexOf returns the position of the step with the given sequence number.
func (p *Plan) indexOf(seq int) (int, error) {
	for i, s := range p.Steps {
		if s.Seq == seq {
			return i, nil
		}
	}
	return -1, fmt.Errorf("%w: step %d does not exist", ErrInvalid, seq)
}

// gate applies the go/no-go sequencing of 9.49.4 to the step at idx, for both starting and
// completing it: a go/no-go step waits for the earlier go/no-go steps, and the open-access step
// waits for every go/no-go item (CUT-1601). In a per-entity runbook a step also waits for its
// prerequisites (CUT-1609).
func (p *Plan) gate(idx int) error {
	s := p.Steps[idx]
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
	var waiting []string
	for _, name := range p.prerequisites(s) {
		if pre, ok := p.StepByName(name); ok && pre.Status != StepDone {
			waiting = append(waiting, name)
		}
	}
	if len(waiting) > 0 {
		return fmt.Errorf("%w: CUT-1609 step %q cannot start: it waits for %q", ErrInvalid, s.Name, waiting)
	}
	return nil
}

// Start records when a step began, so the runbook can show elapsed against planned while it runs
// (FR-49.6, 9.49.6). A step starts once, not after it is done, not on a rolled-back plan, and under
// the same go/no-go gate as completion.
func (p *Plan) Start(seq int, at time.Time, by string) error {
	idx, err := p.indexOf(seq)
	if err != nil {
		return err
	}
	if p.RolledBack {
		return fmt.Errorf("%w: plan was rolled back; its steps cannot be started", ErrTransition)
	}
	if at.IsZero() {
		return fmt.Errorf("%w: a start time is required", ErrInvalid)
	}
	s := &p.Steps[idx]
	if s.Status == StepDone {
		return fmt.Errorf("%w: step %d is already complete", ErrInvalid, seq)
	}
	if s.StartedAt != nil {
		return fmt.Errorf("%w: step %d already started at %s", ErrInvalid, seq, s.StartedAt.UTC().Format(time.RFC3339))
	}
	if err := p.gate(idx); err != nil {
		return err
	}
	started := at.UTC()
	s.StartedAt, s.StartedBy = &started, by
	p.Version++
	return nil
}

// MeasuredDuration returns the whole minutes a started step has run at now, for a completion that
// gives no duration of its own. A step that was never started has no measurement.
func (p *Plan) MeasuredDuration(seq int, now time.Time) (int, error) {
	idx, err := p.indexOf(seq)
	if err != nil {
		return 0, err
	}
	s := p.Steps[idx]
	if s.StartedAt == nil {
		return 0, fmt.Errorf("%w: step %d was not started, so actualDurationMin is required", ErrInvalid, seq)
	}
	return ElapsedMin(*s.StartedAt, now), nil
}

// ElapsedMin is the whole minutes from start to now, never negative.
func ElapsedMin(start, now time.Time) int {
	if now.Before(start) {
		return 0
	}
	return int(now.Sub(start) / time.Minute)
}

// Complete marks a step done. A go/no-go step may complete only after the earlier go/no-go
// steps are done; the open-access step may start only when every go/no-go item is done (9.49.4).
func (p *Plan) Complete(seq, actualMin int) error {
	idx, err := p.indexOf(seq)
	if err != nil {
		return err
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
	if err := p.gate(idx); err != nil {
		return err
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

// Running reports whether the step has started and is not yet done.
func (s Step) Running() bool { return s.StartedAt != nil && s.Status != StepDone }

// Timing is elapsed against planned for one step at now (9.49.6): a running step counts from its
// start, a done step reports its recorded duration, and a step not started has no timing (tone
// empty). A running step at or above the amber percentage carries the CUT-1602 warning.
func (s Step) Timing(now time.Time, amberPct, redPct int) (elapsedMin int, tone, warning string) {
	switch {
	case s.Status == StepDone:
		return s.ActualDurationMin, OverrunTone(s.PlannedDurationMin, s.ActualDurationMin, amberPct, redPct), ""
	case s.Running():
		elapsedMin = ElapsedMin(*s.StartedAt, now)
		tone = OverrunTone(s.PlannedDurationMin, elapsedMin, amberPct, redPct)
		if tone != ToneOK {
			warning = fmt.Sprintf("CUT-1602 Step %q at %d%% of planned duration.", s.Name, elapsedMin*100/s.PlannedDurationMin)
		}
		return elapsedMin, tone, warning
	}
	return 0, "", ""
}
