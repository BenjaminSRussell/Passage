package api

import (
	"context"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/events"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/domain"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/store"
)

const (
	dutyInvoiceAuditRelease = "InvoiceAuditRelease"
	dutyErsPriceConfirm     = "ErsPriceConfirm"
)

type auditPolicyRequest struct {
	LegalEntityID string        `json:"legalEntityId"`
	SampleRatePct *fieldDecimal `json:"sampleRatePct"`

	ThresholdAmount   *fieldDecimal `json:"thresholdAmount"`
	ThresholdCurrency string        `json:"thresholdCurrency"`
}

type auditPolicyBody struct {
	LegalEntityID string `json:"legalEntityId"`
	SampleRatePct string `json:"sampleRatePct"`

	ThresholdAmount   string `json:"thresholdAmount,omitempty"`
	ThresholdCurrency string `json:"thresholdCurrency,omitempty"`
	RuleVersion       int    `json:"ruleVersion"`
}

type ersVendorRequest struct {
	LegalEntityID string `json:"legalEntityId"`
	Enabled       *bool  `json:"enabled"`
}

type ersVendorBody struct {
	LegalEntityID string `json:"legalEntityId"`
	VendorID      string `json:"vendorId"`
	Enabled       bool   `json:"enabled"`
}

type releaseRequest struct {
	LegalEntityID string `json:"legalEntityId"`
}

type rejectRequest struct {
	LegalEntityID string `json:"legalEntityId"`
	Reason        string `json:"reason"`
}

const maxRejectReason = 500

func principalWithUser(w http.ResponseWriter, r *http.Request) (tenant.Principal, bool) {
	p, ok := tenant.FromContext(r.Context())
	if !ok || !ids.Valid(p.TenantID) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusUnauthorized, "AUTH-0001", "missing or malformed tenant"))
		return p, false
	}
	if p.UserID == "" {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusUnauthorized, "AUTH-0001", "the request needs an authenticated user so the change can be audited"))
		return p, false
	}
	return p, true
}

