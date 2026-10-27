package api

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/planning/internal/domain"
	"github.com/BenjaminSRussell/passage/services/planning/internal/store"
)

// maxFirmOrders is the batch limit of POST planned-orders:firm (4.22.5).
const maxFirmOrders = 1000

// firmRequest is the body of POST /v1/planning/planned-orders:firm.
type firmRequest struct {
	PlannedOrderIDs []string `json:"plannedOrderIds"`
	Grouping        string   `json:"grouping"`
}

// validate returns field errors: 1 to 1,000 distinct UUIDs and a known grouping.
func (req firmRequest) validate() map[string]string {
	fields := map[string]string{}
	if len(req.PlannedOrderIDs) == 0 || len(req.PlannedOrderIDs) > maxFirmOrders {
		fields["plannedOrderIds"] = fmt.Sprintf("must hold 1 to %d ids", maxFirmOrders)
	}
	seen := make(map[string]bool, len(req.PlannedOrderIDs))
	for i, id := range req.PlannedOrderIDs {
		field := fmt.Sprintf("plannedOrderIds[%d]", i)
		switch {
		case !ids.Valid(id):
			fields[field] = "must be a UUID"
		case seen[strings.ToLower(id)]:
			fields[field] = "is listed more than once"
		}
		seen[strings.ToLower(id)] = true
	}
	if !domain.ValidGrouping(req.Grouping) {
		fields["grouping"] = "must be none, vendor, vendor-date or vendor-week"
	}
	return fields
}

// firmPlannedOrders firms up to 1,000 planned orders of current plan versions
// into target orders (FR-22.10). It requires Idempotency-Key: a replay returns
// the stored batch with 200 and Idempotent-Replayed: true.
func (a *API) firmPlannedOrders(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	key := r.Header.Get(idempotencyKey)
	if key == "" {
		validationFailed(w, r, map[string]string{idempotencyKey: "header is required on firming"})
		return
	}
	var req firmRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if req.Grouping == "" {
		req.Grouping = domain.DefaultGrouping
	}
	if fields := req.validate(); len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	// Ids are compared lowercase and the fingerprint does not depend on their
	// order, since the grouping does not either.
	orderIDs := make([]string, len(req.PlannedOrderIDs))
	for i, id := range req.PlannedOrderIDs {
		orderIDs[i] = strings.ToLower(id)
	}
	slices.Sort(orderIDs)
	body, _ := json.Marshal(firmRequest{PlannedOrderIDs: orderIDs, Grouping: req.Grouping})
	batch, replayed, err := a.store.FirmPlannedOrders(r.Context(), store.FirmRequest{
		IdempotencyKey: key, InputHash: fmt.Sprintf("%x", sha256.Sum256(body)), Grouping: req.Grouping, OrderIDs: orderIDs,
	})
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if replayed {
		w.Header().Set("Idempotent-Replayed", "true")
		httpx.JSON(w, http.StatusOK, batch)
		return
	}
	httpx.JSON(w, http.StatusCreated, batch)
}
