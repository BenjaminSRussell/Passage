package api

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/warehouse/internal/domain"
	"github.com/BenjaminSRussell/passage/services/warehouse/internal/store"
)

// Duties of receiving and dock scheduling: the PRD's WMS_RECEIVE and
// WMS_SHIP_CONFIRM (§6.31.17).
const (
	dutyReceive     = "warehouse.receipt.register"
	dutyShipConfirm = "warehouse.shipment.confirm"
)

const (
	maxDoorCode     = 20
	maxReference    = 100
	maxASNLines     = 500
	maxAppointments = 500
	qtyScale        = 6
)

// Error codes of receiving and dock scheduling (README "Error codes").
const (
	codeDoorNotFound    = "WMS-DOOR-NOT-FOUND"
	codeReceiptNotFound = "WMS-RECEIPT-NOT-FOUND"
)

// registerReceiving mounts the dock and receiving routes inside the
// tenant-checked group of registerPersisted.
func (a *API) registerReceiving(r chi.Router) {
	r.With(authz.RequireDuty(dutySetupMaintain)).Post("/v1/warehouse/dock-doors", a.createDoor)
	r.With(authz.RequireDuty(dutyInquiry)).Get("/v1/warehouse/dock-doors", a.listDoors)
	r.With(authz.Require(anyDuty{dutyReceive, dutyShipConfirm}, dutyReceive+" or "+dutyShipConfirm, "")).
		Post("/v1/warehouse/dock-appointments", a.createAppointment)
	r.With(authz.RequireDuty(dutyInquiry)).Get("/v1/warehouse/dock-appointments", a.listAppointments)
	r.With(authz.RequireDuty(dutyReceive)).Post("/v1/warehouse/receipts:from-asn", a.registerASN)
	r.With(authz.RequireDuty(dutyInquiry)).Get("/v1/warehouse/receipts", a.listReceipts)
	r.With(authz.RequireDuty(dutyInquiry)).Get("/v1/warehouse/receipts/{id}", a.getReceipt)
	r.With(authz.RequireDuty(dutyReceive)).Post("/v1/warehouse/receipts/{id}/lines/{n}:receive", a.receiveLine)
}

// anyDuty allows a principal holding at least one of its duties. Booking a
// door needs the duty of the appointment's direction, which the handler checks
// once the body is read; this keeps callers with neither out before that.
type anyDuty []string

func (d anyDuty) Allow(_ context.Context, p tenant.Principal, _, _ string) (bool, error) {
	for _, duty := range d {
		if p.HasDuty(duty) {
			return true, nil
		}
	}
	return false, nil
}

// fineScale reports whether d has at most places decimals, so a numeric column
// stores it without rounding.
func fineScale(d decimal.Decimal, places int32) bool {
	return d.Exponent() >= -places || d.Equal(d.Round(places))
}

type doorInput struct {
	WarehouseID string `json:"warehouseId"`
	Code        string `json:"code"`
}

