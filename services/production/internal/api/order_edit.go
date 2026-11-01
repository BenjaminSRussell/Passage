package api

import (
	"fmt"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/production/internal/domain"
	"github.com/BenjaminSRussell/passage/services/production/internal/store"
)

// DutyBOMAfterRelease is "Maintain order BOM after release" (PRD 4.21.17): editing
// the BOM or route of a Released order needs it on top of production.order.write.
const DutyBOMAfterRelease = "production.order.bom-after-release"

// maxOrderEdits bounds the edits of one request.
const maxOrderEdits = 200

// editOrder applies order-specific BOM and route edits (FR-21.18) under If-Match.
func (a *API) editOrder(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	id, ok := orderID(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	ifMatch, err := parseIfMatch(r.Header.Get("If-Match"))
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	var edits domain.OrderEdits
	if err := httpx.Decode(r, &edits); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if fields := validateOrderEdits(edits); len(fields) > 0 {
		invalid(w, r, fields)
		return
	}
	order, err := a.store.EditOrder(r.Context(), store.OrderEdit{OrderID: id, IfMatch: ifMatch, Edits: edits,
		ReleaseDuty: p.HasDuty(DutyBOMAfterRelease), Actor: p.UserID, TraceID: middleware.GetReqID(r.Context())})
	if err != nil {
		a.refuse(w, r, err)
		return
	}
	w.Header().Set("ETag", etag(order.Version))
	httpx.JSON(w, http.StatusOK, order)
}

// validateOrderEdits checks the shape of the edits; the domain applies the
// rules that need the order (existence, consumption, journals, requirements).
func validateOrderEdits(e domain.OrderEdits) map[string]string {
	fields := map[string]string{}
	switch n := e.Count(); {
	case n == 0:
		fields["lines"] = "at least one line or operation edit is required"
	case n > maxOrderEdits:
		fields["lines"] = fmt.Sprintf("at most %d edits in one request", maxOrderEdits)
	}
	optional := func(name string, v *decimal.Decimal) {
		if v != nil {
			checkQuantity(name, *v, false, fields)
		}
	}
	for i, l := range e.Lines {
		at := fmt.Sprintf("lines[%d]", i)
		if !l.Op.Valid() {
			fields[at+".op"] = "must be add, remove or change"
		}
		if l.LineNo == "" || len(l.LineNo) > maxLineNo {
			fields[at+".lineNo"] = fmt.Sprintf("is required, at most %d characters", maxLineNo)
		}
		if l.ItemID != nil && !ids.Valid(*l.ItemID) {
			fields[at+".itemId"] = "must be a UUID"
		}
		if l.Op == domain.EditAdd && (l.ItemID == nil || l.QtyPer == nil) {
			fields[at] = "an added line needs itemId and qtyPer"
		}
		if l.Op == domain.EditRemove && (l.ItemID != nil || l.QtyPer != nil || l.SeriesSize != nil || l.ScrapPct != nil ||
			l.RoundUp != nil || l.UnitCost != nil || l.Flushing != nil) {
			fields[at] = "a removal carries lineNo only"
		}
		optional(at+".qtyPer", l.QtyPer)
		optional(at+".seriesSize", l.SeriesSize)
		optional(at+".scrapPct", l.ScrapPct)
		optional(at+".unitCost", l.UnitCost)
		if l.Flushing != nil && !l.Flushing.Valid() {
			fields[at+".flushing"] = "must be manual, start, finish or available-at-location"
		}
	}
	for i, op := range e.Operations {
		at := fmt.Sprintf("operations[%d]", i)
		if !op.Op.Valid() {
			fields[at+".op"] = "must be add, remove or change"
		}
		if op.OperNumber < 1 {
			fields[at+".operNumber"] = "must be a positive whole number"
		}
		if op.ResourceID != nil && !ids.Valid(*op.ResourceID) {
			fields[at+".resourceId"] = "must be a UUID"
		}
		if op.Op == domain.EditAdd && op.ResourceID == nil {
			fields[at] = "an added operation needs resourceId"
		}
		if op.Op == domain.EditRemove && op.HasFields() {
			fields[at] = "a removal carries operNumber only"
		}
		optional(at+".setupTimeH", op.SetupH)
		optional(at+".runTimeHPer", op.RunHPer)
		optional(at+".scrapPct", op.ScrapPct)
		optional(at+".laborRate", op.LaborRate)
		optional(at+".overheadRate", op.OverheadRate)
		optional(at+".subcontractPrice", op.SubcontractPrice)
		optional(at+".queueBeforeH", op.QueueBeforeH)
		optional(at+".queueAfterH", op.QueueAfterH)
		optional(at+".transitH", op.TransitH)
	}
	return fields
}

// getOrderEdits serves the audit trail of an order's BOM and route edits (FR-21.18).
func (a *API) getOrderEdits(w http.ResponseWriter, r *http.Request) {
	id, ok := orderID(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	entries, err := a.store.OrderEdits(r.Context(), id)
	if err != nil {
		a.fail(w, r, "read production order edits", err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"orderId": id, "items": entries})
}
