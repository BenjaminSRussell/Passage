package api

import (
	"net/http"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/production/internal/domain"
	"github.com/BenjaminSRussell/passage/services/production/internal/store"
)

// defaultParameters are the service settings that apply to a site with no stored parameters.
func (a *API) defaultParameters() domain.Parameters {
	return domain.Parameters{RAFTolerancePct: a.cfg.RAFTolerancePct, OverConsumptionPct: a.cfg.OverConsumptionPct,
		AutoStartOnFeedback: a.cfg.AutoStartOnFeedback, EstimateOnCreate: a.cfg.EstimateOnCreate,
		SplitAllocation: domain.AllocProportional, TriggerPointsEnabled: true}
}

type parametersList struct {
	LegalEntityID string                   `json:"legalEntityId"`
	Items         []store.StoredParameters `json:"items"`
	Defaults      domain.Parameters        `json:"defaults"`
	Resolved      *resolvedParameters      `json:"resolved,omitempty"`
}

// resolvedParameters is what applies to one site and whether a stored row decided it.
type resolvedParameters struct {
	domain.Parameters
	Stored bool `json:"stored"`
}

func (a *API) listParameters(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	le := q.Get("legalEntityId")
	if le == "" {
		le = p.LegalEntityID
	}
	fields := map[string]string{}
	if !ids.Valid(le) {
		fields["legalEntityId"] = "must be a UUID (defaults to the caller's legal entity)"
	} else if !tenant.InLegalEntityScope(p, le) {
		fields["legalEntityId"] = "must be the legal entity of the caller"
	}
	site := q.Get("siteId")
	if site != "" && !ids.Valid(site) {
		fields["siteId"] = "must be a UUID"
	}
	if len(fields) > 0 {
		invalid(w, r, fields)
		return
	}
	items, err := a.store.ListParameters(r.Context(), le)
	if err != nil {
		a.fail(w, r, "list production parameters", err)
		return
	}
	out := parametersList{LegalEntityID: le, Items: items, Defaults: a.defaultParameters()}
	if site != "" {
		rows := make([]domain.Parameters, len(items))
		for i, it := range items {
			rows[i] = it.Parameters
		}
		resolved, stored := domain.ResolveParameters(rows, site, out.Defaults)
		out.Resolved = &resolvedParameters{Parameters: resolved, Stored: stored}
	}
	httpx.JSON(w, http.StatusOK, out)
}

// parametersRequest carries every setting as a pointer so that a missing one is refused
// instead of silently switching a flag off.
type parametersRequest struct {
	LegalEntityID        string           `json:"legalEntityId"`
	SiteID               string           `json:"siteId"`
	RAFTolerancePct      *decimal.Decimal `json:"rafTolerancePct"`
	OverConsumptionPct   *decimal.Decimal `json:"overConsumptionPct"`
	AutoStartOnFeedback  *bool            `json:"autoStartOnFeedback"`
	EstimateOnCreate     *bool            `json:"estimateOnCreate"`
	SplitAllocation      string           `json:"splitAllocation"`
	TriggerPointsEnabled *bool            `json:"triggerPointsEnabled"`
	AsBuiltRequired      *bool            `json:"asBuiltRequired"`
}

func (a *API) saveParameters(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	ifMatch := 0
	if raw := r.Header.Get("If-Match"); raw != "" {
		v, err := parseIfMatch(raw)
		if err != nil {
			httpx.Fail(w, r, err)
			return
		}
		ifMatch = v
	}
	var req parametersRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if req.LegalEntityID == "" {
		req.LegalEntityID = p.LegalEntityID
	}
	if req.SiteID == "" {
		req.SiteID = domain.AllKey
	}
	fields := map[string]string{}
	if !ids.Valid(req.LegalEntityID) {
		fields["legalEntityId"] = "must be a UUID"
	} else if !tenant.InLegalEntityScope(p, req.LegalEntityID) {
		fields["legalEntityId"] = "must be the legal entity of the caller"
	}
	if req.SiteID != domain.AllKey && !ids.Valid(req.SiteID) {
		fields["siteId"] = "must be a UUID or All"
	}
	prm := domain.Parameters{SiteID: req.SiteID, SplitAllocation: domain.Allocation(req.SplitAllocation)}
	missing := "is required"
	if req.RAFTolerancePct == nil {
		fields["rafTolerancePct"] = missing
	} else {
		prm.RAFTolerancePct = *req.RAFTolerancePct
	}
	if req.OverConsumptionPct == nil {
		fields["overConsumptionPct"] = missing
	} else {
		prm.OverConsumptionPct = *req.OverConsumptionPct
	}
	for name, v := range map[string]*bool{"autoStartOnFeedback": req.AutoStartOnFeedback, "estimateOnCreate": req.EstimateOnCreate,
		"triggerPointsEnabled": req.TriggerPointsEnabled, "asBuiltRequired": req.AsBuiltRequired} {
		if v == nil {
			fields[name] = missing
		}
	}
	if len(fields) == 0 {
		prm.AutoStartOnFeedback, prm.EstimateOnCreate = *req.AutoStartOnFeedback, *req.EstimateOnCreate
		prm.TriggerPointsEnabled, prm.AsBuiltRequired = *req.TriggerPointsEnabled, *req.AsBuiltRequired
		for k, v := range prm.Validate() {
			fields[k] = v
		}
	}
	if len(fields) > 0 {
		invalid(w, r, fields)
		return
	}
	saved, created, err := a.store.SaveParameters(r.Context(), store.ParametersSave{LegalEntityID: req.LegalEntityID, Parameters: prm, IfMatch: ifMatch, Actor: p.UserID})
	if err != nil {
		a.fail(w, r, "save production parameters", err)
		return
	}
	w.Header().Set("ETag", etag(saved.Version))
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	httpx.JSON(w, status, saved)
}
