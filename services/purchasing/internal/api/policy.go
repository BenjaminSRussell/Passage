package api

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/purchasing/internal/domain"
)

// dutyPolicyMaintain saves the purchasing policy (FR-17.13); reading it needs
// procurement.purchase-order.inquire or this duty.
const dutyPolicyMaintain = "procurement.policy.maintain"

// policyBody is the PUT body. Every flag is required so a save never changes a
// setting by omission.
type policyBody struct {
	ChangeManagementDefault   *bool `json:"changeManagementDefault"`
	POFourEyes                *bool `json:"poFourEyes"`
	AllowInvoiceBeforeReceipt *bool `json:"allowInvoiceBeforeReceipt"`
	// CategoryAccessMode is optional: left out, the saved mode (SOFT for a new policy) stays.
	CategoryAccessMode *string `json:"categoryAccessMode"`
}

// policyEntity reads and checks the {legalEntityId} path parameter against the
// caller's legal-entity scope.
func policyEntity(r *http.Request) (string, error) {
	id := chi.URLParam(r, "legalEntityId")
	if !ids.Valid(id) {
		return "", invalid("legalEntityId", "must be a UUID")
	}
	p, err := principal(r.Context())
	if err != nil {
		return "", err
	}
	if err := inScope(p, id); err != nil {
		return "", err
	}
	return id, nil
}

// listPolicies handles GET /purchasing-policies: the saved policies of the caller's legal entities.
func (a *API) listPolicies(w http.ResponseWriter, r *http.Request) {
	items, err := a.store.ListPolicies(r.Context())
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, pageBody[domain.Policy]{Items: items})
}

// getPolicy handles GET /purchasing-policies/{legalEntityId}. A legal entity with no saved
// policy answers the defaults at version 0; the ETag is the version PUT takes as If-Match.
func (a *API) getPolicy(w http.ResponseWriter, r *http.Request) {
	id, err := policyEntity(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	out, err := a.store.GetPolicy(r.Context(), id)
	if err != nil {
		fail(w, r, err)
		return
	}
	w.Header().Set("ETag", etag(out.Version))
	httpx.JSON(w, http.StatusOK, out)
}

// putPolicy handles PUT /purchasing-policies/{legalEntityId}: saves the policy under
// If-Match (0 creates it) and answers the saved row. PUT is idempotent, so no
// Idempotency-Key is taken.
func (a *API) putPolicy(w http.ResponseWriter, r *http.Request) {
	id, err := policyEntity(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	want, err := ifMatch(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	var body policyBody
	if err := httpx.Decode(r, &body); err != nil {
		fail(w, r, err)
		return
	}
	for name, v := range map[string]*bool{"changeManagementDefault": body.ChangeManagementDefault,
		"poFourEyes": body.POFourEyes, "allowInvoiceBeforeReceipt": body.AllowInvoiceBeforeReceipt} {
		if v == nil {
			fail(w, r, invalid(name, "is required"))
			return
		}
	}
	mode := ""
	if body.CategoryAccessMode != nil {
		mode = *body.CategoryAccessMode
		if !domain.ValidAccessMode(mode) {
			fail(w, r, invalid("categoryAccessMode", "must be HARD, SOFT or OFF"))
			return
		}
	} else {
		cur, err := a.store.GetPolicy(r.Context(), id)
		if err != nil {
			fail(w, r, err)
			return
		}
		mode = cur.CategoryAccessMode
	}
	out, err := a.store.PutPolicy(r.Context(), domain.Policy{LegalEntityID: id,
		ChangeManagementDefault: *body.ChangeManagementDefault, POFourEyes: *body.POFourEyes,
		AllowInvoiceBeforeReceipt: *body.AllowInvoiceBeforeReceipt, CategoryAccessMode: mode}, want)
	if err != nil {
		fail(w, r, err)
		return
	}
	w.Header().Set("ETag", etag(out.Version))
	httpx.JSON(w, http.StatusOK, out)
}
