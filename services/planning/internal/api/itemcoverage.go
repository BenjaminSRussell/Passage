package api

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/planning/internal/domain"
	"github.com/BenjaminSRussell/passage/services/planning/internal/store"
)

// itemCoverageDTO is the wire form of an item coverage (data model
// ItemCoverage with the FR-22.16..FR-22.20 fields). Absent references are
// null; quantities are decimal strings. Version is also sent as the ETag.
type itemCoverageDTO struct {
	ID                      string  `json:"id"`
	Version                 int     `json:"version"`
	ItemID                  string  `json:"itemId"`
	SiteID                  string  `json:"siteId"`
	WarehouseID             *string `json:"warehouseId"`
	CoverageGroupID         string  `json:"coverageGroupId"`
	CoverageGroupCode       string  `json:"coverageGroupCode"`
	MinQty                  string  `json:"minQty"`
	MaxQty                  string  `json:"maxQty"`
	SafetyMarginReceiptDays int     `json:"safetyMarginReceiptDays"`
	LeadTimeDays            *int    `json:"leadTimeDays"`
	VendorID                *string `json:"vendorId"`
	PlannedOrderType        string  `json:"plannedOrderType"`
	PlanningStrategy        string  `json:"planningStrategy"`
	PlanningScope           string  `json:"planningScope"`
	DecouplingPoint         bool    `json:"decouplingPoint"`
	BufferProfileID         *string `json:"bufferProfileId"`
	RequiredShelfLifeDays   int     `json:"requiredShelfLifeDays"`
	PriorityBased           bool    `json:"priorityBased"`
}

func optionalString(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func itemCoverageDTOOf(c store.ItemCoverage) itemCoverageDTO {
	s := c.Spec
	return itemCoverageDTO{ID: c.ID, Version: c.Version, ItemID: s.ItemID, SiteID: s.SiteID, WarehouseID: optionalString(s.WarehouseID),
		CoverageGroupID: s.CoverageGroupID, CoverageGroupCode: c.CoverageGroupCode, MinQty: s.MinQty.String(), MaxQty: s.MaxQty.String(),
		SafetyMarginReceiptDays: s.SafetyMarginReceiptDays, LeadTimeDays: s.LeadTimeDays, VendorID: optionalString(s.VendorID),
		PlannedOrderType: s.PlannedOrderType, PlanningStrategy: s.PlanningStrategy, PlanningScope: s.PlanningScope,
		DecouplingPoint: s.DecouplingPoint, BufferProfileID: optionalString(s.BufferProfileID),
		RequiredShelfLifeDays: s.RequiredShelfLifeDays, PriorityBased: s.PriorityBased}
}

func writeItemCoverage(w http.ResponseWriter, status int, c store.ItemCoverage) {
	w.Header().Set("ETag", etagOf(c.Version))
	httpx.JSON(w, status, itemCoverageDTOOf(c))
}

// itemCoverageRequest is the body of a create or a full update. A field the
// caller omits keeps the default set before decoding (min and max 0, purchase
// orders, the mts-net strategy and site scope); an explicit null leaves the
// default too. Quantities are decimal strings, never floats.
type itemCoverageRequest struct {
	LegalEntityID           string `json:"legalEntityId"`
	ItemID                  string `json:"itemId"`
	SiteID                  string `json:"siteId"`
	WarehouseID             string `json:"warehouseId"`
	CoverageGroupID         string `json:"coverageGroupId"`
	MinQty                  string `json:"minQty"`
	MaxQty                  string `json:"maxQty"`
	SafetyMarginReceiptDays int    `json:"safetyMarginReceiptDays"`
	LeadTimeDays            *int   `json:"leadTimeDays"`
	VendorID                string `json:"vendorId"`
	PlannedOrderType        string `json:"plannedOrderType"`
	PlanningStrategy        string `json:"planningStrategy"`
	PlanningScope           string `json:"planningScope"`
	DecouplingPoint         bool   `json:"decouplingPoint"`
	BufferProfileID         string `json:"bufferProfileId"`
	RequiredShelfLifeDays   int    `json:"requiredShelfLifeDays"`
	PriorityBased           bool   `json:"priorityBased"`
}

// decodeItemCoverage reads and validates the body; it answers the request
// itself and returns false when the body is unusable.
func decodeItemCoverage(w http.ResponseWriter, r *http.Request) (domain.ItemCoverageSpec, string, bool) {
	d := domain.DefaultItemCoverage("", "", "")
	req := itemCoverageRequest{MinQty: "0", MaxQty: "0", PlannedOrderType: d.PlannedOrderType,
		PlanningStrategy: d.PlanningStrategy, PlanningScope: d.PlanningScope}
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return domain.ItemCoverageSpec{}, "", false
	}
	spec := domain.ItemCoverageSpec{ItemID: req.ItemID, SiteID: req.SiteID, WarehouseID: req.WarehouseID,
		CoverageGroupID: req.CoverageGroupID, SafetyMarginReceiptDays: req.SafetyMarginReceiptDays,
		LeadTimeDays: req.LeadTimeDays, VendorID: req.VendorID, PlannedOrderType: req.PlannedOrderType,
		PlanningStrategy: req.PlanningStrategy, PlanningScope: req.PlanningScope, DecouplingPoint: req.DecouplingPoint,
		BufferProfileID: req.BufferProfileID, RequiredShelfLifeDays: req.RequiredShelfLifeDays, PriorityBased: req.PriorityBased}
	fields := map[string]string{}
	var err error
	if spec.MinQty, err = decimal.NewFromString(req.MinQty); err != nil {
		fields["minQty"] = "must be a decimal string"
	}
	if spec.MaxQty, err = decimal.NewFromString(req.MaxQty); err != nil {
		fields["maxQty"] = "must be a decimal string"
	}
	for k, v := range spec.Validate() {
		if _, ok := fields[k]; !ok {
			fields[k] = v
		}
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return domain.ItemCoverageSpec{}, "", false
	}
	return spec, req.LegalEntityID, true
}

