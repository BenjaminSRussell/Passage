package api

import (
	"fmt"
	"net/http"
	"strconv"
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
	CompletedBy        string     `json:"completedBy,omitempty"`
	CompletedAt        *time.Time `json:"completedAt,omitempty"`
}

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
	// Rekeying is set once the plan is rolled back: the lines exported for re-keying (FR-49.9).
	Rekeying *rekeyingDTO `json:"rekeying,omitempty"`
	Steps    []stepDTO    `json:"steps,omitempty"`
}

func (s *Server) toPlanDTO(p domain.Plan, withSteps bool) planDTO {
	out := planDTO{PlanID: p.PlanID, T0: p.T0, Entities: p.Entities, AbortOffsetMin: p.AbortOffsetMin, RolledBack: p.RolledBack,
		Approvers: p.Approvers, Version: p.Version, CreatedAt: p.CreatedAt, Packs: []string{}, OpenGoNoGo: p.OpenGoNoGo()}
	if out.Approvers == nil {
		out.Approvers = []string{}
	}
	if out.Entities == nil {
		out.Entities = []string{}
	}
	if out.OpenGoNoGo == nil {
		out.OpenGoNoGo = []string{}
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
		out.Steps = make([]stepDTO, 0, len(p.Steps))
		for _, st := range p.Steps {
			out.Steps = append(out.Steps, stepDTO{Seq: st.Seq, OffsetMin: st.OffsetMin, Name: st.Name, Owner: st.Owner, PlannedDurationMin: st.PlannedDurationMin,
				ActualDurationMin: st.ActualDurationMin, Status: st.Status, GoNoGo: st.GoNoGo, Pack: st.Pack, CompletedBy: st.CompletedBy, CompletedAt: st.CompletedAt})
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
	plan, err := domain.GeneratePlan(domain.PlanInput{
		PlanID: ids.New(), T0: req.T0.UTC(), Packs: packs, Entities: req.Entities,
		AbortOffsetH: req.AbortOffsetHours, Rehearsal: req.RehearsalDurations,
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

type completeRequest struct {
	ActualDurationMin int `json:"actualDurationMin"`
	Version           int `json:"version,omitempty"`
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

type rollbackRequest struct {
	Approvers []string     `json:"approvers"`
	Postings  []postingDTO `json:"postings"`
	Version   int          `json:"version,omitempty"`
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
	plan, replayed, err := s.store.Rollback(r.Context(), idem, store.RollbackRequest{PlanID: id, Approvers: req.Approvers, Postings: postings,
		ExpectedVersion: req.Version, By: p.UserID})
	if err != nil {
		fail(w, r, err)
		return
	}
	respond(w, http.StatusOK, replayed, s.toPlanDTO(plan, true))
}
