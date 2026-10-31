package api

import (
	"fmt"
	"net/http"
	"time"
	"unicode/utf8"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/store"
)

const (
	dateLayout         = "2006-01-02"
	maxPackingSlip     = 64
	maxCarrier         = 128
	maxUnit            = 16
	maxOverDeliveryPct = 100
)

type fieldDecimal struct {
	wireDecimal
	err error
}

func (d *fieldDecimal) UnmarshalJSON(b []byte) error {
	d.err = d.wireDecimal.UnmarshalJSON(b)
	return nil
}

type arrivalLine struct {
	POLineID         string        `json:"poLineId"`
	POLineNo         *int          `json:"poLineNo"`
	ProductID        string        `json:"productId"`
	OrderedRemaining *fieldDecimal `json:"orderedRemaining"`
	OverDeliveryPct  *fieldDecimal `json:"overDeliveryPct"`
	UnitPrice        *fieldDecimal `json:"unitPrice"`
	Unit             string        `json:"unit"`
	BatchTracked     bool          `json:"batchTracked"`
	SerialTracked    bool          `json:"serialTracked"`
}

type arrivalRequest struct {
	VendorID        string        `json:"vendorId"`
	PurchaseOrderID string        `json:"purchaseOrderId"`
	LegalEntityID   string        `json:"legalEntityId"`
	PackingSlip     string        `json:"packingSlip"`
	Carrier         string        `json:"carrier"`
	PalletCount     *int          `json:"palletCount"`
	ReceiptDate     string        `json:"receiptDate"`
	Currency        string        `json:"currency"`
	ExchangeRate    *fieldDecimal `json:"exchangeRate"`
	Lines           []arrivalLine `json:"lines"`
}

type receiptLineBody struct {
	LineNo           int      `json:"lineNo"`
	POLineID         string   `json:"poLineId"`
	POLineNo         *int     `json:"poLineNo,omitempty"`
	ProductID        string   `json:"productId"`
	OrderedRemaining string   `json:"orderedRemaining"`
	OverDeliveryPct  string   `json:"overDeliveryPct"`
	UnitPrice        string   `json:"unitPrice"`
	Unit             string   `json:"unit"`
	BatchTracked     bool     `json:"batchTracked"`
	SerialTracked    bool     `json:"serialTracked"`
	Qty              string   `json:"qty"`
	Batch            string   `json:"batch"`
	Serials          []string `json:"serials"`
	LocationID       string   `json:"locationId"`
	InventoryStatus  string   `json:"inventoryStatus"`
	Amount           *string  `json:"amount"`
	AmountAcct       *string  `json:"amountAcct"`
}

type receiptBody struct {
	ID              string            `json:"id"`
	ReceiptNumber   string            `json:"receiptNumber"`
	State           string            `json:"state"`
	Version         int               `json:"version"`
	VendorID        string            `json:"vendorId"`
	PurchaseOrderID string            `json:"purchaseOrderId"`
	LegalEntityID   string            `json:"legalEntityId"`
	PackingSlip     string            `json:"packingSlip"`
	Carrier         string            `json:"carrier"`
	PalletCount     *int              `json:"palletCount,omitempty"`
	ReceiptDate     string            `json:"receiptDate"`
	Currency        string            `json:"currency"`
	ExchangeRate    string            `json:"exchangeRate"`
	PostingDate     string            `json:"postingDate,omitempty"`
	LedgerEntryID   string            `json:"ledgerEntryId,omitempty"`
	CreatedAt       string            `json:"createdAt"`
	Lines           []receiptLineBody `json:"lines"`
}

func nullableString(d decimal.NullDecimal) *string {
	if !d.Valid {
		return nil
	}
	s := d.Decimal.String()
	return &s
}

