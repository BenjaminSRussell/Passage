package api

import (
	"log/slog"
	"net/http"
	"regexp"
	"slices"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/org/internal/domain"
	"github.com/BenjaminSRussell/passage/services/org/internal/store"
)

const (
	units             = "/v1/org/uom/units"
	conversions       = "/v1/org/uom/conversions"
	unitCodeRule      = "must be 1 to 10 characters of A-Z, a-z, 0-9, / or %"
	maxUnitPrecision  = 6
	unitNameRule      = "must be 1 to 120 characters"
	unitPrecisionRule = "is required and must be an integer from 0 to 6"
)

type createUnitRequest struct {
	Code             string `json:"code"`
	Name             string `json:"name"`
	Class            string `json:"class"`
	DecimalPrecision *int   `json:"decimalPrecision"`
}

var conversionDecimal = regexp.MustCompile(`^-?[0-9]{1,16}(\.[0-9]{1,12})?$`)

type createConversionRequest struct {
	FromCode string  `json:"fromCode"`
	ToCode   string  `json:"toCode"`
	Factor   string  `json:"factor"`
	Offset   *string `json:"offset"`
}

type conversionItem struct {
	ID            string          `json:"id"`
	FromCode      string          `json:"fromCode"`
	ToCode        string          `json:"toCode"`
	Class         string          `json:"class"`
	Factor        decimal.Decimal `json:"factor"`
	Offset        decimal.Decimal `json:"offset"`
	InverseFactor decimal.Decimal `json:"inverseFactor"`
	InverseOffset decimal.Decimal `json:"inverseOffset"`
	RoundTrip     bool            `json:"roundTrip"`
	System        bool            `json:"system"`
	ProductID     *string         `json:"productId"`
	Status        string          `json:"status"`
}

func newConversionItem(c store.Conversion) conversionItem {
	inverse := domain.Conversion{Factor: c.Factor, Offset: c.Offset}
	item := conversionItem{ID: c.ID, FromCode: c.FromCode, ToCode: c.ToCode, Class: c.Class, Factor: c.Factor, Offset: c.Offset, RoundTrip: domain.RoundTrip(inverse), System: c.System, Status: c.Status}
	item.InverseFactor, item.InverseOffset = domain.Inverse(inverse)
	return item
}

func parseConversionDecimal(raw string) (decimal.Decimal, bool) {
	if !conversionDecimal.MatchString(raw) {
		return decimal.Zero, false
	}
	d, err := decimal.NewFromString(raw)
	return d, err == nil
}

type unitList struct {
	Items []store.Unit `json:"items"`
}

func (a *API) registerUnits(r chi.Router) {
	r.With(authz.RequireDuty(dutyRead)).Get(units, a.listUnits)
	r.With(authz.RequireDuty(dutyRead)).Get(units+"/{code}", a.getUnit)
	r.With(authz.RequireDuty(dutyReference)).Post(units, a.createUnit)
	r.With(authz.RequireDuty(dutyReference)).Patch(units+"/{code}", a.patchUnit)
	r.With(authz.RequireDuty(dutyRead)).Get(conversions, a.listConversions)
	r.With(authz.RequireDuty(dutyReference)).Post(conversions, a.createConversion)
}

func (a *API) listUnits(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	q := r.URL.Query()
	f := store.UnitFilter{Class: q.Get("class"), Status: q.Get("status")}
	if f.Class != "" && !slices.Contains(store.UnitClasses(), f.Class) {
		badQuery(w, r, "class", "must be one of "+strings.Join(store.UnitClasses(), ", "))
		return
	}
	if f.Status != "" && !validStatus(f.Status) {
		badQuery(w, r, "status", "must be active or inactive")
		return
	}
	items, err := a.store.ListUnits(r.Context(), f)
	if err != nil {
		slog.Error("list units failed", "err", err, "correlationId", middleware.GetReqID(r.Context()))
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, unitList{Items: items})
}

func (a *API) getUnit(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	unit, err := a.store.GetUnit(r.Context(), chi.URLParam(r, "code"))
	if err != nil {
		slog.Error("get unit failed", "err", err, "correlationId", middleware.GetReqID(r.Context()))
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, unit)
}

