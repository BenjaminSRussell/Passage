package api

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/money"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/domain"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/store"
)

const (
	maxBatch     = 128
	maxSerial    = 64
	maxSerials   = 1000
	inventoryOK  = "AVAILABLE"
	etagFormat   = `"%d"`
	cancelKind   = "CANCELLATION"
	correctKind  = "CORRECTION"
	missingMatch = "If-Match header is required"
)

const (
	maxCWQty = 1_000_000
	// qtyScale is the numeric(20,6) bound of receiving_receipt_line.qty, for
	// every received quantity (a larger or finer value was a 500 or silently
	// rounded before).
	qtyScale = "must have at most 6 decimals and 14 integer digits"
)

var inventoryStatuses = []string{inventoryOK, "QUARANTINE", "BLOCKED"}

type catchWeightSpec struct {
	Nominal *fieldDecimal `json:"nominal"`
	Min     *fieldDecimal `json:"min"`
	Max     *fieldDecimal `json:"max"`
}

type receivedFields struct {
	Qty             *fieldDecimal    `json:"qty"`
	CWQty           *int             `json:"cwQty"`
	CatchWeight     *catchWeightSpec `json:"catchWeight"`
	Batch           string           `json:"batch"`
	Serials         []string         `json:"serials"`
	LocationID      string           `json:"locationId"`
	InventoryStatus string           `json:"inventoryStatus"`
}

type receivedLine struct {
	LineNo int `json:"lineNo"`
	receivedFields
}

type registerRequest struct {
	Lines []receivedLine `json:"lines"`
}

type directLine struct {
	arrivalLine
	receivedFields
}

type directReceiptRequest struct {
	arrivalRequest
	Lines []directLine `json:"lines"`
}

type deltaLine struct {
	LineNo   int           `json:"lineNo"`
	QtyDelta *fieldDecimal `json:"qtyDelta"`
	// CWQtyDelta corrects the case count of a catch-weight line together with
	// its weight (qtyDelta); BR-24.5 is checked on the corrected pair.
	CWQtyDelta *int `json:"cwQtyDelta"`
}

type correctRequest struct {
	PostingDate string      `json:"postingDate"`
	Lines       []deltaLine `json:"lines"`
}

type cancelRequest struct {
	PostingDate string `json:"postingDate"`
}

type registerInput struct {
	Qty             decimal.Decimal
	CWQty           int
	CatchWeight     domain.CatchWeight
	Batch           string
	Serials         []string
	LocationID      string
	InventoryStatus string
}

func validationProblem(fields map[string]string) *httpx.Problem {
	problem := httpx.NewProblem(http.StatusBadRequest, "COM-0001", "request failed validation")
	problem.Errors = fields
	return problem
}

func inScope(p tenant.Principal, legalEntityID string) error {
	if tenant.InLegalEntityScope(p, legalEntityID) {
		return nil
	}
	return httpx.NewProblem(http.StatusForbidden, "AUTH-0003", "legal entity is outside the caller's scope")
}

func parseCatchWeight(fields map[string]string, name string, spec *catchWeightSpec) domain.CatchWeight {
	nom, okN := decimalField(fields, name+".nominal", spec.Nominal, true)
	minv, okMin := decimalField(fields, name+".min", spec.Min, true)
	maxv, okMax := decimalField(fields, name+".max", spec.Max, true)
	out := domain.CatchWeight{Nominal: nom, Min: minv, Max: maxv}
	if okN && okMin && okMax {
		if err := out.Validate(); err != nil {
			fields[name] = err.(*domain.Error).Detail
		}
	}
	return out
}

