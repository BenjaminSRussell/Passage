package api

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/go-chi/chi/v5/middleware"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/money"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/domain"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/store"
)

const (
	maxSettlementsPerRun = 100

	skipAwaitsReceipt    = "AWAITS_RECEIPT"
	skipLineCancelled    = "LINE_CANCELLED"
	skipAlreadyInvoiced  = "ALREADY_INVOICED"
	skipUnknownRowStatus = "UNKNOWN_ROW_STATUS"
	skipLineInvoiced     = "LINE_INVOICED"
	skipNoProduct        = "NO_PRODUCT"
	skipAlreadySettled   = "ALREADY_SETTLED"
	skipOtherVendor      = "OTHER_VENDOR"
	skipOtherEntity      = "OTHER_LEGAL_ENTITY"
	skipNoAmount         = "NO_AMOUNT"
	skipUnusableCurrency = "CURRENCY_MISMATCH"
)

type planRunRequest struct {
	PurchaseOrderID string `json:"purchaseOrderId"`
	LegalEntityID   string `json:"legalEntityId"`
	PostingDate     string `json:"postingDate"`
	LedgerID        string `json:"ledgerId"`
	FiscalPeriodID  string `json:"fiscalPeriodId"`
}

type consignmentRunRequest struct {
	LegalEntityID  string `json:"legalEntityId"`
	VendorID       string `json:"vendorId"`
	PostingDate    string `json:"postingDate"`
	LedgerID       string `json:"ledgerId"`
	FiscalPeriodID string `json:"fiscalPeriodId"`
}

type settlementRunRequest struct {
	PurchaseOrderID string
	LegalEntityID   string
	VendorID        string
	PostingDate     string
	LedgerID        string
	FiscalPeriodID  string
}

type runRequestBody interface {
	common() settlementRunRequest
}

func (q *planRunRequest) common() settlementRunRequest {
	return settlementRunRequest{PurchaseOrderID: q.PurchaseOrderID, LegalEntityID: q.LegalEntityID, PostingDate: q.PostingDate, LedgerID: q.LedgerID, FiscalPeriodID: q.FiscalPeriodID}
}

func (q *consignmentRunRequest) common() settlementRunRequest {
	return settlementRunRequest{LegalEntityID: q.LegalEntityID, VendorID: q.VendorID, PostingDate: q.PostingDate, LedgerID: q.LedgerID, FiscalPeriodID: q.FiscalPeriodID}
}

type planLink struct {
	purchaseOrderID string
	lineNo          int
	seq             int
	productID       string
}

type settlementBody struct {
	ID             string `json:"id"`
	Kind           string `json:"kind"`
	SourceRef      string `json:"sourceRef"`
	LegalEntityID  string `json:"legalEntityId"`
	VendorID       string `json:"vendorId"`
	PostingDate    string `json:"postingDate"`
	Currency       string `json:"currency"`
	Amount         string `json:"amount"`
	LedgerID       string `json:"ledgerId"`
	FiscalPeriodID string `json:"fiscalPeriodId"`
	LedgerEntryID  string `json:"ledgerEntryId"`
}

type skippedBody struct {
	SourceRef string `json:"sourceRef"`
	Reason    string `json:"reason"`
	Detail    string `json:"detail,omitempty"`
}

type settlementRunBody struct {
	Settled []settlementBody `json:"settled"`
	Skipped []skippedBody    `json:"skipped"`
	HasMore bool             `json:"hasMore"`
}

type settlementRun struct {
	api      *API
	ctx      context.Context
	tenantID string
	req      settlementRunBase
	body     settlementRunBody

	purchasingDown bool
}

type settlementRunBase struct {
	legalEntityID  string
	ledgerID       string
	fiscalPeriodID string
	postingDate    time.Time
}

func (r *settlementRun) full() bool { return len(r.body.Settled) >= maxSettlementsPerRun }

func (r *settlementRun) skip(sourceRef, reason string) {
	r.body.Skipped = append(r.body.Skipped, skippedBody{SourceRef: sourceRef, Reason: reason})
}

