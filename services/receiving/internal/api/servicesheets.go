package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/events"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/money"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/domain"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/store"
)

const (
	dutySheetMaintain = "ServiceEntryMaintain"
	dutySheetAccept   = "ServiceEntryAccept"
	maxSheetText      = 200
)

type sheetLineRequest struct {
	POLineNo    int          `json:"poLineNo"`
	Description string       `json:"description"`
	Unit        string       `json:"unit"`
	Qty         *wireDecimal `json:"qty"`
	UnitPrice   *wireDecimal `json:"unitPrice"`
}

type sheetRequest struct {
	LegalEntityID   string             `json:"legalEntityId"`
	PurchaseOrderID string             `json:"purchaseOrderId"`
	Lines           []sheetLineRequest `json:"lines"`
}

type sheetDecisionRequest struct {
	Reason string `json:"reason"`
}

type sheetLineBody struct {
	POLineNo    int    `json:"poLineNo"`
	Description string `json:"description"`
	Unit        string `json:"unit"`
	Qty         string `json:"qty"`
	UnitPrice   string `json:"unitPrice"`
	Amount      string `json:"amount"`
}

type sheetBody struct {
	ID              string          `json:"id"`
	LegalEntityID   string          `json:"legalEntityId"`
	VendorID        string          `json:"vendorId"`
	PurchaseOrderID string          `json:"purchaseOrderId"`
	Currency        string          `json:"currency"`
	Status          string          `json:"status"`
	Total           string          `json:"total"`
	CreatedBy       string          `json:"createdBy"`
	CreatedAt       string          `json:"createdAt"`
	DecidedBy       string          `json:"decidedBy,omitempty"`
	DecidedAt       string          `json:"decidedAt,omitempty"`
	RejectReason    string          `json:"rejectReason,omitempty"`
	ReceiptID       string          `json:"receiptId,omitempty"`
	LedgerEntryID   string          `json:"ledgerEntryId,omitempty"`
	Lines           []sheetLineBody `json:"lines"`
}

type sheetDecidedData struct {
	SheetID         string                 `json:"sheet_id"`
	LegalEntityID   string                 `json:"legal_entity_id"`
	VendorID        string                 `json:"vendor_id"`
	PurchaseOrderID string                 `json:"purchase_order_id"`
	Currency        string                 `json:"currency"`
	Total           string                 `json:"total"`
	Decision        string                 `json:"decision"`
	DecidedBy       string                 `json:"decided_by"`
	Reason          string                 `json:"reason,omitempty"`
	Lines           []sheetDecidedLineData `json:"lines"`
}

type sheetDecidedLineData struct {
	POLineNo int    `json:"po_line_no"`
	Qty      string `json:"qty"`
	Amount   string `json:"amount"`
}

func toSheetBody(s store.ServiceSheet) sheetBody {
	out := sheetBody{
		ID: s.ID, LegalEntityID: s.LegalEntityID, VendorID: s.VendorID, PurchaseOrderID: s.PurchaseOrderID, Currency: s.Currency, Status: s.Status,
		Total: s.Total.String(), CreatedBy: s.CreatedBy, CreatedAt: s.CreatedAt.UTC().Format(time.RFC3339), DecidedBy: s.DecidedBy, RejectReason: s.RejectReason,
		ReceiptID: s.ReceiptID, LedgerEntryID: s.LedgerEntryID, Lines: make([]sheetLineBody, 0, len(s.Lines)),
	}
	if s.DecidedAt != nil {
		out.DecidedAt = s.DecidedAt.UTC().Format(time.RFC3339)
	}
	for _, l := range s.Lines {
		out.Lines = append(out.Lines, sheetLineBody{POLineNo: l.POLineNo, Description: l.Description, Unit: l.Unit, Qty: l.Qty.String(), UnitPrice: l.UnitPrice.String(), Amount: l.Amount.String()})
	}
	return out
}

func sheetDecidedEvent(tenantID string, s store.ServiceSheet) (events.Event, error) {
	data := sheetDecidedData{
		SheetID: s.ID, LegalEntityID: s.LegalEntityID, VendorID: s.VendorID, PurchaseOrderID: s.PurchaseOrderID, Currency: s.Currency, Total: s.Total.String(),
		Decision: s.Status, DecidedBy: s.DecidedBy, Reason: s.RejectReason, Lines: make([]sheetDecidedLineData, 0, len(s.Lines)),
	}
	for _, l := range s.Lines {
		data.Lines = append(data.Lines, sheetDecidedLineData{POLineNo: l.POLineNo, Qty: l.Qty.String(), Amount: l.Amount.String()})
	}
	return newEvent("receiving", "serviceentrysheet", "decided", s.ID, tenantID, data)
}

