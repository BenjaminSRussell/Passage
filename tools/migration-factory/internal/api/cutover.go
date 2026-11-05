package api

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"

	"github.com/BenjaminSRussell/passage/tools/migration-factory/internal/domain"
	"github.com/BenjaminSRussell/passage/tools/migration-factory/internal/store"
)

type generateRequest struct {
	T0                 time.Time      `json:"t0"`
	Packs              []string       `json:"packs"`
	Entities           []string       `json:"entities"`
	AbortOffsetHours   int            `json:"abortOffsetHours"`
	RehearsalDurations map[string]int `json:"rehearsalDurationsMin"`
	// RehearsalPlanID carries the durations measured on that earlier plan into this one (FR-49.6);
	// rehearsalDurationsMin entries override its values.
	RehearsalPlanID string `json:"rehearsalPlanId"`
	// ExpandByEntity generates the per-entity runbook: a load and a reconcile step per pack and
	// legal entity, and the per-entity freeze, extract, reconcile, sign-off and hypercare steps.
	ExpandByEntity bool `json:"expandByEntity"`
}

type stepDTO struct {
	Seq                int        `json:"seq"`
	OffsetMin          int        `json:"offsetMin"`
	Name               string     `json:"name"`
	Owner              string     `json:"owner"`
	PlannedDurationMin int        `json:"plannedDurationMin"`
	ActualDurationMin  int        `json:"actualDurationMin"`
	Status             string     `json:"status"`
	GoNoGo             bool       `json:"goNoGo"`
	Pack               string     `json:"pack,omitempty"`
	Entity             string     `json:"entity,omitempty"`
	CompletedBy        string     `json:"completedBy,omitempty"`
	CompletedAt        *time.Time `json:"completedAt,omitempty"`
	// StartedAt is set once the step is started; the runbook timer runs from it (FR-49.6).
	StartedAt *time.Time `json:"startedAt,omitempty"`
	StartedBy string     `json:"startedBy,omitempty"`
	// ElapsedMin, Tone and OverrunWarning compare elapsed with planned at the time of the
	// response (9.49.6): from the start for a running step, the recorded duration for a done one.
	// They are left out for a step that has not started.
	ElapsedMin     *int   `json:"elapsedMin,omitempty"`
	Tone           string `json:"tone,omitempty"`
	OverrunWarning string `json:"overrunWarning,omitempty"`
}

// rollbackStepDTO is one step of the generated rollback plan (FR-49.9).
type rollbackStepDTO struct {
	Seq                int    `json:"seq"`
	OffsetMin          int    `json:"offsetMin"`
	Name               string `json:"name"`
	Owner              string `json:"owner"`
	PlannedDurationMin int    `json:"plannedDurationMin"`
	DecisionPoint      bool   `json:"decisionPoint"`
}

// Runbook overrun thresholds (cutover.step_overrun_amber_pct / red_pct defaults, 9.49.13).
const (
	overrunAmberPct = 110
	overrunRedPct   = 125
)

type planDTO struct {
	PlanID         string    `json:"planId"`
	T0             time.Time `json:"t0"`
	Packs          []string  `json:"packs"`
	Entities       []string  `json:"entities"`
	AbortOffsetMin int       `json:"abortOffsetMin"`
	RolledBack     bool      `json:"rolledBack"`
	Approvers      []string  `json:"approvers"`
	Version        int       `json:"version"`
	CreatedAt      time.Time `json:"createdAt"`
	StepsDone      int       `json:"stepsDone"`
	StepsTotal     int       `json:"stepsTotal"`
	OpenGoNoGo     []string  `json:"openGoNoGo"`
	// AbortDue and AbortWarning report BR-49.9 at the time of the response: the abort deadline
	// has passed with go/no-go items open, or it is less than an hour away (CUT-1604).
	AbortDue     bool   `json:"abortDue"`
	AbortWarning string `json:"abortWarning,omitempty"`
	// Decision is the latest go/no-go decision, nil until one is recorded.
	Decision *decisionSummaryDTO `json:"decision,omitempty"`
	// RehearsalPlanID and RehearsalCarried say which earlier plan the planned durations were measured on
	// and how many steps took one (FR-49.6).
	RehearsalPlanID  string `json:"rehearsalPlanId,omitempty"`
	RehearsalCarried int    `json:"rehearsalCarried"`
	// PendingRollback is the first approval of a rollback, waiting for a second, different migration.cutover.manage.
	PendingRollback *pendingRollbackDTO `json:"pendingRollback,omitempty"`
	// Rekeying is set once the plan is rolled back: the lines exported for re-keying (FR-49.9).
	Rekeying *rekeyingDTO `json:"rekeying,omitempty"`
	Steps    []stepDTO    `json:"steps,omitempty"`
	// RollbackSteps is the generated rollback plan (FR-49.9); sent with the steps.
	RollbackSteps []rollbackStepDTO `json:"rollbackSteps,omitempty"`
}

