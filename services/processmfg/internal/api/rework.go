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

// registerRework mounts the rework batch order (FR-24.18, AC-24.18): create
// from a stored batch, read, and receive the output. Writes need the duty of
// the batch orders (PRD 4.24.17 Maintain batch orders); receiving an output
// planned as available is a release and also needs the disposition duty. The
// stand-alone plan stays at rework-orders:preview.
func (a *API) registerRework(r chi.Router) {
	write := authz.RequireDuty(DutyBatchOrderWrite)
	r.With(write).Post("/v1/production/rework-orders", a.createRework)
	r.With(authz.RequireDuty(DutyRead)).Get("/v1/production/rework-orders", a.listRework)
	r.With(authz.RequireDuty(DutyRead)).Get("/v1/production/rework-orders/{id}", a.getRework)
	r.With(write).Post("/v1/production/rework-orders/{id}:receive", a.receiveRework)
}

type createReworkRequest struct {
	LegalEntityID string          `json:"legalEntityId"`
	FormulaID     string          `json:"formulaId"`
	BatchSize     decimal.Decimal `json:"batchSize"`
	SourceBatchID string          `json:"sourceBatchId"`
	SourceVersion int             `json:"sourceVersion"`
	SourceQty     decimal.Decimal `json:"sourceQty"`
	ReworkQty     decimal.Decimal `json:"reworkQty"`
	OutputQty     decimal.Decimal `json:"outputQty"`
	Params        reworkParamsDTO `json:"params"`
}

// createRework creates the rework batch order of a stored batch: the batch
// order is made from the rework formula at the batch size, linked to the batch
// with the quantity reworked, the planned output and the FR-24.18 parameters
// (omitted ones take the defaults of rework-orders:preview). The register
// keeps no quantity, so the caller gives the batch quantity and the version of
// the batch it read.
func (a *API) createRework(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	var req createReworkRequest
	idem, ok := decodeCommand(w, r, &req)
	if !ok {
		return
	}
	if req.LegalEntityID == "" {
		req.LegalEntityID = p.LegalEntityID
	}
	fields := map[string]string{}
	if !ids.Valid(req.FormulaID) {
		fields["formulaId"] = "must be a UUID"
	}
	if !ids.Valid(req.LegalEntityID) {
		fields["legalEntityId"] = "must be a UUID (defaults to the caller's legal entity)"
	}
	source, ok := ids.Canonical(req.SourceBatchID)
	if !ok {
		fields["sourceBatchId"] = "must be a UUID"
	}
	if req.SourceVersion < 1 {
		fields["sourceVersion"] = "must be the batch version you read (1 or more)"
	}
	for name, v := range map[string]decimal.Decimal{"batchSize": req.BatchSize, "sourceQty": req.SourceQty, "reworkQty": req.ReworkQty, "outputQty": req.OutputQty} {
		if !validQty(v) {
			fields[name] = "must be a positive quantity below 10^12 with at most 6 decimals"
		}
	}
	if len(fields) > 0 {
		invalid(w, r, fields)
		return
	}
	if !tenant.InLegalEntityScope(p, req.LegalEntityID) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusForbidden, "AUTH-0003", "legal entity "+req.LegalEntityID+" is outside your scope"))
		return
	}
	params := domain.DefaultReworkParams()
	if req.Params.KeepBatchNumber != nil {
		params.KeepBatchNumber = *req.Params.KeepBatchNumber
	}
	if req.Params.RequireQualityOrder != nil {
		params.RequireQualityOrder = *req.Params.RequireQualityOrder
	}
	if req.Params.OutputDisposition != "" {
		params.OutputDisposition = req.Params.OutputDisposition
	}
	params.Capitalize = req.Params.Capitalize
	out, fresh, err := a.store.CreateReworkOrder(r.Context(), store.NewRework{LegalEntityID: req.LegalEntityID, FormulaID: req.FormulaID,
		BatchSize: req.BatchSize, SourceBatchID: source, SourceVersion: req.SourceVersion, SourceQty: req.SourceQty,
		InputQty: req.ReworkQty, OutputQty: req.OutputQty, Params: params}, idem)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeCommand(w, fresh, http.StatusCreated, out)
}

func (a *API) listRework(w http.ResponseWriter, r *http.Request) {
	if _, ok := principal(w, r); !ok {
		return
	}
	limit, ok := limitParam(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	f := store.ReworkFilter{Status: q.Get("status"), SourceBatchID: q.Get("sourceBatchId"), Limit: limit}
	fields := map[string]string{}
	if f.Status != "" && f.Status != "planned" && f.Status != "received" {
		fields["status"] = "must be planned or received"
	}
	if f.SourceBatchID != "" {
		id, ok := ids.Canonical(f.SourceBatchID)
		if !ok {
			fields["sourceBatchId"] = "must be a UUID"
		}
		f.SourceBatchID = id
	}
	if len(fields) > 0 {
		invalid(w, r, fields)
		return
	}
	items, err := a.store.ListReworkOrders(r.Context(), f)
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
}

func (a *API) getRework(w http.ResponseWriter, r *http.Request) {
	if _, ok := principal(w, r); !ok {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	out, err := a.store.GetReworkOrder(r.Context(), id)
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}

type receiveReworkRequest struct {
	Version        int    `json:"version"`
	NewBatchNumber string `json:"newBatchNumber"`
}

// receiveRework takes the output of a rework back into the batch register once
// its batch order is reported as finished. An output planned as available also
// needs the disposition duty, checked by the store against the stored plan.
func (a *API) receiveRework(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var req receiveReworkRequest
	idem, ok := decodeCommand(w, r, &req)
	if !ok {
		return
	}
	fields := map[string]string{}
	if req.Version < 1 {
		fields["version"] = "must be the version you read (1 or more)"
	}
	number := strings.TrimSpace(req.NewBatchNumber)
	if number != req.NewBatchNumber || len(number) > maxBatchNumber {
		fields["newBatchNumber"] = fmt.Sprintf("must be at most %d characters without leading or trailing spaces", maxBatchNumber)
	}
	if len(fields) > 0 {
		invalid(w, r, fields)
		return
	}
	out, changed, err := a.store.ReceiveReworkOrder(r.Context(), id, number, req.Version, p.HasDuty(DutyBatchDisposition), idem)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeCommand(w, changed, http.StatusOK, out)
}
