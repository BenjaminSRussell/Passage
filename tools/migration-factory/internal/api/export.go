package api

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/tenant"

	"github.com/BenjaminSRussell/passage/tools/migration-factory/internal/domain"
	"github.com/BenjaminSRussell/passage/tools/migration-factory/internal/store"
)

// exportFile serves one export of a cutover plan as CSV (9.49.15): the runbook with its timings, the
// rollback plan, the go/no-go checklist as evaluated now, the decision history, or the reconciliation
// of the plan's latest pack runs. The checklist shows attested items as the pending GO approval
// confirmed them, like the readiness view.
func (s *Server) exportFile(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	name := chi.URLParam(r, "file")
	known := false
	for _, f := range domain.ExportFiles {
		known = known || f == name
	}
	if !known {
		fail(w, r, invalid("file must be one of runbook.csv, rollback-plan.csv, go-no-go.csv, decisions.csv, reconciliation.csv"))
		return
	}
	// The reconciliation carries the runs' control totals, which are read under migration.factory.view (9.49.17), not
	// part of the runbook screen a cutover manager sees: it needs both duties.
	if name == domain.ExportReconciliation {
		if p, ok := tenant.FromContext(r.Context()); !ok || !p.HasDuty(DutyMigrationView) {
			httpx.Fail(w, r, httpx.NewProblem(http.StatusForbidden, "AUTH-0003", "reconciliation.csv carries control totals: it needs migration.factory.view as well as migration.cutover.manage"))
			return
		}
	}
	var body []byte
	switch name {
	case domain.ExportRunbook, domain.ExportRollbackPlan:
		plan, perr := s.store.GetPlan(r.Context(), id)
		if perr != nil {
			fail(w, r, perr)
			return
		}
		if name == domain.ExportRunbook {
			body, err = domain.RenderRunbookCSV(plan, s.now(), overrunAmberPct, overrunRedPct)
		} else {
			body, err = domain.RenderRollbackPlanCSV(plan)
		}
	default:
		facts, ferr := s.store.GetReadiness(r.Context(), id)
		if ferr != nil {
			fail(w, r, ferr)
			return
		}
		switch name {
		case domain.ExportDecisions:
			body, err = domain.RenderDecisionsCSV(facts.Decisions)
		case domain.ExportGoNoGo:
			var att []domain.Attestation
			var wv []domain.Waiver
			if len(facts.Pending) > 0 {
				att, wv = facts.Pending[0].Attestations, facts.Pending[0].Waivers
			}
			items, eerr := domain.EvaluateChecklist(facts, att, wv)
			if eerr != nil {
				fail(w, r, eerr)
				return
			}
			body, err = domain.RenderChecklistCSV(items)
		default:
			var runs []domain.ReconciliationRun
			latest := map[string]domain.PackFact{}
			for _, pf := range facts.Packs {
				latest[string(pf.Pack)+"|"+pf.Company] = pf
			}
			for _, pk := range facts.Plan.Packs {
				for _, e := range facts.Plan.Entities {
					rr := domain.ReconciliationRun{Pack: pk, Company: e}
					if pf, ok := latest[string(pk)+"|"+e]; ok {
						run, gerr := s.store.GetRun(r.Context(), pf.RunID)
						switch {
						case gerr == nil:
							rr.Run = &run
						case !errors.Is(gerr, domain.ErrNotFound) && !errors.Is(gerr, store.ErrNotFound):
							fail(w, r, gerr)
							return
						}
					}
					runs = append(runs, rr)
				}
			}
			body, err = domain.RenderReconciliationCSV(runs)
		}
	}
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="cutover-%s-%s"`, id, name))
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}
