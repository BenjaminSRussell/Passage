package api

import (
	"net/http"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
)

// listNetRequirements serves the MD04-style time-phased view of one item in
// the current version of a plan (FR-22.12, AC-22.10).
func (a *API) listNetRequirements(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	q := r.URL.Query()
	code, itemID := q.Get("planCode"), q.Get("itemId")
	fields := map[string]string{}
	if !planCodePattern.MatchString(code) {
		fields["planCode"] = "is required"
	}
	if !ids.Valid(itemID) {
		fields["itemId"] = "is required and must be a UUID"
	}
	warehouseID := q.Get("warehouseId")
	if warehouseID != "" && !ids.Valid(warehouseID) {
		fields["warehouseId"] = "must be a UUID"
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	limit, ok := limitParam(w, r)
	if !ok {
		return
	}
	rows, err := a.store.ListNetRequirements(r.Context(), code, itemID, warehouseID, limit)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": rows})
}
