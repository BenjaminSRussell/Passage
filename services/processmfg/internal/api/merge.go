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
	"github.com/BenjaminSRussell/passage/services/processmfg/internal/domain"
	"github.com/BenjaminSRussell/passage/services/processmfg/internal/store"
)

const mergeStoredPath = "/v1/production/batches:merge"

// registerMerge mounts the stored batch merge and the genealogy read
// (FR-24.16, AC-24.12).
func (a *API) registerMerge(r chi.Router) {
	r.With(authz.RequireDuty(DutyBatchMerge)).Post(mergeStoredPath, a.mergeStored)
	r.With(authz.RequireDuty(DutyRead)).Get("/v1/production/batches/{id}/genealogy", a.getGenealogy)
}

type mergeSourceDTO struct {
	BatchID     string          `json:"batchId"`
	Version     int             `json:"version"`
	WarehouseID string          `json:"warehouseId"`
	CWUnit      string          `json:"cwUnit"`
	Qty         decimal.Decimal `json:"qty"`
	UnitCost    decimal.Decimal `json:"unitCost"`
}

type mergeStoredRequest struct {
	TargetBatchNumber string           `json:"targetBatchNumber"`
	Sources           []mergeSourceDTO `json:"sources"`
	MergeOverride     []string         `json:"mergeOverride"`
}

// mergeStored merges stored batches into a new batch (FR-24.16): the
// dispositions and attribute values come from the batch register, the caller
// passes the quantity, cost per unit, warehouse and catch-weight unit of each
// source (the register keeps none) and the version it read. The rule and its
// refusals (PMF-0006) are those of batch-merge:preview.
func (a *API) mergeStored(w http.ResponseWriter, r *http.Request) {
	if _, ok := principal(w, r); !ok {
		return
	}
	var req mergeStoredRequest
	idem, ok := decodeCommand(w, r, &req)
	if !ok {
		return
	}
	fields := map[string]string{}
	number := strings.TrimSpace(req.TargetBatchNumber)
	if len(number) < 1 || len(req.TargetBatchNumber) > maxBatchNumber {
		fields["targetBatchNumber"] = fmt.Sprintf("must be 1 to %d characters", maxBatchNumber)
	}
	if n := len(req.Sources); n < 2 || n > store.MaxMergeSources {
		fields["sources"] = fmt.Sprintf("must list 2 to %d batches", store.MaxMergeSources)
	}
	if len(req.MergeOverride) > domain.MaxFormulaLines {
		fields["mergeOverride"] = fmt.Sprintf("at most %d attribute names", domain.MaxFormulaLines)
	}
	override := make(map[string]bool, len(req.MergeOverride))
	for _, n := range req.MergeOverride {
		if l := len(n); l < 1 || l > maxAttrName {
			fields["mergeOverride"] = fmt.Sprintf("attribute names must be 1 to %d characters", maxAttrName)
		}
		override[n] = true
	}
	if len(fields) > 0 {
		invalid(w, r, fields)
		return
	}
	seen := map[string]bool{}
	in := store.MergeInput{TargetNumber: number, Override: override, Sources: make([]store.MergeSource, len(req.Sources))}
	for i, s := range req.Sources {
		id, ok := ids.Canonical(s.BatchID)
		if !ok || seen[id] {
			fields[fmt.Sprintf("sources[%d].batchId", i)] = "must be a distinct UUID per source"
		}
		seen[id] = true
		if s.Version < 1 {
			fields[fmt.Sprintf("sources[%d].version", i)] = "must be the version you read (1 or more)"
		}
		if !validRef(s.WarehouseID) {
			fields[fmt.Sprintf("sources[%d].warehouseId", i)] = fmt.Sprintf("must be 1 to %d characters", maxRefLen)
		}
		if len(s.CWUnit) > maxRefLen {
			fields[fmt.Sprintf("sources[%d].cwUnit", i)] = fmt.Sprintf("at most %d characters", maxRefLen)
		}
		if !validQty(s.Qty) {
			fields[fmt.Sprintf("sources[%d].qty", i)] = "must be a positive quantity below 10^12 with at most 6 decimals"
		}
		if s.UnitCost.IsNegative() || s.UnitCost.Exponent() < -domain.QtyPrecision || s.UnitCost.GreaterThanOrEqual(maxQty()) {
			fields[fmt.Sprintf("sources[%d].unitCost", i)] = "must be from 0 below 10^12 with at most 6 decimals"
		}
		in.Sources[i] = store.MergeSource{BatchID: id, Version: s.Version, WarehouseID: s.WarehouseID, CWUnit: s.CWUnit, Qty: s.Qty, UnitCost: s.UnitCost}
	}
	if len(fields) > 0 {
		invalid(w, r, fields)
		return
	}
	res, fresh, err := a.store.MergeBatches(r.Context(), in, idem)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeCommand(w, fresh, http.StatusCreated, res)
}

func (a *API) getGenealogy(w http.ResponseWriter, r *http.Request) {
	if _, ok := principal(w, r); !ok {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	g, err := a.store.GetGenealogy(r.Context(), id)
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, g)
}
