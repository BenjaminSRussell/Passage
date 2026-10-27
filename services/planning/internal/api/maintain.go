package api

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/planning/internal/store"
)

const codePrecondition = "precondition_required"

// etagOf renders an entity version as its ETag.
func etagOf(version int) string { return `"` + strconv.Itoa(version) + `"` }

// ifMatch reads the If-Match precondition: 428 when it is absent, 400 when it
// is not an ETag "<version>" of at least minVersion.
func ifMatch(w http.ResponseWriter, r *http.Request, minVersion int) (int, bool) {
	raw := strings.TrimSpace(r.Header.Get("If-Match"))
	if raw == "" {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusPreconditionRequired, codePrecondition, "If-Match header is required; send the ETag you read"))
		return 0, false
	}
	v, err := strconv.Atoi(strings.Trim(strings.TrimPrefix(raw, "W/"), `"`))
	if err != nil || v < minVersion {
		validationFailed(w, r, map[string]string{"If-Match": `must be an ETag of the form "<version>"`})
		return 0, false
	}
	return v, true
}

func orderNotFound(w http.ResponseWriter, r *http.Request) {
	httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, store.CodeOrderNotFound, "planned order not found"))
}

// orderPatchRequest is a planner's change to a planned order; absent fields are unchanged.
type orderPatchRequest struct {
	Qty          *decimal.Decimal `json:"qty"`
	OrderDate    *string          `json:"orderDate"`
	DeliveryDate *string          `json:"deliveryDate"`
	VendorID     *string          `json:"vendorId"`
	Status       *string          `json:"status"`
}

func (req orderPatchRequest) validate() map[string]string {
	fields := map[string]string{}
	if req.Qty == nil && req.OrderDate == nil && req.DeliveryDate == nil && req.VendorID == nil && req.Status == nil {
		fields["body"] = "at least one of qty, orderDate, deliveryDate, vendorId or status is required"
	}
	if req.Qty != nil && (!req.Qty.IsPositive() || req.Qty.Exponent() < -6) {
		fields["qty"] = "must be positive with at most 6 decimals"
	}
	if req.OrderDate != nil {
		if _, err := time.Parse(dateLayout, *req.OrderDate); err != nil {
			fields["orderDate"] = "must be a date as YYYY-MM-DD"
		}
	}
	if req.DeliveryDate != nil {
		if _, err := time.Parse(dateLayout, *req.DeliveryDate); err != nil {
			fields["deliveryDate"] = "must be a date as YYYY-MM-DD"
		}
	}
	if req.VendorID != nil && !ids.Valid(*req.VendorID) {
		fields["vendorId"] = "must be a UUID"
	}
	if req.Status != nil && *req.Status != "approved" {
		fields["status"] = "may only be set to approved; firming is a separate command"
	}
	return fields
}

// patchPlannedOrder changes or approves an open planned order (FR-22.11). It
// requires If-Match with the order's current ETag.
func (a *API) patchPlannedOrder(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id := chi.URLParam(r, "id")
	if !ids.Valid(id) {
		orderNotFound(w, r)
		return
	}
	expected, ok := ifMatch(w, r, 1)
	if !ok {
		return
	}
	var req orderPatchRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if fields := req.validate(); len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	patch := store.OrderPatch{Qty: req.Qty, OrderDate: req.OrderDate, DeliveryDate: req.DeliveryDate,
		VendorID: req.VendorID, Approve: req.Status != nil}
	o, err := a.store.UpdatePlannedOrder(r.Context(), id, expected, patch)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	w.Header().Set("ETag", etagOf(o.Version))
	httpx.JSON(w, http.StatusOK, o)
}

// deletePlannedOrder deletes an open planned order (FR-22.11). It requires If-Match.
func (a *API) deletePlannedOrder(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id := chi.URLParam(r, "id")
	if !ids.Valid(id) {
		orderNotFound(w, r)
		return
	}
	expected, ok := ifMatch(w, r, 1)
	if !ok {
		return
	}
	if err := a.store.DeletePlannedOrder(r.Context(), id, expected); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