func (a *API) setAuditPolicy(w http.ResponseWriter, r *http.Request) {
	p, ok := principalWithUser(w, r)
	if !ok {
		return
	}
	var req auditPolicyRequest
	if err := httpx.Decode(r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	fields := map[string]string{}
	checkUUID(fields, "legalEntityId", req.LegalEntityID)
	rate, present := decimalField(fields, "sampleRatePct", req.SampleRatePct, true)
	if present && !domain.ValidSampleRate(rate) {
		fields["sampleRatePct"] = "must be from 0 to 100 with at most two decimals"
	}
	policy := domain.AuditPolicy{RatePct: rate}
	if threshold, given := decimalField(fields, "thresholdAmount", req.ThresholdAmount, false); given {
		if !threshold.IsPositive() {
			fields["thresholdAmount"] = "must be greater than zero"
		}
		if len(req.ThresholdCurrency) != 3 || !isUpperAlpha(req.ThresholdCurrency) {
			fields["thresholdCurrency"] = "is required with thresholdAmount as a 3-letter upper-case ISO 4217 code"
		}
		policy.Threshold, policy.ThresholdCurrency = threshold, req.ThresholdCurrency
	} else if req.ThresholdCurrency != "" {
		fields["thresholdAmount"] = "is required with thresholdCurrency"
	}
	if len(fields) > 0 {
		httpx.Fail(w, r, validationProblem(fields))
		return
	}
	if err := inScope(p, req.LegalEntityID); err != nil {
		writeError(w, r, err)
		return
	}
	var saved domain.AuditPolicy
	err := a.store.SetAuditPolicy(r.Context(), req.LegalEntityID, policy, p.UserID, func(old, next domain.AuditPolicy) (events.Event, error) {
		saved = next
		return auditPolicyChangedEvent(p.TenantID, req.LegalEntityID, old, next, p.UserID)
	})
	if err != nil {
		writeError(w, r, err)
		return
	}
	body := auditPolicyBody{LegalEntityID: req.LegalEntityID, SampleRatePct: saved.RatePct.StringFixed(2), RuleVersion: saved.RuleVersion}
	if saved.Threshold.IsPositive() {
		body.ThresholdAmount, body.ThresholdCurrency = saved.Threshold.String(), saved.ThresholdCurrency
	}
	httpx.JSON(w, http.StatusOK, body)
}

func (a *API) setErsVendor(w http.ResponseWriter, r *http.Request) {
	p, ok := principalWithUser(w, r)
	if !ok {
		return
	}
	var req ersVendorRequest
	if err := httpx.Decode(r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	vendorID := chi.URLParam(r, "vendorId")
	fields := map[string]string{}
	checkUUID(fields, "vendorId", vendorID)
	checkUUID(fields, "legalEntityId", req.LegalEntityID)
	if req.Enabled == nil {
		fields["enabled"] = "is required"
	}
	if len(fields) > 0 {
		httpx.Fail(w, r, validationProblem(fields))
		return
	}
	if err := inScope(p, req.LegalEntityID); err != nil {
		writeError(w, r, err)
		return
	}
	err := a.store.SetErsVendor(r.Context(), req.LegalEntityID, vendorID, *req.Enabled, p.UserID, func(old bool) (events.Event, error) {
		return ersVendorChangedEvent(p.TenantID, req.LegalEntityID, vendorID, old, *req.Enabled, p.UserID)
	})
	if err != nil {
		writeError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, ersVendorBody{LegalEntityID: req.LegalEntityID, VendorID: vendorID, Enabled: *req.Enabled})
}

func (a *API) releaseAuditBlock(w http.ResponseWriter, r *http.Request) {
	p, ok := principalWithUser(w, r)
	if !ok {
		return
	}
	var req releaseRequest
	if err := httpx.Decode(r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	id := chi.URLParam(r, "id")
	fields := map[string]string{}
	checkUUID(fields, "id", id)
	checkUUID(fields, "legalEntityId", req.LegalEntityID)
	if len(fields) > 0 {
		httpx.Fail(w, r, validationProblem(fields))
		return
	}
	if err := inScope(p, req.LegalEntityID); err != nil {
		writeError(w, r, err)
		return
	}
	ctx := r.Context()
	vendorID, err := a.store.InvoiceVendor(ctx, id)
	if err != nil {
		writeError(w, r, err)
		return
	}
	group := a.vendorGroup(ctx, nil, id, req.LegalEntityID, vendorID)
	released, err := a.store.ReleaseInvoice(ctx, id, req.LegalEntityID, p.UserID, func(inv store.Invoice) (store.InvoiceOutcome, error) {
		var parties domain.SelfBillParties
		if inv.Source == domain.InvoiceSourceERS {
			var err error
			if parties, err = a.selfBillParties(ctx, inv); err != nil {
				return store.InvoiceOutcome{}, err
			}
		}
		lines := make([]domain.HeldLine, len(inv.Lines))
		subjects := []string{inv.ID}
		for i, l := range inv.Lines {
			lines[i] = domain.HeldLine{Qty: l.Qty, UnitPrice: l.UnitPrice, StandardCost: l.StandardCost, Service: l.Service, Clearing: l.GrirCleared}
			subjects = append(subjects, l.ReceiptID)
		}
		entry, err := domain.PostHeldInvoice(ctx, domain.VendorInvoice{
			ID: inv.ID, LegalEntityID: inv.LegalEntityID, LedgerID: inv.LedgerID, FiscalPeriodID: inv.FiscalPeriodID,
			PostingDate: inv.PostingDate, InvoiceDate: inv.InvoiceDate, Currency: inv.Currency, TaxAmount: inv.TaxAmount, TaxCode: inv.TaxCode, VendorGroupCode: group.Code,
		}, lines, a.ports.Periods, a.ledgerFor(subjects...), a.ports.Profile)
		if err != nil {
			return store.InvoiceOutcome{}, err
		}
		total := entry.Lines[len(entry.Lines)-1].Amount.Value.Neg()
		evt, err := invoicePostedEvent(p.TenantID, inv, group, entry.LedgerEntryID, total, entry.GrirCleared)
		outcome := store.InvoiceOutcome{LedgerEntryID: entry.LedgerEntryID, Total: total, Event: evt}
		if inv.Source == domain.InvoiceSourceERS {
			outcome.SelfBill = a.selfBillBuild(p.TenantID, inv, parties)
		}
		return outcome, err
	})
	if err != nil {
		writeError(w, r, err)
		return
	}
	a.pendingPostings.finish(invoiceSubjects(released)...)
	a.auditReleased.Add(1)
	a.invoiced.Add(1)
	httpx.JSON(w, http.StatusOK, toInvoiceBody(released))
}

func (a *API) rejectAuditBlock(w http.ResponseWriter, r *http.Request) {
	p, ok := principalWithUser(w, r)
	if !ok {
		return
	}
	var req rejectRequest
	if err := httpx.Decode(r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	id := chi.URLParam(r, "id")
	reason := strings.TrimSpace(req.Reason)
	fields := map[string]string{}
	checkUUID(fields, "id", id)
	checkUUID(fields, "legalEntityId", req.LegalEntityID)
	checkLength(fields, "reason", reason, 1, maxRejectReason)
	if len(fields) > 0 {
		httpx.Fail(w, r, validationProblem(fields))
		return
	}
	if err := inScope(p, req.LegalEntityID); err != nil {
		writeError(w, r, err)
		return
	}
	rejected, err := a.store.RejectInvoice(r.Context(), id, req.LegalEntityID, p.UserID, reason, func(inv store.Invoice) (events.Event, error) {
		return invoiceRejectedEvent(p.TenantID, inv, reason, p.UserID)
	})
	if err != nil {
		writeError(w, r, err)
		return
	}
	a.auditRejected.Add(1)
	httpx.JSON(w, http.StatusOK, toInvoiceBody(rejected))
}

type ersPriceRequest struct {
	LegalEntityID string        `json:"legalEntityId"`
	UnitPrice     *fieldDecimal `json:"unitPrice"`
}

type ersPriceBody struct {
	ReceiptID string `json:"receiptId"`
	LineNo    int    `json:"lineNo"`
	UnitPrice string `json:"unitPrice"`
}

func (a *API) confirmErsPrice(w http.ResponseWriter, r *http.Request) {
	p, ok := principalWithUser(w, r)
	if !ok {
		return
	}
	var req ersPriceRequest
	if err := httpx.Decode(r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	id := chi.URLParam(r, "id")
	lineNo, lineErr := strconv.Atoi(chi.URLParam(r, "lineNo"))
	fields := map[string]string{}
	checkUUID(fields, "id", id)
	checkUUID(fields, "legalEntityId", req.LegalEntityID)
	if lineErr != nil || lineNo < 1 {
		fields["lineNo"] = "must be at least 1"
	}
	price, present := decimalField(fields, "unitPrice", req.UnitPrice, true)
	if present && price.IsNegative() {
		fields["unitPrice"] = "must not be negative"
	}
	if len(fields) > 0 {
		httpx.Fail(w, r, validationProblem(fields))
		return
	}
	if err := inScope(p, req.LegalEntityID); err != nil {
		writeError(w, r, err)
		return
	}
	ctx := r.Context()
	rec, err := a.store.GetReceipt(ctx, id)
	if err == nil && rec.LegalEntityID != req.LegalEntityID {
		err = store.ErrNotFound
	}
	if err != nil {
		writeError(w, r, err)
		return
	}
	poPrice, err := a.ersConfirmablePrice(ctx, rec, lineNo, p.UserID)
	if err != nil {
		writeError(w, r, err)
		return
	}
	if !samePrice(price, poPrice) {
		writeError(w, r, poProblem(http.StatusConflict, "AP-ERS-002", "unitPrice %s differs from the current purchase order price %s", price.String(), poPrice.Round(priceScale).String()))
		return
	}
	err = a.store.ConfirmErsPrice(ctx, req.LegalEntityID, id, lineNo, price, p.UserID, func(old decimal.NullDecimal) (events.Event, error) {
		return ersPriceConfirmedEvent(p.TenantID, rec, lineNo, old, price, p.UserID)
	})
	if err != nil {
		writeError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, ersPriceBody{ReceiptID: id, LineNo: lineNo, UnitPrice: price.String()})
}

func (a *API) ersConfirmablePrice(ctx context.Context, rec store.Receipt, lineNo int, by string) (decimal.Decimal, error) {
	idx := slices.IndexFunc(rec.Lines, func(l store.Line) bool { return l.LineNo == lineNo })
	switch {
	case idx < 0:
		return decimal.Zero, store.ErrNotFound
	case rec.State != domain.Posted:
		return decimal.Zero, poProblem(http.StatusConflict, "AP-ERS-003", "only a posted receipt can have its price confirmed, this one is %s", rec.State)
	case strings.TrimSpace(rec.PostedBy) == "" || store.SameActor(rec.PostedBy, by):
		return decimal.Zero, poProblem(http.StatusForbidden, "AP-ERS-004", "the user who posted a receipt cannot confirm its ERS price (and a receipt without a recorded poster cannot be confirmed)")
	}
	enabled, err := a.store.ErsVendor(ctx, rec.LegalEntityID, rec.VendorID)
	if err != nil {
		return decimal.Zero, err
	}
	if !enabled {
		return decimal.Zero, poProblem(http.StatusConflict, "AP-ERS-003", "the vendor is not enabled for evaluated receipt settlement")
	}
	po, err := a.loadPurchaseOrder(ctx, rec.LegalEntityID, rec.PurchaseOrderID)
	if err != nil {
		return decimal.Zero, err
	}
	line := rec.Lines[idx]
	var poLine domain.PurchaseOrderLine
	if line.POLineNo > 0 {
		at := slices.IndexFunc(po.Lines, func(l domain.PurchaseOrderLine) bool { return l.LineNo == line.POLineNo && l.Usable() })
		if at < 0 {
			return decimal.Zero, poProblem(http.StatusConflict, "AP-ERS-003", "purchase order line %d is gone or cancelled", line.POLineNo)
		}
		poLine = po.Lines[at]
	} else {
		var reason string
		if poLine, reason = matchPOLine(po, line.ProductID, 0); reason != "" {
			return decimal.Zero, poProblem(http.StatusConflict, "AP-ERS-003", "the receipt line has no single purchase order line (%s)", reason)
		}
	}
	return poLine.NetUnitPrice(), nil
}
