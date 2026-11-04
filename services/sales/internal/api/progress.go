package api

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/sales/internal/domain"
	"github.com/BenjaminSRussell/passage/services/sales/internal/store"
)

const (
	orderProgress = "/v1/sales/order-progress"
	// dutyRecordDelivery lets the fulfillment service (packing slip posted) and
	// dutyRecordInvoice the billing service (invoice posted) report what was
	// delivered or invoiced on an order; each kind needs its own duty so that
	// neither caller can report the other's documents.
	dutyRecordDelivery = "sales.order.record-delivery"
	dutyRecordInvoice  = "sales.order.record-invoice"
	// maxProgressSource bounds the packing slip, shipment or invoice reference.
	maxProgressSource = 100
	// quantityPlaces is the scale of a quantity (numeric(20,6)).
	quantityPlaces = 6
)

// progressLineRequest is the quantity a report covers on one order line.
// ActualWeight is the catch-weight of a DELIVERED report (FR-24.7).
type progressLineRequest struct {
	LineNo       int     `json:"lineNo"`
	Quantity     string  `json:"quantity"`
	ActualWeight *string `json:"actualWeight,omitempty"`
}

// recordProgressRequest is the body of POST /v1/sales/orders/{id}:record-progress.
type recordProgressRequest struct {
	Kind      string                `json:"kind"`
	SourceRef string                `json:"sourceRef"`
	Lines     []progressLineRequest `json:"lines"`
}

// recordProgress applies a delivery (packing slip posted, FR-28.8, duty
// sales.order.record-delivery) or an invoice report (duty
// sales.order.record-invoice) to an order (BR-27.5). It takes an
// Idempotency-Key; the quantities are what this report covers, never
// cumulative, so a partial shipment reports only what shipped.
func (a *API) recordProgress(w http.ResponseWriter, r *http.Request) {
	orderRoute(func(w http.ResponseWriter, r *http.Request, id string) {
		key := r.Header.Get(idempotencyKey)
		var req recordProgressRequest
		if err := httpx.Decode(r, &req); err != nil {
			httpx.Fail(w, r, err)
			return
		}
		fields := map[string]string{}
		if key == "" {
			fields[idempotencyKey] = "is required"
		}
		req.SourceRef = strings.TrimSpace(req.SourceRef)
		if req.Kind != domain.ProgressDelivered && req.Kind != domain.ProgressInvoiced {
			fields["kind"] = "must be DELIVERED or INVOICED"
		}
		if n := len([]rune(req.SourceRef)); n < 1 || n > maxProgressSource {
			fields["sourceRef"] = fmt.Sprintf("must be 1 to %d characters", maxProgressSource)
		}
		if n := len(req.Lines); n < 1 || n > maxLines {
			fields["lines"] = fmt.Sprintf("must have 1 to %d lines", maxLines)
		}
		in := store.NewProgress{OrderID: id, Kind: req.Kind, SourceRef: req.SourceRef}
		seen := map[int]bool{}
		for i, l := range req.Lines {
			name := fmt.Sprintf("lines[%d]", i)
			if l.LineNo < 1 || seen[l.LineNo] {
				fields[name+".lineNo"] = "must be a line number, once per report"
			}
			seen[l.LineNo] = true
			q, err := decimal.NewFromString(l.Quantity)
			if err != nil || !q.IsPositive() || q.Exponent() < -quantityPlaces {
				fields[name+".quantity"] = fmt.Sprintf("must be a positive decimal with at most %d places", quantityPlaces)
			}
			var actual *decimal.Decimal
			if l.ActualWeight != nil {
				w, werr := decimal.NewFromString(*l.ActualWeight)
				if werr != nil || !w.IsPositive() || !domain.FitsWeightColumn(w) {
					fields[name+".actualWeight"] = fmt.Sprintf("must be a positive decimal with at most %d places and 14 integer digits", quantityPlaces)
				} else {
					actual = &w
				}
			}
			in.Lines = append(in.Lines, store.ProgressLineInput{LineNo: l.LineNo, Quantity: q, ActualWeight: actual})
		}
		if len(fields) > 0 {
			validationFailed(w, r, fields)
			return
		}
		p, _ := tenant.FromContext(r.Context())
		if !mayRecord(p, req.Kind) {
			httpx.Fail(w, r, httpx.NewProblem(http.StatusForbidden, "AUTH-0003", fmt.Sprintf("missing duty for a %s report: %s for deliveries, %s for invoices", req.Kind, dutyRecordDelivery, dutyRecordInvoice)))
			return
		}
		hash, err := domain.RequestHash(canonicalProgress(in))
		if err != nil {
			a.fail(w, r, "hash request", err)
			return
		}
		in.IdempotencyKey, in.RequestHash, in.Actor = key, hash, p.UserID
		saved, created, err := a.store.RecordProgress(r.Context(), in, a.now().UTC().Truncate(time.Microsecond))
		if err != nil {
			a.fail(w, r, "record progress", err)
			return
		}
		status := http.StatusOK
		if created {
			status = http.StatusCreated
		}
		httpx.JSON(w, status, saved)
	})(w, r)
}

// mayRecord reports whether p holds the duty for reports of kind.
func mayRecord(p tenant.Principal, kind string) bool {
	switch kind {
	case domain.ProgressDelivered:
		return p.HasDuty(dutyRecordDelivery)
	case domain.ProgressInvoiced:
		return p.HasDuty(dutyRecordInvoice)
	}
	return false
}

// canonicalProgress is what a report's request hash covers: the kind, the
// source and the lines in line order with their quantities normalised, so a
// retry that orders its lines differently or writes 4 as 4.0 is the same
// report rather than a conflicting one.
func canonicalProgress(in store.NewProgress) any {
	type line struct {
		LineNo       int     `json:"lineNo"`
		Quantity     string  `json:"quantity"`
		ActualWeight *string `json:"actualWeight,omitempty"`
	}
	lines := make([]line, 0, len(in.Lines))
	for _, l := range in.Lines {
		item := line{LineNo: l.LineNo, Quantity: l.Quantity.String()}
		if l.ActualWeight != nil {
			s := l.ActualWeight.String()
			item.ActualWeight = &s
		}
		lines = append(lines, item)
	}
	sort.Slice(lines, func(i, j int) bool { return lines[i].LineNo < lines[j].LineNo })
	return struct {
		Kind      string `json:"kind"`
		SourceRef string `json:"sourceRef"`
		Lines     []line `json:"lines"`
	}{in.Kind, in.SourceRef, lines}
}

// listProgress lists the stored delivery and invoice reports, newest first.
func (a *API) listProgress(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	legalEntity, ok := creditLegalEntity(w, r, false)
	if !ok {
		return
	}
	q := r.URL.Query()
	f := store.ProgressFilter{LegalEntityID: legalEntity, OrderID: q.Get("orderId"), Kind: q.Get("kind")}
	fields := map[string]string{}
	if f.OrderID != "" && !ids.Valid(f.OrderID) {
		fields["orderId"] = "must be a UUID"
	}
	if f.Kind != "" && f.Kind != domain.ProgressDelivered && f.Kind != domain.ProgressInvoiced {
		fields["kind"] = "must be DELIVERED or INVOICED"
	}
	limit, ok := pageLimit(q.Get("limit"))
	if !ok {
		fields["limit"] = fmt.Sprintf("must be 1 to %d", maxPageSize)
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	f.Limit = limit
	items, err := a.store.ListProgress(r.Context(), f)
	if err != nil {
		a.fail(w, r, "list order progress", err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
}
