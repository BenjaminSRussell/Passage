package api

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/planning/internal/domain"
	"github.com/BenjaminSRussell/passage/services/planning/internal/store"
)

func (a *API) registerAllocation(r chi.Router) {
	r.With(authz.RequireDuty(dutyRead)).Get("/v1/planning/forecast-allocation-keys", a.listAllocationKeys)
	r.With(authz.RequireDuty(dutyMaintainSetup)).Post("/v1/planning/forecast-allocation-keys", a.createAllocationKey)
	r.With(authz.RequireDuty(dutyRead)).Get("/v1/planning/forecast-allocation-keys/{code}", a.getAllocationKey)
	r.With(authz.RequireDuty(dutyMaintainSetup)).Put("/v1/planning/forecast-allocation-keys/{code}", a.updateAllocationKey)
	r.With(authz.RequireDuty(dutyMaintainForecast)).Post("/v1/planning/forecasts/{modelCode}:allocate", a.allocateForecast)
}

type allocationLineDTO struct {
	ItemID    string          `json:"itemId"`
	Share     decimal.Decimal `json:"share"`
	UomFactor decimal.Decimal `json:"uomFactor"`
}

// allocationKeyDTO is the wire form of a key. Lines is empty in a list; LineCount
// always says how many items the key holds.
type allocationKeyDTO struct {
	ID          string              `json:"id"`
	Version     int                 `json:"version"`
	Code        string              `json:"code"`
	Name        string              `json:"name"`
	ForecastUom string              `json:"forecastUom"`
	LineCount   int                 `json:"lineCount"`
	Lines       []allocationLineDTO `json:"lines"`
}

func allocationKeyDTOOf(k store.AllocationKey) allocationKeyDTO {
	lines := make([]allocationLineDTO, 0, len(k.Spec.Lines))
	for _, l := range k.Spec.Lines {
		lines = append(lines, allocationLineDTO{ItemID: l.ItemID, Share: l.Share, UomFactor: l.UomFactor})
	}
	return allocationKeyDTO{ID: k.ID, Version: k.Version, Code: k.Spec.Code, Name: k.Spec.Name, ForecastUom: k.Spec.ForecastUom, LineCount: k.LineCount, Lines: lines}
}

func writeAllocationKey(w http.ResponseWriter, status int, k store.AllocationKey) {
	w.Header().Set("ETag", etagOf(k.Version))
	httpx.JSON(w, status, allocationKeyDTOOf(k))
}

// allocationKeyRequest is a create or update body. A line without uomFactor keeps
// the factor 1 (the forecast unit is the inventory unit).
type allocationKeyRequest struct {
	Code        string              `json:"code"`
	Name        string              `json:"name"`
	ForecastUom string              `json:"forecastUom"`
	Lines       []allocationLineDTO `json:"lines"`
}

func (req *allocationKeyRequest) UnmarshalJSON(b []byte) error {
	type plain struct {
		Code        string `json:"code"`
		Name        string `json:"name"`
		ForecastUom string `json:"forecastUom"`
		Lines       []struct {
			ItemID    string           `json:"itemId"`
			Share     decimal.Decimal  `json:"share"`
			UomFactor *decimal.Decimal `json:"uomFactor"`
		} `json:"lines"`
	}
	var p plain
	if err := json.Unmarshal(b, &p); err != nil {
		return err
	}
	req.Code, req.Name, req.ForecastUom = p.Code, p.Name, p.ForecastUom
	req.Lines = make([]allocationLineDTO, len(p.Lines))
	for i, l := range p.Lines {
		req.Lines[i] = allocationLineDTO{ItemID: l.ItemID, Share: l.Share, UomFactor: decimal.NewFromInt(1)}
		if l.UomFactor != nil {
			req.Lines[i].UomFactor = *l.UomFactor
		}
	}
	return nil
}

// spec is the domain form of the body. Item ids are put in canonical form, so the
// same item spelled in capitals is caught as a repeat, and the remainder of a split
// goes to the same item whatever spelling the caller used.
func (req allocationKeyRequest) spec(code string) domain.AllocationKeySpec {
	lines := make([]domain.AllocationLine, len(req.Lines))
	for i, l := range req.Lines {
		item := l.ItemID
		if c, ok := ids.Canonical(item); ok {
			item = c
		}
		lines[i] = domain.AllocationLine{ItemID: item, Share: l.Share, UomFactor: l.UomFactor}
	}
	return domain.AllocationKeySpec{Code: code, Name: req.Name, ForecastUom: req.ForecastUom, Lines: lines}
}

func allocationCodeParam(w http.ResponseWriter, r *http.Request) (string, bool) {
	code := chi.URLParam(r, "code")
	if !planCodePattern.MatchString(code) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, store.CodeAllocationKeyNotFound, "allocation key not found"))
		return "", false
	}
	return code, true
}

