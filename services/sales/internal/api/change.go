package api

import (
	"fmt"
	"net/http"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/sales/internal/store"
)

const maxChangeReason = 500

// lineChangeRequest sets any editable field of one line; absent fields stay.
type lineChangeRequest struct {
	LineNo            int     `json:"lineNo"`
	Quantity          *string `json:"quantity"`
	UnitPrice         *string `json:"unitPrice"`
	DiscPct           *string `json:"discPct"`
	RequestedShipDate *string `json:"requestedShipDate"`
}

// changeOrderRequest is the PATCH body (FR-27.9).
type changeOrderRequest struct {
	Reason            string              `json:"reason"`
	RequestedShipDate *string             `json:"requestedShipDate"`
	Lines             []lineChangeRequest `json:"lines"`
}

// cancelOrderRequest is the optional body of :cancel.
type cancelOrderRequest struct {
	Reason string `json:"reason"`
}

// buildChange validates a PATCH body. Field errors are keyed by JSON path.
func buildChange(req changeOrderRequest) (store.ChangeRequest, map[string]string) {
	f := map[string]string{}
	if len(req.Reason) > maxChangeReason {
		f["reason"] = "must be at most 500 characters"
	}
	if req.RequestedShipDate != nil {
		if _, err := time.Parse(dayLayout, *req.RequestedShipDate); err != nil {
			f["requestedShipDate"] = "must be a date such as 2027-03-04"
		}
	}
	if len(req.Lines) == 0 && req.RequestedShipDate == nil {
		f["lines"] = "nothing to change: send lines or requestedShipDate"
	}
	if len(req.Lines) > maxLines {
		f["lines"] = fmt.Sprintf("must have at most %d lines", maxLines)
	}
	out := store.ChangeRequest{Reason: req.Reason, RequestedShipDate: req.RequestedShipDate}
	for i, l := range req.Lines {
		path := func(name string) string { return fmt.Sprintf("lines[%d].%s", i, name) }
		if l.LineNo < 1 {
			f[path("lineNo")] = "must be 1 or more"
		}
		c := store.LineChange{LineNo: l.LineNo, RequestedShipDate: l.RequestedShipDate}
		c.Quantity = optionalDecimal(f, path("quantity"), l.Quantity)
		c.UnitPrice = optionalDecimal(f, path("unitPrice"), l.UnitPrice)
		c.DiscPct = optionalDecimal(f, path("discPct"), l.DiscPct)
		if l.RequestedShipDate != nil {
			if _, err := time.Parse(dayLayout, *l.RequestedShipDate); err != nil {
				f[path("requestedShipDate")] = "must be a date such as 2027-03-04"
			}
		}
		out.Lines = append(out.Lines, c)
	}
	return out, f
}

// optionalDecimal parses an optional decimal field, recording a field error.
func optionalDecimal(f map[string]string, key string, s *string) *decimal.Decimal {
	if s == nil {
		return nil
	}
	d, err := decimal.NewFromString(*s)
	if err != nil {
		f[key] = "must be a decimal"
		return nil
	}
	return &d
}

// patchOrder changes a confirmed or open order. If-Match carries the ETag; the
// response is the order at its new version (AC-27.7).
func (a *API) patchOrder(w http.ResponseWriter, r *http.Request) {
	orderRoute(func(w http.ResponseWriter, r *http.Request, id string) {
		version, ok := ifMatch(w, r)
		if !ok {
			return
		}
		var req changeOrderRequest
		if err := httpx.Decode(r, &req); err != nil {
			httpx.Fail(w, r, err)
			return
		}
		in, fields := buildChange(req)
		if len(fields) > 0 {
			validationFailed(w, r, fields)
			return
		}
		p, _ := tenant.FromContext(r.Context())
		in.Approver = p.HasDuty(dutyOrderApprove)
		order, err := a.store.ChangeOrder(r.Context(), id, version, in, p.UserID, a.now())
		if err != nil {
			a.fail(w, r, "change order", err)
			return
		}
		if order.PendingChange != nil {
			// The customer's changes need approval: nothing was applied.
			setETag(w, order.Version)
			httpx.JSON(w, http.StatusAccepted, order)
			return
		}
		if order.Version != version {
			a.changed.Add(1)
		}
		setETag(w, order.Version)
		httpx.JSON(w, http.StatusOK, order)
	})(w, r)
}

// cancelOrder cancels every line of an order that has not shipped (BR-27.5).
// The duty is sales.order.confirm, as for :confirm (PRD §5.27.6 SalesOrderConfirm).
func (a *API) cancelOrder(w http.ResponseWriter, r *http.Request) {
	orderRoute(func(w http.ResponseWriter, r *http.Request, id string) {
		version, ok := ifMatch(w, r)
		if !ok {
			return
		}
		var req cancelOrderRequest
		if r.ContentLength != 0 {
			if err := httpx.Decode(r, &req); err != nil {
				httpx.Fail(w, r, err)
				return
			}
		}
		if len(req.Reason) > maxChangeReason {
			validationFailed(w, r, map[string]string{"reason": "must be at most 500 characters"})
			return
		}
		p, _ := tenant.FromContext(r.Context())
		order, err := a.store.CancelOrderAs(r.Context(), id, version, req.Reason, p.UserID, p.HasDuty(dutyOrderApprove), a.now())
		if err != nil {
			a.fail(w, r, "cancel order", err)
			return
		}
		if order.PendingChange != nil {
			// The customer's changes need approval: nothing was cancelled.
			setETag(w, order.Version)
			httpx.JSON(w, http.StatusAccepted, order)
			return
		}
		a.canceled.Add(1)
		setETag(w, order.Version)
		httpx.JSON(w, http.StatusOK, order)
	})(w, r)
}

// listOrderVersions returns the order's change history, newest first (FR-27.9,
// PRD §5.27.14 "Order change history").
func (a *API) listOrderVersions(w http.ResponseWriter, r *http.Request) {
	orderRoute(func(w http.ResponseWriter, r *http.Request, id string) {
		versions, err := a.store.ListOrderVersions(r.Context(), id)
		if err != nil {
			a.fail(w, r, "list order versions", err)
			return
		}
		httpx.JSON(w, http.StatusOK, map[string]any{"items": versions})
	})(w, r)
}
