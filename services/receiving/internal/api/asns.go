package api

import (
	"fmt"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/domain"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/store"
)

const (
	dutyAsnMaintain = "receiving.asn.maintain"
	dutyAsnReceive  = "receiving.asn.receive"
	maxTracking     = 64
)

type asnLineRequest struct {
	POLineNo   int          `json:"poLineNo"`
	Qty        *wireDecimal `json:"qty"`
	Batch      string       `json:"batch"`
	Serials    []string     `json:"serials"`
	ExpiryDate string       `json:"expiryDate"`
}

type asnRequest struct {
	LegalEntityID   string           `json:"legalEntityId"`
	PurchaseOrderID string           `json:"purchaseOrderId"`
	Carrier         string           `json:"carrier"`
	TrackingNumber  string           `json:"trackingNumber"`
	ShipDate        string           `json:"shipDate"`
	ExpectedArrival string           `json:"expectedArrival"`
	Lines           []asnLineRequest `json:"lines"`
}

type asnStartRequest struct {
	ReceiptDate string `json:"receiptDate"`
	LocationID  string `json:"locationId"`
}

type asnLineBody struct {
	POLineNo   int      `json:"poLineNo"`
	Qty        string   `json:"qty"`
	Batch      string   `json:"batch,omitempty"`
	Serials    []string `json:"serials,omitempty"`
	ExpiryDate string   `json:"expiryDate,omitempty"`
}

type asnBody struct {
	ID              string        `json:"id"`
	LegalEntityID   string        `json:"legalEntityId"`
	VendorID        string        `json:"vendorId"`
	PurchaseOrderID string        `json:"purchaseOrderId"`
	Status          string        `json:"status"`
	Carrier         string        `json:"carrier,omitempty"`
	TrackingNumber  string        `json:"trackingNumber,omitempty"`
	ShipDate        string        `json:"shipDate,omitempty"`
	ExpectedArrival string        `json:"expectedArrival,omitempty"`
	CreatedBy       string        `json:"createdBy"`
	CreatedAt       string        `json:"createdAt"`
	ReceiptID       string        `json:"receiptId,omitempty"`
	Lines           []asnLineBody `json:"lines"`
}

type asnRegistrationLine struct {
	LineNo     int      `json:"lineNo"`
	Qty        string   `json:"qty"`
	Batch      string   `json:"batch,omitempty"`
	Serials    []string `json:"serials,omitempty"`
	LocationID string   `json:"locationId,omitempty"`
	ExpiryDate string   `json:"expiryDate,omitempty"`
}

type asnRegistration struct {
	Lines []asnRegistrationLine `json:"lines"`
}

type asnReceiptBody struct {
	Asn          asnBody         `json:"asn"`
	Receipt      receiptBody     `json:"receipt"`
	Registration asnRegistration `json:"registration"`
}

type asnPage struct {
	Items      []asnBody `json:"items"`
	NextCursor string    `json:"nextCursor,omitempty"`
}

func formatDate(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format(dateLayout)
}

func toAsnBody(a store.Asn) asnBody {
	out := asnBody{
		ID: a.ID, LegalEntityID: a.LegalEntityID, VendorID: a.VendorID, PurchaseOrderID: a.PurchaseOrderID, Status: a.Status, Carrier: a.Carrier,
		TrackingNumber: a.TrackingNumber, ShipDate: formatDate(a.ShipDate), ExpectedArrival: formatDate(a.ExpectedArrival), CreatedBy: a.CreatedBy,
		CreatedAt: a.CreatedAt.UTC().Format(time.RFC3339), ReceiptID: a.ReceiptID, Lines: make([]asnLineBody, 0, len(a.Lines)),
	}
	for _, l := range a.Lines {
		out.Lines = append(out.Lines, asnLineBody{POLineNo: l.POLineNo, Qty: l.Qty.String(), Batch: l.Batch, Serials: l.Serials, ExpiryDate: l.ExpiryDate})
	}
	return out
}