func (s *Server) toPlanDTO(p domain.Plan, withSteps bool) planDTO {
	out := planDTO{PlanID: p.PlanID, T0: p.T0, Entities: p.Entities, AbortOffsetMin: p.AbortOffsetMin, RolledBack: p.RolledBack,
		Approvers: p.Approvers, Version: p.Version, CreatedAt: p.CreatedAt, Packs: []string{}, OpenGoNoGo: p.OpenGoNoGo(),
		Decision: toDecisionSummary(p.LatestDecision), RehearsalPlanID: p.RehearsalPlanID, RehearsalCarried: p.RehearsalCarried}
	if out.Approvers == nil {
		out.Approvers = []string{}
	}
	if out.Entities == nil {
		out.Entities = []string{}
	}
	if out.OpenGoNoGo == nil {
		out.OpenGoNoGo = []string{}
	}
	if p.PendingRollback != nil && !p.RolledBack {
		out.PendingRollback = &pendingRollbackDTO{Approver: p.PendingRollback.Approver, ApprovedAt: p.PendingRollback.ApprovedAt}
	}
	for _, pk := range p.Packs {
		out.Packs = append(out.Packs, string(pk))
	}
	for _, st := range p.Steps {
		if st.Status == domain.StepDone {
			out.StepsDone++
		}
	}
	out.StepsTotal = len(p.Steps)
	if p.RolledBack {
		out.Rekeying = &rekeyingDTO{JournalLines: p.Rekey.JournalLines, DocumentLines: p.Rekey.DocumentLines, Excluded: p.Rekey.Excluded}
	} else {
		out.AbortDue, out.AbortWarning = p.AbortDue(int(s.now().Sub(p.T0).Minutes()))
	}
	if withSteps {
		now := s.now()
		out.Steps = make([]stepDTO, 0, len(p.Steps))
		for _, st := range p.Steps {
			d := stepDTO{Seq: st.Seq, OffsetMin: st.OffsetMin, Name: st.Name, Owner: st.Owner, PlannedDurationMin: st.PlannedDurationMin,
				ActualDurationMin: st.ActualDurationMin, Status: st.Status, GoNoGo: st.GoNoGo, Pack: st.Pack, Entity: st.Entity, CompletedBy: st.CompletedBy, CompletedAt: st.CompletedAt,
				StartedAt: st.StartedAt, StartedBy: st.StartedBy}
			if elapsed, tone, warning := st.Timing(now, overrunAmberPct, overrunRedPct); tone != "" {
				d.ElapsedMin, d.Tone, d.OverrunWarning = &elapsed, tone, warning
			}
			out.Steps = append(out.Steps, d)
		}
		for _, rs := range p.RollbackPlan() {
			out.RollbackSteps = append(out.RollbackSteps, rollbackStepDTO{Seq: rs.Seq, OffsetMin: rs.OffsetMin, Name: rs.Name, Owner: rs.Owner,
				PlannedDurationMin: rs.PlannedDurationMin, DecisionPoint: rs.DecisionPoint})
		}
	}
	return out
}

func (s *Server) generatePlan(w http.ResponseWriter, r *http.Request) {
	var req generateRequest
	idem, err := readCommand(r, &req, 1<<20)
	if err != nil {
		fail(w, r, err)
		return
	}
	p, ok := principal(w, r)
	if !ok {
		return
	}
	packs := make([]domain.Pack, 0, len(req.Packs))
	for _, name := range req.Packs {
		pk, err := domain.ParsePack(name)
		if err != nil {
			fail(w, r, err)
			return
		}
		packs = append(packs, pk)
	}
	rehearsal := domain.DurationOverrides{}
	if id := strings.TrimSpace(req.RehearsalPlanID); id != "" {
		if !ids.Valid(id) {
			fail(w, r, invalid("rehearsalPlanId must be a plan id"))
			return
		}
		source, err := s.store.GetPlan(r.Context(), id)
		if err != nil {
			fail(w, r, err)
			return
		}
		rehearsal = source.MeasuredDurations()
		if len(rehearsal) == 0 {
			fail(w, r, invalid("the rehearsal plan has no measured step durations yet: complete its steps first"))
			return
		}
		req.RehearsalPlanID = source.PlanID
	}
	for name, d := range req.RehearsalDurations {
		rehearsal[name] = d
	}
	plan, err := domain.GeneratePlan(domain.PlanInput{
		PlanID: ids.New(), T0: req.T0.UTC(), Packs: packs, Entities: req.Entities,
		AbortOffsetH: req.AbortOffsetHours, Rehearsal: rehearsal, RehearsalPlanID: req.RehearsalPlanID, ExpandByEntity: req.ExpandByEntity,
	})
	if err != nil {
		fail(w, r, err)
		return
	}
	out, replayed, err := s.store.CreatePlan(r.Context(), idem, plan, p.UserID)
	if err != nil {
		fail(w, r, err)
		return
	}
	respond(w, http.StatusCreated, replayed, s.toPlanDTO(out, true))
}

