package api

import (
	"fmt"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/httpx"

	"github.com/BenjaminSRussell/passage/services/finreport/internal/domain"
)

// CodeRenderFailed is REPORT-0016 REPORT_RENDER_FAILED (PRD §2.15.18): a
// renderer failed for one format; the other formats are unaffected.
const CodeRenderFailed = "REPORT-0016"

// outputFormat is one rendered file format of a run.
type outputFormat struct {
	contentType string
	render      func(domain.Definition, []domain.Cell, domain.Stamp) ([]byte, error)
}

// outputFormats are the formats rendered so far (FR-15.6, PRD §2.15.15). All
// of them render the same stored cells and stamp, so the files of one run
// agree. pdf answers 404 and is listed in NOT-IMPLEMENTED.md: it needs the
// Chromium renderer sidecar.
var outputFormats = map[string]outputFormat{
	"csv":  {"text/csv; charset=utf-8", domain.RenderCSV},
	"html": {"text/html; charset=utf-8", domain.RenderHTML},
	"xlsx": {"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", domain.RenderXLSX},
}

// runOutput renders a stored run as a file (FR-15.6, PRD §2.15.15). The file
// is rendered from the stored cells and the definition version of the run, so
// the same run always gives the same bytes.
func (a *API) runOutput(w http.ResponseWriter, r *http.Request) {
	id, ok := runID(w, r)
	if !ok {
		return
	}
	format := chi.URLParam(r, "format")
	out, ok := outputFormats[format]
	if !ok {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, "COM-0404",
			fmt.Sprintf("output format %q is not available; csv, html and xlsx are", format)))
		return
	}
	run, def, cells, err := a.store.RunOutput(r.Context(), id)
	if err != nil {
		fail(w, r, err)
		return
	}
	body, err := out.render(def, cells, domain.Stamp{
		RunID: run.ID, DefinitionCode: run.DefinitionCode, DefinitionVersion: run.DefinitionVersion,
		LedgerID: run.LedgerID, Period: fmt.Sprintf("%04d-%02d", run.PeriodYear, run.PeriodNo), CurrencyType: run.CurrencyType,
		HWM: run.HWMByLedger, ResultHash: run.ResultHash, GeneratedAt: run.StartedAt, GeneratedBy: run.RequestedBy,
	})
	if err != nil {
		slog.ErrorContext(r.Context(), "render run output", "run", run.ID, "format", format, "error", err)
		httpx.Fail(w, r, httpx.NewProblem(http.StatusInternalServerError, CodeRenderFailed,
			fmt.Sprintf("%s rendering failed for run %s.", format, run.ID)))
		return
	}
	w.Header().Set("Content-Type", out.contentType)
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s-%s.%s"`, run.DefinitionCode, run.ID, format))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}