func toReceiptBody(r store.Receipt) receiptBody {
	out := receiptBody{
		ID: r.ID, ReceiptNumber: r.Number, State: string(r.State), Version: r.Version, VendorID: r.VendorID, PurchaseOrderID: r.PurchaseOrderID,
		LegalEntityID: r.LegalEntityID, PackingSlip: r.PackingSlip, Carrier: r.Carrier, PalletCount: r.PalletCount,
		ReceiptDate: r.Date.Format(dateLayout), Currency: r.Currency, ExchangeRate: r.ExchangeRate.String(),
		LedgerEntryID: r.LedgerEntryID, CreatedAt: r.CreatedAt.UTC().Format(time.RFC3339), Lines: make([]receiptLineBody, 0, len(r.Lines)),
	}
	if r.PostingDate != nil {
		out.PostingDate = r.PostingDate.Format(dateLayout)
	}
	for _, l := range r.Lines {
		serials := l.Serials
		if serials == nil {
			serials = []string{}
		}
		var poLineNo *int
		if l.POLineNo > 0 {
			poLineNo = &l.POLineNo
		}
		out.Lines = append(out.Lines, receiptLineBody{
			LineNo: l.LineNo, POLineID: l.POLineID, POLineNo: poLineNo, ProductID: l.ProductID, OrderedRemaining: l.OrderedRemaining.String(),
			OverDeliveryPct: l.OverDeliveryPct.String(), UnitPrice: l.UnitPrice.String(), Unit: l.Unit, BatchTracked: l.BatchTracked,
			SerialTracked: l.SerialTracked, Qty: l.Qty.String(), Batch: l.Batch, Serials: serials, LocationID: l.LocationID,
			InventoryStatus: l.InventoryStatus, Amount: nullableString(l.Amount), AmountAcct: nullableString(l.AmountAcct),
		})
	}
	return out
}

func checkUUID(fields map[string]string, name, v string) {
	if !ids.Valid(v) {
		fields[name] = "must be a UUID"
	}
}

func checkLength(fields map[string]string, name, v string, min, max int) {
	if n := utf8.RuneCountInString(v); n < min || n > max {
		fields[name] = fmt.Sprintf("must be %d to %d characters", min, max)
	}
}

func decimalField(fields map[string]string, name string, v *fieldDecimal, required bool) (decimal.Decimal, bool) {
	switch {
	case v == nil && required:
		fields[name] = "is required"
	case v != nil && v.err != nil:
		fields[name] = v.err.Error()
	case v != nil:
		return v.Decimal, true
	}
	return decimal.Zero, false
}

