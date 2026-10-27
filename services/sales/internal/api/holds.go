package api

import (
	"net/http"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/sales/internal/domain"
	"github.com/BenjaminSRussell/passage/services/sales/internal/store"
)

const maxReasonCode = 80

// applyHoldRequest is the body of POST /v1/sales/orders/{id}/holds.
type applyHoldRequest struct {
	HoldType   string `json:"holdType"`
	ReasonCode string `json:"reasonCode"`
}

// applyHold places a MANUAL, FRAUD or PRICE_REVIEW hold (FR-27.8, duty
// SalesOrderHold). CREDIT holds are placed only by the credit check. If-Match
// carries the order ETag, so a repeated request cannot apply the hold twice.
func (a *API) applyHold(w http.ResponseWriter, r *http.Request) {
	orderRoute(func(w http.ResponseWriter, r *http.Request, id string) {
		var req applyHoldRequest
		if err := httpx.Decode(r, &req); err != nil {
			httpx.Fail(w, r, err)
			return
		}
		fields := map[string]string{}
		if !domain.CanApplyHold(req.HoldType) {
			fields["holdType"] = "must be MANUAL, FRAUD or PRICE_REVIEW"
		}
		if n := len(req.ReasonCode); n == 0 || n > maxReasonCode {
			fields["reasonCode"] = "must be 1 to 80 characters"
		}
		if len(fields) > 0 {
			validationFailed(w, r, fields)
			return
		}
		version, ok := ifMatch(w, r)
		if !ok {
			return
		}
		p, _ := tenant.FromContext(r.Context())
		order, err := a.store.ApplyHold(r.Context(), id, version,
			store.HoldRequest{HoldType: req.HoldType, ReasonCode: req.ReasonCode}, p.UserID)
		if err != nil {
			a.fail(w, r, "apply hold", err)
			return
		}
		a.applied.Add(1)
		setETag(w, order.Version)
		httpx.JSON(w, http.StatusCreated, order)
	})(w, r)
}
