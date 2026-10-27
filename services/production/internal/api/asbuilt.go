package api

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/production/internal/domain"
	"github.com/BenjaminSRussell/passage/services/production/internal/store"
)

// ---- as-built genealogy (FR-21.19) ----

// asBuiltRequest assigns one component serial or batch to a finished serial or
// batch of the order. Qty defaults to 1, the only quantity a serial takes.
type asBuiltRequest struct {
	FinishedSerialOrBatch  string           `json:"finishedSerialOrBatch"`
	ComponentItemID        string           `json:"componentItemId"`
	Tracking               domain.Tracking  `json:"tracking"`
	ComponentSerialOrBatch string           `json:"componentSerialOrBatch"`
	Qty                    *decimal.Decimal `json:"qty"`
}

func validateAsBuilt(req asBuiltRequest, fields map[string]string) domain.AsBuilt {
	a := domain.AsBuilt{FinishedSerial: req.FinishedSerialOrBatch, ComponentItemID: req.ComponentItemID, Tracking: req.Tracking,
		ComponentSerial: req.ComponentSerialOrBatch, Qty: decimal.NewFromInt(1)}
	ident := fmt.Sprintf("is required, 1 to %d characters without surrounding spaces", domain.MaxTrackingID)
	if !domain.ValidTrackingID(a.FinishedSerial) {
		fields["finishedSerialOrBatch"] = ident
	}
	if !domain.ValidTrackingID(a.ComponentSerial) {
		fields["componentSerialOrBatch"] = ident
	}
	if !ids.Valid(a.ComponentItemID) {
		fields["componentItemId"] = "must be a UUID"
	}
	if !a.Tracking.Valid() {
		fields["tracking"] = "must be serial or batch"
	}
	if req.Qty != nil {
		a.Qty = *req.Qty
		checkQuantity("qty", a.Qty, true, fields)
	}
	if a.Tracking == domain.TrackSerial && !a.Qty.Equal(decimal.NewFromInt(1)) {
		fields["qty"] = "a serial component is consumed in quantity 1"
	}
	return a
}

// assignAsBuilt serves POST /orders/{id}/as-built: 201 with the record, or
// 200 with Idempotent-Replayed on a replay of the same key and body.
func (a *API) assignAsBuilt(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	id, ok := orderID(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	var req asBuiltRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	key, fields := idempotencyKey(r)
	in := validateAsBuilt(req, fields)
	if len(fields) > 0 {
		invalid(w, r, fields)
		return
	}
	rec, created, err := a.store.AssignAsBuilt(r.Context(), store.NewAsBuilt{OrderID: id, AsBuilt: in, Actor: p.UserID,
		IdempotencyKey: key, RequestHash: hashOf(struct {
			Order string
			In    domain.AsBuilt
		}{id, in})})
	if err != nil {
		a.fail(w, r, "assign as-built record", err)
		return
	}
	if !created {
		a.replayed.Add(1)
		w.Header().Set("Idempotent-Replayed", "true")
		httpx.JSON(w, http.StatusOK, rec)
		return
	}
	httpx.JSON(w, http.StatusCreated, rec)
}

// orderAsBuilt serves GET /orders/{id}/as-built: the attached records of the order.
func (a *API) orderAsBuilt(w http.ResponseWriter, r *http.Request) {
	id, ok := orderID(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	items, err := a.store.ListAsBuilt(r.Context(), store.AsBuiltFilter{OrderID: id})
	if err != nil {
		a.fail(w, r, "list as-built records", err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"orderId": id, "items": items})
}

// listAsBuilt serves GET /as-built (4.21.14 "As-built / where-used by
// serial"): finishedSerialOrBatch gives the as-built of a finished serial,
// componentSerialOrBatch the where-used of a component serial. At least one
// filter is required so the inquiry never scans the tenant.
func (a *API) listAsBuilt(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := store.AsBuiltFilter{OrderID: q.Get("orderId"), FinishedSerialOrBatch: q.Get("finishedSerialOrBatch"),
		ComponentSerialOrBatch: q.Get("componentSerialOrBatch"), ComponentItemID: q.Get("componentItemId")}
	fields := map[string]string{}
	if f.FinishedSerialOrBatch == "" && f.ComponentSerialOrBatch == "" && f.OrderID == "" {
		fields["finishedSerialOrBatch"] = "set finishedSerialOrBatch, componentSerialOrBatch or orderId"
	}
	for name, v := range map[string]string{"orderId": f.OrderID, "componentItemId": f.ComponentItemID} {
		if v != "" && !ids.Valid(v) {
			fields[name] = "must be a UUID"
		}
	}
	for name, v := range map[string]string{"finishedSerialOrBatch": f.FinishedSerialOrBatch, "componentSerialOrBatch": f.ComponentSerialOrBatch} {
		if v != "" && !domain.ValidTrackingID(v) {
			fields[name] = fmt.Sprintf("1 to %d characters without surrounding spaces", domain.MaxTrackingID)
		}
	}
	if len(fields) > 0 {
		invalid(w, r, fields)
		return
	}
	items, err := a.store.ListAsBuilt(r.Context(), f)
	if err != nil {
		a.fail(w, r, "list as-built records", err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
}

// detachAsBuilt serves POST /as-built/{id}:detach. Detaching is idempotent:
// a detached record is returned as it is.
func (a *API) detachAsBuilt(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	raw, action, found := strings.Cut(chi.URLParam(r, "id"), ":")
	if !found || action != "detach" || !ids.Valid(raw) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, unknownActionCode, "use :detach on an as-built record"))
		return
	}
	rec, err := a.store.DetachAsBuilt(r.Context(), raw, p.UserID)
	if err != nil {
		a.fail(w, r, "detach as-built record", err)
		return
	}
	httpx.JSON(w, http.StatusOK, rec)
}
