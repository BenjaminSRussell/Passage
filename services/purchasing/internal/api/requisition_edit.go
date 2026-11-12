package api

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/purchasing/internal/store"
)

// requisitionPatch is the body of PATCH /requisitions/{id} (BR-17.1). Lines, when present, replace all the lines;
// the legal entity cannot change.
type requisitionPatch struct {
	OperatingUnitID *string                `json:"operatingUnitId,omitempty"`
	Currency        *string                `json:"currency,omitempty"`
	Lines           *[]requisitionLineBody `json:"lines,omitempty"`
}

// patchRequisition handles PATCH /requisitions/{id}: the requester edits a DRAFT requisition, a new one or one
// reopened after a rejection or withdrawn from review. If-Match carries the version; lines are re-priced as on
// create. Any other status is refused by the store with PRC-REQ-STATE, after a replayed Idempotency-Key is answered.
func (a *API) patchRequisition(w http.ResponseWriter, r *http.Request) {
	userID, err := caller(r.Context())
	if err != nil {
		fail(w, r, err)
		return
	}
	id, err := pathID(r)
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
	var body requisitionPatch
	if err := httpx.Decode(r, &body); err != nil {
		fail(w, r, err)
		return
	}
	if body.OperatingUnitID == nil && body.Currency == nil && body.Lines == nil {
		fail(w, r, invalid("body", "needs operatingUnitId, currency or lines"))
		return
	}
	p, _ := principal(r.Context())
	cur, err := a.store.GetRequisition(r.Context(), id)
	if err != nil {
		fail(w, r, err)
		return
	}
	if err := inScope(p, cur.LegalEntityID); err != nil {
		fail(w, r, err)
		return
	}
	if cur.RequesterID != userID {
		fail(w, r, httpx.NewProblem(http.StatusForbidden, codeNotRequester, "only the requester can edit this requisition"))
		return
	}
	edit := store.RequisitionEdit{OperatingUnitID: cur.OperatingUnitID, ActorID: userID}
	if body.OperatingUnitID != nil {
		edit.OperatingUnitID = strings.TrimSpace(*body.OperatingUnitID)
		if err := uuidField("operatingUnitId", edit.OperatingUnitID, false); err != nil {
			fail(w, r, err)
			return
		}
	}
	if version > cur.Version {
		// Not a replay (that names a version the requisition had), and the store must not reach that version
		// with the lines left unpriced below.
		fail(w, r, httpx.NewProblem(http.StatusConflict, store.CodeVersionConflict,
			fmt.Sprintf("requisition is at version %d, not %d; reload and retry", cur.Version, version)))
		return
	}
	// The status and version are the store's to check, under its row lock and after an Idempotency-Key replay (a
	// retried edit of a requisition submitted since is answered with the requisition, not a 409). Lines are priced
	// only when the edit can apply: a requisition that is not a DRAFT at the If-Match version goes to the store
	// unpriced, which replays it or refuses it (PRC-REQ-STATE or PRC-VERSION-CONFLICT) without writing.
	if body.Lines == nil {
		if body.Currency != nil && !strings.EqualFold(strings.TrimSpace(*body.Currency), cur.Currency) {
			fail(w, r, invalid("currency", "can change only together with lines, which are priced again"))
			return
		}
	} else if cur.Status == store.RequisitionDraft && cur.Version == version {
		lines, err := keepPunchoutFields(cur, *body.Lines)
		if err != nil {
			fail(w, r, err)
			return
		}
		full := requisitionBody{LegalEntityID: cur.LegalEntityID, OperatingUnitID: edit.OperatingUnitID, Currency: cur.Currency, Lines: lines}
		if body.Currency != nil {
			full.Currency = *body.Currency
		}
		resolved, err := a.applyCatalogEntries(r, full)
		if err != nil {
			fail(w, r, err)
			return
		}
		req, estimate, err := buildRequisition(resolved, userID)
		if err != nil {
			fail(w, r, err)
			return
		}
		if err := a.estimatePrices(r.Context(), &req, estimate); err != nil {
			fail(w, r, err)
			return
		}
		edit.Currency, edit.Lines, edit.Total = req.Currency, req.Lines, req.Total
	}
	out, err := a.store.UpdateRequisition(r.Context(), id, version, edit,
		store.Idem{Key: key, Hash: hashBody("requisition.update", map[string]any{"id": id, "version": version, "body": body})})
	if err != nil {
		fail(w, r, err)
		return
	}
	a.requisitionsEdited.Add(1)
	w.Header().Set("ETag", etag(out.Version))
	httpx.JSON(w, http.StatusOK, out)
}

// cancelLineBody is the body of POST /requisitions/{id}:cancel-line.
type cancelLineBody struct {
	LineID  string `json:"lineId"`
	Comment string `json:"comment,omitempty"`
}

// cancelRequisitionLine handles POST /requisitions/{id}:cancel-line (BR-17.1): the requester cancels one OPEN line
// of an APPROVED requisition while the rest continues. If-Match carries the requisition version.
func (a *API) cancelRequisitionLine(w http.ResponseWriter, r *http.Request, id, userID string) {
	p, _ := principal(r.Context())
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
	var body cancelLineBody
	if err := httpx.Decode(r, &body); err != nil {
		fail(w, r, err)
		return
	}
	if err := uuidField("lineId", body.LineID, true); err != nil {
		fail(w, r, err)
		return
	}
	body.Comment = strings.TrimSpace(body.Comment)
	if len([]rune(body.Comment)) > maxDecisionCommentLen {
		fail(w, r, invalid("comment", "must be at most 1000 characters"))
		return
	}
	cur, err := a.store.GetRequisition(r.Context(), id)
	if err != nil {
		fail(w, r, err)
		return
	}
	if err := inScope(p, cur.LegalEntityID); err != nil {
		fail(w, r, err)
		return
	}
	if cur.RequesterID != userID {
		fail(w, r, httpx.NewProblem(http.StatusForbidden, codeNotRequester, "only the requester can cancel a line of this requisition"))
		return
	}
	out, err := a.store.CancelRequisitionLine(r.Context(), id, version, body.LineID, userID, body.Comment,
		store.Idem{Key: key, Hash: hashBody("requisition.cancel-line", map[string]any{"id": id, "version": version, "lineId": body.LineID, "comment": body.Comment})})
	if err != nil {
		fail(w, r, err)
		return
	}
	a.requisitionLinesCancelled.Add(1)
	w.Header().Set("ETag", etag(out.Version))
	httpx.JSON(w, http.StatusOK, out)
}