// itemCoverageID reads {id}; an ill-formed id cannot exist, so it is 404.
func itemCoverageID(w http.ResponseWriter, r *http.Request) (string, bool) {
	id := chi.URLParam(r, "id")
	if !ids.Valid(id) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, store.CodeItemCoverageNotFound, "item coverage not found"))
		return "", false
	}
	return id, true
}

// listItemCoverages returns the tenant's item coverages, optionally of one item.
func (a *API) listItemCoverages(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	itemID := r.URL.Query().Get("itemId")
	if itemID != "" && !ids.Valid(itemID) {
		validationFailed(w, r, map[string]string{"itemId": "must be a UUID"})
		return
	}
	limit, ok := limitParam(w, r)
	if !ok {
		return
	}
	covs, err := a.store.ListItemCoverages(r.Context(), itemID, limit)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	out := make([]itemCoverageDTO, 0, len(covs))
	for _, c := range covs {
		out = append(out, itemCoverageDTOOf(c))
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": out})
}

func (a *API) getItemCoverage(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id, ok := itemCoverageID(w, r)
	if !ok {
		return
	}
	c, err := a.store.GetItemCoverage(r.Context(), id)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	writeItemCoverage(w, http.StatusOK, c)
}

// createItemCoverage stores a new item coverage (Maintain planning setup). An
// optional Idempotency-Key makes a retry return the first result with 200 and
// Idempotent-Replayed: true.
func (a *API) createItemCoverage(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	key := r.Header.Get(idempotencyKey)
	if len(key) > 255 {
		validationFailed(w, r, map[string]string{idempotencyKey: "must be at most 255 characters"})
		return
	}
	spec, legalEntityID, ok := decodeItemCoverage(w, r)
	if !ok {
		return
	}
	body, _ := json.Marshal([]any{legalEntityID, spec})
	c, replayed, err := a.store.CreateItemCoverage(r.Context(), store.ItemCoverageCreate{
		LegalEntityID: legalEntityID, IdempotencyKey: key, InputHash: fmt.Sprintf("%x", sha256.Sum256(body)), Spec: spec,
	})
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if replayed {
		w.Header().Set("Idempotent-Replayed", "true")
		writeItemCoverage(w, http.StatusOK, c)
		return
	}
	writeItemCoverage(w, http.StatusCreated, c)
}

// updateItemCoverage replaces an item coverage under If-Match; omitted fields
// take the defaults, as on create.
func (a *API) updateItemCoverage(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id, ok := itemCoverageID(w, r)
	if !ok {
		return
	}
	expected, ok := ifMatch(w, r, 1)
	if !ok {
		return
	}
	spec, _, ok := decodeItemCoverage(w, r)
	if !ok {
		return
	}
	c, err := a.store.UpdateItemCoverage(r.Context(), id, expected, spec)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	writeItemCoverage(w, http.StatusOK, c)
}
