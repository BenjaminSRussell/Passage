package api

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/inventory/internal/domain"
	"github.com/BenjaminSRussell/passage/services/inventory/internal/store"
)

// Transfer and ABC privileges (3.16.17): TransferOrderMaintain, TransferOrderPost
// and the ABC part of InventorySetupMaintain.
const (
	dutyTransferWrite  = "inventory.transfer.write"
	dutyTransferPost   = "inventory.transfer.post"
	dutyABCWrite       = "inventory.abc.write"
	transferOrdersPath = "/v1/inventory/transfer-orders"
	inTransitPath      = "/v1/inventory/transfers/in-transit"
	transferLinesPath  = "/v1/inventory/transfer-order-lines"
	abcRunsPath        = "/v1/inventory/abc-runs"
	dateFormat         = "2006-01-02"
	maxReceiptLines    = 1000
	warehouseCode      = 60 // maximum length of a warehouse or site code
)

type transferRequest struct {
	LegalEntityID      string                `json:"legalEntityId"`
	FromWarehouseID    string                `json:"fromWarehouseId"`
	ToWarehouseID      string                `json:"toWarehouseId"`
	ToSiteID           string                `json:"toSiteId"`
	TransitWarehouseID string                `json:"transitWarehouseId"`
	UseTransit         *bool                 `json:"useTransit"`
	ShipDate           string                `json:"shipDate"`
	PlannedReceiptDate string                `json:"plannedReceiptDate"`
	DeliveryTerms      string                `json:"deliveryTerms"`
	ModeOfDelivery     string                `json:"modeOfDelivery"`
	Draft              bool                  `json:"draft"`
	Lines              []transferLineRequest `json:"lines"`
}

type transferLineRequest struct {
	ProductID        string            `json:"productId"`
	Dimensions       map[string]string `json:"dimensions"`
	Unit             string            `json:"unit"`
	Qty              string            `json:"qty"`
	UnderdeliveryPct string            `json:"underdeliveryPct"`
	OverdeliveryPct  string            `json:"overdeliveryPct"`
}

// transferEditRequest is the PATCH body: every field is optional, and an empty
// plannedReceiptDate, toSiteId, deliveryTerms or modeOfDelivery clears it.
type transferEditRequest struct {
	ShipDate           *string               `json:"shipDate"`
	PlannedReceiptDate *string               `json:"plannedReceiptDate"`
	ToSiteID           *string               `json:"toSiteId"`
	DeliveryTerms      *string               `json:"deliveryTerms"`
	ModeOfDelivery     *string               `json:"modeOfDelivery"`
	AddLines           []transferLineRequest `json:"addLines"`
	UpdateLines        []lineEditRequest     `json:"updateLines"`
	RemoveLineIDs      []string              `json:"removeLineIds"`
}

type lineEditRequest struct {
	LineID           string  `json:"lineId"`
	Qty              *string `json:"qty"`
	UnderdeliveryPct *string `json:"underdeliveryPct"`
	OverdeliveryPct  *string `json:"overdeliveryPct"`
}

type shipRequest struct {
	ShipDate string            `json:"shipDate"`
	Lines    []shipLineRequest `json:"lines"`
}

type shipLineRequest struct {
	LineID string `json:"lineId"`
	Qty    string `json:"qty"`
}

type receiveRequest struct {
	ReceiptDate string               `json:"receiptDate"`
	Lines       []receiveLineRequest `json:"lines"`
}

type receiveLineRequest struct {
	LineID     string `json:"lineId"`
	Qty        string `json:"qty"`
	CloseShort bool   `json:"closeShort"`
}

type transferLineResponse struct {
	ID               string            `json:"id"`
	LineNo           int               `json:"lineNo"`
	ProductID        string            `json:"productId"`
	Dimensions       map[string]string `json:"dimensions"`
	Unit             string            `json:"unit"`
	Qty              string            `json:"qty"`
	ShippedQty       string            `json:"shippedQty"`
	ReceivedQty      string            `json:"receivedQty"`
	InTransit        string            `json:"inTransit"`
	UnderdeliveryPct string            `json:"underdeliveryPct"`
	OverdeliveryPct  string            `json:"overdeliveryPct"`
	UnshippedQty     string            `json:"unshippedQty"`
	Closed           bool              `json:"closed"`
}