func parseReceived(fields map[string]string, prefix string, f receivedFields) registerInput {
	in := registerInput{Batch: f.Batch, Serials: f.Serials, LocationID: f.LocationID, InventoryStatus: f.InventoryStatus}
	if v, ok := decimalField(fields, prefix+"qty", f.Qty, true); ok {
		if v.Sign() <= 0 {
			fields[prefix+"qty"] = "must be greater than zero"
		} else if !domain.FitsColumn(v) {
			fields[prefix+"qty"] = qtyScale
		}
		in.Qty = v
	}
	if f.CWQty != nil {
		if *f.CWQty < 1 || *f.CWQty > maxCWQty {
			fields[prefix+"cwQty"] = fmt.Sprintf("must be a whole number from 1 to %d", maxCWQty)
		} else {
			in.CWQty = *f.CWQty
		}
	}
	if f.CatchWeight != nil {
		in.CatchWeight = parseCatchWeight(fields, prefix+"catchWeight", f.CatchWeight)
	}
	if (f.CWQty != nil) != (f.CatchWeight != nil) {
		fields[prefix+"cwQty"] = "catchWeight and cwQty must be sent together"
	}
	checkUUID(fields, prefix+"locationId", f.LocationID)
	if utf8.RuneCountInString(f.Batch) > maxBatch {
		fields[prefix+"batch"] = fmt.Sprintf("must be at most %d characters", maxBatch)
	}
	if in.InventoryStatus == "" {
		in.InventoryStatus = inventoryOK
	}
	if !slices.Contains(inventoryStatuses, in.InventoryStatus) {
		fields[prefix+"inventoryStatus"] = "must be AVAILABLE, QUARANTINE or BLOCKED"
	}
	if len(f.Serials) > maxSerials {
		fields[prefix+"serials"] = fmt.Sprintf("must hold at most %d serials", maxSerials)
	}
	for i, s := range f.Serials {
		if n := utf8.RuneCountInString(s); n < 1 || n > maxSerial {
			fields[fmt.Sprintf("%sserials[%d]", prefix, i)] = fmt.Sprintf("must be 1 to %d characters", maxSerial)
		}
	}
	return in
}

func versionMismatch(have, want int) error {
	return httpx.NewProblem(http.StatusPreconditionFailed, "COM-0412", fmt.Sprintf("receipt is at version %d, If-Match carried %d", have, want))
}

func checkVersion(have, want int) error {
	if want > 0 && have != want {
		return versionMismatch(have, want)
	}
	return nil
}

func ifMatch(r *http.Request) (int, error) {
	raw := strings.Trim(strings.TrimSpace(r.Header.Get("If-Match")), `"`)
	if raw == "" {
		return 0, httpx.NewProblem(http.StatusPreconditionRequired, "COM-0428", missingMatch)
	}
	v, err := strconv.Atoi(raw)
	if err != nil || v < 1 {
		return 0, validationProblem(map[string]string{"If-Match": "must carry the receipt version number"})
	}
	return v, nil
}

func toDomain(r store.Receipt) domain.Receipt {
	out := domain.Receipt{ID: r.ID, LegalEntityID: r.LegalEntityID, Date: r.Date, State: r.State, LedgerEntryID: r.LedgerEntryID}
	for _, l := range r.Lines {
		out.Lines = append(out.Lines, domain.ReceiptLine{
			LineNo: l.LineNo, ProductID: l.ProductID, Qty: l.Qty, InvoicedQty: l.InvoicedQty, ClearedAmount: l.ClearedAmount, OrderedRemaining: l.OrderedRemaining,
			OverDeliveryPct: l.OverDeliveryPct, UnitPrice: money.Amount{Value: l.UnitPrice, Currency: l.Currency},
			BatchTracked: l.BatchTracked, SerialTracked: l.SerialTracked, Tracking: domain.Tracking{Batch: l.Batch, Serials: l.Serials},
			CWQty: l.CWQty, CatchWeight: domain.CatchWeight{Nominal: l.CWNominal, Min: l.CWMin, Max: l.CWMax},
		})
	}
	return out
}

func registerFn(inputs map[int]registerInput, version int, poQty map[int]decimal.Decimal) store.RegisterFunc {
	return func(r store.Receipt, recorded map[domain.SerialKey]struct{}) ([]store.Line, error) {
		if err := checkVersion(r.Version, version); err != nil {
			return nil, err
		}
		if len(inputs) != len(r.Lines) {
			return nil, &domain.Error{Code: domain.CodeInvalidReceipt, Detail: fmt.Sprintf("receipt %s has %d lines and the request carries %d", r.ID, len(r.Lines), len(inputs))}
		}
		dr := toDomain(r)
		lines := slices.Clone(r.Lines)
		for i := range lines {
			in, ok := inputs[lines[i].LineNo]
			if !ok {
				return nil, &domain.Error{Code: domain.CodeInvalidReceipt, Detail: fmt.Sprintf("line %d has no received quantity", lines[i].LineNo)}
			}
			if in.CWQty > 0 && !domain.IsWeightUnit(lines[i].Unit) {
				// Also without a PO line: qty is a weight, so the line's unit must be one.
				return nil, &domain.Error{Code: domain.CodeCatchWeightUnit, Detail: fmt.Sprintf("line %d is received in %q; a catch-weight line is received and priced per mass unit", lines[i].LineNo, lines[i].Unit)}
			}
			if lines[i].POLineNo > 0 {
				ordered, known := poQty[lines[i].LineNo]
				if !known {
					return nil, &domain.Error{Code: domain.CodeInvalidReceipt, Detail: fmt.Sprintf("line %d has no purchase order quantity to check against", lines[i].LineNo)}
				}
				if err := checkOpenQuantity(r.Lines[i], in.Qty, ordered); err != nil {
					return nil, err
				}
			}
			dr.Lines[i].Qty, dr.Lines[i].Tracking = in.Qty, domain.Tracking{Batch: in.Batch, Serials: in.Serials}
			dr.Lines[i].CWQty, dr.Lines[i].CatchWeight = in.CWQty, in.CatchWeight
			lines[i].Qty, lines[i].Batch, lines[i].Serials, lines[i].LocationID, lines[i].InventoryStatus = in.Qty, in.Batch, in.Serials, in.LocationID, in.InventoryStatus
			lines[i].CWQty, lines[i].CWNominal, lines[i].CWMin, lines[i].CWMax = in.CWQty, in.CatchWeight.Nominal, in.CatchWeight.Min, in.CatchWeight.Max
		}
		if _, err := dr.Register(recorded); err != nil {
			return nil, err
		}
		return lines, nil
	}
}

