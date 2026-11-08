package api

import (
	"encoding/base64"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/org/internal/store"
)

const (
	dutyOperatingUnit = "org.operating-unit.write"
	operatingUnits    = "/v1/org/operating-units"
	costCenterType    = "cost_center"
)

var (
	operatingUnitTypes = []string{"department", costCenterType, "business_unit", "value_stream"}
	operatingUnitCode  = regexp.MustCompile(`^[A-Z0-9][A-Z0-9_-]{1,19}$`)
)

type createOperatingUnitRequest struct {
	Type                 string  `json:"type"`
	Code                 string  `json:"code"`
	Name                 string  `json:"name"`
	DefaultLegalEntityID *string `json:"defaultLegalEntityId"`
	ManagerPartyID       *string `json:"managerPartyId"`
}

type updateOperatingUnitRequest struct {
	Version *int `json:"version"`
	store.OperatingUnitPatch
}

type operatingUnitList struct {
	Items      []store.OperatingUnit `json:"items"`
	NextCursor string                `json:"nextCursor,omitempty"`
}

func (a *API) registerOperatingUnits(r chi.Router) {
	r.With(authz.RequireDuty(dutyOperatingUnit)).Post(operatingUnits, a.createOperatingUnit)
	r.With(authz.RequireDuty(dutyOperatingUnit)).Post(operatingUnitsImport, a.importOperatingUnits)
	r.With(authz.RequireDuty(dutyRead)).Get(operatingUnits, a.listOperatingUnits)
	r.With(authz.RequireDuty(dutyRead)).Get(operatingUnits+"/{id}", operatingUnitRoute(a.getOperatingUnit))
	r.With(authz.RequireDuty(dutyOperatingUnit)).Patch(operatingUnits+"/{id}", operatingUnitRoute(a.updateOperatingUnit))
}

func validationFailed(w http.ResponseWriter, r *http.Request, fields map[string]string) {
	p := httpx.NewProblem(http.StatusBadRequest, "COM-0001", validationDetail)
	p.Errors = fields
	httpx.Fail(w, r, p)
}

func optionalID(fields map[string]string, field string, id *string, allowEmpty bool) {
	if id != nil && !(allowEmpty && *id == "") && !ids.Valid(*id) {
		fields[field] = "must be a UUID"
	}
}

