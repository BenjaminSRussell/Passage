package api

import (
	"log/slog"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/org/internal/store"
)

const (
	warehouses         = "/v1/org/warehouses"
	invalidWarehouseTy = "must be standard, transit, quarantine or subcontractor"
)

type createWarehouseRequest struct {
	SiteID   string  `json:"siteId"`
	Code     string  `json:"code"`
	Name     string  `json:"name"`
	Type     *string `json:"type"`
	TimeZone *string `json:"timeZone"`
}

type updateWarehouseRequest struct {
	Version *int `json:"version"`
	store.WarehousePatch
}

type warehouseList struct {
	Items      []store.Warehouse `json:"items"`
	NextCursor string            `json:"nextCursor,omitempty"`
}

func warehouseType(value string) bool {
	switch value {
	case "standard", "transit", "quarantine", "subcontractor":
		return true
	}
	return false
}

func (a *API) registerWarehouses(r chi.Router) {
	r.With(authz.RequireDuty(dutySite)).Post(warehouses, a.createWarehouse)
	r.With(authz.RequireDuty(dutyRead)).Get(warehouses, a.listWarehouses)
	r.With(authz.RequireDuty(dutyRead)).Get(warehouses+"/{id}", warehouseRoute(a.getWarehouse))
	r.With(authz.RequireDuty(dutySite)).Patch(warehouses+"/{id}", warehouseRoute(a.updateWarehouse))
}

func (a *API) createWarehouse(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var req createWarehouseRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	in := store.Warehouse{SiteID: canonicalID(req.SiteID), Code: strings.ToUpper(req.Code), Name: strings.TrimSpace(req.Name)}
	fields := validateShared(&in.Name, req.TimeZone, nil, nil)
	if req.TimeZone != nil {
		in.TimeZone = *req.TimeZone
	}
	if req.Type != nil {
		in.Type = *req.Type
		if !warehouseType(in.Type) {
			fields["type"] = invalidWarehouseTy
		}
	}
	if !canonicalUUID(in.SiteID) {
		fields["siteId"] = "is required and must be a UUID"
	}
	if !siteCode.MatchString(in.Code) {
		fields["code"] = "must be 1 to 10 characters of A-Z, 0-9, _ or -, starting with A-Z or 0-9"
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	warehouse, err := a.store.CreateWarehouse(r.Context(), in, r.Header.Get(idempotencyKey))
	if err != nil {
		slog.Error("create warehouse failed", "err", err, "correlationId", middleware.GetReqID(r.Context()))
		httpx.Fail(w, r, err)
		return
	}
	a.warehouseCreated.Add(1)
	httpx.JSON(w, http.StatusCreated, warehouse)
}

func (a *API) listWarehouses(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	limit, cursor, err := pageParams(r)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	var after store.SiteKey
	if cursor != "" {
		var ok bool
		if after, ok = decodeCodeCursor(cursor); !ok {
			badQuery(w, r, "cursor", "is invalid")
			return
		}
	}
	q := r.URL.Query()
	f := store.WarehouseFilter{LegalEntityID: q.Get("legalEntityId"), SiteID: q.Get("siteId"), Type: q.Get("type"), Status: q.Get("status")}
	for _, c := range []struct {
		field, rule string
		invalid     bool
	}{
		{"legalEntityId", "must be a UUID", f.LegalEntityID != "" && !ids.Valid(f.LegalEntityID)},
		{"siteId", "must be a UUID", f.SiteID != "" && !ids.Valid(f.SiteID)},
		{"type", invalidWarehouseTy, f.Type != "" && !warehouseType(f.Type)},
		{"status", "must be active or inactive", f.Status != "" && !validStatus(f.Status)},
	} {
		if c.invalid {
			badQuery(w, r, c.field, c.rule)
			return
		}
	}
	page, err := a.store.ListWarehouses(r.Context(), f, after, limit)
	if err != nil {
		slog.Error("list warehouses failed", "err", err, "correlationId", middleware.GetReqID(r.Context()))
		httpx.Fail(w, r, err)
		return
	}
	body := warehouseList{Items: page.Items}
	if page.Next != nil {
		body.NextCursor = encodeCodeCursor(*page.Next)
	}
	httpx.JSON(w, http.StatusOK, body)
}

func warehouseRoute(next func(http.ResponseWriter, *http.Request, string)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !validTenant(w, r) {
			return
		}
		id := chi.URLParam(r, "id")
		if !ids.Valid(id) {
			httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, store.CodeWarehouseNotFound, "warehouse not found"))
			return
		}
		next(w, r, id)
	}
}

func (a *API) getWarehouse(w http.ResponseWriter, r *http.Request, id string) {
	warehouse, err := a.store.GetWarehouse(r.Context(), id)
	if err != nil {
		slog.Error("get warehouse failed", "err", err, "correlationId", middleware.GetReqID(r.Context()))
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, warehouse)
}

func (a *API) updateWarehouse(w http.ResponseWriter, r *http.Request, id string) {
	var req updateWarehouseRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if req.Name != nil {
		trimmed := strings.TrimSpace(*req.Name)
		req.Name = &trimmed
	}
	fields := validateShared(req.Name, req.TimeZone, nil, nil)
	if req.Version == nil || *req.Version < 1 {
		fields["version"] = "is required and must be at least 1"
	}
	if req.Status != nil && !validStatus(*req.Status) {
		fields["status"] = "must be active or inactive"
	}
	if req.Type != nil && !warehouseType(*req.Type) {
		fields["type"] = invalidWarehouseTy
	}
	optionalID(fields, "addressId", req.AddressID, true)
	if req.SiteID != nil {
		canonical := canonicalID(*req.SiteID)
		req.SiteID = &canonical
		if !canonicalUUID(canonical) {
			fields["siteId"] = "must be a UUID"
		}
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	warehouse, err := a.store.UpdateWarehouse(r.Context(), id, *req.Version, req.WarehousePatch)
	if err != nil {
		slog.Error("update warehouse failed", "err", err, "correlationId", middleware.GetReqID(r.Context()))
		httpx.Fail(w, r, err)
		return
	}
	if warehouse.Version != *req.Version {
		a.warehouseUpdated.Add(1)
	}
	httpx.JSON(w, http.StatusOK, warehouse)
}

func canonicalID(id string) string { return strings.ToLower(strings.TrimSpace(id)) }

func canonicalUUID(id string) bool { return len(id) == 36 && ids.Valid(id) }