func (a *API) directReceipt(w http.ResponseWriter, r *http.Request) {
	p, ok := tenant.FromContext(r.Context())
	if !ok || !ids.Valid(p.TenantID) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusUnauthorized, "AUTH-0001", "missing or malformed tenant"))
		return
	}
	var req directReceiptRequest
	if err := httpx.Decode(r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	arrival := req.arrivalRequest
	arrival.Lines = make([]arrivalLine, len(req.Lines))
	for i, l := range req.Lines {
		arrival.Lines[i] = l.arrivalLine
	}
	in, fields := buildArrival(arrival)
	a.requirePOLine(in, fields)
	inputs := map[int]registerInput{}
	for i, l := range req.Lines {
		inputs[i+1] = parseReceived(fields, fmt.Sprintf("lines[%d].", i), l.receivedFields)
	}
	if len(fields) > 0 {
		httpx.Fail(w, r, validationProblem(fields))
		return
	}
	if err := inScope(p, req.LegalEntityID); err != nil {
		writeError(w, r, err)
		return
	}
	poQty, err := a.bindPurchaseOrder(r.Context(), &in)
	if err != nil {
		writeError(w, r, err)
		return
	}
	created, err := a.store.DirectReceipt(r.Context(), in, registerFn(inputs, 0, poQty))
	if err != nil {
		writeError(w, r, err)
		return
	}
	a.registered.Add(1)
	w.Header().Set("Location", "/v1/receiving/receipts/"+created.ID)
	writeReceipt(w, http.StatusCreated, created)
}

func writeReceipt(w http.ResponseWriter, status int, rec store.Receipt) {
	w.Header().Set("ETag", fmt.Sprintf(etagFormat, rec.Version))
	httpx.JSON(w, status, toReceiptBody(rec))
}

type command struct {
	principal tenant.Principal
	id        string
	version   int
}

func (a *API) command(w http.ResponseWriter, r *http.Request) (command, bool) {
	p, ok := tenant.FromContext(r.Context())
	if !ok || !ids.Valid(p.TenantID) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusUnauthorized, "AUTH-0001", "missing or malformed tenant"))
		return command{}, false
	}
	id := chi.URLParam(r, "id")
	if !ids.Valid(id) {
		httpx.Fail(w, r, validationProblem(map[string]string{"id": "must be a UUID"}))
		return command{}, false
	}
	version, err := ifMatch(r)
	if err != nil {
		writeError(w, r, err)
		return command{}, false
	}
	return command{principal: p, id: id, version: version}, true
}