func (a *API) createDoor(w http.ResponseWriter, r *http.Request) {
	var in doorInput
	fields := map[string]string{}
	idem, err := command(r, &in, fields)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if !ids.Valid(in.WarehouseID) {
		fields["warehouseId"] = "must be a UUID"
	}
	code := strings.TrimSpace(in.Code)
	if code == "" || len(code) > maxDoorCode {
		fields["code"] = fmt.Sprintf("is required, at most %d characters", maxDoorCode)
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	out, replayed, err := a.st.CreateDoor(r.Context(), idem, domain.DockDoor{ID: ids.New(), WarehouseID: strings.ToLower(in.WarehouseID), Code: code})
	if err != nil {
		fail(w, r, err, codeDoorNotFound)
		return
	}
	answer(w, http.StatusCreated, replayed, out)
}

func (a *API) listDoors(w http.ResponseWriter, r *http.Request) {
	wh := r.URL.Query().Get("warehouseId")
	if !ids.Valid(wh) {
		validationFailed(w, r, map[string]string{"warehouseId": "must be a UUID"})
		return
	}
	out, err := a.st.ListDoors(r.Context(), wh)
	if err != nil {
		fail(w, r, err, codeDoorNotFound)
		return
	}
	if out == nil {
		out = []domain.DockDoor{}
	}
	httpx.JSON(w, http.StatusOK, page[domain.DockDoor]{Items: out})
}

type appointmentInput struct {
	WarehouseID string                      `json:"warehouseId"`
	DoorID      string                      `json:"doorId"`
	Direction   domain.AppointmentDirection `json:"direction"`
	Reference   string                      `json:"reference"`
	Start       time.Time                   `json:"start"`
	End         time.Time                   `json:"end"`
}

// createAppointment books a door window (FR-31.8): INBOUND needs the receive
// duty, OUTBOUND the ship-confirm duty. An overlap is WMS-0108 (409).
func (a *API) createAppointment(w http.ResponseWriter, r *http.Request) {
	var in appointmentInput
	fields := map[string]string{}
	idem, err := command(r, &in, fields)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if !ids.Valid(in.WarehouseID) {
		fields["warehouseId"] = "must be a UUID"
	}
	if !ids.Valid(in.DoorID) {
		fields["doorId"] = "must be a UUID"
	}
	var need string
	switch in.Direction {
	case domain.Inbound:
		need = dutyReceive
	case domain.Outbound:
		need = dutyShipConfirm
	default:
		fields["direction"] = "must be INBOUND or OUTBOUND"
	}
	ref := strings.TrimSpace(in.Reference)
	if ref == "" || len(ref) > maxReference {
		fields["reference"] = fmt.Sprintf("is required (the ASN, shipment or load), at most %d characters", maxReference)
	}
	switch {
	case in.Start.IsZero():
		fields["start"] = "is required (RFC 3339)"
	case !domain.OnSlotGrid(in.Start):
		fields["start"] = "must be on the 15-minute grid"
	}
	switch {
	case in.End.IsZero():
		fields["end"] = "is required (RFC 3339)"
	case !domain.OnSlotGrid(in.End):
		fields["end"] = "must be on the 15-minute grid"
	case !in.Start.IsZero() && !in.End.After(in.Start):
		fields["end"] = "must be after start"
	case !in.Start.IsZero() && in.End.Sub(in.Start) > domain.MaxAppointment:
		fields["end"] = "must be at most 24 hours after start"
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	if p, _ := tenant.FromContext(r.Context()); !p.HasDuty(need) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusForbidden, "AUTH-0003", "missing duty "+need))
		return
	}
	out, replayed, err := a.st.CreateAppointment(r.Context(), idem, domain.Appointment{ID: ids.New(), WarehouseID: strings.ToLower(in.WarehouseID),
		DoorID: strings.ToLower(in.DoorID), Direction: in.Direction, Reference: ref, Start: in.Start.UTC(), End: in.End.UTC()})
	if err != nil {
		fail(w, r, err, codeDoorNotFound)
		return
	}
	answer(w, http.StatusCreated, replayed, out)
}

// timeParam reads an optional RFC 3339 query parameter.
func timeParam(r *http.Request, name string, fields map[string]string) time.Time {
	raw := r.URL.Query().Get(name)
	if raw == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		fields[name] = "must be an RFC 3339 time"
	}
	return t.UTC()
}

