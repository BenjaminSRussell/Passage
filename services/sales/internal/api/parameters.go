package api

import (
	"net/http"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/sales/internal/domain"
	"github.com/BenjaminSRussell/passage/services/sales/internal/store"
)

// parametersRequest is the PUT body of /sales/parameters (PRD 5.27.13). The
// PUT replaces every value, so each one is required: a missing
// quotationProbabilityDefault must not save as 0.
type parametersRequest struct {
	QuotationExpiryDays         *int   `json:"quotationExpiryDays"`
	QuotationProbabilityDefault *int   `json:"quotationProbabilityDefault"`
	CreditReleaseValidityDays   *int   `json:"creditReleaseValidityDays"`
	CreditHoldSLABusinessDays   *int   `json:"creditHoldSlaBusinessDays"`
	DefaultOrderType            string `json:"defaultOrderType"`
}

// parameters returns the domain values of r, with a field error for each
// missing one.
func (r parametersRequest) parameters() (domain.Parameters, map[string]string) {
	f := map[string]string{}
	val := func(v *int, name string) int {
		if v == nil {
			f[name] = "is required"
			return 0
		}
		return *v
	}
	out := domain.Parameters{QuotationExpiryDays: val(r.QuotationExpiryDays, "quotationExpiryDays"),
		QuotationProbabilityDefault: val(r.QuotationProbabilityDefault, "quotationProbabilityDefault"),
		CreditReleaseValidityDays:   val(r.CreditReleaseValidityDays, "creditReleaseValidityDays"),
		CreditHoldSLABusinessDays:   val(r.CreditHoldSLABusinessDays, "creditHoldSlaBusinessDays"),
		DefaultOrderType:            r.DefaultOrderType}
	for k, v := range out.Validate() {
		if _, missing := f[k]; !missing {
			f[k] = v
		}
	}
	return out, f
}

// getParameters returns the legal entity's parameters, or the defaults with
// version 0 when none were saved.
func (a *API) getParameters(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	legalEntity, ok := creditLegalEntity(w, r, true)
	if !ok {
		return
	}
	params, err := a.store.GetParameters(r.Context(), legalEntity)
	if err != nil {
		a.fail(w, r, "get parameters", err)
		return
	}
	setETag(w, params.Version)
	httpx.JSON(w, http.StatusOK, params)
}

// putParameters creates or replaces the parameters. If-Match carries the
// current ETag, or "0" to create.
func (a *API) putParameters(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	legalEntity, ok := creditLegalEntity(w, r, true)
	if !ok {
		return
	}
	var req parametersRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	values, fields := req.parameters()
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	in := store.SalesParameters{LegalEntityID: legalEntity, Parameters: values}
	version, ok := ifMatch(w, r)
	if !ok {
		return
	}
	p, _ := tenant.FromContext(r.Context())
	saved, err := a.store.PutParameters(r.Context(), in, version, p.UserID)
	if err != nil {
		a.fail(w, r, "put parameters", err)
		return
	}
	setETag(w, saved.Version)
	httpx.JSON(w, http.StatusOK, saved)
}
