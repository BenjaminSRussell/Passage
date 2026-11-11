package api

import (
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"

	"github.com/BenjaminSRussell/passage/tools/migration-factory/internal/domain"
)

type openingMappingRequest struct {
	LegalEntityID string `json:"legalEntityId"`
	LedgerID      string `json:"ledgerId"`
}

// putOpeningTarget records, under a cutover plan, the legal entity and ledger one legacy company's opening
// journal is booked to; the opening journal then refuses any other target for that company (#549).
func (s *Server) putOpeningTarget(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	sys, err := domain.ParseSourceSystem(chi.URLParam(r, "system"))
	if err != nil {
		fail(w, r, err)
		return
	}
	company := strings.TrimSpace(chi.URLParam(r, "company"))
	if company == "" {
		fail(w, r, invalid("company is required"))
		return
	}
	var req openingMappingRequest
	if _, err := readCommand(r, &req, 1<<16); err != nil {
		fail(w, r, err)
		return
	}
	p, ok := principal(w, r)
	if !ok {
		return
	}
	if err := legalEntity(p, req.LegalEntityID); err != nil {
		fail(w, r, err)
		return
	}
	ledgerID := strings.TrimSpace(req.LedgerID)
	if ledgerID == "" {
		fail(w, r, invalid("ledgerId is required"))
		return
	}
	m, err := s.store.SetOpeningMapping(r.Context(), id, sys, company, req.LegalEntityID, ledgerID, p.UserID)
	if err != nil {
		failOpening(w, r, err, nil)
		return
	}
	httpx.JSON(w, http.StatusOK, m)
}

func (s *Server) listOpeningTargets(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	out, err := s.store.ListOpeningMappings(r.Context(), id)
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"targets": out})
}

type releaseOpeningRequest struct {
	Reason string `json:"reason"`
}

// releaseOpeningTarget frees a company's pinned opening target when nothing is posted to it. An
// administrator act: the reason and the person are recorded.
func (s *Server) releaseOpeningTarget(w http.ResponseWriter, r *http.Request) {
	sys, err := domain.ParseSourceSystem(chi.URLParam(r, "system"))
	if err != nil {
		fail(w, r, err)
		return
	}
	company := strings.TrimSpace(chi.URLParam(r, "company"))
	if company == "" {
		fail(w, r, invalid("company is required"))
		return
	}
	var req releaseOpeningRequest
	if _, err := readCommand(r, &req, 1<<16); err != nil {
		fail(w, r, err)
		return
	}
	p, ok := principal(w, r)
	if !ok {
		return
	}
	rel, err := s.store.ReleaseOpeningTarget(r.Context(), sys, company, req.Reason, p.UserID)
	if errors.Is(err, dbx.ErrNotTenantWide) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusForbidden, "AUTH-0003", "releasing an opening target needs the tenant.administer duty and no legal-entity scope, because it checks the postings of every legal entity"))
		return
	}
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, rel)
}
