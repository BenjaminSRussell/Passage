package api

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/planning/internal/domain"
	"github.com/BenjaminSRussell/passage/services/planning/internal/store"
)

var (
	maxDAF      = decimal.RequireFromString("999.999") // numeric(6,3)
	maxPriority = decimal.NewFromInt(1000000)
)

// bufferProfileRecordDTO is the wire form of a stored buffer profile (FR-22.16).
// Version is also sent as the ETag.
type bufferProfileRecordDTO struct {
	ID                  string `json:"id"`
	Version             int    `json:"version"`
	Code                string `json:"code"`
	ItemType            string `json:"itemType"`
	LeadTimeFactor      string `json:"leadTimeFactor"`
	VariabilityFactor   string `json:"variabilityFactor"`
	OrderCycleDays      int    `json:"orderCycleDays"`
	MinOrderQty         string `json:"minOrderQty"`
	SpikeThresholdPct   string `json:"spikeThresholdPct"`
	SpikeHorizonDays    *int   `json:"spikeHorizonDays"`
	AduWindowPastDays   int    `json:"aduWindowPastDays"`
	AduWindowFutureDays int    `json:"aduWindowFutureDays"`
}

func bufferProfileDTOOf(p store.BufferProfile) bufferProfileRecordDTO {
	s := p.Spec
	return bufferProfileRecordDTO{ID: p.ID, Version: p.Version, Code: s.Code, ItemType: s.ItemType,
		LeadTimeFactor: s.LeadTimeFactor.String(), VariabilityFactor: s.VariabilityFactor.String(),
		OrderCycleDays: s.OrderCycleDays, MinOrderQty: s.MinOrderQty.String(), SpikeThresholdPct: s.SpikeThresholdPct.String(),
		SpikeHorizonDays: s.SpikeHorizonDays, AduWindowPastDays: s.PastDays, AduWindowFutureDays: s.FutureDays}
}

func writeBufferProfile(w http.ResponseWriter, status int, p store.BufferProfile) {
	w.Header().Set("ETag", etagOf(p.Version))
	httpx.JSON(w, status, bufferProfileDTOOf(p))
}

// bufferProfileRequest is the body of a create or a full update. Omitted
// fields keep the defaults set before decoding (4.22.13: spike threshold 50,
// ADU window 90 past and 0 future days, minimum order qty 0). Factors and
// quantities are decimal strings, never floats.
type bufferProfileRequest struct {
	Code                string `json:"code"`
	ItemType            string `json:"itemType"`
	LeadTimeFactor      string `json:"leadTimeFactor"`
	VariabilityFactor   string `json:"variabilityFactor"`
	OrderCycleDays      int    `json:"orderCycleDays"`
	MinOrderQty         string `json:"minOrderQty"`
	SpikeThresholdPct   string `json:"spikeThresholdPct"`
	SpikeHorizonDays    *int   `json:"spikeHorizonDays"`
	AduWindowPastDays   int    `json:"aduWindowPastDays"`
	AduWindowFutureDays int    `json:"aduWindowFutureDays"`
}

// decodeBufferProfile reads and validates the body; it answers the request
// itself and returns false when the body is unusable.
func decodeBufferProfile(w http.ResponseWriter, r *http.Request) (domain.BufferProfileSpec, bool) {
	d := domain.DefaultBufferProfile("", "")
	req := bufferProfileRequest{MinOrderQty: "0", SpikeThresholdPct: d.SpikeThresholdPct.String(), AduWindowPastDays: d.PastDays}
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return domain.BufferProfileSpec{}, false
	}
	spec := domain.BufferProfileSpec{Code: req.Code, ItemType: req.ItemType, OrderCycleDays: req.OrderCycleDays,
		SpikeHorizonDays: req.SpikeHorizonDays, PastDays: req.AduWindowPastDays, FutureDays: req.AduWindowFutureDays}
	fields := map[string]string{}
	parse := func(name, raw string, dst *decimal.Decimal) {
		v, err := decimal.NewFromString(raw)
		if err != nil {
			fields[name] = "must be a decimal string"
			return
		}
		*dst = v
	}
	parse("leadTimeFactor", req.LeadTimeFactor, &spec.LeadTimeFactor)
	parse("variabilityFactor", req.VariabilityFactor, &spec.VariabilityFactor)
	parse("minOrderQty", req.MinOrderQty, &spec.MinOrderQty)
	parse("spikeThresholdPct", req.SpikeThresholdPct, &spec.SpikeThresholdPct)
	for k, v := range spec.Validate() {
		if _, ok := fields[k]; !ok {
			fields[k] = v
		}
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return domain.BufferProfileSpec{}, false
	}
	return spec, true
}

