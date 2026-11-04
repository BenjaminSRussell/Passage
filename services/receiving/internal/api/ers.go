package api

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5/middleware"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/money"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/domain"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/store"
)

const (
	ersPageSize  = 200
	ersScanPages = 5
	priceScale   = 6

	skipNotPosted       = "RECEIPT_NOT_POSTED"
	skipNotErs          = "NOT_ERS_VENDOR"
	skipNoOrder         = "PO_NOT_FOUND"
	skipOrderDenied     = "PO_FORBIDDEN"
	skipOrderOpen       = "PO_NOT_APPROVED"
	skipNoPOLine        = "NO_PO_LINE"
	skipManyPOLines     = "AMBIGUOUS_PO_LINE"
	skipPlanSettled     = "PLAN_SETTLED"
	skipPartial         = "PARTIALLY_INVOICED"
	skipPriceChange     = "PRICE_CHANGED"
	skipSelfConfirmed   = "PRICE_CONFIRMED_BY_RUNNER"
	skipTaxUndefined    = "TAX_NOT_DETERMINED"
	skipPartyIncomplete = "PARTY_DATA_INCOMPLETE"
)

type ersRunRequest struct {
	LegalEntityID   string `json:"legalEntityId"`
	VendorID        string `json:"vendorId"`
	PurchaseOrderID string `json:"purchaseOrderId"`
	PostingDate     string `json:"postingDate"`
	LedgerID        string `json:"ledgerId"`
	FiscalPeriodID  string `json:"fiscalPeriodId"`
}

func (q *ersRunRequest) common() settlementRunRequest {
	return settlementRunRequest{PurchaseOrderID: q.PurchaseOrderID, LegalEntityID: q.LegalEntityID, VendorID: q.VendorID, PostingDate: q.PostingDate, LedgerID: q.LedgerID, FiscalPeriodID: q.FiscalPeriodID}
}

type ersSettledBody struct {
	InvoiceID     string `json:"invoiceId"`
	InvoiceNumber string `json:"invoiceNumber"`
	ReceiptID     string `json:"receiptId"`
	ReceiptNumber string `json:"receiptNumber"`
	VendorID      string `json:"vendorId"`
	Currency      string `json:"currency"`
	Total         string `json:"total"`
	Status        string `json:"status"`
	LedgerEntryID string `json:"ledgerEntryId"`
}

type ersRunBody struct {
	Settled []ersSettledBody `json:"settled"`
	Skipped []skippedBody    `json:"skipped"`
	HasMore bool             `json:"hasMore"`
}

type ersOrder struct {
	po   domain.PurchaseOrder
	skip string
}

type ersRun struct {
	api       *API
	ctx       context.Context
	principal tenant.Principal
	base      settlementRunBase
	orders    map[string]ersOrder
	vendors   map[string]bool
	groups    groupMemo
	body      ersRunBody
}

func (r *ersRun) full() bool { return len(r.body.Settled) >= maxSettlementsPerRun }

func (r *ersRun) skip(ref, reason string) {
	r.body.Skipped = append(r.body.Skipped, skippedBody{SourceRef: ref, Reason: reason})
}

func (r *ersRun) skipWithDetail(ref, reason, detail string) {
	r.body.Skipped = append(r.body.Skipped, skippedBody{SourceRef: ref, Reason: reason, Detail: detail})
}

func samePrice(a, b decimal.Decimal) bool { return a.Round(priceScale).Equal(b.Round(priceScale)) }

func (r *ersRun) order(id string) (ersOrder, error) {
	if found, ok := r.orders[id]; ok {
		return found, nil
	}
	if r.api.ports.PurchaseOrders == nil {
		return ersOrder{}, unavailable("purchase order lookup is not configured")
	}
	po, err := r.api.ports.PurchaseOrders.Get(r.ctx, r.base.legalEntityID, id)
	out := ersOrder{po: po}
	switch {
	case errors.Is(err, domain.ErrPurchaseOrderNotFound):
		out.skip = skipNoOrder
	case errors.Is(err, domain.ErrPurchaseOrderForbidden):
		out.skip = skipOrderDenied
	case err != nil:
		slog.ErrorContext(r.ctx, "purchase order lookup failed", "err", err, "purchaseOrderId", id, "correlationId", middleware.GetReqID(r.ctx))
		return ersOrder{}, unavailable("purchase order lookup unavailable")
	case !po.Usable():
		out.skip = skipOrderOpen
	}
	r.orders[id] = out
	return out, nil
}

func (r *ersRun) ersVendor(vendorID string) (bool, error) {
	if enabled, ok := r.vendors[vendorID]; ok {
		return enabled, nil
	}
	enabled, err := r.api.store.ErsVendor(r.ctx, r.base.legalEntityID, vendorID)
	if err == nil {
		r.vendors[vendorID] = enabled
	}
	return enabled, err
}