func (a *API) createUnit(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) || rejectScopedCaller(w, r, "create units") {
		return
	}
	idemKey, keyOK := idempotencyKeyOf(w, r)
	if !keyOK {
		return
	}
	var req createUnitRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	in := store.Unit{Code: req.Code, Name: strings.TrimSpace(req.Name), Class: req.Class}
	fields := validateShared(&in.Name, nil, nil, nil)
	if _, bad := fields["name"]; bad {
		fields["name"] = unitNameRule
	}
	if !store.ValidUnitCode(in.Code) {
		fields["code"] = unitCodeRule
	}
	if !slices.Contains(store.UnitClasses(), in.Class) {
		fields["class"] = "must be one of " + strings.Join(store.UnitClasses(), ", ")
	}
	if req.DecimalPrecision == nil || *req.DecimalPrecision < 0 || *req.DecimalPrecision > maxUnitPrecision {
		fields["decimalPrecision"] = unitPrecisionRule
	} else {
		in.DecimalPrecision = *req.DecimalPrecision
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	unit, err := a.store.CreateUnit(r.Context(), in, idemKey)
	if err != nil {
		slog.Error("create unit failed", "err", err, "correlationId", middleware.GetReqID(r.Context()))
		httpx.Fail(w, r, err)
		return
	}
	a.unitCreated.Add(1)
	httpx.JSON(w, http.StatusCreated, unit)
}

func (a *API) patchUnit(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) || rejectScopedCaller(w, r, "change unit status") {
		return
	}
	var req referenceStatusRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if !validStatus(req.Status) {
		badQuery(w, r, "status", "must be active or inactive")
		return
	}
	unit, err := a.store.SetUnitStatus(r.Context(), chi.URLParam(r, "code"), req.Status)
	if err != nil {
		slog.Error("set unit status failed", "err", err, "correlationId", middleware.GetReqID(r.Context()))
		httpx.Fail(w, r, err)
		return
	}
	a.referenceChanged.Add(1)
	httpx.JSON(w, http.StatusOK, unit)
}

func (a *API) listConversions(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	q := r.URL.Query()
	f := store.ConversionFilter{Class: q.Get("class"), Unit: q.Get("unit"), Status: q.Get("status")}
	if f.Class != "" && !slices.Contains(store.UnitClasses(), f.Class) {
		badQuery(w, r, "class", "must be one of "+strings.Join(store.UnitClasses(), ", "))
		return
	}
	if f.Status != "" && !validStatus(f.Status) {
		badQuery(w, r, "status", "must be active or inactive")
		return
	}
	convs, err := a.store.ListConversions(r.Context(), f)
	if err != nil {
		slog.Error("list conversions failed", "err", err, "correlationId", middleware.GetReqID(r.Context()))
		httpx.Fail(w, r, err)
		return
	}
	items := make([]conversionItem, len(convs))
	for i, c := range convs {
		items[i] = newConversionItem(c)
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
}

func (a *API) createConversion(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) || rejectScopedCaller(w, r, "create conversions") {
		return
	}
	idemKey, keyOK := idempotencyKeyOf(w, r)
	if !keyOK {
		return
	}
	var req createConversionRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	if !store.ValidUnitCode(req.FromCode) {
		fields["fromCode"] = unitCodeRule
	}
	if !store.ValidUnitCode(req.ToCode) {
		fields["toCode"] = unitCodeRule
	} else if req.FromCode == req.ToCode {
		fields["toCode"] = "must differ from fromCode"
	}
	factor, ok := parseConversionDecimal(req.Factor)
	if !ok || !factor.IsPositive() {
		fields["factor"] = "is required and must be a decimal greater than 0 with at most 16 integer digits and 12 decimals"
	}
	offset := decimal.Zero
	if req.Offset != nil {
		if offset, ok = parseConversionDecimal(*req.Offset); !ok {
			fields["offset"] = "must be a decimal with at most 16 integer digits and 12 decimals"
		}
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	conv, created, err := a.store.CreateConversion(r.Context(), store.Conversion{FromCode: req.FromCode, ToCode: req.ToCode, Factor: factor, Offset: offset}, idemKey)
	if err != nil {
		slog.Error("create conversion failed", "err", err, "correlationId", middleware.GetReqID(r.Context()))
		httpx.Fail(w, r, err)
		return
	}
	if created {
		a.conversionCreated.Add(1)
	}
	httpx.JSON(w, http.StatusCreated, newConversionItem(conv))
}