func (s *Server) listPlans(w http.ResponseWriter, r *http.Request) {
	var f store.PlanFilter
	var err error
	if f.Limit, err = listLimit(r); err != nil {
		fail(w, r, err)
		return
	}
	if f.After, err = after(r); err != nil {
		fail(w, r, err)
		return
	}
	plans, err := s.store.ListPlans(r.Context(), f)
	if err != nil {
		fail(w, r, err)
		return
	}
	items := make([]planDTO, 0, len(plans))
	for _, p := range plans {
		items = append(items, s.toPlanDTO(p, false))
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) getPlan(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	plan, err := s.store.GetPlan(r.Context(), id)
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, s.toPlanDTO(plan, true))
}

// completeRequest completes a step. Without actualDurationMin the duration is measured from the
// step's start; a step that was never started needs it.
type completeRequest struct {
	ActualDurationMin *int `json:"actualDurationMin"`
	Version           int  `json:"version,omitempty"`
}

type startRequest struct {
	Version int `json:"version,omitempty"`
}

// startStep starts a runbook step so the runbook can time it against its planned duration (FR-49.6).
func (s *Server) startStep(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	seq, err := parseSeq(chi.URLParam(r, "seq"))
	if err != nil {
		fail(w, r, err)
		return
	}
	var req startRequest
	idem, err := readCommand(r, &req, 1<<16)
	if err != nil {
		fail(w, r, err)
		return
	}
	p, ok := principal(w, r)
	if !ok {
		return
	}
	if req.Version < 0 {
		fail(w, r, invalid("version must be >= 1 when given"))
		return
	}
	plan, replayed, err := s.store.StartStep(r.Context(), idem, store.StepStart{PlanID: id, Seq: seq, ExpectedVersion: req.Version, By: p.UserID})
	if err != nil {
		fail(w, r, err)
		return
	}
	respond(w, http.StatusOK, replayed, s.toPlanDTO(plan, true))
}

func parseSeq(s string) (int, error) {
	n, err := strconv.Atoi(s)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("%w: step sequence %q is not a positive integer", domain.ErrInvalid, s)
	}
	return n, nil
}

func (s *Server) completeStep(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	seq, err := parseSeq(chi.URLParam(r, "seq"))
	if err != nil {
		fail(w, r, err)
		return
	}
	var req completeRequest
	idem, err := readCommand(r, &req, 1<<16)
	if err != nil {
		fail(w, r, err)
		return
	}
	p, ok := principal(w, r)
	if !ok {
		return
	}
	if req.Version < 0 {
		fail(w, r, invalid("version must be >= 1 when given"))
		return
	}
	plan, replayed, err := s.store.CompleteStep(r.Context(), idem, store.StepCompletion{
		PlanID: id, Seq: seq, ActualDurationMin: req.ActualDurationMin, ExpectedVersion: req.Version, By: p.UserID,
	})
	if err != nil {
		fail(w, r, err)
		return
	}
	respond(w, http.StatusOK, replayed, s.toPlanDTO(plan, true))
}

// rollbackRequest has no approvers: the approver is the authenticated caller (CUT-1603, CUT-1605).
type rollbackRequest struct {
	Postings []postingDTO `json:"postings"`
	Version  int          `json:"version,omitempty"`
}

// pendingRollbackDTO is the first approval of a rollback, waiting for a second migration.cutover.manage.
type pendingRollbackDTO struct {
	Approver   string    `json:"approver"`
	ApprovedAt time.Time `json:"approvedAt"`
}

func (s *Server) rollback(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	var req rollbackRequest
	idem, err := readCommand(r, &req, rollbackBodyLimit)
	if err != nil {
		fail(w, r, err)
		return
	}
	p, ok := principal(w, r)
	if !ok {
		return
	}
	if req.Version < 0 {
		fail(w, r, invalid("version must be >= 1 when given"))
		return
	}
	postings, err := toPostings(req.Postings)
	if err != nil {
		fail(w, r, err)
		return
	}
	res, replayed, err := s.store.Rollback(r.Context(), idem, store.RollbackRequest{PlanID: id, Postings: postings,
		ExpectedVersion: req.Version, By: approverID(p)})
	if err != nil {
		fail(w, r, err)
		return
	}
	status := http.StatusOK
	if res.Pending != nil {
		status = http.StatusAccepted
	}
	respond(w, status, replayed, s.toPlanDTO(res.Plan, true))
}
