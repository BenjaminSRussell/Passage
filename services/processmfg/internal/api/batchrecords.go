package api

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/processmfg/internal/domain"
	"github.com/BenjaminSRussell/passage/services/processmfg/internal/store"
)

// DutyBatchDisposition is "Maintain batch disposition" (PRD 4.24.17, AC-24.10),
// kept apart from "Maintain batch attributes" so the person who records test
// values need not be the one who releases or blocks the batch.
const DutyBatchDisposition = "production.batch-disposition.write"

const maxBatchNumber = 40

// registerBatchRecords mounts the batch quality record (FR-24.5, AC-24.10):
// attribute definitions per item, batches with their recorded attributes and
// disposition history.
func (a *API) registerBatchRecords(r chi.Router) {
	attr := authz.RequireDuty(DutyBatchAttributeWrite)
	read := authz.RequireDuty(DutyRead)
	r.With(attr).Post("/v1/production/batch-attribute-definitions", a.defineAttribute)
	r.With(read).Get("/v1/production/batch-attribute-definitions", a.listAttributeDefinitions)
	r.With(attr).Post("/v1/production/batches", a.registerBatch)
	r.With(read).Get("/v1/production/batches", a.listBatches)
	r.With(read).Get("/v1/production/batches/{id}", a.getBatch)
	r.With(attr).Put("/v1/production/batches/{id}/attributes", a.recordAttributes)
	r.With(authz.RequireDuty(DutyBatchDisposition)).Post("/v1/production/batches/{id}:disposition", a.changeDisposition)
}

func (a *API) defineAttribute(w http.ResponseWriter, r *http.Request) {
	if _, ok := principal(w, r); !ok {
		return
	}
	var req struct {
		ItemID string `json:"itemId"`
		Role   string `json:"role"`
		attributeDefDTO
	}
	idem, ok := decodeCommand(w, r, &req)
	if !ok {
		return
	}
	fields := map[string]string{}
	if !ids.Valid(req.ItemID) {
		fields["itemId"] = "must be a UUID"
	}
	if n := len(req.Name); n < 1 || n > maxAttrName {
		fields["name"] = fmt.Sprintf("must be 1 to %d characters", maxAttrName)
	}
	if len(req.Options) > maxOptions {
		fields["options"] = fmt.Sprintf("at most %d options", maxOptions)
	}
	for _, o := range req.Options {
		if len(o) > maxTextValue {
			fields["options"] = fmt.Sprintf("each option at most %d characters", maxTextValue)
		}
	}
	if req.Role == "" {
		req.Role = domain.RoleGeneric
	}
	if err := domain.CheckRole(req.Name, req.Kind, req.Role); err != nil {
		fields["role"] = "must be generic, or potency on a numeric attribute"
	}
	for name, v := range map[string]*decimal.Decimal{"target": req.Target, "min": req.Min, "max": req.Max} {
		if err := boundedDecimal(name, v); err != nil {
			fields[name] = "must be below 10^12 with at most 6 decimals"
		}
	}
	if len(fields) > 0 {
		invalid(w, r, fields)
		return
	}
	d, fresh, err := a.store.DefineAttribute(r.Context(), store.AttributeDefinition{ItemID: req.ItemID, Name: req.Name, Kind: req.Kind, Role: req.Role,
		Target: req.Target, Min: req.Min, Max: req.Max, Options: req.Options}, idem)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeCommand(w, fresh, http.StatusCreated, d)
}

func (a *API) listAttributeDefinitions(w http.ResponseWriter, r *http.Request) {
	if _, ok := principal(w, r); !ok {
		return
	}
	limit, ok := limitParam(w, r)
	if !ok {
		return
	}
	itemID := r.URL.Query().Get("itemId")
	if itemID != "" && !ids.Valid(itemID) {
		invalid(w, r, map[string]string{"itemId": "must be a UUID"})
		return
	}
	items, err := a.store.ListAttributeDefinitions(r.Context(), itemID, limit)
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
}

type batchRequest struct {
	LegalEntityID string  `json:"legalEntityId"`
	ItemID        string  `json:"itemId"`
	BatchNumber   string  `json:"batchNumber"`
	BatchOrderID  *string `json:"batchOrderId"`
	Disposition   string  `json:"disposition"`
}