type transferResponse struct {
	ID                 string                 `json:"id"`
	LegalEntityID      string                 `json:"legalEntityId"`
	Number             string                 `json:"number"`
	Status             string                 `json:"status"`
	FromWarehouseID    string                 `json:"fromWarehouseId"`
	ToWarehouseID      string                 `json:"toWarehouseId"`
	ToSiteID           string                 `json:"toSiteId,omitempty"`
	TransitWarehouseID string                 `json:"transitWarehouseId,omitempty"`
	UseTransit         bool                   `json:"useTransit"`
	ShipDate           string                 `json:"shipDate"`
	PlannedReceiptDate string                 `json:"plannedReceiptDate,omitempty"`
	DeliveryTerms      string                 `json:"deliveryTerms,omitempty"`
	ModeOfDelivery     string                 `json:"modeOfDelivery,omitempty"`
	ReceiptDate        string                 `json:"receiptDate,omitempty"`
	Version            int                    `json:"version"`
	CreatedAt          time.Time              `json:"createdAt"`
	Lines              []transferLineResponse `json:"lines"`
}

type inTransitItem struct {
	OrderID            string            `json:"orderId"`
	Number             string            `json:"number"`
	LineID             string            `json:"lineId"`
	ProductID          string            `json:"productId"`
	Dimensions         map[string]string `json:"dimensions"`
	Unit               string            `json:"unit"`
	FromWarehouseID    string            `json:"fromWarehouseId"`
	ToWarehouseID      string            `json:"toWarehouseId"`
	TransitWarehouseID string            `json:"transitWarehouseId"`
	ShipDate           string            `json:"shipDate"`
	DaysInTransit      int               `json:"daysInTransit"`
	InTransitQty       string            `json:"inTransitQty"`
	ClosedShort        bool              `json:"closedShort"`
}

type abcThresholdsRequest struct {
	A string `json:"a"`
	B string `json:"b"`
	C string `json:"c"`
}

type abcRunRequest struct {
	LegalEntityID string                `json:"legalEntityId"`
	Criterion     string                `json:"criterion"`
	ZeroClass     string                `json:"zeroClass"`
	Thresholds    *abcThresholdsRequest `json:"thresholds"`
}

type abcItemResponse struct {
	ProductID string `json:"productId"`
	Site      string `json:"site"`
	Value     string `json:"value"`
	CumPct    string `json:"cumPct"`
	Class     string `json:"class"`
}

type abcRunResponse struct {
	ID            string            `json:"id"`
	LegalEntityID string            `json:"legalEntityId"`
	Criterion     string            `json:"criterion"`
	Thresholds    map[string]string `json:"thresholds"`
	ZeroClass     string            `json:"zeroClass"`
	CreatedAt     time.Time         `json:"createdAt"`
	ClassCounts   map[string]int    `json:"classCounts"`
	Items         []abcItemResponse `json:"items,omitempty"`
}

// registerTransfers mounts the transfer order, in-transit and ABC routes (FR-16.15, 16.16, 16.19).
func (a *API) registerTransfers(r chi.Router) {
	r.With(authz.RequireDuty(dutyTransferWrite)).Post(transferOrdersPath, a.createTransfer)
	r.With(authz.RequireDuty(dutyRead)).Get(transferOrdersPath, a.listTransfers)
	r.With(authz.RequireDuty(dutyRead)).Get(transferOrdersPath+"/{id}", transferRoute(a.getTransfer))
	r.With(authz.RequireDuty(dutyTransferWrite)).Patch(transferOrdersPath+"/{id}", transferRoute(a.editTransfer))
	r.With(authz.RequireDuty(dutyTransferPost)).Post(transferOrdersPath+"/{id}:ship", transferRoute(a.shipTransfer))
	r.With(authz.RequireDuty(dutyTransferPost)).Post(transferOrdersPath+"/{id}:receive", transferRoute(a.receiveTransfer))
	r.With(authz.RequireDuty(dutyTransferWrite)).Post(transferOrdersPath+"/{id}:cancel", transferRoute(a.cancelTransfer))
	r.With(authz.RequireDuty(dutyTransferWrite)).Post(transferOrdersPath+"/{id}:confirm", transferRoute(a.confirmTransfer))
	r.With(authz.RequireDuty(dutyTransferWrite)).Delete(transferOrdersPath+"/{id}", transferRoute(a.deleteTransfer))
	r.With(authz.RequireDuty(dutyRead)).Get(inTransitPath, a.listInTransit)
	r.With(authz.RequireDuty(dutyRead)).Get(transferLinesPath, a.listTransferLines)
	r.With(authz.RequireDuty(dutyABCWrite)).Post(abcRunsPath, a.runABC)
	r.With(authz.RequireDuty(dutyRead)).Get(abcRunsPath, a.listABCRuns)
	r.With(authz.RequireDuty(dutyRead)).Get(abcRunsPath+"/{id}", a.getABCRun)
}

