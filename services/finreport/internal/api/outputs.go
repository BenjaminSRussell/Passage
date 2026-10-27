package api

import (
	"fmt"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/httpx"

	"github.com/BenjaminSRussell/passage/services/finreport/internal/domain"
)

// runOutput renders a stored run as a file (FR-15.6, PRD §2.15.15). Only csv
// is rendered so far; pdf, xlsx and html answer 404 and are listed in
// NOT-IMPLEMENTED.md. The file is rendered from the stored cells and the
// definition version of the run, so the same run always gives the same bytes.
func (a *API) runOutput(w http.ResponseWriter, r *http.Request) {
	id, ok := runID(w, r)
	if !ok {
		return
	}
	format := chi.URLParam(r, "format")
	if format != "csv" {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, "COM-0404",
			fmt.Sprintf("output format %q is not available; csv is", format)))
		return
	}
	run, def, cells, err := a.store.RunOutput(r.Context(), id)
	if err != nil {
		fail(w, r, err)
		return
	}
	body, err := domain.RenderCSV(def, cells, domain.Stamp{
		RunID: run.ID, DefinitionCode: run.DefinitionCode, DefinitionVersion: run.DefinitionVersion,
		LedgerID: run.LedgerID, Period: fmt.Sprintf("%04d-%02d", run.PeriodYear, run.PeriodNo), CurrencyType: run.CurrencyType,
		HWM: run.HWMByLedger, ResultHash: run.ResultHash, GeneratedAt: run.StartedAt, GeneratedBy: run.RequestedBy,
	})
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s-%s.csv"`, run.DefinitionCode, run.ID))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}