func (r *settlementRun) settle(kind, sourceRef, vendorID, currency, debitAccount string, documentDate time.Time, amount decimal.Decimal, link planLink) error {
	s := domain.Settlement{
		ID: stableID(r.tenantID, "settlement", kind, sourceRef), Kind: kind, LegalEntityID: r.req.legalEntityID, LedgerID: r.req.ledgerID,
		FiscalPeriodID: r.req.fiscalPeriodID, PostingDate: r.req.postingDate, DocumentDate: documentDate, Currency: currency, DebitAccount: debitAccount, Amount: amount,
	}
	in := store.Settlement{
		ID: s.ID, Kind: kind, SourceRef: sourceRef, LegalEntityID: s.LegalEntityID, VendorID: vendorID, Currency: currency, Amount: amount,
		PostingDate: s.PostingDate, LedgerID: s.LedgerID, FiscalPeriodID: s.FiscalPeriodID,
		PurchaseOrderID: link.purchaseOrderID, POLineNo: link.lineNo, ProductID: link.productID,
	}
	posted, replayed, err := r.api.store.PostSettlement(r.ctx, in, func() (store.SettlementOutcome, error) {
		entry, err := domain.PostSettlement(r.ctx, s, r.api.ports.Periods, r.api.ledgerFor(s.ID), r.api.ports.Profile)
		if err != nil {
			return store.SettlementOutcome{}, err
		}
		evt, err := settlementPostedEvent(r.tenantID, in, entry.LedgerEntryID)
		return store.SettlementOutcome{LedgerEntryID: entry.LedgerEntryID, Event: evt}, err
	})
	if errors.Is(err, store.ErrLineInvoiced) {
		r.skip(sourceRef, skipLineInvoiced)
		return nil
	}
	if err != nil {
		return err
	}
	if replayed {
		r.markInvoiced(link, posted.ID, posted.Amount)
		r.skip(sourceRef, skipAlreadySettled)
		return nil
	}
	r.api.pendingPostings.finish(s.ID)
	r.markInvoiced(link, posted.ID, posted.Amount)
	r.api.settlements.Add(1)
	scale := money.MinorUnits(currency)
	r.body.Settled = append(r.body.Settled, settlementBody{
		ID: posted.ID, Kind: kind, SourceRef: sourceRef, LegalEntityID: posted.LegalEntityID, VendorID: vendorID, PostingDate: posted.PostingDate.Format(dateLayout),
		Currency: currency, Amount: amount.StringFixed(scale), LedgerID: posted.LedgerID, FiscalPeriodID: posted.FiscalPeriodID, LedgerEntryID: posted.LedgerEntryID,
	})
	return nil
}

func (r *settlementRun) markInvoiced(link planLink, settlementID string, amount decimal.Decimal) {
	if link.seq == 0 || r.api.ports.PlanRowSettlements == nil {
		return
	}
	if r.purchasingDown {
		r.api.planMarkFailed.Add(1)
		slog.WarnContext(r.ctx, "plan row settled but purchasing not told: purchasing was unreachable earlier in this run", "purchaseOrderId", link.purchaseOrderID,
			"lineNo", link.lineNo, "seq", link.seq, "settlementId", settlementID, "correlationId", middleware.GetReqID(r.ctx))
		return
	}
	err := r.api.ports.PlanRowSettlements.MarkInvoiced(r.ctx, r.req.legalEntityID, link.purchaseOrderID, link.lineNo, link.seq, settlementID, amount)
	if err == nil {
		return
	}
	r.api.planMarkFailed.Add(1)
	r.purchasingDown = !errors.Is(err, domain.ErrPlanRowMarkRejected)
	slog.ErrorContext(r.ctx, "plan row settled but purchasing not told; a later plan run retries", "err", err, "purchaseOrderId", link.purchaseOrderID,
		"lineNo", link.lineNo, "seq", link.seq, "settlementId", settlementID, "correlationId", middleware.GetReqID(r.ctx))
}

