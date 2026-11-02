package api

import (
	"net/http"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/engineering/internal/domain"
)

// updateBomVersion saves an edited draft BOM version (BR-9.3): the request carries the full header and
// every line, plus the version number the editor loaded. The lines are replaced as a whole, so a line
// left out is deleted; an omitted usage or category keeps the stored one. The product, status and id
// cannot change here (copy a version to change the product, :activate and :cancel to change the
// status). A stale version number is 409 ENG-BOM-VERSION-CONFLICT and nothing is written; the editor
// reloads the draft and merges line by line.
// Like creating a draft, saving one emits no event.
func (a *API) updateBomVersion(w http.ResponseWriter, r *http.Request) {
	var body bomVersionBody
	if !readBody(w, r, &body) {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	fields := map[string]string{}
	if body.ID != "" && body.ID != id {
		fields["id"] = "must match the version in the path, or be omitted"
	}
	if body.Version < 1 {
		fields["version"] = "is required: the version number you loaded"
	}
	if body.Status != "" && body.Status != domain.StatusDraft {
		fields["status"] = "cannot be changed by an edit; use :activate or :cancel"
	}
	body.Status = ""
	draft, problems := body.toDomain()
	for field, problem := range problems {
		fields[field] = problem
	}
	if len(fields) > 0 {
		invalid(w, r, fields)
		return
	}
	stored, err := a.store.GetBomVersion(r.Context(), id)
	if err != nil {
		a.fail(w, r, "update bom version", err, &a.bomFailed)
		return
	}
	if stored.ProductID != draft.ProductID {
		invalid(w, r, map[string]string{"productId": "cannot be changed; copy the version to another product instead"})
		return
	}
	draft.ID, draft.CopiedFrom = id, stored.CopiedFrom
	// FR-9.19: an edit that omits usage or category keeps the stored one, so a client that predates
	// them cannot turn a costing or document BOM into a production material BOM by saving it.
	if body.Usage == "" {
		draft.Usage = stored.Usage
	}
	if body.Category == "" {
		draft.Category = stored.Category
	}
	updated, err := a.store.UpdateBomDraft(r.Context(), draft, body.Version, copyIdem(r, id, body), checkVersion(draft))
	if err != nil {
		if domain.HasCode(err, domain.CodeBomVersionConflict) {
			a.bomConflicts.Add(1)
		}
		a.fail(w, r, "update bom version", err, &a.bomFailed)
		return
	}
	a.bomUpdated.Add(1)
	httpx.JSON(w, http.StatusOK, bodyFromVersion(updated))
}
