package api

import (
	"encoding/json"
	"net/http"
	"regexp"
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
// fulfillment.warehouse.release; changing cogs_at_shipment, an accounting policy, also needs
// store.DutyCogsPolicy (checked in the store against the saved value).
func (a *API) routesLegalEntitySetting(r chi.Router) {
	r.With(authz.RequireDuty("fulfillment.warehouse.view")).Get("/v1/fulfillment/legal-entity-settings", a.listLegalEntitySettings)
	r.With(authz.RequireDuty("fulfillment.warehouse.view")).Get("/v1/fulfillment/legal-entity-settings/{legalEntityId}", a.getLegalEntitySetting)
	r.With(authz.RequireDuty("fulfillment.warehouse.release")).Put("/v1/fulfillment/legal-entity-settings/{legalEntityId}", a.saveLegalEntitySetting)
}

type legalEntitySettingRequest struct {
	CogsAtShipment *bool `json:"cogsAtShipment"`
	AsnEnabled     *bool `json:"asnEnabled"`
	// GS1CompanyPrefix (6 to 12 digits), SSCCExtensionDigit (0 to 9) and
	// PackagingFeeBillable came after the two flags: left out, the saved value
	// stays (a client that predates them never clears them); null returns the
	// prefix or digit to the process default.
	GS1CompanyPrefix     optional[string] `json:"gs1CompanyPrefix"`
	SSCCExtensionDigit   optional[int]    `json:"ssccExtensionDigit"`
	PackagingFeeBillable optional[bool]   `json:"packagingFeeBillable"`
}

// optional records whether a JSON member was present at all; null is present
// with Null set.
type optional[T any] struct {
	Set   bool
	Null  bool
	Value T
}

func (o *optional[T]) UnmarshalJSON(b []byte) error {
	o.Set = true
	if string(b) == "null" {
		o.Null = true
		return nil
	}
	return json.Unmarshal(b, &o.Value)
}

// nullable is the store change of a member that null resets.
func (o optional[T]) nullable() store.Change[*T] {
	if !o.Set || o.Null {
		return store.Change[*T]{Set: o.Set}
	}
	v := o.Value
	return store.Change[*T]{Set: true, Value: &v}
}

var gs1PrefixPattern = regexp.MustCompile(`^[0-9]{6,12}$`)

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
	if g := req.GS1CompanyPrefix; g.Set && !g.Null && !gs1PrefixPattern.MatchString(g.Value) {
		fail(w, r, "gs1CompanyPrefix", "must be 6 to 12 digits, or null for the process default")
		return
	}
	if d := req.SSCCExtensionDigit; d.Set && !d.Null && (d.Value < 0 || d.Value > 9) {
		fail(w, r, "ssccExtensionDigit", "must be a digit from 0 to 9, or null for the process default")
		return
	}
	if req.PackagingFeeBillable.Null {
		fail(w, r, "packagingFeeBillable", "must be true or false (leave it out to keep the saved value)")
		return
	}
	p, _ := passagetenant.FromContext(r.Context())
	in := store.LegalEntitySettingInput{CogsAtShipment: *req.CogsAtShipment, AsnEnabled: *req.AsnEnabled,
		GS1CompanyPrefix: req.GS1CompanyPrefix.nullable(), SSCCExtensionDigit: req.SSCCExtensionDigit.nullable(),
		PackagingFeeBillable: store.Change[bool]{Set: req.PackagingFeeBillable.Set, Value: req.PackagingFeeBillable.Value}, CanChangeCogs: p.HasDuty(store.DutyCogsPolicy)}
	s, err := a.store.SaveLegalEntitySetting(r.Context(), id, version, in, idem)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	etag(w, s.Version)
	httpx.JSON(w, http.StatusOK, s)
}