func buildArrival(req arrivalRequest) (store.Receipt, map[string]string) {
	fields := map[string]string{}
	checkUUID(fields, "vendorId", req.VendorID)
	checkUUID(fields, "purchaseOrderId", req.PurchaseOrderID)
	checkUUID(fields, "legalEntityId", req.LegalEntityID)
	checkLength(fields, "packingSlip", req.PackingSlip, 1, maxPackingSlip)
	if utf8.RuneCountInString(req.Carrier) > maxCarrier {
		fields["carrier"] = fmt.Sprintf("must be at most %d characters", maxCarrier)
	}
	if req.PalletCount != nil && *req.PalletCount < 0 {
		fields["palletCount"] = "must not be negative"
	}
	date, err := time.Parse(dateLayout, req.ReceiptDate)
	if err != nil {
		fields["receiptDate"] = "must be a YYYY-MM-DD date"
	}
	if len(req.Currency) != 3 || !isUpperAlpha(req.Currency) {
		fields["currency"] = "must be a 3-letter upper-case ISO 4217 code"
	}
	rate := decimal.NewFromInt(1)
	if v, ok := decimalField(fields, "exchangeRate", req.ExchangeRate, false); ok {
		if v.Sign() <= 0 {
			fields["exchangeRate"] = "must be greater than zero"
		}
		rate = v
	}
	if len(req.Lines) == 0 || len(req.Lines) > maxLines {
		fields["lines"] = fmt.Sprintf("must hold 1 to %d lines", maxLines)
	}
	in := store.Receipt{
		VendorID: req.VendorID, PurchaseOrderID: req.PurchaseOrderID, LegalEntityID: req.LegalEntityID, PackingSlip: req.PackingSlip,
		Carrier: req.Carrier, PalletCount: req.PalletCount, Date: date, Currency: req.Currency, ExchangeRate: rate,
		Lines: make([]store.Line, 0, len(req.Lines)),
	}
	for i, l := range req.Lines {
		name := func(field string) string { return fmt.Sprintf("lines[%d].%s", i, field) }
		if l.POLineNo != nil {
			if l.POLineID != "" {
				fields[name("poLineId")] = "is derived from poLineNo and must be omitted"
			}
		} else {
			checkUUID(fields, name("poLineId"), l.POLineID)
		}
		checkUUID(fields, name("productId"), l.ProductID)
		checkLength(fields, name("unit"), l.Unit, 1, maxUnit)
		line := store.Line{
			LineNo: i + 1, POLineID: l.POLineID, ProductID: l.ProductID, Unit: l.Unit, Currency: req.Currency,
			BatchTracked: l.BatchTracked, SerialTracked: l.SerialTracked,
		}
		fromPO := l.POLineNo != nil
		if fromPO {
			line.POLineNo = *l.POLineNo
			if line.POLineNo < 1 {
				fields[name("poLineNo")] = "must be at least 1"
			}
			if l.OrderedRemaining != nil {
				fields[name("orderedRemaining")] = poOverrideMessage
			}
			if l.UnitPrice != nil {
				fields[name("unitPrice")] = poOverrideMessage
			}
		}
		if v, ok := decimalField(fields, name("orderedRemaining"), l.OrderedRemaining, !fromPO); ok && !fromPO {
			if v.Sign() <= 0 {
				fields[name("orderedRemaining")] = "must be greater than zero"
			}
			line.OrderedRemaining = v
		}
		if v, ok := decimalField(fields, name("overDeliveryPct"), l.OverDeliveryPct, false); ok {
			if v.Sign() < 0 || v.GreaterThan(decimal.NewFromInt(maxOverDeliveryPct)) {
				fields[name("overDeliveryPct")] = "must be between 0 and 100"
			}
			line.OverDeliveryPct = v
		}
		if v, ok := decimalField(fields, name("unitPrice"), l.UnitPrice, !fromPO); ok && !fromPO {
			if v.Sign() < 0 {
				fields[name("unitPrice")] = "must not be negative"
			}
			line.UnitPrice = v
		}
		in.Lines = append(in.Lines, line)
	}
	return in, fields
}

func isUpperAlpha(s string) bool {
	for _, c := range s {
		if c < 'A' || c > 'Z' {
			return false
		}
	}
	return true
}

func (a *API) arrive(w http.ResponseWriter, r *http.Request) {
	p, ok := tenant.FromContext(r.Context())
	if !ok || !ids.Valid(p.TenantID) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusUnauthorized, "AUTH-0001", "missing or malformed tenant"))
		return
	}
	var req arrivalRequest
	if err := httpx.Decode(r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	in, fields := buildArrival(req)
	a.requirePOLine(in, fields)
	if len(fields) > 0 {
		problem := httpx.NewProblem(http.StatusBadRequest, "COM-0001", "request failed validation")
		problem.Errors = fields
		httpx.Fail(w, r, problem)
		return
	}
	if err := inScope(p, req.LegalEntityID); err != nil {
		writeError(w, r, err)
		return
	}
	if _, err := a.bindPurchaseOrder(r.Context(), &in); err != nil {
		writeError(w, r, err)
		return
	}
	created, err := a.store.CreateArrival(r.Context(), in)
	if err != nil {
		writeError(w, r, err)
		return
	}
	a.arrived.Add(1)
	w.Header().Set("Location", "/v1/receiving/receipts/"+created.ID)
	httpx.JSON(w, http.StatusCreated, toReceiptBody(created))
}