func matchPOLine(po domain.PurchaseOrder, productID string, poLineNo int) (domain.PurchaseOrderLine, string) {
	if poLineNo > 0 {
		for _, l := range po.Lines {
			if l.Usable() && l.LineNo == poLineNo {
				return l, ""
			}
		}
		return domain.PurchaseOrderLine{}, skipNoPOLine
	}
	var found []domain.PurchaseOrderLine
	for _, l := range po.Lines {
		if l.Usable() && strings.EqualFold(l.ProductID, productID) {
			found = append(found, l)
		}
	}
	switch len(found) {
	case 0:
		return domain.PurchaseOrderLine{}, skipNoPOLine
	case 1:
		return found[0], ""
	}
	return domain.PurchaseOrderLine{}, skipManyPOLines
}

func (r *ersRun) invoiceFor(rec store.Receipt, po domain.PurchaseOrder, rejected int, confirmed map[int]store.PriceConfirmation) (store.Invoice, string, string) {
	number := ersInvoiceNumber(rec)
	if rejected > 0 {
		number = fmt.Sprintf("%s-R%d", number, rejected)
	}
	in := store.Invoice{
		ID: stableID(r.principal.TenantID, rec.VendorID, strings.ToLower(number)), LegalEntityID: rec.LegalEntityID, VendorID: rec.VendorID, InvoiceNumber: number,
		InvoiceDate: r.base.postingDate, PostingDate: r.base.postingDate, Currency: rec.Currency, LedgerID: r.base.ledgerID, FiscalPeriodID: r.base.fiscalPeriodID,
		Source: domain.InvoiceSourceERS,
	}
	open, invoiced := 0, 0
	for _, l := range rec.Lines {
		if l.InvoicedQty.IsPositive() {
			invoiced++
		} else {
			open++
		}
	}
	if open > 0 && invoiced > 0 {
		return store.Invoice{}, skipPartial, ""
	}
	for _, l := range rec.Lines {
		if l.InvoicedQty.IsPositive() {
			continue
		}
		poLine, reason := matchPOLine(po, l.ProductID, l.POLineNo)
		if reason != "" {
			return store.Invoice{}, reason, ""
		}
		price := poLine.NetUnitPrice()
		confirmation, ok := confirmed[l.LineNo]
		if !samePrice(l.UnitPrice, price) && ok && samePrice(confirmation.Price, price) && store.SameActor(confirmation.By, r.principal.UserID) {
			return store.Invoice{}, skipSelfConfirmed, fmt.Sprintf("AP-ERS-004: the price on line %d was confirmed by the user running the settlement", l.LineNo)
		}
		if !samePrice(l.UnitPrice, price) && (!ok || !samePrice(confirmation.Price, price)) {
			return store.Invoice{}, skipPriceChange, fmt.Sprintf("AP-ERS-001: PO price changed since receipt (%s -> %s) on line %d", l.UnitPrice.StringFixed(priceScale), price.Round(priceScale).StringFixed(priceScale), l.LineNo)
		}
		in.Lines = append(in.Lines, store.InvoiceLine{
			ReceiptID: rec.ID, ReceiptLineNo: l.LineNo, Qty: l.Qty, UnitPrice: price, Amount: l.Qty.Mul(price).RoundBank(money.MinorUnits(rec.Currency)),
		})
	}
	if len(in.Lines) == 0 {
		return store.Invoice{}, skipAlreadyInvoiced, ""
	}
	if rejected > 0 {
		in.AuditReason = auditReasonReissue
	}
	return in, "", ""
}