func (a *API) registerBatch(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	var req batchRequest
	idem, ok := decodeCommand(w, r, &req)
	if !ok {
		return
	}
	if req.LegalEntityID == "" {
		req.LegalEntityID = p.LegalEntityID
	}
	if req.Disposition == "" {
		req.Disposition = domain.DispositionAvailable
	}
	fields := map[string]string{}
	if !ids.Valid(req.LegalEntityID) {
		fields["legalEntityId"] = "must be a UUID (defaults to the caller's legal entity)"
	}
	if !ids.Valid(req.ItemID) {
		fields["itemId"] = "must be a UUID"
	}
	if n := len(strings.TrimSpace(req.BatchNumber)); n < 1 || len(req.BatchNumber) > maxBatchNumber {
		fields["batchNumber"] = fmt.Sprintf("must be 1 to %d characters", maxBatchNumber)
	}
	if req.BatchOrderID != nil && !ids.Valid(*req.BatchOrderID) {
		fields["batchOrderId"] = "must be a UUID"
	}
	if !domain.ValidDisposition(req.Disposition) {
		fields["disposition"] = "must be available, on-hold, rejected or quarantine"
	}
	if len(fields) > 0 {
		invalid(w, r, fields)
		return
	}
	if !tenant.InLegalEntityScope(p, req.LegalEntityID) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusForbidden, "AUTH-0003", "legal entity "+req.LegalEntityID+" is outside your scope"))
		return
	}
	b, fresh, err := a.store.RegisterBatch(r.Context(), store.NewBatch{LegalEntityID: req.LegalEntityID, ItemID: req.ItemID,
		BatchNumber: strings.TrimSpace(req.BatchNumber), BatchOrderID: req.BatchOrderID, Disposition: req.Disposition}, idem)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeCommand(w, fresh, http.StatusCreated, b)
}

func (a *API) listBatches(w http.ResponseWriter, r *http.Request) {
	if _, ok := principal(w, r); !ok {
		return
	}
	limit, ok := limitParam(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	f := store.BatchFilter{ItemID: q.Get("itemId"), Disposition: q.Get("disposition"), Limit: limit}
	if f.ItemID != "" && !ids.Valid(f.ItemID) {
		invalid(w, r, map[string]string{"itemId": "must be a UUID"})
		return
	}
	if f.Disposition != "" && !domain.ValidDisposition(f.Disposition) {
		invalid(w, r, map[string]string{"disposition": "must be available, on-hold, rejected or quarantine"})
		return
	}
	items, err := a.store.ListBatches(r.Context(), f)
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
}

func (a *API) getBatch(w http.ResponseWriter, r *http.Request) {
	if _, ok := principal(w, r); !ok {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	b, err := a.store.GetBatch(r.Context(), id)
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, b)
}

type recordRequest struct {
	Version int                 `json:"version"`
	Values  map[string]valueDTO `json:"values"`
}

func (a *API) recordAttributes(w http.ResponseWriter, r *http.Request) {
	if _, ok := principal(w, r); !ok {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var req recordRequest
	idem, ok := decodeCommand(w, r, &req)
	if !ok {
		return
	}
	fields := map[string]string{}
	if req.Version < 1 {
		fields["version"] = "must be the version you read (1 or more)"
	}
	if len(req.Values) < 1 || len(req.Values) > maxDefinitions {
		fields["values"] = fmt.Sprintf("must carry 1 to %d attribute values", maxDefinitions)
	}
	if len(fields) > 0 {
		invalid(w, r, fields)
		return
	}
	values := make(map[string]domain.Value, len(req.Values))
	for name, v := range req.Values {
		dv, err := v.toValue("values." + name)
		if err != nil {
			fail(w, r, err)
			return
		}
		if dv.Empty() {
			invalid(w, r, map[string]string{"values." + name: "needs one of num, text or date"})
			return
		}
		values[name] = dv
	}
	res, fresh, err := a.store.RecordAttributes(r.Context(), id, values, req.Version, idem)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeCommand(w, fresh, http.StatusOK, res)
}

type dispositionRequest struct {
	Version     int    `json:"version"`
	Disposition string `json:"disposition"`
	Reason      string `json:"reason"`
}

func (a *API) changeDisposition(w http.ResponseWriter, r *http.Request) {
	if _, ok := principal(w, r); !ok {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var req dispositionRequest
	idem, ok := decodeCommand(w, r, &req)
	if !ok {
		return
	}
	fields := map[string]string{}
	if req.Version < 1 {
		fields["version"] = "must be the version you read (1 or more)"
	}
	if !domain.ValidDisposition(req.Disposition) {
		fields["disposition"] = "must be available, on-hold, rejected or quarantine"
	}
	if n := len(strings.TrimSpace(req.Reason)); n < 1 || len(req.Reason) > maxReasonLen {
		fields["reason"] = fmt.Sprintf("must be 1 to %d characters", maxReasonLen)
	}
	if len(fields) > 0 {
		invalid(w, r, fields)
		return
	}
	b, fresh, err := a.store.ChangeDisposition(r.Context(), id, req.Disposition, strings.TrimSpace(req.Reason), req.Version, idem)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeCommand(w, fresh, http.StatusOK, b)
}
