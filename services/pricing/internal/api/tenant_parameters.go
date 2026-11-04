package api

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5/middleware"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/pricing/internal/domain"
	"github.com/BenjaminSRussell/passage/services/pricing/internal/store"
)

// tenantParametersPath serves the pricing parameters of the caller's tenant (PRD 5.26.13
// scope tenant: cache_ttl_seconds, campaign_kill_switch_effect). Reading takes pricing.view;
// changing takes pricing.admin.
const tenantParametersPath = "/v1/pricing/tenant-parameters"

// tenantParametersRequest is the PUT body. The PUT replaces every value, so each is required.
type tenantParametersRequest struct {
	// CacheTTLSeconds is 10 to 600, or 0 to follow the deployment's PRICING_CACHE_TTL.
	CacheTTLSeconds          *int    `json:"cacheTtlSeconds"`
	CampaignKillSwitchEffect *string `json:"campaignKillSwitchEffect"`
	// Version is the version read (0 when the tenant has no stored parameters).
	Version int `json:"version"`
}

type tenantParametersView struct {
	CacheTTLSeconds          int        `json:"cacheTtlSeconds"`
	CampaignKillSwitchEffect string     `json:"campaignKillSwitchEffect"`
	Version                  int        `json:"version"`
	UpdatedBy                string     `json:"updatedBy,omitempty"`
	UpdatedAt                *time.Time `json:"updatedAt,omitempty"`
}

func tenantParametersResponse(p store.TenantParameters) tenantParametersView {
	if p.CampaignKillSwitchEffect == "" { // a store with nothing for the tenant
		p.CampaignKillSwitchEffect = store.DefaultTenantParameters().CampaignKillSwitchEffect
	}
	return tenantParametersView{CacheTTLSeconds: p.CacheTTLSeconds, CampaignKillSwitchEffect: p.CampaignKillSwitchEffect,
		Version: p.Version, UpdatedBy: p.UpdatedBy, UpdatedAt: p.UpdatedAt}
}

func (a *API) getTenantParameters(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	p, err := a.uncached().GetTenantParameters(r.Context())
	if err != nil {
		slog.Error("get pricing tenant parameters failed", "err", err, "correlationId", middleware.GetReqID(r.Context()))
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, tenantParametersResponse(p))
}

func (a *API) setTenantParameters(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var req tenantParametersRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	if req.CacheTTLSeconds == nil {
		fields["cacheTtlSeconds"] = "is required (0 follows the deployment)"
	} else if *req.CacheTTLSeconds != 0 && !domain.ValidCacheTTL(*req.CacheTTLSeconds) {
		fields["cacheTtlSeconds"] = "must be 0 or from 10 to 600"
	}
	if req.CampaignKillSwitchEffect == nil {
		fields["campaignKillSwitchEffect"] = "is required"
	} else if !domain.ValidKillEffect(*req.CampaignKillSwitchEffect) {
		fields["campaignKillSwitchEffect"] = "must be NEXT_RESOLVE or IMMEDIATE_CACHE_FLUSH"
	}
	if req.Version < 0 {
		fields["version"] = "must be the version read, or 0 when none was stored"
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	p, err := a.store.SetTenantParameters(r.Context(), store.TenantParameters{CacheTTLSeconds: *req.CacheTTLSeconds,
		CampaignKillSwitchEffect: *req.CampaignKillSwitchEffect}, req.Version, r.Header.Get(idempotencyKey))
	if err != nil {
		slog.Error("set pricing tenant parameters failed", "err", err, "correlationId", middleware.GetReqID(r.Context()))
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, tenantParametersResponse(p))
}