func (r *ersRun) receipt(receiptID string) error {
	rec, err := r.api.store.GetReceipt(r.ctx, receiptID)
	if err != nil {
		return err
	}
	ref := rec.Number
	switch {
	case rec.State != domain.Posted:
		r.skip(ref, skipNotPosted)
		return nil
	case rec.LegalEntityID != r.base.legalEntityID:
		r.skip(ref, skipOtherEntity)
		return nil
	}
	enabled, err := r.ersVendor(rec.VendorID)
	if err != nil {
		return err
	}
	if !enabled {
		r.skip(ref, skipNotErs)
		return nil
	}
	order, err := r.order(rec.PurchaseOrderID)
	if err != nil {
		return err
	}
	switch {
	case order.skip != "":
		r.skip(ref, order.skip)
		return nil
	case order.po.Currency != rec.Currency:
		r.skip(ref, skipUnusableCurrency)
		return nil
	case !strings.EqualFold(order.po.VendorID, rec.VendorID):
		r.skip(ref, skipOtherVendor)
		return nil
	}
	rejected, err := r.api.store.RejectedInvoiceCount(r.ctx, rec.VendorID, ersInvoiceNumber(rec))
	if err != nil {
		return err
	}
	confirmed, err := r.api.store.ErsPriceConfirmations(r.ctx, rec.ID)
	if err != nil {
		return err
	}
	in, reason, detail := r.invoiceFor(rec, order.po, rejected, confirmed)
	if reason != "" {
		r.skipWithDetail(ref, reason, detail)
		return nil
	}
	posted, err := r.api.submitInvoice(r.ctx, r.groups, r.principal, in)
	if errors.Is(err, domain.ErrTaxNotDetermined) {
		r.skip(ref, skipTaxUndefined)
		return nil
	}
	if errors.Is(err, domain.ErrSelfBillPartyIncomplete) {
		r.skipWithDetail(ref, skipPartyIncomplete, err.Error())
		return nil
	}
	var domainErr *domain.Error
	if errors.As(err, &domainErr) {
		reasons := map[string]string{domain.CodeDuplicateInvoice: skipAlreadySettled, domain.CodePlanSettled: skipPlanSettled, domain.CodeOverInvoice: skipLineInvoiced}
		if reason, known := reasons[domainErr.Code]; known {
			r.skip(ref, reason)
			return nil
		}
	}
	if err != nil {
		return err
	}
	r.api.ersSettled.Add(1)
	r.body.Settled = append(r.body.Settled, ersSettledBody{
		InvoiceID: posted.ID, InvoiceNumber: posted.InvoiceNumber, ReceiptID: rec.ID, ReceiptNumber: rec.Number, VendorID: posted.VendorID, Currency: posted.Currency,
		Total: posted.Total.StringFixed(money.MinorUnits(posted.Currency)), Status: posted.Status, LedgerEntryID: posted.LedgerEntryID,
	})
	return nil
}

func (a *API) runErsSettlement(w http.ResponseWriter, r *http.Request) {
	p, ok := principalWithUser(w, r)
	if !ok {
		return
	}
	start, req, ok := a.startRun(w, r, &ersRunRequest{}, false)
	if !ok {
		return
	}
	fields := map[string]string{}
	if req.VendorID != "" {
		checkUUID(fields, "vendorId", req.VendorID)
	}
	if req.PurchaseOrderID != "" {
		checkUUID(fields, "purchaseOrderId", req.PurchaseOrderID)
	}
	if req.VendorID == "" && req.PurchaseOrderID == "" {
		fields["vendorId"] = "vendorId or purchaseOrderId is required"
	}
	if len(fields) > 0 {
		httpx.Fail(w, r, validationProblem(fields))
		return
	}
	run := &ersRun{
		api: a, ctx: r.Context(), principal: p, base: start.req, orders: map[string]ersOrder{}, vendors: map[string]bool{}, groups: groupMemo{},
		body: ersRunBody{Settled: []ersSettledBody{}, Skipped: []skippedBody{}},
	}
	if err := run.scan(req); err != nil {
		writeError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, run.body)
}

func (r *ersRun) scan(req settlementRunRequest) error {
	seen := map[string]struct{}{}
	for _, status := range []string{"OPEN", "PARTIAL"} {
		more, err := r.scanStatus(req, status, seen)
		if err != nil {
			return err
		}
		if more {
			r.body.HasMore = true
			return nil
		}
	}
	return nil
}

func (r *ersRun) scanStatus(req settlementRunRequest, status string, seen map[string]struct{}) (bool, error) {
	filter := store.GrirFilter{VendorID: req.VendorID, PurchaseOrderID: req.PurchaseOrderID, Status: status, Limit: ersPageSize}
	for page := 0; page < ersScanPages; page++ {
		got, err := r.api.store.ListGrir(r.ctx, filter)
		if err != nil {
			return false, err
		}
		items := got.Items
		more := len(items) > filter.Limit
		if more {
			items = items[:filter.Limit]
		}
		for _, item := range items {
			if _, done := seen[item.ReceiptID]; done {
				continue
			}
			if r.full() {
				return true, nil
			}
			seen[item.ReceiptID] = struct{}{}
			if err := r.item(item, status); err != nil {
				return false, err
			}
		}
		if !more {
			return false, nil
		}
		last := items[len(items)-1]
		filter.CursorCreatedAt, filter.CursorID = last.CreatedAt, last.ID
	}
	return true, nil
}

func (r *ersRun) item(item store.GrirItem, status string) error {
	if status == "OPEN" {
		return r.receipt(item.ReceiptID)
	}
	enabled, err := r.ersVendor(item.VendorID)
	if err != nil {
		return err
	}
	if enabled {
		r.skip(item.ReceiptNumber, skipPartial)
	}
	return nil
}
