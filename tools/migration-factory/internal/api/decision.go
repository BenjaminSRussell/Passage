package api

import (
	"net/http"
	"time"

	"github.com/BenjaminSRussell/passage/platform/httpx"

	"github.com/BenjaminSRussell/passage/tools/migration-factory/internal/domain"
	"github.com/BenjaminSRussell/passage/tools/migration-factory/internal/store"
)

// decisionSummaryDTO is the latest go/no-go decision, carried on the plan.
type decisionSummaryDTO struct {
	Seq       int       `json:"seq"`
	Decision  string    `json:"decision"`
	Approvers []string  `json:"approvers"`
	Reason    string    `json:"reason,omitempty"`
	DecidedBy string    `json:"decidedBy,omitempty"`
	DecidedAt time.Time `json:"decidedAt"`
}

func toDecisionSummary(d *domain.Decision) *decisionSummaryDTO {
	if d == nil {
		return nil
	}
	return &decisionSummaryDTO{Seq: d.Seq, Decision: d.Decision, Approvers: d.Approvers, Reason: d.Reason, DecidedBy: d.DecidedBy, DecidedAt: d.DecidedAt}
}

// decisionDTO is a recorded decision with the checklist it was taken on.
type decisionDTO struct {
	decisionSummaryDTO
	PlanVersion int                 `json:"planVersion"`
	Checklist   []domain.ItemResult `json:"checklist"`
}

func toDecisionDTO(d domain.Decision) decisionDTO {
	s := toDecisionSummary(&d)
	return decisionDTO{decisionSummaryDTO: *s, PlanVersion: d.PlanVersion, Checklist: d.Checklist}
}

// readinessDTO is the go/no-go view of a plan: the checklist evaluated now (attested items show
// UNCONFIRMED until a decision attests them), whether every item the factory evaluates itself is met,
// and the decisions so far.
type readinessDTO struct {
	PlanID      string              `json:"planId"`
	Version     int                 `json:"version"`
	RolledBack  bool                `json:"rolledBack"`
	SystemReady bool                `json:"systemReady"`
	Items       []domain.ItemResult `json:"items"`
	OpenItems   []string            `json:"openItems"`
	Decisions   []decisionDTO       `json:"decisions"`
	// PendingGo is the first approval of a GO waiting for a second, different CutoverManager; nil when none.
	PendingGo *domain.GoApproval `json:"pendingGo,omitempty"`
}

func (s *Server) readiness(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	facts, err := s.store.GetReadiness(r.Context(), id)
	if err != nil {
		fail(w, r, err)
		return
	}
	// Attested items show as the pending GO approval confirmed them, so the second approver sees what
	// they are approving; without one they are UNCONFIRMED.
	var pending *domain.GoApproval
	var att []domain.Attestation
	if len(facts.Pending) > 0 {
		pending = &facts.Pending[0]
		att = pending.Attestations
	}
	items, err := domain.EvaluateChecklist(facts, att)
	if err != nil {
		fail(w, r, err)
		return
	}
	open := []string{}
	systemReady := !facts.Plan.RolledBack
	for _, it := range items {
		if it.Status != domain.ItemMet {
			open = append(open, it.Code)
			if it.Source == domain.ItemSourceSystem {
				systemReady = false
			}
		}
	}
	out := readinessDTO{PlanID: facts.Plan.PlanID, Version: facts.Plan.Version, RolledBack: facts.Plan.RolledBack,
		SystemReady: systemReady, Items: items, OpenItems: open, Decisions: make([]decisionDTO, 0, len(facts.Decisions)), PendingGo: pending}
	for _, d := range facts.Decisions {
		out.Decisions = append(out.Decisions, toDecisionDTO(d))
	}
	httpx.JSON(w, http.StatusOK, out)
}

// decisionRequest has no approvers: the approver is the authenticated caller (CUT-1605).
type decisionRequest struct {
	Decision     string               `json:"decision"`
	Reason       string               `json:"reason"`
	Attestations []domain.Attestation `json:"attestations"`
	Version      int                  `json:"version,omitempty"`
}

// decisionResponse is the plan after the request with either the recorded decision (201) or the
// first approval of a GO that waits for a second approver (202).
type decisionResponse struct {
	Decision  *decisionDTO       `json:"decision,omitempty"`
	PendingGo *domain.GoApproval `json:"pendingGo,omitempty"`
	Plan      planDTO            `json:"plan"`
}

func (s *Server) recordDecision(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	var req decisionRequest
	idem, err := readCommand(r, &req, 1<<20)
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
	if len(req.Attestations) > len(domain.Checklist) {
		fail(w, r, invalid("too many attestations"))
		return
	}
	res, replayed, err := s.store.RecordDecision(r.Context(), idem, store.DecisionRequest{PlanID: id, ExpectedVersion: req.Version,
		Input: domain.DecisionInput{Decision: req.Decision, Reason: req.Reason, Attestations: req.Attestations, By: approverID(p)}})
	if err != nil {
		fail(w, r, err)
		return
	}
	out := decisionResponse{PendingGo: res.Approval, Plan: s.toPlanDTO(res.Plan, false)}
	status := http.StatusAccepted
	if res.Decision != nil {
		d := toDecisionDTO(*res.Decision)
		out.Decision, status = &d, http.StatusCreated
	}
	respond(w, status, replayed, out)
}
