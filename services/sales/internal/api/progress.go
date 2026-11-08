package api

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

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
	// maxTrackingEntries bounds the batch and serial entries of one line, and
	// maxTrackingValue a batch or serial number (inventory's dimension limit).
	maxTrackingEntries = 1000
	maxTrackingValue   = 60
)

// progressLineRequest is the quantity a report covers on one order line.
// ActualWeight is the catch-weight of a DELIVERED report (FR-24.7).
type progressLineRequest struct {
	LineNo       int                       `json:"lineNo"`
	Quantity     string                    `json:"quantity"`
	ActualWeight *string                   `json:"actualWeight,omitempty"`
	Tracking     []progressTrackingRequest `json:"tracking,omitempty"`
	// Final marks a DELIVERED line as the last delivery for it: a short
	// quantity within the under-delivery tolerance completes the line.
	Final bool `json:"final,omitempty"`
}

// progressTrackingRequest is the part of a delivered line taken from one
// batch or serial (#480): quantity in the line's unit, and on a catch-weight
// line split over several entries each entry's actualWeight.
type progressTrackingRequest struct {
	Batch        string  `json:"batch,omitempty"`
	Serial       string  `json:"serial,omitempty"`
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
			if l.Final && req.Kind != domain.ProgressDelivered {
				fields[name+".final"] = "applies only to a DELIVERED report"
			}
			tracking := progressTracking(fields, name, req.Kind, q, actual, l.Tracking)
			in.Lines = append(in.Lines, store.ProgressLineInput{LineNo: l.LineNo, Quantity: q, ActualWeight: actual, Tracking: tracking, Final: l.Final})
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

// progressTracking validates the batch and serial entries of one report line
// (#480): only on a DELIVERED report; each entry names a batch, a serial or
// both and a positive quantity (exactly 1 for a serial); no batch and serial
// pair twice; the quantities add up to the line's. Entry weights are all set
// or all absent, and when set the line's actualWeight is their sum.
func progressTracking(fields map[string]string, name, kind string, quantity decimal.Decimal, actual *decimal.Decimal, req []progressTrackingRequest) []store.ProgressTracking {
	if len(req) == 0 {
		return nil
	}
	if kind != domain.ProgressDelivered {
		fields[name+".tracking"] = "applies only to a DELIVERED report"
		return nil
	}
	if len(req) > maxTrackingEntries {
		fields[name+".tracking"] = fmt.Sprintf("must have at most %d entries", maxTrackingEntries)
		return nil
	}
	out := make([]store.ProgressTracking, 0, len(req))
	seen := map[[2]string]bool{}
	sum, weights, weighed := decimal.Zero, decimal.Zero, 0
	for i, t := range req {
		entry := fmt.Sprintf("%s.tracking[%d]", name, i)
		batch, serial := strings.TrimSpace(t.Batch), strings.TrimSpace(t.Serial)
		switch {
		case batch == "" && serial == "":
			fields[entry] = "must name a batch or a serial"
		case !trackingValue(batch) || !trackingValue(serial):
			fields[entry] = fmt.Sprintf("batch and serial must be at most %d printable characters", maxTrackingValue)
		case seen[[2]string{batch, serial}] || (serial != "" && seen[[2]string{"\x00", serial}]):
			fields[entry] = "names the same batch or serial as an earlier entry"
		}
		seen[[2]string{batch, serial}] = true
		if serial != "" {
			seen[[2]string{"\x00", serial}] = true
		}
		q, err := decimal.NewFromString(t.Quantity)
		switch {
		case err != nil || !q.IsPositive() || q.Exponent() < -quantityPlaces:
			fields[entry+".quantity"] = fmt.Sprintf("must be a positive decimal with at most %d places", quantityPlaces)
		case serial != "" && !q.Equal(decimal.NewFromInt(1)):
			fields[entry+".quantity"] = "must be 1 for a serial"
		}
		sum = sum.Add(q)
		item := store.ProgressTracking{Batch: batch, Serial: serial, Quantity: q}
		if t.ActualWeight != nil {
			w, werr := decimal.NewFromString(*t.ActualWeight)
			if werr != nil || !w.IsPositive() || !domain.FitsWeightColumn(w) {
				fields[entry+".actualWeight"] = fmt.Sprintf("must be a positive decimal with at most %d places and 14 integer digits", quantityPlaces)
			} else {
				item.ActualWeight = &w
				weights = weights.Add(w)
			}
			weighed++
		}
		out = append(out, item)
	}
	if _, bad := fields[name+".quantity"]; !bad && !sum.Equal(quantity) {
		fields[name+".tracking"] = fmt.Sprintf("entry quantities add up to %s, not the line's %s", sum, quantity)
	}
	switch {
	case weighed > 0 && weighed < len(req):
		fields[name+".tracking"] = "actualWeight must be set on every entry or on none"
	case weighed > 0 && (actual == nil || !actual.Equal(weights)):
		fields[name+".actualWeight"] = "must be the sum of the tracking entries' actualWeight"
	}
	return out
}

// trackingValue reports whether a batch or serial number fits inventory's
// dimension value: at most maxTrackingValue characters, none of them control.
func trackingValue(v string) bool {
	if utf8.RuneCountInString(v) > maxTrackingValue {
		return false
	}
	for _, r := range v {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
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
	type entry struct {
		Batch        string  `json:"batch,omitempty"`
		Serial       string  `json:"serial,omitempty"`
		Quantity     string  `json:"quantity"`
		ActualWeight *string `json:"actualWeight,omitempty"`
	}
	type line struct {
		LineNo       int     `json:"lineNo"`
		Quantity     string  `json:"quantity"`
		ActualWeight *string `json:"actualWeight,omitempty"`
		Tracking     []entry `json:"tracking,omitempty"`
		Final        bool    `json:"final,omitempty"`
	}
	lines := make([]line, 0, len(in.Lines))
	for _, l := range in.Lines {
		item := line{LineNo: l.LineNo, Quantity: l.Quantity.String(), Final: l.Final}
		if l.ActualWeight != nil {
			s := l.ActualWeight.String()
			item.ActualWeight = &s
		}
		// Entries keep their order: it is the order inventory issues them in.
		for _, t := range l.Tracking {
			e := entry{Batch: t.Batch, Serial: t.Serial, Quantity: t.Quantity.String()}
			if t.ActualWeight != nil {
				s := t.ActualWeight.String()
				e.ActualWeight = &s
			}
			item.Tracking = append(item.Tracking, e)
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
