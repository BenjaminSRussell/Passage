package api

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/money"
	"github.com/BenjaminSRussell/passage/services/purchasing/internal/domain"
	"github.com/BenjaminSRussell/passage/services/purchasing/internal/store"
)

// splitBody is the body of POST /requisition-lines/{id}:split (FR-17.3): the quantities of the new lines.
// The original line keeps what is left, which must stay above zero.
type splitBody struct {
	Quantities []string `json:"quantities"`
}

// requisitionLineAction handles the custom methods on a requisition line; today only ":split".
func (a *API) requisitionLineAction(w http.ResponseWriter, r *http.Request) {
	id, action, found := strings.Cut(chi.URLParam(r, "id"), ":")
	if !found || action != "split" || !ids.Valid(id) {
		fail(w, r, httpx.NewProblem(http.StatusNotFound, codeUnknownAction, "unknown requisition line action"))
		return
	}
	a.splitRequisitionLine(w, r, id)
}

// splitRequisitionLine cuts new OPEN lines out of an OPEN line of an APPROVED requisition so the parts can be
// ordered from different vendors or sourced separately. If-Match carries the requisition version; the store
// takes the line under lock and refuses one a running RFQ holds (PRC-REQ-006).
func (a *API) splitRequisitionLine(w http.ResponseWriter, r *http.Request, lineID string) {
	p, err := principal(r.Context())
	if err != nil {
		fail(w, r, err)
		return
	}
	key, err := idempotencyKey(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	version, err := ifMatch(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	var body splitBody
	if err := httpx.Decode(r, &body); err != nil {
		fail(w, r, err)
		return
	}
	if len(body.Quantities) == 0 {
		fail(w, r, invalid("quantities", "needs at least one quantity"))
		return
	}
	if len(body.Quantities) > domain.MaxSplitParts {
		fail(w, r, invalid("quantities", "may have at most "+strconv.Itoa(domain.MaxSplitParts)+" quantities"))
		return
	}
	quantities := make([]decimal.Decimal, 0, len(body.Quantities))
	for i, raw := range body.Quantities {
		q, err := positiveQty("quantities["+strconv.Itoa(i)+"]", strings.TrimSpace(raw))
		if err != nil {
			fail(w, r, err)
			return
		}
		if q.Exponent() < -domain.QtyScale {
			fail(w, r, invalid("quantities["+strconv.Itoa(i)+"]", "may have at most "+strconv.Itoa(domain.QtyScale)+" decimals"))
			return
		}
		quantities = append(quantities, q)
	}
	found, err := a.store.RequisitionLinesByID(r.Context(), []string{lineID})
	if err != nil {
		fail(w, r, err)
		return
	}
	if len(found) != 1 {
		fail(w, r, httpx.NewProblem(http.StatusNotFound, store.CodeRequisitionLineNotFound, "requisition line "+lineID+" not found"))
		return
	}
	if err := inScope(p, found[0].LegalEntityID); err != nil {
		fail(w, r, err)
		return
	}
	out, err := a.store.SplitRequisitionLine(r.Context(), lineID, version, quantities, money.MinorUnits(found[0].Currency), p.UserID,
		store.Idem{Key: key, Hash: hashBody("requisition-line.split", map[string]any{"id": lineID, "version": version, "quantities": body.Quantities})})
	if err != nil {
		fail(w, r, err)
		return
	}
	a.requisitionLinesSplit.Add(1)
	w.Header().Set("ETag", etag(out.Version))
	httpx.JSON(w, http.StatusOK, out)
}