func validateSheet(req sheetRequest) map[string]string {
	fields := map[string]string{}
	checkUUID(fields, "legalEntityId", req.LegalEntityID)
	checkUUID(fields, "purchaseOrderId", req.PurchaseOrderID)
	if len(req.Lines) == 0 || len(req.Lines) > domain.MaxSheetLines {
		fields["lines"] = fmt.Sprintf("must hold 1 to %d lines", domain.MaxSheetLines)
	}
	for i, l := range req.Lines {
		key := func(name string) string { return fmt.Sprintf("lines[%d].%s", i, name) }
		if l.POLineNo < 1 {
			fields[key("poLineNo")] = "must be a purchase order line number"
		}
		if utf8.RuneCountInString(l.Description) > maxSheetText || utf8.RuneCountInString(l.Unit) > 20 {
			fields[key("description")] = "text is too long"
		}
		if l.Qty == nil || !l.Qty.IsPositive() {
			fields[key("qty")] = "is required and must be greater than zero"
		}
		if l.UnitPrice == nil || l.UnitPrice.IsNegative() {
			fields[key("unitPrice")] = "is required and must not be negative"
		}
	}
	return fields
}

func (a *API) createServiceSheet(w http.ResponseWriter, r *http.Request) {
	p, ok := principalWithUser(w, r)
	if !ok {
		return
	}
	var req sheetRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if fields := validateSheet(req); len(fields) > 0 {
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
	byNo := map[int]domain.PurchaseOrderLine{}
	for _, l := range po.Lines {
		byNo[l.LineNo] = l
	}
	sheet := store.ServiceSheet{ID: ids.New(), LegalEntityID: req.LegalEntityID, VendorID: po.VendorID, PurchaseOrderID: req.PurchaseOrderID, Currency: po.Currency, CreatedBy: p.UserID}
	limits := map[int]decimal.Decimal{}
	for i, l := range req.Lines {
		line, ok := byNo[l.POLineNo]
		switch {
		case !ok:
			writeError(w, r, validationProblem(map[string]string{fmt.Sprintf("lines[%d].poLineNo", i): "no such line on the purchase order"}))
			return
		case !line.Usable():
			writeError(w, r, poProblem(http.StatusUnprocessableEntity, "RCV-PO-002", "purchase order line %d is cancelled", l.POLineNo))
			return
		}
		limits[l.POLineNo] = line.Amount(po.Currency)
		amount := domain.SheetAmount(l.Qty.Decimal, l.UnitPrice.Decimal, po.Currency)
		sheet.Lines = append(sheet.Lines, domain.SheetLine{POLineNo: l.POLineNo, Description: l.Description, Unit: l.Unit, Qty: l.Qty.Decimal, UnitPrice: l.UnitPrice.Decimal, Amount: amount})
		sheet.Total = sheet.Total.Add(amount)
	}
	created, err := a.store.CreateSheet(r.Context(), sheet, limits)
	if err != nil {
		writeError(w, r, err)
		return
	}
	a.sheetsCreated.Add(1)
	w.Header().Set("Location", "/v1/receiving/service-entry-sheets/"+created.ID)
	httpx.JSON(w, http.StatusCreated, toSheetBody(created))
}

func (a *API) loadSheet(w http.ResponseWriter, r *http.Request, p tenant.Principal) (store.ServiceSheet, bool) {
	id := chi.URLParam(r, "id")
	if !ids.Valid(id) {
		httpx.Fail(w, r, validationProblem(map[string]string{"id": "must be a UUID"}))
		return store.ServiceSheet{}, false
	}
	sheet, err := a.store.GetSheet(r.Context(), id)
	if err == nil {
		err = inScope(p, sheet.LegalEntityID)
	}
	if err != nil {
		writeSheetError(w, r, err)
		return store.ServiceSheet{}, false
	}
	return sheet, true
}

func writeSheetError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, store.ErrSheetNotFound) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, "RCV-NF-008", "service entry sheet not found"))
		return
	}
	writeError(w, r, err)
}

func (a *API) getServiceSheet(w http.ResponseWriter, r *http.Request) {
	p, ok := principalWithUser(w, r)
	if !ok {
		return
	}
	if sheet, ok := a.loadSheet(w, r, p); ok {
		httpx.JSON(w, http.StatusOK, toSheetBody(sheet))
	}
}