func validateAsn(req asnRequest) (store.Asn, map[string]string) {
	fields := map[string]string{}
	checkUUID(fields, "legalEntityId", req.LegalEntityID)
	checkUUID(fields, "purchaseOrderId", req.PurchaseOrderID)
	if utf8.RuneCountInString(req.Carrier) > maxCarrier {
		fields["carrier"] = fmt.Sprintf("must be at most %d characters", maxCarrier)
	}
	if utf8.RuneCountInString(req.TrackingNumber) > maxTracking {
		fields["trackingNumber"] = fmt.Sprintf("must be at most %d characters", maxTracking)
	}
	in := store.Asn{
		LegalEntityID: req.LegalEntityID, PurchaseOrderID: req.PurchaseOrderID, Carrier: req.Carrier, TrackingNumber: strings.TrimSpace(req.TrackingNumber),
		ShipDate: optionalDate(fields, "shipDate", req.ShipDate), ExpectedArrival: optionalDate(fields, "expectedArrival", req.ExpectedArrival),
	}
	if !in.ShipDate.IsZero() && !in.ExpectedArrival.IsZero() && in.ExpectedArrival.Before(in.ShipDate) {
		fields["expectedArrival"] = "must not be before the ship date"
	}
	if len(req.Lines) == 0 || len(req.Lines) > domain.MaxAsnLines {
		fields["lines"] = fmt.Sprintf("must hold 1 to %d lines", domain.MaxAsnLines)
	}
	seen := map[int]int{}
	for i, l := range req.Lines {
		key := func(name string) string { return fmt.Sprintf("lines[%d].%s", i, name) }
		line := domain.AsnLine{POLineNo: l.POLineNo, Batch: l.Batch, Serials: l.Serials, ExpiryDate: l.ExpiryDate}
		if l.POLineNo < 1 {
			fields[key("poLineNo")] = "must be a purchase order line number"
		} else if first, dup := seen[l.POLineNo]; dup {
			fields[key("poLineNo")] = fmt.Sprintf("duplicates lines[%d].poLineNo", first)
		} else {
			seen[l.POLineNo] = i
		}
		switch {
		case l.Qty == nil || !l.Qty.IsPositive():
			fields[key("qty")] = "is required and must be greater than zero"
		case !domain.FitsColumn(l.Qty.Decimal):
			fields[key("qty")] = qtyScale
		default:
			line.Qty = l.Qty.Decimal
		}
		if utf8.RuneCountInString(l.Batch) > maxBatch {
			fields[key("batch")] = fmt.Sprintf("must be at most %d characters", maxBatch)
		}
		if len(l.Serials) > maxSerials {
			fields[key("serials")] = fmt.Sprintf("must hold at most %d serials", maxSerials)
		} else if len(l.Serials) > 0 && !decimal.NewFromInt(int64(len(l.Serials))).Equal(line.Qty) {
			fields[key("serials")] = "must hold one serial per unit shipped"
		}
		seenSerial := map[string]int{}
		for j, s := range l.Serials {
			name := fmt.Sprintf("lines[%d].serials[%d]", i, j)
			if n := utf8.RuneCountInString(s); n < 1 || n > maxSerial {
				fields[name] = fmt.Sprintf("must be 1 to %d characters", maxSerial)
			} else if first, dup := seenSerial[s]; dup {
				fields[name] = fmt.Sprintf("duplicates serials[%d]", first)
			} else {
				seenSerial[s] = j
			}
		}
		optionalDate(fields, key("expiryDate"), l.ExpiryDate)
		in.Lines = append(in.Lines, line)
	}
	return in, fields
}