func (a *API) startRun(w http.ResponseWriter, r *http.Request, dst runRequestBody, needPurchaseOrder bool) (*settlementRun, settlementRunRequest, bool) {
	p, ok := tenant.FromContext(r.Context())
	if !ok || !ids.Valid(p.TenantID) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusUnauthorized, "AUTH-0001", "missing or malformed tenant"))
		return nil, settlementRunRequest{}, false
	}
	if err := httpx.Decode(r, dst); err != nil {
		writeError(w, r, err)
		return nil, settlementRunRequest{}, false
	}
	req := dst.common()
	fields := map[string]string{}
	checkUUID(fields, "legalEntityId", req.LegalEntityID)
	if req.FiscalPeriodID != "" {
		checkUUID(fields, "fiscalPeriodId", req.FiscalPeriodID)
	}
	if needPurchaseOrder {
		checkUUID(fields, "purchaseOrderId", req.PurchaseOrderID)
	}
	if req.LedgerID != "" {
		checkUUID(fields, "ledgerId", req.LedgerID)
	}
	postingDate, err := time.Parse(dateLayout, req.PostingDate)
	if err != nil {
		fields["postingDate"] = "must be a YYYY-MM-DD date"
	}
	if len(fields) > 0 {
		httpx.Fail(w, r, validationProblem(fields))
		return nil, req, false
	}
	if err := inScope(p, req.LegalEntityID); err != nil {
		writeError(w, r, err)
		return nil, req, false
	}
	if req.LedgerID != "" && req.LedgerID != a.ports.Profile.LedgerID {
		writeError(w, r, &domain.Error{Code: domain.CodeInvalidReceipt, Detail: "ledgerId differs from the ledger this service posts to"})
		return nil, req, false
	}
	fiscalPeriodID, err := a.resolveFiscalPeriod(r.Context(), req.LegalEntityID, postingDate, req.FiscalPeriodID)
	if err != nil {
		writeError(w, r, err)
		return nil, req, false
	}
	run := &settlementRun{
		api: a, ctx: r.Context(), tenantID: p.TenantID,
		req:  settlementRunBase{legalEntityID: req.LegalEntityID, ledgerID: a.ports.Profile.LedgerID, fiscalPeriodID: fiscalPeriodID, postingDate: postingDate},
		body: settlementRunBody{Settled: []settlementBody{}, Skipped: []skippedBody{}},
	}
	return run, req, true
}

func (a *API) runPlanSettlement(w http.ResponseWriter, r *http.Request) {
	run, req, ok := a.startRun(w, r, &planRunRequest{}, true)
	if !ok {
		return
	}
	if a.ports.InvoicingPlans == nil {
		httpx.Fail(w, r, unavailable("invoicing plan lookup is not configured"))
		return
	}
	po, err := a.loadPurchaseOrder(run.ctx, req.LegalEntityID, req.PurchaseOrderID)
	if err != nil {
		writeError(w, r, err)
		return
	}
	if po.LegalEntityID != req.LegalEntityID {
		writeError(w, r, &domain.Error{Code: domain.CodeInvalidReceipt, Detail: "the purchase order belongs to another legal entity"})
		return
	}
	plans, err := a.ports.InvoicingPlans.List(run.ctx, req.LegalEntityID, po.ID)
	if err != nil {
		writeError(w, r, a.planLookupProblem(run.ctx, err, po.ID))
		return
	}
	slices.SortFunc(plans, func(x, y domain.InvoicingPlan) int { return x.LineNo - y.LineNo })
	linesByNo := make(map[int]domain.PurchaseOrderLine, len(po.Lines))
	for _, l := range po.Lines {
		linesByNo[l.LineNo] = l
	}
planLoop:
	for _, plan := range plans {
		for _, row := range plan.Rows {
			if row.DueDate.After(run.req.postingDate) {
				continue
			}
			sourceRef := fmt.Sprintf("%s/%d/%d", po.ID, plan.LineNo, row.Seq)
			switch line, found := linesByNo[plan.LineNo]; {
			case row.Status == domain.PlanRowInvoiced:
				run.skip(sourceRef, skipAlreadyInvoiced)
			case row.Status != domain.PlanRowOpen:
				run.skip(sourceRef, skipUnknownRowStatus)
			case !found || !line.Usable():
				run.skip(sourceRef, skipLineCancelled)
			case line.ProductID == "":
				run.skip(sourceRef, skipNoProduct)
			case !plan.SettleWithoutReceipt:
				run.skip(sourceRef, skipAwaitsReceipt)
			case plan.Currency != "" && plan.Currency != po.Currency:
				run.skip(sourceRef, skipUnusableCurrency)
			case !row.Amount.IsPositive():
				run.skip(sourceRef, skipNoAmount)
			case run.full():
				run.body.HasMore = true
				break planLoop
			default:
				link := planLink{purchaseOrderID: po.ID, lineNo: plan.LineNo, seq: row.Seq, productID: line.ProductID}
				if err := run.settle(domain.SettlementPlanRow, sourceRef, po.VendorID, po.Currency, a.ports.Profile.GRIR, row.DueDate, row.Amount, link); err != nil {
					writeError(w, r, err)
					return
				}
			}
		}
	}
	httpx.JSON(w, http.StatusOK, run.body)
}

