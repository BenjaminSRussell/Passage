package api

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	passagetenant "github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/fulfillment/internal/store"
)

// routesLegalEntitySetting mounts the posting settings per legal entity
// (cogs_at_shipment and asn_enabled, section 5.28.13). Saving needs
// WarehouseRelease; changing cogs_at_shipment, an accounting policy, also needs
// store.DutyCogsPolicy (checked in the store against the saved value).
func (a *API) routesLegalEntitySetting(r chi.Router) {
	r.With(authz.RequireDuty("WarehouseView")).Get("/v1/fulfillment/legal-entity-settings", a.listLegalEntitySettings)
	r.With(authz.RequireDuty("WarehouseView")).Get("/v1/fulfillment/legal-entity-settings/{legalEntityId}", a.getLegalEntitySetting)
	r.With(authz.RequireDuty("WarehouseRelease")).Put("/v1/fulfillment/legal-entity-settings/{legalEntityId}", a.saveLegalEntitySetting)
}

type legalEntitySettingRequest struct {
	CogsAtShipment *bool `json:"cogsAtShipment"`
	AsnEnabled     *bool `json:"asnEnabled"`
}

func legalEntityParam(w http.ResponseWriter, r *http.Request) (string, bool) {
	id := chi.URLParam(r, "legalEntityId")
	if !ids.Valid(id) {
		fail(w, r, "legalEntityId", "must be a UUID")
		return "", false
	}
	return id, true
}

func (a *API) listLegalEntitySettings(w http.ResponseWriter, r *http.Request) {
	items, err := a.store.ListLegalEntitySettings(r.Context())
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, listResponse[store.LegalEntitySetting]{Items: items})
}

func (a *API) getLegalEntitySetting(w http.ResponseWriter, r *http.Request) {
	id, ok := legalEntityParam(w, r)
	if !ok {
		return
	}
	s, err := a.store.GetLegalEntitySetting(r.Context(), id)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	etag(w, s.Version)
	httpx.JSON(w, http.StatusOK, s)
}

func (a *API) saveLegalEntitySetting(w http.ResponseWriter, r *http.Request) {
	id, ok := legalEntityParam(w, r)
	if !ok {
		return
	}
	raw := strings.Trim(strings.TrimSpace(r.Header.Get("If-Match")), `"`)
	if raw == "" {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusPreconditionRequired, CodePreconditionIf, "If-Match with the setting version is required (0 when none is saved)"))
		return
	}
	version, err := strconv.Atoi(raw)
	if err != nil || version < 0 {
		fail(w, r, "If-Match", "must be the setting version as an integer, 0 when none is saved")
		return
	}
	var req legalEntitySettingRequest
	_, idem, err := command(r, &req)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if req.CogsAtShipment == nil {
		fail(w, r, "cogsAtShipment", "is required (true or false)")
		return
	}
	if req.AsnEnabled == nil {
		fail(w, r, "asnEnabled", "is required (true or false)")
		return
	}
	p, _ := passagetenant.FromContext(r.Context())
	in := store.LegalEntitySettingInput{CogsAtShipment: *req.CogsAtShipment, AsnEnabled: *req.AsnEnabled, CanChangeCogs: p.HasDuty(store.DutyCogsPolicy)}
	s, err := a.store.SaveLegalEntitySetting(r.Context(), id, version, in, idem)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	etag(w, s.Version)
	httpx.JSON(w, http.StatusOK, s)
}
