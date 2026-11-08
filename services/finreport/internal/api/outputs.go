package api

import (
	"fmt"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/httpx"

	"github.com/BenjaminSRussell/passage/services/finreport/internal/outputs"
)

// CodeRenderFailed is REPORT-0016 REPORT_RENDER_FAILED (PRD §2.15.18): a
// renderer failed for one format; the other formats are unaffected.
const CodeRenderFailed = "REPORT-0016"

// runOutput renders a stored run as a file (FR-15.6, PRD §2.15.15). The file
// is rendered from the stored cells and the definition version of the run, so
// the same run always gives the same bytes.
func (a *API) runOutput(w http.ResponseWriter, r *http.Request) {
	id, ok := runID(w, r)
	if !ok {
		return
	}
	format := chi.URLParam(r, "format")
	out, ok := outputs.Formats[format]
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
	body, err := out.Render(def, cells, outputs.Stamp(run))
	if err != nil {
		slog.ErrorContext(r.Context(), "render run output", "run", run.ID, "format", format, "error", err)
		httpx.Fail(w, r, httpx.NewProblem(http.StatusInternalServerError, CodeRenderFailed,
			fmt.Sprintf("%s rendering failed for run %s.", format, run.ID)))
		return
	}
	w.Header().Set("Content-Type", out.ContentType)
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s-%s.%s"`, run.DefinitionCode, run.ID, format))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}