func (a *API) createOperatingUnit(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	idemKey, keyOK := idempotencyKeyOf(w, r)
	if !keyOK {
		return
	}
	var req createOperatingUnitRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	in := store.OperatingUnit{Type: req.Type, Code: strings.ToUpper(req.Code), Name: strings.TrimSpace(req.Name),
		DefaultLegalEntityID: req.DefaultLegalEntityID, ManagerPartyID: req.ManagerPartyID}
	fields := map[string]string{}
	if !slices.Contains(operatingUnitTypes, in.Type) {
		fields["type"] = "must be department, cost_center, business_unit or value_stream"
	}
	if !operatingUnitCode.MatchString(in.Code) {
		fields["code"] = "must be 2 to 20 characters of A-Z, 0-9, _ or -, starting with A-Z or 0-9"
	}
	if in.Name == "" || utf8.RuneCountInString(in.Name) > maxNameLength {
		fields["name"] = fmt.Sprintf("must be 1 to %d characters", maxNameLength)
	}
	optionalID(fields, "defaultLegalEntityId", in.DefaultLegalEntityID, false)
	optionalID(fields, "managerPartyId", in.ManagerPartyID, false)
	if in.ManagerPartyID != nil && in.Type != costCenterType {
		fields["managerPartyId"] = "is allowed only for cost_center units"
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	unit, err := a.store.CreateOperatingUnit(r.Context(), in, idemKey)
	if err != nil {
		slog.Error("create operating unit failed", "err", err, "correlationId", middleware.GetReqID(r.Context()))
		httpx.Fail(w, r, err)
		return
	}
	a.ouCreated.Add(1)
	httpx.JSON(w, http.StatusCreated, unit)
}

func (a *API) listOperatingUnits(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	limit, cursor, err := pageParams(r)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	var after store.OperatingUnitKey
	if cursor != "" {
		var ok bool
		if after.Type, after.Code, ok = strings.Cut(cursor, "/"); !ok {
			badQuery(w, r, "cursor", "is invalid")
			return
		}
	}
	q := r.URL.Query()
	f := store.OperatingUnitFilter{Type: q.Get("type"), DefaultLegalEntityID: q.Get("defaultLegalEntityId"), Status: q.Get("status"), HierarchyTypeID: q.Get("hierarchyTypeId")}
	asOf, asOfErr := a.now().UTC().Truncate(24*time.Hour), error(nil)
	if raw := q.Get("asOf"); raw != "" {
		asOf, asOfErr = time.Parse(dateLayout, raw)
	}
	f.AsOf = asOf
	for _, c := range []struct {
		field, rule string
		invalid     bool
	}{
		{"type", "must be department, cost_center, business_unit or value_stream", f.Type != "" && !slices.Contains(operatingUnitTypes, f.Type)},
		{"defaultLegalEntityId", "must be a UUID", f.DefaultLegalEntityID != "" && !ids.Valid(f.DefaultLegalEntityID)},
		{"status", "must be active or inactive", f.Status != "" && !validStatus(f.Status)},
		{"hierarchyTypeId", "must be a UUID", f.HierarchyTypeID != "" && !ids.Valid(f.HierarchyTypeID)},
		{"asOf", "must be a date in YYYY-MM-DD form", asOfErr != nil},
		{"asOf", "is allowed only together with hierarchyTypeId", q.Get("asOf") != "" && f.HierarchyTypeID == ""},
	} {
		if c.invalid {
			badQuery(w, r, c.field, c.rule)
			return
		}
	}
	page, err := a.store.ListOperatingUnits(r.Context(), f, after, limit)
	if err != nil {
		slog.Error("list operating units failed", "err", err, "correlationId", middleware.GetReqID(r.Context()))
		httpx.Fail(w, r, err)
		return
	}
	body := operatingUnitList{Items: page.Items}
	if page.Next != nil {
		body.NextCursor = base64.RawURLEncoding.EncodeToString([]byte(page.Next.Type + "/" + page.Next.Code))
	}
	httpx.JSON(w, http.StatusOK, body)
}

func operatingUnitRoute(next func(http.ResponseWriter, *http.Request, string)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !validTenant(w, r) {
			return
		}
		id := chi.URLParam(r, "id")
		if !ids.Valid(id) {
			httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, store.CodeOUNotFound, "operating unit not found"))
			return
		}
		next(w, r, id)
	}
}

func (a *API) getOperatingUnit(w http.ResponseWriter, r *http.Request, id string) {
	unit, err := a.store.GetOperatingUnit(r.Context(), id)
	if err != nil {
		slog.Error("get operating unit failed", "err", err, "correlationId", middleware.GetReqID(r.Context()))
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, unit)
}

func (a *API) updateOperatingUnit(w http.ResponseWriter, r *http.Request, id string) {
	var req updateOperatingUnitRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	if req.Version == nil || *req.Version < 1 {
		fields["version"] = "is required and must be at least 1"
	}
	if req.Name != nil {
		trimmed := strings.TrimSpace(*req.Name)
		req.Name = &trimmed
		if trimmed == "" || utf8.RuneCountInString(trimmed) > maxNameLength {
			fields["name"] = fmt.Sprintf("must be 1 to %d characters", maxNameLength)
		}
	}
	if req.Status != nil && !validStatus(*req.Status) {
		fields["status"] = "must be active or inactive"
	}
	optionalID(fields, "defaultLegalEntityId", req.DefaultLegalEntityID, true)
	optionalID(fields, "managerPartyId", req.ManagerPartyID, true)
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	unit, err := a.store.UpdateOperatingUnit(r.Context(), id, *req.Version, req.OperatingUnitPatch)
	if err != nil {
		slog.Error("update operating unit failed", "err", err, "correlationId", middleware.GetReqID(r.Context()))
		httpx.Fail(w, r, err)
		return
	}
	if unit.Version != *req.Version {
		a.ouUpdated.Add(1)
	}
	httpx.JSON(w, http.StatusOK, unit)
}