// deliveryCode checks a delivery terms or mode of delivery code: optional, at most 20 characters.
func deliveryCode(fields map[string]string, name, value string) {
	if utf8.RuneCountInString(value) > domain.MaxDeliveryCode {
		fields[name] = fmt.Sprintf("must be at most %d characters", domain.MaxDeliveryCode)
	}
}

// transferRoute checks the tenant and the transfer order id before the handler runs.
func transferRoute(next func(http.ResponseWriter, *http.Request, string)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !validTenant(w, r) {
			return
		}
		id := chi.URLParam(r, "id")
		if !ids.Valid(id) {
			httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, domain.CodeTransferNotFound, "transfer order not found"))
			return
		}
		next(w, r, id)
	}
}

// code checks a warehouse or site code: required unless optional, at most 60 characters.
func code(fields map[string]string, name, value string, optional bool) {
	switch {
	case value == "" && !optional:
		fields[name] = "is required"
	case len(value) > warehouseCode:
		fields[name] = fmt.Sprintf("must be at most %d characters", warehouseCode)
	}
}

func (a *API) createTransfer(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var req transferRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	if !ids.Valid(req.LegalEntityID) {
		fields["legalEntityId"] = "must be a UUID"
	}
	req.FromWarehouseID, req.ToWarehouseID = strings.TrimSpace(req.FromWarehouseID), strings.TrimSpace(req.ToWarehouseID)
	req.ToSiteID, req.TransitWarehouseID = strings.TrimSpace(req.ToSiteID), strings.TrimSpace(req.TransitWarehouseID)
	code(fields, "fromWarehouseId", req.FromWarehouseID, false)
	code(fields, "toWarehouseId", req.ToWarehouseID, false)
	code(fields, "toSiteId", req.ToSiteID, true)
	code(fields, "transitWarehouseId", req.TransitWarehouseID, true)
	req.DeliveryTerms, req.ModeOfDelivery = strings.TrimSpace(req.DeliveryTerms), strings.TrimSpace(req.ModeOfDelivery)
	deliveryCode(fields, "deliveryTerms", req.DeliveryTerms)
	deliveryCode(fields, "modeOfDelivery", req.ModeOfDelivery)
	shipDate, err := time.Parse(dateFormat, req.ShipDate)
	if err != nil {
		fields["shipDate"] = "must be a date (YYYY-MM-DD)"
	}
	var plannedReceipt time.Time
	if strings.TrimSpace(req.PlannedReceiptDate) != "" {
		if plannedReceipt, err = time.Parse(dateFormat, strings.TrimSpace(req.PlannedReceiptDate)); err != nil {
			fields["plannedReceiptDate"] = "must be a date (YYYY-MM-DD)"
		} else if plannedReceipt.Before(shipDate) {
			fields["plannedReceiptDate"] = "must not be before the ship date"
		}
	}
	useTransit := true
	if req.UseTransit != nil {
		useTransit = *req.UseTransit
	}
	if !useTransit {
		req.TransitWarehouseID = ""
	}
	switch {
	case len(req.Lines) == 0:
		fields["lines"] = "must hold at least one line"
	case len(req.Lines) > maxReceiptLines:
		fields["lines"] = fmt.Sprintf("must hold at most %d lines", maxReceiptLines)
	}
	lines := make([]domain.TransferLine, 0, len(req.Lines))
	for i, l := range req.Lines {
		lines = append(lines, parseTransferLine(fields, fmt.Sprintf("lines[%d].", i), i+1, l))
	}
	key := strings.TrimSpace(r.Header.Get(idempotencyHeader))
	if key == "" || len(key) > maxKeyLength {
		fields[idempotencyHeader] = fmt.Sprintf("is required (at most %d characters)", maxKeyLength)
	}
	if len(fields) > 0 {
		fieldProblem(w, r, fields)
		return
	}
	in := domain.TransferOrder{LegalEntityID: req.LegalEntityID, FromWarehouse: req.FromWarehouseID, ToWarehouse: req.ToWarehouseID,
		ToSite: req.ToSiteID, TransitWarehouse: req.TransitWarehouseID, UseTransit: useTransit, ShipDate: shipDate, PlannedReceipt: plannedReceipt,
		DeliveryTerms: req.DeliveryTerms, ModeOfDelivery: req.ModeOfDelivery, Lines: lines}
	if req.Draft {
		in.Status = domain.TransferDraft
	}
	out, replayed, err := a.store.CreateTransfer(r.Context(), key, in)
	if err != nil {
		a.failed(w, r, "create transfer order", err)
		return
	}
	status := http.StatusCreated
	if replayed {
		status = http.StatusOK
		w.Header().Set("Idempotent-Replayed", "true")
	}
	writeTransfer(w, status, out)
}

