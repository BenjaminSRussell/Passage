package api

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/pricing/internal/domain"
	"github.com/BenjaminSRussell/passage/services/pricing/internal/store"
)

const (
	campaignsPath       = "/v1/pricing/campaigns"
	campaignPath        = "/v1/pricing/campaigns/{id}"
	campaignKillPath    = "/v1/pricing/campaigns/{id}:kill"
	campaignRestorePath = "/v1/pricing/campaigns/{id}:restore"
)

type campaignRequest struct {
	LegalEntityID       string `json:"legalEntityId"`
	Name                string `json:"name"`
	ValidFrom           string `json:"validFrom"`
	ValidTo             string `json:"validTo"`
	TargetCustomerGroup string `json:"targetCustomerGroup"`
	Budget              string `json:"budget"`
	Currency            string `json:"currency"`
}

type campaignView struct {
	ID                  string `json:"id"`
	LegalEntityID       string `json:"legalEntityId"`
	Name                string `json:"name"`
	ValidFrom           string `json:"validFrom"`
	ValidTo             string `json:"validTo"`
	TargetCustomerGroup string `json:"targetCustomerGroup"`
	Budget              string `json:"budget"`
	Currency            string `json:"currency"`
	Killed              bool   `json:"killed"`
	Version             int    `json:"version"`
}

func campaignResponse(c domain.Campaign) campaignView {
	return campaignView{ID: c.ID, LegalEntityID: c.LegalEntityID, Name: c.Name,
		ValidFrom: c.ValidFrom.Format(dateLayout), ValidTo: c.ValidTo.Format(dateLayout),
		TargetCustomerGroup: c.TargetCustomerGroup, Budget: c.Budget.String(), Currency: c.Currency,
		Killed: c.Killed, Version: c.Version}
}

// registerCampaignRoutes mounts the campaign surface (FR-26.10). PricingMaintain covers
// campaigns (§5.26.17); reads take PricingView.
func (a *API) registerCampaignRoutes(r chi.Router) {
	r.With(authz.RequireDuty(dutyMaintain)).Post(campaignsPath, a.createCampaign)
	r.With(authz.RequireDuty(dutyView)).Get(campaignsPath, a.listCampaigns)
	r.With(authz.RequireDuty(dutyView)).Get(campaignPath, a.getCampaign)
	r.With(authz.RequireDuty(dutyMaintain)).Post(campaignKillPath, a.campaignSwitch(true))
	r.With(authz.RequireDuty(dutyMaintain)).Post(campaignRestorePath, a.campaignSwitch(false))
}

func (a *API) createCampaign(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var req campaignRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	if !ids.Valid(req.LegalEntityID) {
		fields["legalEntityId"] = "must be a UUID"
	}
	from, err1 := time.Parse(dateLayout, req.ValidFrom)
	to, err2 := time.Parse(dateLayout, req.ValidTo)
	if err1 != nil || err2 != nil {
		fields["validity"] = "validFrom and validTo must be dates in YYYY-MM-DD form"
	}
	budget, err := decimal.NewFromString(req.Budget)
	if err != nil {
		fields["budget"] = "must be a decimal"
	}
	c := domain.Campaign{LegalEntityID: req.LegalEntityID, Name: req.Name, ValidFrom: from, ValidTo: to,
		TargetCustomerGroup: req.TargetCustomerGroup, Budget: budget, Currency: req.Currency}
	for k, v := range domain.ValidateCampaign(c) {
		if _, set := fields[k]; !set {
			fields[k] = v
		}
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	out, err := a.store.CreateCampaign(r.Context(), c, r.Header.Get(idempotencyKey))
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, campaignResponse(out))
}

func (a *API) listCampaigns(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	le := r.URL.Query().Get("legalEntityId")
	if le != "" && !ids.Valid(le) {
		validationFailed(w, r, map[string]string{"legalEntityId": "must be a UUID"})
		return
	}
	rows, err := a.store.ListCampaigns(r.Context(), le, store.MaxListLimit)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	items := make([]campaignView, 0, len(rows))
	for _, c := range rows {
		items = append(items, campaignResponse(c))
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
}

func (a *API) getCampaign(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id, ok := uuidParam(w, r, domain.CodeCampaignNotFound, "campaign")
	if !ok {
		return
	}
	c, err := a.store.GetCampaign(r.Context(), id)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, campaignResponse(c))
}

// campaignSwitch returns the kill or restore handler: both take {"version"} and an
// Idempotency-Key. A killed campaign's records drop out of the next resolve (NEXT_RESOLVE).
func (a *API) campaignSwitch(kill bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !validTenant(w, r) {
			return
		}
		id, ok := uuidParam(w, r, domain.CodeCampaignNotFound, "campaign")
		if !ok {
			return
		}
		var req versionRequest
		if err := httpx.Decode(r, &req); err != nil {
			httpx.Fail(w, r, err)
			return
		}
		if req.Version < 1 {
			validationFailed(w, r, map[string]string{"version": "is required and must be at least 1"})
			return
		}
		c, err := a.store.SetCampaignKilled(r.Context(), id, req.Version, kill, r.Header.Get(idempotencyKey))
		if err != nil {
			httpx.Fail(w, r, err)
			return
		}
		httpx.JSON(w, http.StatusOK, campaignResponse(c))
	}
}