func (a *API) registerReceipt(w http.ResponseWriter, r *http.Request) {
	cmd, ok := a.command(w, r)
	if !ok {
		return
	}
	var req registerRequest
	if err := httpx.Decode(r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	fields := map[string]string{}
	inputs := map[int]registerInput{}
	if len(req.Lines) == 0 || len(req.Lines) > maxLines {
		fields["lines"] = fmt.Sprintf("must hold 1 to %d lines", maxLines)
	}
	for i, l := range req.Lines {
		prefix := fmt.Sprintf("lines[%d].", i)
		if l.LineNo < 1 {
			fields[prefix+"lineNo"] = "must be at least 1"
		}
		if _, dup := inputs[l.LineNo]; dup {
			fields[prefix+"lineNo"] = "appears twice"
		}
		inputs[l.LineNo] = parseReceived(fields, prefix, l.receivedFields)
	}
	if len(fields) > 0 {
		httpx.Fail(w, r, validationProblem(fields))
		return
	}
	arrived, err := a.store.GetReceipt(r.Context(), cmd.id)
	if err != nil {
		writeError(w, r, err)
		return
	}
	poQty, err := a.registrationQuantities(r.Context(), arrived)
	if err != nil {
		writeError(w, r, err)
		return
	}
	registered, err := a.store.Register(r.Context(), cmd.id, registerFn(inputs, cmd.version, poQty))
	if err != nil {
		writeError(w, r, err)
		return
	}
	a.registered.Add(1)
	writeReceipt(w, http.StatusOK, registered)
}

func (a *API) postReceipt(w http.ResponseWriter, r *http.Request) {
	cmd, ok := a.command(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	posted, err := a.store.Post(ctx, cmd.id, func(rec store.Receipt) (store.PostOutcome, error) {
		if err := checkVersion(rec.Version, cmd.version); err != nil {
			return store.PostOutcome{}, err
		}
		next, items, err := toDomain(rec).Post(ctx, a.ledgerFor(rec.ID), a.ports.Profile)
		if err != nil {
			return store.PostOutcome{}, err
		}
		amounts := make([]store.LineAmount, len(items))
		for i, item := range items {
			amounts[i] = store.LineAmount{LineNo: item.LineNo, Amount: item.ReceivedAmount.Value, AmountAcct: acct(item.ReceivedAmount.Value, rec.ExchangeRate)}
		}
		evt, err := receiptPostedEvent(cmd.principal.TenantID, rec, amounts, next.LedgerEntryID)
		return store.PostOutcome{PostingDate: rec.Date, LedgerEntryID: next.LedgerEntryID, Amounts: amounts, Event: evt}, err
	})
	if err != nil {
		writeError(w, r, err)
		return
	}
	a.posted.Add(1)
	a.pendingPostings.finish(cmd.id)
	writeReceipt(w, http.StatusOK, posted)
}

func (a *API) correctReceipt(w http.ResponseWriter, r *http.Request) {
	cmd, ok := a.command(w, r)
	if !ok {
		return
	}
	var req correctRequest
	if err := httpx.Decode(r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	fields := map[string]string{}
	deltas := map[int]decimal.Decimal{}
	caseDeltas := map[int]int{}
	if len(req.Lines) == 0 || len(req.Lines) > maxLines {
		fields["lines"] = fmt.Sprintf("must hold 1 to %d lines", maxLines)
	}
	for i, l := range req.Lines {
		prefix := fmt.Sprintf("lines[%d].", i)
		if l.LineNo < 1 {
			fields[prefix+"lineNo"] = "must be at least 1"
		}
		if _, dup := deltas[l.LineNo]; dup {
			fields[prefix+"lineNo"] = "appears twice"
		}
		if v, ok := decimalField(fields, prefix+"qtyDelta", l.QtyDelta, true); ok {
			if v.IsZero() {
				fields[prefix+"qtyDelta"] = "must not be zero"
			} else if !domain.FitsColumn(v) {
				fields[prefix+"qtyDelta"] = qtyScale
			}
			deltas[l.LineNo] = v
		}
		if l.CWQtyDelta != nil {
			if d := *l.CWQtyDelta; d == 0 || d < -maxCWQty || d > maxCWQty {
				fields[prefix+"cwQtyDelta"] = fmt.Sprintf("must be a non-zero whole number from -%d to %d", maxCWQty, maxCWQty)
			} else {
				caseDeltas[l.LineNo] = d
			}
		}
	}
	if len(fields) > 0 {
		httpx.Fail(w, r, validationProblem(fields))
		return
	}
	postingDate := optionalDate(fields, "postingDate", req.PostingDate)
	if len(fields) > 0 {
		httpx.Fail(w, r, validationProblem(fields))
		return
	}
	ctx := r.Context()
	poQty, err := a.correctionQuantities(ctx, cmd.id, deltas)
	if err != nil {
		writeError(w, r, err)
		return
	}
	corrected, err := a.store.Correct(ctx, cmd.id, func(rec store.Receipt) (store.Adjustment, error) {
		if err := checkVersion(rec.Version, cmd.version); err != nil {
			return store.Adjustment{}, err
		}
		for _, l := range rec.Lines {
			if delta := deltas[l.LineNo]; l.POLineNo > 0 && delta.IsPositive() {
				ordered, known := poQty[l.LineNo]
				if !known {
					return store.Adjustment{}, &domain.Error{Code: domain.CodeInvalidReceipt, Detail: fmt.Sprintf("line %d has no purchase order quantity to check against", l.LineNo)}
				}
				if err := checkOpenQuantity(l, l.Qty.Add(delta), ordered); err != nil {
					return store.Adjustment{}, err
				}
			}
		}
		seq := rec.CorrectionSeq + 1
		dr, err := toDomain(rec).WithCaseDeltas(caseDeltas)
		if err != nil {
			return store.Adjustment{}, err
		}
		entryID, changes, err := dr.Correct(ctx, a.ledgerFor(rec.ID), a.ports.Profile, seq, postingDate, deltas)
		if err != nil {
			return store.Adjustment{}, err
		}
		adj := store.Adjustment{LedgerEntryID: entryID}
		var eventLines []correctedLineData
		for _, c := range changes {
			line := rec.Lines[slices.IndexFunc(rec.Lines, func(l store.Line) bool { return l.LineNo == c.LineNo })]
			amountDelta := c.Amount.Value.Sub(line.Amount.Decimal)
			adj.Lines = append(adj.Lines, store.LineChange{LineNo: c.LineNo, Qty: c.Qty, CWQty: c.CWQty, Amount: c.Amount.Value, AmountAcct: acct(c.Amount.Value, rec.ExchangeRate)})
			eventLines = append(eventLines, correctedLineData{
				LineNo: c.LineNo, POLineID: line.POLineID, ProductID: line.ProductID, QtyDelta: c.Qty.Sub(line.Qty).String(), CWQtyDelta: c.CWQty - line.CWQty,
				AmountDelta: amountDelta.String(), AmountAcctDelta: acct(amountDelta, rec.ExchangeRate).String(),
			})
		}
		evt, err := receiptCorrectedEvent(cmd.principal.TenantID, correctKind, rec, seq, entryID, eventLines)
		adj.Event = &evt
		return adj, err
	})
	if err != nil {
		writeError(w, r, err)
		return
	}
	a.adjusted.Add(1)
	a.pendingPostings.finish(cmd.id)
	writeReceipt(w, http.StatusOK, corrected)
}

func (a *API) cancelReceipt(w http.ResponseWriter, r *http.Request) {
	cmd, ok := a.command(w, r)
	if !ok {
		return
	}
	var req cancelRequest
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		httpx.Fail(w, r, validationProblem(map[string]string{"body": "unreadable"}))
		return
	}
	if len(bytes.TrimSpace(raw)) > 0 {
		r.Body = io.NopCloser(bytes.NewReader(raw))
		if err := httpx.Decode(r, &req); err != nil {
			writeError(w, r, err)
			return
		}
	}
	fields := map[string]string{}
	postingDate := optionalDate(fields, "postingDate", req.PostingDate)
	if len(fields) > 0 {
		httpx.Fail(w, r, validationProblem(fields))
		return
	}
	ctx := r.Context()
	cancelled, err := a.store.Cancel(ctx, cmd.id, func(rec store.Receipt) (store.Adjustment, error) {
		if err := checkVersion(rec.Version, cmd.version); err != nil {
			return store.Adjustment{}, err
		}
		if rec.State != domain.Posted {
			if err := a.store.EnsureNoPending(ctx, rec.ID); err != nil {
				return store.Adjustment{}, err
			}
		}
		entryID, err := toDomain(rec).Cancel(ctx, a.ledgerFor(rec.ID), a.ports.Profile, postingDate)
		if err != nil || rec.State != domain.Posted {
			return store.Adjustment{}, err
		}
		eventLines := make([]correctedLineData, len(rec.Lines))
		for i, l := range rec.Lines {
			eventLines[i] = correctedLineData{
				LineNo: l.LineNo, POLineID: l.POLineID, ProductID: l.ProductID, QtyDelta: l.Qty.Neg().String(),
				AmountDelta: l.Amount.Decimal.Neg().String(), AmountAcctDelta: l.AmountAcct.Decimal.Neg().String(),
			}
		}
		evt, err := receiptCorrectedEvent(cmd.principal.TenantID, cancelKind, rec, rec.CorrectionSeq, entryID, eventLines)
		return store.Adjustment{LedgerEntryID: entryID, Event: &evt}, err
	})
	if err != nil {
		writeError(w, r, err)
		return
	}
	a.adjusted.Add(1)
	a.pendingPostings.finish(cmd.id)
	writeReceipt(w, http.StatusOK, cancelled)
}

func (a *API) getReceipt(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !ids.Valid(id) {
		httpx.Fail(w, r, validationProblem(map[string]string{"id": "must be a UUID"}))
		return
	}
	rec, err := a.store.GetReceipt(r.Context(), id)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeReceipt(w, http.StatusOK, rec)
}