// listAllocationKeys returns the keys ordered by code, without their lines.
func (a *API) listAllocationKeys(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	limit, ok := limitParam(w, r)
	if !ok {
		return
	}
	keys, err := a.store.ListAllocationKeys(r.Context(), limit)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	out := make([]allocationKeyDTO, 0, len(keys))
	for _, k := range keys {
		out = append(out, allocationKeyDTOOf(k))
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": out})
}

func (a *API) getAllocationKey(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	code, ok := allocationCodeParam(w, r)
	if !ok {
		return
	}
	k, err := a.store.GetAllocationKey(r.Context(), code)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	writeAllocationKey(w, http.StatusOK, k)
}

// createAllocationKey stores a new key (FR-22.8). An optional Idempotency-Key makes a
// retry return the first result with 200 and Idempotent-Replayed: true.
func (a *API) createAllocationKey(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	key := r.Header.Get(idempotencyKey)
	if len(key) > 255 {
		validationFailed(w, r, map[string]string{idempotencyKey: "must be at most 255 characters"})
		return
	}
	var req allocationKeyRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	spec := req.spec(req.Code)
	if fields := spec.Validate(); len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	body, _ := json.Marshal(allocationKeyDTOOf(store.AllocationKey{Spec: spec}))
	k, replayed, err := a.store.CreateAllocationKey(r.Context(), store.AllocationKeyCreate{
		IdempotencyKey: key, InputHash: fmt.Sprintf("%x", sha256.Sum256(body)), Spec: spec,
	})
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if replayed {
		w.Header().Set("Idempotent-Replayed", "true")
		writeAllocationKey(w, http.StatusOK, k)
		return
	}
	writeAllocationKey(w, http.StatusCreated, k)
}

// updateAllocationKey replaces the name, unit and lines of a key under If-Match.
func (a *API) updateAllocationKey(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	code, ok := allocationCodeParam(w, r)
	if !ok {
		return
	}
	expected, ok := ifMatch(w, r, 1)
	if !ok {
		return
	}
	var req allocationKeyRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	spec := req.spec(code)
	if fields := spec.Validate(); len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	k, err := a.store.UpdateAllocationKey(r.Context(), expected, spec)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	writeAllocationKey(w, http.StatusOK, k)
}

type allocatePeriodDTO struct {
	PeriodStart string          `json:"periodStart"`
	PeriodEnd   string          `json:"periodEnd"`
	Qty         decimal.Decimal `json:"qty"`
}

// allocateRequest splits aggregate quantities, in the key's forecast unit, across the
// key's items at one site.
type allocateRequest struct {
	LegalEntityID string              `json:"legalEntityId"`
	KeyCode       string              `json:"keyCode"`
	SiteID        string              `json:"siteId"`
	Periods       []allocatePeriodDTO `json:"periods"`
}

func (req allocateRequest) validate() map[string]string {
	fields := map[string]string{}
	if !planCodePattern.MatchString(req.KeyCode) {
		fields["keyCode"] = codeMessage
	}
	if !ids.Valid(req.SiteID) {
		fields["siteId"] = "must be a UUID"
	}
	if n := len(req.Periods); n == 0 || n > domain.MaxAllocationPeriods {
		fields["periods"] = "must hold 1 to " + strconv.Itoa(domain.MaxAllocationPeriods) + " periods"
	}
	seen := map[string]bool{}
	for i, p := range req.Periods {
		prefix := "periods[" + strconv.Itoa(i) + "]."
		start, errStart := time.Parse(dateLayout, p.PeriodStart)
		end, errEnd := time.Parse(dateLayout, p.PeriodEnd)
		if errStart != nil {
			fields[prefix+"periodStart"] = "must be a date as YYYY-MM-DD"
		}
		if errEnd != nil {
			fields[prefix+"periodEnd"] = "must be a date as YYYY-MM-DD"
		}
		if errStart == nil && errEnd == nil && end.Before(start) {
			fields[prefix+"periodEnd"] = "must not be before periodStart"
		}
		if !validQty(p.Qty) || p.Qty.GreaterThan(domain.MaxForecastQty) {
			fields[prefix+"qty"] = "must be 0 or more with at most 6 decimals, up to 999999999999.999999"
		}
		if seen[p.PeriodStart] {
			fields[prefix+"periodStart"] = "repeats a period of this request"
		}
		seen[p.PeriodStart] = true
	}
	return fields
}

// allocateForecast splits aggregate forecast quantities across the items of an
// allocation key and writes them as manual cells of a model (FR-22.8, BR-22.10). It
// needs If-Match on the model (0 creates it).
func (a *API) allocateForecast(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	code := chi.URLParam(r, "modelCode")
	if !planCodePattern.MatchString(code) {
		validationFailed(w, r, map[string]string{"modelCode": codeMessage})
		return
	}
	expected, ok := ifMatch(w, r, 0)
	if !ok {
		return
	}
	var req allocateRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if fields := req.validate(); len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	siteID, _ := ids.Canonical(req.SiteID)
	periods := make([]store.AllocationPeriod, len(req.Periods))
	for i, p := range req.Periods {
		periods[i] = store.AllocationPeriod{Start: p.PeriodStart, End: p.PeriodEnd, Qty: p.Qty}
	}
	out, err := a.store.AllocateForecast(r.Context(), store.AllocateRequest{
		ModelCode: code, KeyCode: req.KeyCode, LegalEntityID: req.LegalEntityID, ExpectedVersion: expected, SiteID: siteID, Periods: periods,
	})
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	w.Header().Set("ETag", etagOf(out.Model.Version))
	httpx.JSON(w, http.StatusOK, map[string]any{"model": out.Model, "keyCode": out.Key.Spec.Code, "keyVersion": out.Key.Version,
		"items": out.Items, "periods": len(periods), "cellsWritten": out.CellsWritten})
}