func (a *API) acceptServiceSheet(w http.ResponseWriter, r *http.Request) {
	p, ok := principalWithUser(w, r)
	if !ok {
		return
	}
	sheet, ok := a.loadSheet(w, r, p)
	if !ok {
		return
	}
	now := time.Now().UTC()
	postingDate := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	fiscalPeriodID, err := a.resolveFiscalPeriod(r.Context(), sheet.LegalEntityID, postingDate, "")
	if err != nil {
		writeError(w, r, err)
		return
	}
	products := a.sheetProductIDs(r.Context(), sheet)
	accepted, err := a.store.AcceptSheet(r.Context(), sheet.ID, p.UserID, func(s store.ServiceSheet, allocated store.Receipt) (store.SheetAcceptOutcome, error) {
		accrual := domain.ServiceAccrual{
			ID: s.ID, LegalEntityID: s.LegalEntityID, LedgerID: a.ports.Profile.LedgerID, FiscalPeriodID: fiscalPeriodID,
			PostingDate: postingDate, Currency: s.Currency,
		}
		rec := store.Receipt{
			ID: allocated.ID, Number: allocated.Number, LegalEntityID: s.LegalEntityID, VendorID: s.VendorID,
			PurchaseOrderID: s.PurchaseOrderID, PackingSlip: serviceSheetSlipPrefix + s.ID, Date: postingDate,
			Currency: s.Currency, Source: domain.ReceiptSourceService,
		}
		for i, l := range s.Lines {
			lineNo := i + 1
			accrual.Lines = append(accrual.Lines, domain.ServiceAccrualLine{
				LineNo: lineNo, Qty: l.Qty, UnitPrice: money.Amount{Value: l.UnitPrice, Currency: s.Currency},
			})
			productID := products[l.POLineNo]
			if productID == "" {
				productID = stableID(p.TenantID, strings.ToLower(s.PurchaseOrderID), "service-product", fmt.Sprint(l.POLineNo))
			}
			rec.Lines = append(rec.Lines, store.Line{
				LineNo: lineNo, POLineNo: l.POLineNo,
				POLineID:  stableID(p.TenantID, strings.ToLower(s.PurchaseOrderID), "po-line", fmt.Sprint(l.POLineNo)),
				ProductID: productID, Qty: l.Qty, UnitPrice: l.UnitPrice, Unit: l.Unit, Currency: s.Currency,
			})
		}
		entry, items, err := domain.PostServiceSheet(r.Context(), accrual, a.ports.Periods, a.ledgerFor(s.ID), a.ports.Profile)
		if err != nil {
			return store.SheetAcceptOutcome{}, err
		}
		amounts := make([]store.LineAmount, len(items))
		for i, item := range items {
			amounts[i] = store.LineAmount{LineNo: item.LineNo, Amount: item.ReceivedAmount.Value, AmountAcct: acct(item.ReceivedAmount.Value, decimal.NewFromInt(1))}
		}
		postedEvt, err := receiptPostedEvent(p.TenantID, rec, amounts, entry.LedgerEntryID)
		if err != nil {
			return store.SheetAcceptOutcome{}, err
		}
		decidedEvt, err := sheetDecidedEvent(p.TenantID, s)
		if err != nil {
			return store.SheetAcceptOutcome{}, err
		}
		return store.SheetAcceptOutcome{Receipt: rec, Amounts: amounts, LedgerEntryID: entry.LedgerEntryID, Events: []events.Event{decidedEvt, postedEvt}}, nil
	})
	if err != nil {
		writeSheetError(w, r, err)
		return
	}
	a.sheetsDecided.Add(1)
	a.posted.Add(1)
	a.pendingPostings.finish(sheet.ID)
	httpx.JSON(w, http.StatusOK, toSheetBody(accepted))
}

func (a *API) sheetProductIDs(ctx context.Context, sheet store.ServiceSheet) map[int]string {
	out := map[int]string{}
	po, err := a.loadPurchaseOrder(ctx, sheet.LegalEntityID, sheet.PurchaseOrderID)
	if err != nil {
		return out
	}
	for _, l := range po.Lines {
		if ids.Valid(l.ProductID) {
			out[l.LineNo] = l.ProductID
		}
	}
	return out
}

func (a *API) rejectServiceSheet(w http.ResponseWriter, r *http.Request) {
	a.decideServiceSheet(w, r, false)
}

func (a *API) decideServiceSheet(w http.ResponseWriter, r *http.Request, accept bool) {
	p, ok := principalWithUser(w, r)
	if !ok {
		return
	}
	var req sheetDecisionRequest
	if !accept {
		if err := httpx.Decode(r, &req); err != nil {
			httpx.Fail(w, r, err)
			return
		}
		req.Reason = strings.TrimSpace(req.Reason)
		if req.Reason == "" || utf8.RuneCountInString(req.Reason) > maxRejectReason {
			httpx.Fail(w, r, validationProblem(map[string]string{"reason": fmt.Sprintf("is required, at most %d characters", maxRejectReason)}))
			return
		}
	}
	sheet, ok := a.loadSheet(w, r, p)
	if !ok {
		return
	}
	decided, err := a.store.DecideSheet(r.Context(), sheet.ID, p.UserID, accept, req.Reason, func(s store.ServiceSheet) (events.Event, error) {
		return sheetDecidedEvent(p.TenantID, s)
	})
	if err != nil {
		writeSheetError(w, r, err)
		return
	}
	a.sheetsDecided.Add(1)
	httpx.JSON(w, http.StatusOK, toSheetBody(decided))
}