func (a *API) createAsn(w http.ResponseWriter, r *http.Request) {
	p, ok := principalWithUser(w, r)
	if !ok {
		return
	}
	var req asnRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	in, fields := validateAsn(req)
	if len(fields) > 0 {
		httpx.Fail(w, r, validationProblem(fields))
		return
	}
	if err := inScope(p, req.LegalEntityID); err != nil {
		writeError(w, r, err)
		return
	}
	po, err := a.loadPurchaseOrder(r.Context(), req.LegalEntityID, req.PurchaseOrderID)
	if err != nil {
		writeError(w, r, err)
		return
	}
	if !strings.EqualFold(po.LegalEntityID, req.LegalEntityID) {
		writeError(w, r, poProblem(http.StatusUnprocessableEntity, "RCV-PO-003", "purchase order belongs to another legal entity"))
		return
	}
	if v := vendorFrom(r.Context()); v != "" && !strings.EqualFold(po.VendorID, v) {
		// A purchase order of another vendor reads as missing, so a vendor cannot probe for order ids.
		writeError(w, r, store.ErrAsnNotFound)
		return
	}
	byNo := map[int]domain.PurchaseOrderLine{}
	for _, l := range po.Lines {
		byNo[l.LineNo] = l
	}
	ordered := map[int]decimal.Decimal{}
	for i, l := range in.Lines {
		line, found := byNo[l.POLineNo]
		switch {
		case !found:
			writeError(w, r, validationProblem(map[string]string{fmt.Sprintf("lines[%d].poLineNo", i): "no such line on the purchase order"}))
			return
		case !line.Usable():
			writeError(w, r, poProblem(http.StatusUnprocessableEntity, "RCV-PO-002", "purchase order line %d is cancelled", l.POLineNo))
			return
		}
		ordered[l.POLineNo] = line.Qty
	}
	in.ID, in.VendorID, in.CreatedBy = ids.New(), po.VendorID, p.UserID
	created, err := a.store.CreateAsn(r.Context(), in, ordered)
	if err != nil {
		writeError(w, r, err)
		return
	}
	a.asnsCreated.Add(1)
	w.Header().Set("Location", "/v1/receiving/asns/"+created.ID)
	httpx.JSON(w, http.StatusCreated, toAsnBody(created))
}

func (a *API) getAsn(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !ids.Valid(id) {
		httpx.Fail(w, r, validationProblem(map[string]string{"id": "must be a UUID"}))
		return
	}
	asn, err := a.store.GetAsn(r.Context(), id)
	if err == nil && vendorFrom(r.Context()) != "" && !strings.EqualFold(asn.VendorID, vendorFrom(r.Context())) {
		err = store.ErrAsnNotFound
	}
	if err != nil {
		writeError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toAsnBody(asn))
}

func (a *API) listAsns(w http.ResponseWriter, r *http.Request) {
	q := query{values: r.URL.Query(), fields: map[string]string{}}
	f := store.AsnFilter{VendorID: q.uuid("vendorId"), PurchaseOrderID: q.uuid("purchaseOrderId"), Limit: q.limit()}
	f.CursorCreatedAt, f.CursorID = q.cursor()
	if v := vendorFrom(r.Context()); v != "" {
		f.VendorID = v
	}
	if len(q.fields) > 0 {
		httpx.Fail(w, r, validationProblem(q.fields))
		return
	}
	rows, err := a.store.ListAsns(r.Context(), f)
	if err != nil {
		writeError(w, r, err)
		return
	}
	page := asnPage{Items: make([]asnBody, 0, len(rows))}
	if len(rows) > f.Limit {
		rows = rows[:f.Limit]
		page.NextCursor = encodeCursor(rows[len(rows)-1].CreatedAt, rows[len(rows)-1].ID)
	}
	for _, asn := range rows {
		page.Items = append(page.Items, toAsnBody(asn))
	}
	httpx.JSON(w, http.StatusOK, page)
}

