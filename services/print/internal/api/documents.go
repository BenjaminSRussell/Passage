package api

import (
	"errors"
	"net/http"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/print/internal/domain"
)

const dutyRenderDocument = "print.document.render"

// renderDocument serves POST /print/v1/documents:render (issue #530). The caller sends a
// purchase order, customer invoice or packing slip as data; the answer is the PDF. Nothing
// is stored and no printer is involved, so the call needs no idempotency key and repeating
// it returns the same bytes.
func (a *API) renderDocument(w http.ResponseWriter, r *http.Request) {
	if _, ok := principal(w, r); !ok {
		return
	}
	var req domain.DocumentRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	pdf, err := domain.RenderDocument(req)
	var de *domain.Error
	if errors.As(err, &de) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusUnprocessableEntity, de.Code, de.Detail))
		return
	}
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "application/pdf")
	w.Header().Set("Content-Disposition", `attachment; filename="document.pdf"`)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(pdf)
}
