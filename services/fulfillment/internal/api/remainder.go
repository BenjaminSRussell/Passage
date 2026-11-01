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

// routesRemainder mounts the short pick action warehouse setting and the
// backorder shipment (FR-28.8).
func (a *API) routesRemainder(r chi.Router) {
	r.With(authz.RequireDuty("WarehouseView")).Get("/v1/fulfillment/warehouse-settings", a.listWarehouseSettings)
	r.With(authz.RequireDuty("WarehouseView")).Get("/v1/fulfillment/warehouse-settings/{warehouseId}", a.getWarehouseSetting)
	r.With(authz.RequireDuty("WarehouseRelease")).Put("/v1/fulfillment/warehouse-settings/{warehouseId}", a.saveWarehouseSetting)
	r.With(authz.RequireDuty("WarehouseRelease")).Post("/v1/fulfillment/shipments/{id}:backorder-remainder", a.backorderRemainder)
}

type warehouseSettingRequest struct {
	ShortPickAction string `json:"shortPickAction"`
	// LegalEntityID is the legal entity the warehouse belongs to; the setting
	// is scoped by it.
	LegalEntityID string `json:"legalEntityId"`
}

type backorderRequest struct {
	PlannedShipDate string `json:"plannedShipDate"`
}

func warehouseParam(w http.ResponseWriter, r *http.Request) (string, bool) {
	id := chi.URLParam(r, "warehouseId")
	if !ids.Valid(id) {
		fail(w, r, "warehouseId", "must be a UUID")
		return "", false
	}
	return id, true
}

func (a *API) listWarehouseSettings(w http.ResponseWriter, r *http.Request) {
	items, err := a.store.ListWarehouseSettings(r.Context())
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, listResponse[store.WarehouseSetting]{Items: items})
}

func (a *API) getWarehouseSetting(w http.ResponseWriter, r *http.Request) {
	id, ok := warehouseParam(w, r)
	if !ok {
		return
	}
	s, err := a.store.GetWarehouseSetting(r.Context(), id)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	etag(w, s.Version)
	httpx.JSON(w, http.StatusOK, s)
}

func (a *API) saveWarehouseSetting(w http.ResponseWriter, r *http.Request) {
	id, ok := warehouseParam(w, r)
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
	var req warehouseSettingRequest
	_, idem, err := command(r, &req)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	action, err := domain.ParseShortPickAction(req.ShortPickAction)
	if err != nil {
		fail(w, r, "shortPickAction", err.Error())
		return
	}
	if !ids.Valid(req.LegalEntityID) {
		fail(w, r, "legalEntityId", "must be the UUID of the legal entity the warehouse belongs to")
		return
	}
	s, err := a.store.SaveWarehouseSetting(r.Context(), id, req.LegalEntityID, version, action, idem)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	etag(w, s.Version)
	httpx.JSON(w, http.StatusOK, s)
}

func (a *API) backorderRemainder(w http.ResponseWriter, r *http.Request) {
	id, version, ok := a.shipmentCommand(w, r)
	if !ok {
		return
	}
	var req backorderRequest
	_, idem, err := command(r, &req)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	var date *time.Time
	if req.PlannedShipDate != "" {
		d, err := time.Parse(dateLayout, req.PlannedShipDate)
		if err != nil {
			fail(w, r, "plannedShipDate", "must be a date as YYYY-MM-DD")
			return
		}
		date = &d
	}
	res, err := a.store.BackorderRemainder(r.Context(), id, version, date, idem)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	etag(w, res.Shipment.Version)
	httpx.JSON(w, http.StatusOK, res)
}
