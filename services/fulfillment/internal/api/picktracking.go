package api

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/fulfillment/internal/domain"
	"github.com/BenjaminSRussell/passage/services/fulfillment/internal/store"
)

// routesTracking mounts the batch, serial and location capture of a line
// (BR-28.10).
func (a *API) routesTracking(r chi.Router) {
	r.With(authz.RequireDuty("fulfillment.warehouse.pick")).Post("/v1/fulfillment/shipments/{id}/lines/{lid}:capture-tracking", a.captureTracking)
}

type trackingEntryDTO struct {
	BatchNo      string `json:"batchNo"`
	SerialNo     string `json:"serialNo"`
	LocationCode string `json:"locationCode"`
	Qty          string `json:"qty"`
}

type captureTrackingRequest struct {
	Entries []trackingEntryDTO `json:"entries"`
}

func (a *API) captureTracking(w http.ResponseWriter, r *http.Request) {
	id, version, ok := a.shipmentCommand(w, r)
	if !ok {
		return
	}
	lid := chi.URLParam(r, "lid")
	if !ids.Valid(lid) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, store.CodeLineNotFound, "shipment line not found"))
		return
	}
	var req captureTrackingRequest
	_, idem, err := command(r, &req)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if len(req.Entries) > 500 {
		fail(w, r, "entries", "at most 500 entries per line")
		return
	}
	entries := make([]domain.TrackingEntry, 0, len(req.Entries))
	for i, e := range req.Entries {
		q, err := nonNegative(e.Qty)
		if err != nil || !q.IsPositive() {
			fail(w, r, fmt.Sprintf("entries[%d].qty", i), "must be a positive decimal with at most 6 decimals")
			return
		}
		entries = append(entries, domain.TrackingEntry{BatchNo: strings.TrimSpace(e.BatchNo), SerialNo: strings.TrimSpace(e.SerialNo),
			LocationCode: strings.TrimSpace(e.LocationCode), Qty: q})
	}
	sh, err := a.store.CaptureTracking(r.Context(), id, lid, version, entries, idem)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	etag(w, sh.Version)
	httpx.JSON(w, http.StatusOK, sh)
}
