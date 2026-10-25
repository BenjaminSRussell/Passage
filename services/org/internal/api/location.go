package api

import (
	"log/slog"
	"net/http"
	"regexp"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/org/internal/store"
)

const (
	locations           = "/v1/org/locations"
	invalidLocationTy   = "must be storage, receiving, shipping or quarantine"
	invalidLocationCode = "must be 1 to 20 characters of A-Z, 0-9, _, . or -, starting with A-Z or 0-9"
)

var locationCode = regexp.MustCompile(`^[A-Z0-9][A-Z0-9_.-]{0,19}$`)

type createLocationRequest struct {
	WarehouseID string  `json:"warehouseId"`
	Code        string  `json:"code"`
	Name        string  `json:"name"`
	Type        *string `json:"type"`
}

type locationList struct {
	Items      []store.Location `json:"items"`
	NextCursor string           `json:"nextCursor,omitempty"`
}

func locationType(value string) bool {
	switch value {
	case "storage", "receiving", "shipping", "quarantine":
		return true
	}
	return false
}

func (a *API) registerLocations(r chi.Router) {
	r.With(authz.RequireDuty(dutySite)).Post(locations, a.createLocation)
	r.With(authz.RequireDuty(dutyRead)).Get(locations, a.listLocations)
	r.With(authz.RequireDuty(dutyRead)).Get(locations+"/{id}", locationRoute(a.getLocation))
}

func (a *API) createLocation(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var req createLocationRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	in := store.Location{WarehouseID: canonicalID(req.WarehouseID), Code: strings.ToUpper(req.Code), Name: strings.TrimSpace(req.Name)}
	fields := validateShared(&in.Name, nil, nil, nil)
	if req.Type != nil {
		in.Type = *req.Type
		if !locationType(in.Type) {
			fields["type"] = invalidLocationTy
		}
	}
	if !canonicalUUID(in.WarehouseID) {
		fields["warehouseId"] = "is required and must be a UUID"
	}
	if !locationCode.MatchString(in.Code) {
		fields["code"] = invalidLocationCode
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	location, err := a.store.CreateLocation(r.Context(), in, r.Header.Get(idempotencyKey))
	if err != nil {
		slog.Error("create location failed", "err", err, "correlationId", middleware.GetReqID(r.Context()))
		httpx.Fail(w, r, err)
		return
	}
	a.locationCreated.Add(1)
	httpx.JSON(w, http.StatusCreated, location)
}

func (a *API) listLocations(w http.ResponseWriter, r *http.Request) {
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
	f := store.LocationFilter{LegalEntityID: q.Get("legalEntityId"), WarehouseID: q.Get("warehouseId"), Type: q.Get("type"), Status: q.Get("status")}
	for _, c := range []struct {
		field, rule string
		invalid     bool
	}{
		{"legalEntityId", "must be a UUID", f.LegalEntityID != "" && !ids.Valid(f.LegalEntityID)},
		{"warehouseId", "must be a UUID", f.WarehouseID != "" && !ids.Valid(f.WarehouseID)},
		{"type", invalidLocationTy, f.Type != "" && !locationType(f.Type)},
		{"status", "must be active or inactive", f.Status != "" && !validStatus(f.Status)},
	} {
		if c.invalid {
			badQuery(w, r, c.field, c.rule)
			return
		}
	}
	page, err := a.store.ListLocations(r.Context(), f, after, limit)
	if err != nil {
		slog.Error("list locations failed", "err", err, "correlationId", middleware.GetReqID(r.Context()))
		httpx.Fail(w, r, err)
		return
	}
	body := locationList{Items: page.Items}
	if page.Next != nil {
		body.NextCursor = encodeCodeCursor(*page.Next)
	}
	httpx.JSON(w, http.StatusOK, body)
}

func locationRoute(next func(http.ResponseWriter, *http.Request, string)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !validTenant(w, r) {
			return
		}
		id := chi.URLParam(r, "id")
		if !ids.Valid(id) {
			httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, store.CodeLocationNotFound, "location not found"))
			return
		}
		next(w, r, id)
	}
}

func (a *API) getLocation(w http.ResponseWriter, r *http.Request, id string) {
	location, err := a.store.GetLocation(r.Context(), id)
	if err != nil {
		slog.Error("get location failed", "err", err, "correlationId", middleware.GetReqID(r.Context()))
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, location)
}