// bufferProfileID reads {id}; an ill-formed id cannot exist, so it is 404.
func bufferProfileID(w http.ResponseWriter, r *http.Request) (string, bool) {
	id := chi.URLParam(r, "id")
	if !ids.Valid(id) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, store.CodeProfileNotFound, "buffer profile not found"))
		return "", false
	}
	return id, true
}

// listBufferProfiles returns the tenant's buffer profiles ordered by code.
func (a *API) listBufferProfiles(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	limit, ok := limitParam(w, r)
	if !ok {
		return
	}
	profiles, err := a.store.ListBufferProfiles(r.Context(), limit)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	out := make([]bufferProfileRecordDTO, 0, len(profiles))
	for _, p := range profiles {
		out = append(out, bufferProfileDTOOf(p))
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": out})
}

func (a *API) getBufferProfile(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id, ok := bufferProfileID(w, r)
	if !ok {
		return
	}
	p, err := a.store.GetBufferProfile(r.Context(), id)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	writeBufferProfile(w, http.StatusOK, p)
}

// createBufferProfile stores a new buffer profile (Maintain planning setup).
// An optional Idempotency-Key makes a retry return the first result with 200
// and Idempotent-Replayed: true.
func (a *API) createBufferProfile(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	key := r.Header.Get(idempotencyKey)
	if len(key) > 255 {
		validationFailed(w, r, map[string]string{idempotencyKey: "must be at most 255 characters"})
		return
	}
	spec, ok := decodeBufferProfile(w, r)
	if !ok {
		return
	}
	body, _ := json.Marshal(spec)
	p, replayed, err := a.store.CreateBufferProfile(r.Context(), store.BufferProfileCreate{
		IdempotencyKey: key, InputHash: fmt.Sprintf("%x", sha256.Sum256(body)), Spec: spec,
	})
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if replayed {
		w.Header().Set("Idempotent-Replayed", "true")
		writeBufferProfile(w, http.StatusOK, p)
		return
	}
	writeBufferProfile(w, http.StatusCreated, p)
}

// updateBufferProfile replaces a buffer profile under If-Match; omitted fields
// take the defaults, as on create.
func (a *API) updateBufferProfile(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id, ok := bufferProfileID(w, r)
	if !ok {
		return
	}
	expected, ok := ifMatch(w, r, 1)
	if !ok {
		return
	}
	spec, ok := decodeBufferProfile(w, r)
	if !ok {
		return
	}
	p, err := a.store.UpdateBufferProfile(r.Context(), id, expected, spec)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	writeBufferProfile(w, http.StatusOK, p)
}

// ddmrpBufferDTO is the wire form of a stored buffer: zones, net-flow position
// and the planned order it implies (BR-22.11). Priority is NFP / TOG.
type ddmrpBufferDTO struct {
	ID              string   `json:"id"`
	ItemID          string   `json:"itemId"`
	SiteID          string   `json:"siteId"`
	WarehouseID     *string  `json:"warehouseId"`
	ProfileID       string   `json:"profileId"`
	ProfileCode     string   `json:"profileCode"`
	ADU             string   `json:"adu"`
	DLTDays         string   `json:"dltDays"`
	DAF             string   `json:"daf"`
	AdjustedADU     string   `json:"adjustedAdu"`
	Zones           zonesDTO `json:"zones"`
	QualifiedDemand string   `json:"qualifiedDemand"`
	NetFlow         string   `json:"netFlow"`
	PlannedQty      string   `json:"plannedQty"`
	Priority        string   `json:"priority"`
	Zone            string   `json:"zone"`
	CalculatedAt    string   `json:"calculatedAt"`
}

func ddmrpBufferDTOOf(b store.DdmrpBuffer) ddmrpBufferDTO {
	st := b.Status
	return ddmrpBufferDTO{ID: b.ID, ItemID: b.ItemID, SiteID: b.SiteID, WarehouseID: optionalString(b.WarehouseID),
		ProfileID: b.ProfileID, ProfileCode: b.ProfileCode, ADU: b.ADU.String(), DLTDays: b.DLTDays.String(), DAF: b.DAF.String(),
		AdjustedADU: st.AdjustedADU.String(), Zones: toZonesDTO(st.Zones), QualifiedDemand: st.QualifiedDemand.String(),
		NetFlow: st.NetFlow.String(), PlannedQty: st.PlannedQty.String(), Priority: st.Priority.String(), Zone: st.Zone,
		CalculatedAt: b.CalculatedAt}
}

// listDdmrpBuffers is the buffer status inquiry (4.22.14): stored buffers,
// filtered by site, zone and maximum priority, most urgent first.
func (a *API) listDdmrpBuffers(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	q := r.URL.Query()
	query := store.BufferQuery{SiteID: q.Get("siteId"), Zone: q.Get("zone")}
	fields := map[string]string{}
	if query.SiteID != "" && !ids.Valid(query.SiteID) {
		fields["siteId"] = "must be a UUID"
	}
	switch query.Zone {
	case "", domain.ZoneRed, domain.ZoneYellow, domain.ZoneGreen:
	default:
		fields["zone"] = "must be red, yellow or green"
	}
	if raw := q.Get("maxPriority"); raw != "" {
		v, err := decimal.NewFromString(raw)
		if err != nil || v.Abs().GreaterThanOrEqual(maxPriority) {
			fields["maxPriority"] = "must be a decimal, for example 0.5 for 50%"
		} else {
			query.MaxPriority = &v
		}
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	limit, ok := limitParam(w, r)
	if !ok {
		return
	}
	query.Limit = limit
	buffers, err := a.store.ListDdmrpBuffers(r.Context(), query)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	out := make([]ddmrpBufferDTO, 0, len(buffers))
	for _, b := range buffers {
		out = append(out, ddmrpBufferDTOOf(b))
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": out})
}

// recalculateBufferRequest is the body of :recalculate. The stored profile
// supplies the factors, spike threshold, spike horizon and ADU window; the rest
// is the state of the item at the site today (until the run supplies it).
type recalculateBufferRequest struct {
	ItemID           string           `json:"itemId"`
	SiteID           string           `json:"siteId"`
	WarehouseID      string           `json:"warehouseId"`
	ProfileID        string           `json:"profileId"`
	ADU              *decimal.Decimal `json:"adu"`
	History          []dayQtyDTO      `json:"history"`
	DAF              *decimal.Decimal `json:"daf"`
	DLTDays          decimal.Decimal  `json:"dltDays"`
	OnHand           decimal.Decimal  `json:"onHand"`
	OpenSupply       decimal.Decimal  `json:"openSupply"`
	Demand           []dayQtyDTO      `json:"demand"`
	OrderMultiple    decimal.Decimal  `json:"orderMultiple"`
	SpikeHorizonDays *int             `json:"spikeHorizonDays"`
}

func (req recalculateBufferRequest) validate() map[string]string {
	f := map[string]string{}
	if !ids.Valid(req.ItemID) {
		f["itemId"] = "must be a UUID"
	}
	if !ids.Valid(req.SiteID) {
		f["siteId"] = "must be a UUID"
	}
	if req.WarehouseID != "" && !ids.Valid(req.WarehouseID) {
		f["warehouseId"] = "must be a UUID"
	}
	if !ids.Valid(req.ProfileID) {
		f["profileId"] = "must be a UUID"
	}
	// What the buffer row stores: DLT numeric(8,2), DAF numeric(6,3).
	if !req.DLTDays.Equal(req.DLTDays.Round(2)) {
		f["dltDays"] = "must have at most 2 decimal places"
	}
	if req.DAF != nil && (req.DAF.GreaterThan(maxDAF) || !req.DAF.Equal(req.DAF.Round(3))) {
		f["daf"] = "must be 0 to 999.999 with at most 3 decimal places"
	}
	return f
}

// recalculateDdmrpBuffer answers POST /v1/planning/ddmrp/buffers:recalculate.
// It evaluates the buffer with the stored profile (BR-22.11) through the same
// path as :evaluate and replaces the stored result of the item, site and
// warehouse scope. Repeating a request stores the same row again, so the
// method needs no Idempotency-Key.
func (a *API) recalculateDdmrpBuffer(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var req recalculateBufferRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if fields := req.validate(); len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	profile, err := a.store.GetBufferProfile(r.Context(), req.ProfileID)
	if err != nil {
		var p *httpx.Problem
		if errors.As(err, &p) && p.Status == http.StatusNotFound {
			// The profile is named in the body, so an unknown one is 422.
			err = httpx.NewProblem(http.StatusUnprocessableEntity, store.CodeProfileNotFound, "buffer profile "+req.ProfileID+" does not exist")
		}
		httpx.Fail(w, r, err)
		return
	}
	ps := profile.Spec
	spike, past, future := ps.SpikeThresholdPct, ps.PastDays, ps.FutureDays
	horizon := req.SpikeHorizonDays
	if horizon == nil {
		horizon = ps.SpikeHorizonDays
	}
	res, fields, err := evaluateBufferInput(bufferEvalRequest{
		Profile: bufferProfileDTO{LeadTimeFactor: ps.LeadTimeFactor, VariabilityFactor: ps.VariabilityFactor,
			OrderCycleDays: ps.OrderCycleDays, MinOrderQty: ps.MinOrderQty, SpikeThresholdPct: &spike},
		ADU: req.ADU, History: req.History, PastDays: &past, FutureDays: &future, DAF: req.DAF,
		DLTDays: req.DLTDays, OnHand: req.OnHand, OpenSupply: req.OpenSupply, Demand: req.Demand,
		SpikeHorizonDays: horizon, OrderMultiple: req.OrderMultiple,
	})
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	stored, err := a.store.UpsertDdmrpBuffer(r.Context(), store.DdmrpBuffer{ItemID: req.ItemID, SiteID: req.SiteID,
		WarehouseID: req.WarehouseID, ProfileID: profile.ID, ADU: res.ADU, DLTDays: req.DLTDays, DAF: res.DAF,
		Status: res.Status})
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, ddmrpBufferDTOOf(stored))
}

// registerBuffers mounts the buffer profile and stored buffer routes
// (FR-22.16). Profiles are planning setup (4.22.17); the recalculation is the
// run-side job of 4.22.16, so it needs Run master planning.
func (a *API) registerBuffers(r chi.Router) {
	r.With(authz.RequireDuty(dutyRead)).Get("/v1/planning/buffer-profiles", a.listBufferProfiles)
	r.With(authz.RequireDuty(dutyMaintainSetup)).Post("/v1/planning/buffer-profiles", a.createBufferProfile)
	r.With(authz.RequireDuty(dutyRead)).Get("/v1/planning/buffer-profiles/{id}", a.getBufferProfile)
	r.With(authz.RequireDuty(dutyMaintainSetup)).Put("/v1/planning/buffer-profiles/{id}", a.updateBufferProfile)
	r.With(authz.RequireDuty(dutyRead)).Get("/v1/planning/ddmrp/buffers", a.listDdmrpBuffers)
	r.With(authz.RequireDuty(dutyRun)).Post("/v1/planning/ddmrp/buffers:recalculate", a.recalculateDdmrpBuffer)
}