// parseTransferLine validates one line request into a domain line, recording
// field problems under prefix.
func parseTransferLine(fields map[string]string, prefix string, lineNo int, l transferLineRequest) domain.TransferLine {
	if !ids.Valid(l.ProductID) {
		fields[prefix+"productId"] = "must be a UUID"
	}
	if !unitCode.MatchString(l.Unit) {
		fields[prefix+"unit"] = "is required (1 to 12 characters of A-Z, 0-9, _ or -)"
	}
	qty, qErr := decimal.NewFromString(strings.TrimSpace(l.Qty))
	if qErr != nil || !qty.IsPositive() {
		fields[prefix+"qty"] = "must be a positive decimal number"
	}
	under, uErr := decimalOr(l.UnderdeliveryPct, decimal.Zero)
	if uErr != nil {
		fields[prefix+"underdeliveryPct"] = "must be a decimal number"
	}
	over, oErr := decimalOr(l.OverdeliveryPct, decimal.Zero)
	if oErr != nil {
		fields[prefix+"overdeliveryPct"] = "must be a decimal number"
	}
	return domain.TransferLine{LineNo: lineNo, ProductID: l.ProductID, Dims: domain.Dimensions(l.Dimensions),
		Unit: l.Unit, Qty: qty, UnderPct: under, OverPct: over}
}

