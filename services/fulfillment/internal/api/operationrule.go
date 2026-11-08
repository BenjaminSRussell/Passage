package api

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/fulfillment/internal/domain"
	"github.com/BenjaminSRussell/passage/services/fulfillment/internal/store"
)

// routesOperationRule mounts the posting and packing parameters per warehouse
// (section 5.28.13: posting_date_source, auto_pack_enabled,
// workbench_group_key_default).
func (a *API) routesOperationRule(r chi.Router) {
	r.With(authz.RequireDuty("fulfillment.warehouse.view")).Get("/v1/fulfillment/operation-rules", a.listOperationRules)
	r.With(authz.RequireDuty("fulfillment.warehouse.view")).Get("/v1/fulfillment/operation-rules/{warehouseId}", a.getOperationRule)
	r.With(authz.RequireDuty("fulfillment.warehouse.release")).Put("/v1/fulfillment/operation-rules/{warehouseId}", a.saveOperationRule)
}

type operationRuleRequest struct {
	LegalEntityID            string `json:"legalEntityId"`
	PostingDateSource        string `json:"postingDateSource"`
	TimeZone                 string `json:"timeZone"`
	AutoPackEnabled          *bool  `json:"autoPackEnabled"`
	WorkbenchGroupKeyDefault string `json:"workbenchGroupKeyDefault"`
}

func (a *API) listOperationRules(w http.ResponseWriter, r *http.Request) {
	items, err := a.store.ListOperationRules(r.Context())
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, listResponse[store.OperationRule]{Items: items})
}

func (a *API) getOperationRule(w http.ResponseWriter, r *http.Request) {
	id, ok := warehouseParam(w, r)
	if !ok {
		return
	}
	rule, err := a.store.GetOperationRule(r.Context(), id)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	etag(w, rule.Version)
	httpx.JSON(w, http.StatusOK, rule)
}

func (a *API) saveOperationRule(w http.ResponseWriter, r *http.Request) {
	id, ok := warehouseParam(w, r)
	if !ok {
		return
	}
	raw := strings.Trim(strings.TrimSpace(r.Header.Get("If-Match")), `"`)
	if raw == "" {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusPreconditionRequired, CodePreconditionIf, "If-Match with the rule version is required (0 when none is saved)"))
		return
	}
	version, err := strconv.Atoi(raw)
	if err != nil || version < 0 {
		fail(w, r, "If-Match", "must be the rule version as an integer, 0 when none is saved")
		return
	}
	var req operationRuleRequest
	_, idem, err := command(r, &req)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if !ids.Valid(req.LegalEntityID) {
		fail(w, r, "legalEntityId", "must be the UUID of the legal entity the warehouse belongs to")
		return
	}
	if !domain.ValidPostingSource(req.PostingDateSource) {
		fail(w, r, "postingDateSource", "must be SHIP_TIMESTAMP, TODAY or MANUAL")
		return
	}
	zone := strings.TrimSpace(req.TimeZone)
	if zone == "" {
		zone = "UTC"
	}
	if _, err := time.LoadLocation(zone); err != nil || len(zone) > 64 || zone == "Local" {
		fail(w, r, "timeZone", "must be an IANA time zone name such as Europe/Berlin")
		return
	}
	if req.AutoPackEnabled == nil {
		fail(w, r, "autoPackEnabled", "is required")
		return
	}
	if _, err := domain.GroupKey(domain.WaveCandidate{}, req.WorkbenchGroupKeyDefault); err != nil {
		fail(w, r, "workbenchGroupKeyDefault", "must be CUSTOMER, CARRIER, ZONE or PRODUCT")
		return
	}
	rule, err := a.store.SaveOperationRule(r.Context(), id, req.LegalEntityID, version, store.OperationRuleInput{
		PostingDateSource: req.PostingDateSource, TimeZone: zone, AutoPackEnabled: *req.AutoPackEnabled,
		WorkbenchGroupKeyDefault: req.WorkbenchGroupKeyDefault}, idem)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	etag(w, rule.Version)
	httpx.JSON(w, http.StatusOK, rule)
}