// listAppointments returns a warehouse's appointments overlapping [from, to),
// earliest first, at most 500.
func (a *API) listAppointments(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	fields := map[string]string{}
	if !ids.Valid(q.Get("warehouseId")) {
		fields["warehouseId"] = "must be a UUID"
	}
	if door := q.Get("doorId"); door != "" && !ids.Valid(door) {
		fields["doorId"] = "must be a UUID"
	}
	from, to := timeParam(r, "from", fields), timeParam(r, "to", fields)
	if !from.IsZero() && !to.IsZero() && !to.After(from) {
		fields["to"] = "must be after from"
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	out, err := a.st.ListAppointments(r.Context(), store.AppointmentFilter{WarehouseID: q.Get("warehouseId"), DoorID: q.Get("doorId"),
		From: from, To: to, Limit: maxAppointments})
	if err != nil {
		fail(w, r, err, codeDoorNotFound)
		return
	}
	if out == nil {
		out = []domain.Appointment{}
	}
	httpx.JSON(w, http.StatusOK, page[domain.Appointment]{Items: out})
}

type asnLineInput struct {
	LineNo      int             `json:"lineNo"`
	ItemID      string          `json:"itemId"`
	LPID        string          `json:"lpId"`
	POLineRef   string          `json:"poLineRef"`
	ExpectedQty decimal.Decimal `json:"expectedQty"`
	Weight      decimal.Decimal `json:"weight"`
	Volume      decimal.Decimal `json:"volume"`
}

type asnInput struct {
	WarehouseID  string           `json:"warehouseId"`
	ASNRef       string           `json:"asnRef"`
	TolerancePct *decimal.Decimal `json:"tolerancePct"`
	Lines        []asnLineInput   `json:"lines"`
}

// registerASN stores an ASN packing structure (FR-31.9) as an OPEN receipt.
// The PRD keys the command by the ASN: a client may send the ASN reference as
// Idempotency-Key, and a second receipt for the ASN under another key is
// WMS-DUPLICATE.
func (a *API) registerASN(w http.ResponseWriter, r *http.Request) {
	var in asnInput
	fields := map[string]string{}
	idem, err := command(r, &in, fields)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if !ids.Valid(in.WarehouseID) {
		fields["warehouseId"] = "must be a UUID"
	}
	ref := strings.TrimSpace(in.ASNRef)
	if ref == "" || len(ref) > maxReference {
		fields["asnRef"] = fmt.Sprintf("is required, at most %d characters", maxReference)
	}
	tol := domain.DefaultASNTolerance()
	if in.TolerancePct != nil {
		tol = *in.TolerancePct
		if tol.IsNegative() || tol.GreaterThan(decimal.NewFromInt(domain.MaxASNTolerancePct)) || !fineScale(tol, 2) {
			fields["tolerancePct"] = fmt.Sprintf("must be 0 to %d with at most 2 decimals", domain.MaxASNTolerancePct)
		}
	}
	if n := len(in.Lines); n == 0 || n > maxASNLines {
		fields["lines"] = fmt.Sprintf("must hold 1 to %d pallets", maxASNLines)
	}
	lines := make([]domain.ReceiptLine, 0, len(in.Lines))
	seenLine, seenLP := map[int]bool{}, map[string]bool{}
	for i, l := range in.Lines {
		p := fmt.Sprintf("lines[%d]", i)
		if l.LineNo < 1 {
			fields[p+".lineNo"] = "must be a positive line number"
		} else if seenLine[l.LineNo] {
			fields[p+".lineNo"] = "is used by another pallet in this ASN"
		}
		seenLine[l.LineNo] = true
		item := strings.TrimSpace(l.ItemID)
		if item == "" || len(item) > maxTextLength {
			fields[p+".itemId"] = fmt.Sprintf("is required, at most %d characters", maxTextLength)
		}
		lp := strings.TrimSpace(l.LPID)
		switch {
		case !domain.ValidLPID(lp):
			fields[p+".lpId"] = fmt.Sprintf("must be 1 to %d letters, digits, '.', '_' or '-'", domain.MaxLPIDLength)
		case seenLP[lp]:
			fields[p+".lpId"] = "is used by another pallet in this ASN"
		}
		seenLP[lp] = true
		po := strings.TrimSpace(l.POLineRef)
		if len(po) > maxReference {
			fields[p+".poLineRef"] = fmt.Sprintf("must be at most %d characters", maxReference)
		}
		if !l.ExpectedQty.IsPositive() || !fineScale(l.ExpectedQty, qtyScale) {
			fields[p+".expectedQty"] = "must be greater than zero, at most 6 decimals"
		}
		if l.Weight.IsNegative() || !fineScale(l.Weight, qtyScale) {
			fields[p+".weight"] = "must not be negative, at most 6 decimals"
		}
		if l.Volume.IsNegative() || !fineScale(l.Volume, qtyScale) {
			fields[p+".volume"] = "must not be negative, at most 6 decimals"
		}
		lines = append(lines, domain.ReceiptLine{LineNo: l.LineNo, ItemID: item, LPID: lp, POLineRef: po,
			ExpectedQty: l.ExpectedQty, Weight: l.Weight, Volume: l.Volume})
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	out, replayed, err := a.st.RegisterASN(r.Context(), idem, domain.Receipt{ID: ids.New(), WarehouseID: strings.ToLower(in.WarehouseID),
		ASNRef: ref, TolerancePct: tol, Lines: lines})
	if err != nil {
		fail(w, r, err, codeReceiptNotFound)
		return
	}
	answer(w, http.StatusCreated, replayed, out)
}

var receiptStatuses = []domain.ReceiptStatus{domain.ReceiptOpen, domain.ReceiptPartial, domain.ReceiptCompleted}

func (a *API) listReceipts(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	fields := map[string]string{}
	if !ids.Valid(q.Get("warehouseId")) {
		fields["warehouseId"] = "must be a UUID"
	}
	if s := q.Get("status"); s != "" {
		known := false
		for _, st := range receiptStatuses {
			known = known || string(st) == s
		}
		if !known {
			fields["status"] = "must be OPEN, PARTIALLY_RECEIVED or RECEIVED"
		}
	}
	limit := pageLimit(r, fields)
	after, ok := decodeCursor(q.Get("cursor"))
	if !ok {
		fields["cursor"] = "is invalid"
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	out, err := a.st.ListReceipts(r.Context(), store.ReceiptFilter{WarehouseID: q.Get("warehouseId"), Status: q.Get("status"), AfterASN: after, Limit: limit})
	if err != nil {
		fail(w, r, err, codeReceiptNotFound)
		return
	}
	httpx.JSON(w, http.StatusOK, newPage(out, limit, func(s domain.ReceiptSummary) string { return s.ASNRef }))
}

func (a *API) getReceipt(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !ids.Valid(id) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, codeReceiptNotFound, "not found"))
		return
	}
	out, err := a.st.GetReceipt(r.Context(), id)
	if err != nil {
		fail(w, r, err, codeReceiptNotFound)
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}

type receiveInput struct {
	Qty decimal.Decimal `json:"qty"`
}

// receiveLine receives one ASN pallet with the quantity that arrived and
// creates its put-away work (FR-31.9). A variance beyond tolerance is a 200
// with a WMS-0107 warning, never an error (BR-31.10).
func (a *API) receiveLine(w http.ResponseWriter, r *http.Request) {
	var in receiveInput
	fields := map[string]string{}
	idem, err := command(r, &in, fields)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	id := chi.URLParam(r, "id")
	if !ids.Valid(id) {
		fields["id"] = "must be a UUID"
	}
	n, convErr := strconv.Atoi(chi.URLParam(r, "n"))
	if convErr != nil || n < 1 {
		fields["n"] = "must be a positive line number"
	}
	if !in.Qty.IsPositive() || !fineScale(in.Qty, qtyScale) {
		fields["qty"] = "must be greater than zero, at most 6 decimals"
	}
	version := ifMatchVersion(r, fields, "receipt")
	who := user(r, fields)
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	out, replayed, err := a.st.ReceiveLine(r.Context(), idem, store.ReceiveInput{ReceiptID: id, LineNo: n, Qty: in.Qty, User: who,
		IfMatch: version, Now: time.Now()})
	if err != nil {
		fail(w, r, err, codeReceiptNotFound)
		return
	}
	if !replayed {
		a.workCreated.Add(1)
		if out.Work.Status == domain.WorkBlockedNoLocation {
			a.workBlocked.Add(1)
		}
	}
	answer(w, http.StatusOK, replayed, out)
}