func (a *API) listTransfers(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	status := r.URL.Query().Get("status")
	if status != "" && !isTransferStatus(status) {
		fieldProblem(w, r, map[string]string{"status": "unknown transfer order status"})
		return
	}
	orders, err := a.store.ListTransfers(r.Context(), status)
	if err != nil {
		a.failed(w, r, "list transfer orders", err)
		return
	}
	items := make([]transferResponse, 0, len(orders))
	for _, o := range orders {
		items = append(items, toTransferResponse(o))
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
}

func isTransferStatus(s string) bool {
	switch s {
	case domain.TransferDraft, domain.TransferCreated, domain.TransferShipped, domain.TransferPartial, domain.TransferReceived, domain.TransferCancelled:
		return true
	}
	return false
}

func (a *API) getTransfer(w http.ResponseWriter, r *http.Request, id string) {
	o, err := a.store.GetTransfer(r.Context(), id)
	if err != nil {
		a.failed(w, r, "get transfer order", err)
		return
	}
	writeTransfer(w, http.StatusOK, o)
}

// optionalDate parses an optional YYYY-MM-DD, defaulting to today (UTC).
func optionalDate(raw string) (time.Time, bool) {
	if strings.TrimSpace(raw) == "" {
		return time.Now().UTC().Truncate(24 * time.Hour), true
	}
	d, err := time.Parse(dateFormat, strings.TrimSpace(raw))
	return d, err == nil
}

// shipTransfer ships the order: the quantities in lines, or what is left on
// every open line when there are none (a partial shipment names its lines).
// The body may name the ship date (default today). If-Match carries the
// order version.
func (a *API) shipTransfer(w http.ResponseWriter, r *http.Request, id string) {
	version, err := ifMatchVersion(r)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	var req shipRequest
	if r.ContentLength != 0 {
		if err := httpx.Decode(r, &req); err != nil {
			httpx.Fail(w, r, err)
			return
		}
	}
	shipDate, ok := optionalDate(req.ShipDate)
	if !ok {
		fieldProblem(w, r, map[string]string{"shipDate": "must be a date (YYYY-MM-DD)"})
		return
	}
	fields := map[string]string{}
	if len(req.Lines) > maxReceiptLines {
		fields["lines"] = fmt.Sprintf("must hold at most %d lines", maxReceiptLines)
	}
	ships := make([]store.TransferShipment, 0, len(req.Lines))
	for i, l := range req.Lines {
		prefix := fmt.Sprintf("lines[%d].", i)
		if !ids.Valid(l.LineID) {
			fields[prefix+"lineId"] = "must be a UUID"
		}
		qty, qErr := decimal.NewFromString(strings.TrimSpace(l.Qty))
		if qErr != nil || !qty.IsPositive() {
			fields[prefix+"qty"] = "must be a positive decimal number"
		}
		ships = append(ships, store.TransferShipment{LineID: l.LineID, Qty: qty})
	}
	if len(fields) > 0 {
		fieldProblem(w, r, fields)
		return
	}
	o, err := a.store.ShipTransfer(r.Context(), id, version, ships, shipDate, a.post.Negative)
	if err != nil {
		a.failed(w, r, "ship transfer order", err)
		return
	}
	writeTransfer(w, http.StatusOK, o)
}

// receiveTransfer books receipts against the order. If-Match carries the order version.
func (a *API) receiveTransfer(w http.ResponseWriter, r *http.Request, id string) {
	version, err := ifMatchVersion(r)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	var req receiveRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	receiptDate, ok := optionalDate(req.ReceiptDate)
	if !ok {
		fields["receiptDate"] = "must be a date (YYYY-MM-DD)"
	}
	switch {
	case len(req.Lines) == 0:
		fields["lines"] = "must hold at least one line"
	case len(req.Lines) > maxReceiptLines:
		fields["lines"] = fmt.Sprintf("must hold at most %d lines", maxReceiptLines)
	}
	receipts := make([]store.TransferReceipt, 0, len(req.Lines))
	for i, l := range req.Lines {
		prefix := fmt.Sprintf("lines[%d].", i)
		if !ids.Valid(l.LineID) {
			fields[prefix+"lineId"] = "must be a UUID"
		}
		qty, err := decimal.NewFromString(strings.TrimSpace(l.Qty))
		if err != nil || !qty.IsPositive() {
			fields[prefix+"qty"] = "must be a positive decimal number"
		}
		receipts = append(receipts, store.TransferReceipt{LineID: l.LineID, Qty: qty, CloseShort: l.CloseShort})
	}
	if len(fields) > 0 {
		fieldProblem(w, r, fields)
		return
	}
	o, err := a.store.ReceiveTransfer(r.Context(), id, version, receipts, receiptDate, a.post.Negative)
	if err != nil {
		a.failed(w, r, "receive transfer order", err)
		return
	}
	writeTransfer(w, http.StatusOK, o)
}

// editTransfer changes a DRAFT or CREATED order (FR-16.15): dates, receiving site,
// delivery terms and mode, and lines added, changed or removed, in one atomic step. If-Match carries the
// order version.
func (a *API) editTransfer(w http.ResponseWriter, r *http.Request, id string) {
	version, err := ifMatchVersion(r)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	var req transferEditRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	var e domain.TransferEdit
	if req.ShipDate != nil {
		d, err := time.Parse(dateFormat, strings.TrimSpace(*req.ShipDate))
		if err != nil {
			fields["shipDate"] = "must be a date (YYYY-MM-DD)"
		}
		e.ShipDate = &d
	}
	if req.PlannedReceiptDate != nil {
		if raw := strings.TrimSpace(*req.PlannedReceiptDate); raw == "" {
			e.ClearPlannedReceipt = true
		} else if d, err := time.Parse(dateFormat, raw); err != nil {
			fields["plannedReceiptDate"] = "must be a date (YYYY-MM-DD) or empty to clear"
		} else {
			e.PlannedReceipt = &d
		}
	}
	if req.ToSiteID != nil {
		site := strings.TrimSpace(*req.ToSiteID)
		code(fields, "toSiteId", site, true)
		e.ToSite = &site
	}
	if req.DeliveryTerms != nil {
		v := strings.TrimSpace(*req.DeliveryTerms)
		deliveryCode(fields, "deliveryTerms", v)
		e.DeliveryTerms = &v
	}
	if req.ModeOfDelivery != nil {
		v := strings.TrimSpace(*req.ModeOfDelivery)
		deliveryCode(fields, "modeOfDelivery", v)
		e.ModeOfDelivery = &v
	}
	if n := len(req.AddLines) + len(req.UpdateLines) + len(req.RemoveLineIDs); n > maxReceiptLines {
		fields["lines"] = fmt.Sprintf("the edit may name at most %d lines", maxReceiptLines)
	}
	for i, l := range req.AddLines {
		e.Add = append(e.Add, parseTransferLine(fields, fmt.Sprintf("addLines[%d].", i), 0, l))
	}
	for i, u := range req.UpdateLines {
		prefix := fmt.Sprintf("updateLines[%d].", i)
		if !ids.Valid(u.LineID) {
			fields[prefix+"lineId"] = "must be a UUID"
		}
		le := domain.LineEdit{LineID: u.LineID}
		for _, f := range []struct {
			name string
			in   *string
			out  **decimal.Decimal
			qty  bool
		}{{"qty", u.Qty, &le.Qty, true}, {"underdeliveryPct", u.UnderdeliveryPct, &le.UnderPct, false}, {"overdeliveryPct", u.OverdeliveryPct, &le.OverPct, false}} {
			if f.in == nil {
				continue
			}
			d, err := decimal.NewFromString(strings.TrimSpace(*f.in))
			switch {
			case err != nil:
				fields[prefix+f.name] = "must be a decimal number"
			case f.qty && !d.IsPositive():
				fields[prefix+f.name] = "must be a positive decimal number"
			}
			*f.out = &d
		}
		e.Update = append(e.Update, le)
	}
	for i, lineID := range req.RemoveLineIDs {
		if !ids.Valid(lineID) {
			fields[fmt.Sprintf("removeLineIds[%d]", i)] = "must be a UUID"
		}
	}
	e.Remove = req.RemoveLineIDs
	if len(fields) > 0 {
		fieldProblem(w, r, fields)
		return
	}
	o, err := a.store.EditTransfer(r.Context(), id, version, e)
	if err != nil {
		a.failed(w, r, "edit transfer order", err)
		return
	}
	writeTransfer(w, http.StatusOK, o)
}

// confirmTransfer turns a DRAFT order into CREATED (If-Match carries the version).
func (a *API) confirmTransfer(w http.ResponseWriter, r *http.Request, id string) {
	version, err := ifMatchVersion(r)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	o, err := a.store.ConfirmTransfer(r.Context(), id, version)
	if err != nil {
		a.failed(w, r, "confirm transfer order", err)
		return
	}
	writeTransfer(w, http.StatusOK, o)
}

// deleteTransfer removes a DRAFT or CREATED order that shipped nothing (If-Match carries the version).
func (a *API) deleteTransfer(w http.ResponseWriter, r *http.Request, id string) {
	version, err := ifMatchVersion(r)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if err := a.store.DeleteTransfer(r.Context(), id, version); err != nil {
		a.failed(w, r, "delete transfer order", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) cancelTransfer(w http.ResponseWriter, r *http.Request, id string) {
	version, err := ifMatchVersion(r)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	o, err := a.store.CancelTransfer(r.Context(), id, version)
	if err != nil {
		a.failed(w, r, "cancel transfer order", err)
		return
	}
	writeTransfer(w, http.StatusOK, o)
}

// listInTransit answers the stock-in-transit inquiry (FR-16.16).
func (a *API) listInTransit(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	rows, err := a.store.ListInTransit(r.Context())
	if err != nil {
		a.failed(w, r, "stock in transit", err)
		return
	}
	now := time.Now().UTC()
	items := make([]inTransitItem, 0, len(rows))
	for _, row := range rows {
		items = append(items, inTransitItem{OrderID: row.OrderID, Number: row.Number, LineID: row.LineID, ProductID: row.ProductID,
			Dimensions: row.DimensionSet, Unit: row.Unit, FromWarehouseID: row.FromWarehouse, ToWarehouseID: row.ToWarehouse,
			TransitWarehouseID: row.TransitWarehouse, ShipDate: row.ShipDate.Format(dateFormat),
			DaysInTransit: domain.InTransitDays(row.ShipDate, now), InTransitQty: row.InTransit.String(), ClosedShort: row.Closed})
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
}

func (a *API) runABC(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var req abcRunRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	if !ids.Valid(req.LegalEntityID) {
		fields["legalEntityId"] = "must be a UUID"
	}
	th := domain.DefaultAbcThresholds()
	if req.Thresholds != nil {
		for name, raw := range map[string]struct {
			in  string
			out *decimal.Decimal
		}{"a": {req.Thresholds.A, &th.A}, "b": {req.Thresholds.B, &th.B}, "c": {req.Thresholds.C, &th.C}} {
			v, err := decimal.NewFromString(strings.TrimSpace(raw.in))
			if err != nil {
				fields["thresholds."+name] = "must be a decimal number"
				continue
			}
			*raw.out = v
		}
	}
	criterion := strings.TrimSpace(req.Criterion)
	if criterion == "" {
		criterion = domain.AbcCriterionValue
	}
	if criterion != domain.AbcCriterionValue {
		fields["criterion"] = "must be VALUE (REVENUE, MARGIN and CARRYING_COST are not available yet)"
	}
	zero := strings.TrimSpace(req.ZeroClass)
	if zero == "" {
		zero = domain.ABCC
	}
	if zero != domain.ABCC && zero != domain.ABCN {
		fields["zeroClass"] = "must be C or N"
	}
	key := strings.TrimSpace(r.Header.Get(idempotencyHeader))
	if key == "" || len(key) > maxKeyLength {
		fields[idempotencyHeader] = fmt.Sprintf("is required (at most %d characters)", maxKeyLength)
	}
	if len(fields) > 0 {
		fieldProblem(w, r, fields)
		return
	}
	run, replayed, err := a.store.RunABC(r.Context(), store.AbcRunInput{IdempotencyKey: key, LegalEntityID: req.LegalEntityID,
		Criterion: criterion, Thresholds: th, ZeroClass: zero})
	if err != nil {
		a.failed(w, r, "run ABC classification", err)
		return
	}
	status := http.StatusCreated
	if replayed {
		status = http.StatusOK
		w.Header().Set("Idempotent-Replayed", "true")
	}
	httpx.JSON(w, status, toABCRunResponse(run, run.ClassCounts(), true))
}

func (a *API) listABCRuns(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	runs, err := a.store.ListABCRuns(r.Context())
	if err != nil {
		a.failed(w, r, "list ABC runs", err)
		return
	}
	items := make([]abcRunResponse, 0, len(runs))
	for _, s := range runs {
		items = append(items, toABCRunResponse(s.Run, s.Counts, false))
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
}

func (a *API) getABCRun(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id := chi.URLParam(r, "id")
	if !ids.Valid(id) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, domain.CodeABCNotFound, "ABC run not found"))
		return
	}
	run, err := a.store.GetABCRun(r.Context(), id)
	if err != nil {
		a.failed(w, r, "get ABC run", err)
		return
	}
	httpx.JSON(w, http.StatusOK, toABCRunResponse(run, run.ClassCounts(), true))
}

func toABCRunResponse(run domain.AbcRun, counts map[string]int, withItems bool) abcRunResponse {
	out := abcRunResponse{ID: run.ID, LegalEntityID: run.LegalEntityID, Criterion: run.Criterion, ZeroClass: run.ZeroClass, CreatedAt: run.CreatedAt,
		Thresholds:  map[string]string{"a": run.Thresholds.A.String(), "b": run.Thresholds.B.String(), "c": run.Thresholds.C.String()},
		ClassCounts: counts}
	if withItems {
		out.Items = []abcItemResponse{}
		for _, it := range run.Items {
			out.Items = append(out.Items, abcItemResponse{ProductID: it.ProductID, Site: it.Site, Value: it.Value.String(),
				CumPct: it.CumPct.StringFixed(2), Class: it.Class})
		}
	}
	return out
}

func toTransferResponse(o domain.TransferOrder) transferResponse {
	out := transferResponse{ID: o.ID, LegalEntityID: o.LegalEntityID, Number: o.Number, Status: o.Status,
		FromWarehouseID: o.FromWarehouse, ToWarehouseID: o.ToWarehouse, ToSiteID: o.ToSite, TransitWarehouseID: o.TransitWarehouse,
		UseTransit: o.UseTransit, DeliveryTerms: o.DeliveryTerms, ModeOfDelivery: o.ModeOfDelivery,
		ShipDate: o.ShipDate.Format(dateFormat), Version: o.Version, CreatedAt: o.CreatedAt,
		Lines: []transferLineResponse{}}
	if !o.ReceiptDate.IsZero() {
		out.ReceiptDate = o.ReceiptDate.Format(dateFormat)
	}
	if !o.PlannedReceipt.IsZero() {
		out.PlannedReceiptDate = o.PlannedReceipt.Format(dateFormat)
	}
	for _, l := range o.Lines {
		out.Lines = append(out.Lines, transferLineResponse{ID: l.ID, LineNo: l.LineNo, ProductID: l.ProductID, Dimensions: l.Dims,
			Unit: l.Unit, Qty: l.Qty.String(), ShippedQty: l.ShippedQty.String(), ReceivedQty: l.ReceivedQty.String(),
			InTransit: l.InTransit().String(), UnderdeliveryPct: l.UnderPct.String(), OverdeliveryPct: l.OverPct.String(),
			UnshippedQty: l.UnshippedQty().String(), Closed: l.Closed})
	}
	return out
}

func writeTransfer(w http.ResponseWriter, status int, o domain.TransferOrder) {
	w.Header().Set("ETag", strconv.Quote(strconv.Itoa(o.Version)))
	httpx.JSON(w, status, toTransferResponse(o))
}

type transferLineItem struct {
	OrderID            string            `json:"orderId"`
	LegalEntityID      string            `json:"legalEntityId"`
	Number             string            `json:"number"`
	OrderStatus        string            `json:"orderStatus"`
	FromWarehouseID    string            `json:"fromWarehouseId"`
	ToWarehouseID      string            `json:"toWarehouseId"`
	ShipDate           string            `json:"shipDate"`
	PlannedReceiptDate string            `json:"plannedReceiptDate,omitempty"`
	LineID             string            `json:"lineId"`
	LineNo             int               `json:"lineNo"`
	ProductID          string            `json:"productId"`
	Dimensions         map[string]string `json:"dimensions"`
	Unit               string            `json:"unit"`
	Qty                string            `json:"qty"`
	ShippedQty         string            `json:"shippedQty"`
	ReceivedQty        string            `json:"receivedQty"`
	RemainingQty       string            `json:"remainingQty"`
	InTransit          string            `json:"inTransit"`
	DaysInTransit      int               `json:"daysInTransit"`
	Closed             bool              `json:"closed"`
	Late               string            `json:"late,omitempty"`
	DaysLate           int               `json:"daysLate"`
}

// listTransferLines answers the transfer order line inquiry and late overview (FR-16.15, 3.16.14).
func (a *API) listTransferLines(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	q := r.URL.Query()
	f := store.TransferLineFilter{LegalEntityID: strings.TrimSpace(q.Get("legalEntityId")), Status: strings.TrimSpace(q.Get("status")),
		ProductID: strings.TrimSpace(q.Get("productId")), FromWarehouse: strings.TrimSpace(q.Get("fromWarehouseId")),
		ToWarehouse: strings.TrimSpace(q.Get("toWarehouseId")), LateReceiptDays: domain.DefaultLateReceiptDays,
		AsOf: time.Now().UTC().Truncate(24 * time.Hour)}
	fields := map[string]string{}
	if f.LegalEntityID != "" && !ids.Valid(f.LegalEntityID) {
		fields["legalEntityId"] = "must be a UUID"
	}
	if f.Status != "" && !isTransferStatus(f.Status) {
		fields["status"] = "unknown transfer order status"
	}
	if f.ProductID != "" && !ids.Valid(f.ProductID) {
		fields["productId"] = "must be a UUID"
	}
	if raw := q.Get("late"); raw != "" {
		late, err := strconv.ParseBool(raw)
		if err != nil {
			fields["late"] = "must be true or false"
		}
		f.LateOnly = late
	}
	if raw := q.Get("lateReceiptDays"); raw != "" {
		days, err := strconv.Atoi(raw)
		if err != nil || days < 0 || days > 3650 {
			fields["lateReceiptDays"] = "must be a whole number of days from 0 to 3650"
		} else {
			f.LateReceiptDays = days
		}
	}
	if raw := q.Get("asOf"); raw != "" {
		if d, err := time.Parse(dateFormat, raw); err != nil {
			fields["asOf"] = "must be a date (YYYY-MM-DD)"
		} else {
			f.AsOf = d
		}
	}
	for name, dst := range map[string]**time.Time{"from": &f.From, "to": &f.To} {
		if raw := q.Get(name); raw != "" {
			d, err := time.Parse(dateFormat, raw)
			if err != nil {
				fields[name] = "must be a date (YYYY-MM-DD)"
				continue
			}
			*dst = &d
		}
	}
	if f.From != nil && f.To != nil && f.To.Before(*f.From) {
		fields["to"] = "must not be before from"
	}
	if len(fields) > 0 {
		fieldProblem(w, r, fields)
		return
	}
	rows, truncated, err := a.store.ListTransferLines(r.Context(), f)
	if err != nil {
		a.failed(w, r, "list transfer order lines", err)
		return
	}
	items := make([]transferLineItem, 0, len(rows))
	for _, row := range rows {
		l := row.Line
		late, daysLate := domain.TransferLineLateness(row.Status, l, row.ShipDate, row.PlannedReceipt, f.AsOf, f.LateReceiptDays)
		inTransit := l.InTransit()
		daysInTransit := 0
		if inTransit.IsPositive() {
			daysInTransit = domain.InTransitDays(row.ShipDate, f.AsOf)
		}
		planned := ""
		if !row.PlannedReceipt.IsZero() {
			planned = row.PlannedReceipt.Format(dateFormat)
		}
		items = append(items, transferLineItem{PlannedReceiptDate: planned, OrderID: row.OrderID, LegalEntityID: row.LegalEntityID, Number: row.Number, OrderStatus: row.Status,
			FromWarehouseID: row.FromWarehouse, ToWarehouseID: row.ToWarehouse, ShipDate: row.ShipDate.Format(dateFormat), LineID: l.ID,
			LineNo: l.LineNo, ProductID: l.ProductID, Dimensions: l.Dims, Unit: l.Unit, Qty: l.Qty.String(), ShippedQty: l.ShippedQty.String(),
			ReceivedQty: l.ReceivedQty.String(), RemainingQty: domain.TransferLineRemaining(row.Status, l).String(), InTransit: inTransit.String(),
			DaysInTransit: daysInTransit, Closed: l.Closed, Late: late, DaysLate: daysLate})
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items, "truncated": truncated})
}