func (a *API) receiptFromAsn(r *http.Request, asn store.Asn, date time.Time) (store.Receipt, error) {
	po, err := a.loadPurchaseOrder(r.Context(), asn.LegalEntityID, asn.PurchaseOrderID)
	if err != nil {
		return store.Receipt{}, err
	}
	byNo := map[int]domain.PurchaseOrderLine{}
	for _, l := range po.Lines {
		byNo[l.LineNo] = l
	}
	rec := store.Receipt{
		LegalEntityID: asn.LegalEntityID, VendorID: asn.VendorID, PurchaseOrderID: asn.PurchaseOrderID, PackingSlip: domain.AsnPackingPrefix + asn.ID + "-" + ids.New()[24:],
		Carrier: asn.Carrier, Date: date, Currency: po.Currency, ExchangeRate: decimal.NewFromInt(1),
	}
	for i, l := range asn.Lines {
		line, found := byNo[l.POLineNo]
		switch {
		case !found:
			return store.Receipt{}, poProblem(http.StatusUnprocessableEntity, "RCV-PO-006", "purchase order line %d is no longer on the purchase order", l.POLineNo)
		case !ids.Valid(line.ProductID) || line.Unit == "":
			return store.Receipt{}, poProblem(http.StatusUnprocessableEntity, "RCV-PO-007", "purchase order line %d has no product or unit to receive", l.POLineNo)
		}
		rec.Lines = append(rec.Lines, store.Line{
			LineNo: i + 1, POLineNo: l.POLineNo, ProductID: line.ProductID, Unit: line.Unit, Currency: po.Currency,
			BatchTracked: l.Batch != "", SerialTracked: len(l.Serials) > 0,
		})
	}
	if _, err := a.bindPurchaseOrder(r.Context(), &rec); err != nil {
		return store.Receipt{}, err
	}
	return rec, nil
}

func (a *API) startAsnReceipt(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !ids.Valid(id) {
		httpx.Fail(w, r, validationProblem(map[string]string{"id": "must be a UUID"}))
		return
	}
	var req asnStartRequest
	if r.ContentLength != 0 {
		if err := httpx.Decode(r, &req); err != nil {
			httpx.Fail(w, r, err)
			return
		}
	}
	fields := map[string]string{}
	date := time.Now().UTC().Truncate(24 * time.Hour)
	if req.ReceiptDate != "" {
		date = optionalDate(fields, "receiptDate", req.ReceiptDate)
	}
	if req.LocationID != "" {
		checkUUID(fields, "locationId", req.LocationID)
	}
	if len(fields) > 0 {
		httpx.Fail(w, r, validationProblem(fields))
		return
	}
	asn, err := a.store.GetAsn(r.Context(), id)
	if err != nil {
		writeError(w, r, err)
		return
	}
	if asn.Status != domain.AsnOpen {
		writeError(w, r, store.AsnConsumed(asn))
		return
	}
	rec, err := a.receiptFromAsn(r, asn, date)
	if err != nil {
		writeError(w, r, err)
		return
	}
	asn, created, err := a.store.StartAsnReceipt(r.Context(), id, rec)
	if err != nil {
		writeError(w, r, err)
		return
	}
	a.asnReceipts.Add(1)
	a.arrived.Add(1)
	w.Header().Set("Location", "/v1/receiving/receipts/"+created.ID)
	httpx.JSON(w, http.StatusCreated, asnReceiptBody{Asn: toAsnBody(asn), Receipt: toReceiptBody(created), Registration: registrationFromAsn(asn, req.LocationID)})
}

func registrationFromAsn(asn store.Asn, locationID string) asnRegistration {
	out := asnRegistration{Lines: make([]asnRegistrationLine, 0, len(asn.Lines))}
	for i, l := range asn.Lines {
		out.Lines = append(out.Lines, asnRegistrationLine{LineNo: i + 1, Qty: l.Qty.String(), Batch: l.Batch, Serials: l.Serials, LocationID: locationID, ExpiryDate: l.ExpiryDate})
	}
	return out
}