func (a *API) planLookupProblem(ctx context.Context, err error, orderID string) error {
	switch {
	case errors.Is(err, domain.ErrPurchaseOrderNotFound):
		return httpx.NewProblem(http.StatusNotFound, "RCV-NF-004", "purchase order not found")
	case errors.Is(err, domain.ErrPurchaseOrderForbidden):
		return httpx.NewProblem(http.StatusForbidden, "AUTH-0003", "purchase order is outside the caller's access")
	}
	slog.ErrorContext(ctx, "invoicing plan lookup failed", "err", err, "purchaseOrderId", orderID, "correlationId", middleware.GetReqID(ctx))
	return unavailable("invoicing plan lookup unavailable")
}

func (a *API) runConsignmentSettlement(w http.ResponseWriter, r *http.Request) {
	run, req, ok := a.startRun(w, r, &consignmentRunRequest{}, false)
	if !ok {
		return
	}
	if a.ports.OwnershipChanges == nil {
		httpx.Fail(w, r, unavailable("inventory ownership change lookup is not configured"))
		return
	}
	journalIDs, truncated, err := a.ports.OwnershipChanges.List(run.ctx, req.LegalEntityID)
	if err != nil {
		writeError(w, r, a.inventoryProblem(run.ctx, err))
		return
	}
	run.body.HasMore = truncated
	settled, err := a.store.SettledSources(run.ctx, domain.SettlementConsignment, journalIDs)
	if err != nil {
		writeError(w, r, err)
		return
	}
	for _, id := range journalIDs {
		if _, done := settled[id]; done {
			continue
		}
		if run.full() {
			run.body.HasMore = true
			break
		}
		change, err := a.ports.OwnershipChanges.Get(run.ctx, req.LegalEntityID, id)
		if err != nil {
			writeError(w, r, a.inventoryProblem(run.ctx, err))
			return
		}
		switch {
		case change.LegalEntityID != req.LegalEntityID:
			run.skip(id, skipOtherEntity)
		case req.VendorID != "" && !strings.EqualFold(change.VendorID, req.VendorID):
			run.skip(id, skipOtherVendor)
		case !change.Amount.IsPositive():
			run.skip(id, skipNoAmount)
		default:
			if err := run.settle(domain.SettlementConsignment, id, change.VendorID, change.Currency, change.AccrualAccount, change.PostingDate, change.Amount, planLink{}); err != nil {
				writeError(w, r, err)
				return
			}
		}
	}
	httpx.JSON(w, http.StatusOK, run.body)
}

func (a *API) inventoryProblem(ctx context.Context, err error) error {
	switch {
	case errors.Is(err, domain.ErrInventoryForbidden):
		return httpx.NewProblem(http.StatusForbidden, "AUTH-0003", "inventory journals are outside the caller's access")
	case errors.Is(err, domain.ErrOwnershipChangeNotFound):
		return httpx.NewProblem(http.StatusNotFound, "RCV-NF-006", "ownership change journal not found")
	}
	slog.ErrorContext(ctx, "inventory lookup failed", "err", err, "correlationId", middleware.GetReqID(ctx))
	return unavailable("inventory lookup unavailable")
}
