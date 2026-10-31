package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"slices"
	"sync/atomic"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/money"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/domain"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/ledger"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/store"
)

const (
	readyTimeout       = 500 * time.Millisecond
	dutyMatchingPolicy = "MatchingPolicyMaintain"
	dutyReceiving      = "ReceivingMaintain"
	dutyReceivingPost  = "ReceivingPost"
	dutyInvoicePost    = "VendorInvoicePost"
	dutyAPInquire      = "APInquire"
	maxLines           = 500
	maxMagnitude       = 1e12
	maxExponent        = 12
	minExponent        = -18
)

// Ports are required; New panics when either is nil.
type Ports struct {
	Periods domain.PeriodControl
	Ledger  domain.LedgerPoster
	Profile domain.PostingProfile

	PurchaseOrders domain.PurchaseOrders
	RequirePOLine  bool
	InvoicingPlans domain.InvoicingPlans

	PlanRowSettlements domain.PlanRowSettlements
	OwnershipChanges   domain.OwnershipChanges
	VendorGroups       domain.VendorGroups
	AuditSampler       *domain.Sampler

	ResolveMinAge time.Duration
	ResolveLease  time.Duration
}

type API struct {
	store           store.Store
	ports           Ports
	resolveMinAge   time.Duration
	resolveLease    time.Duration
	evaluated       atomic.Int64
	rejected        atomic.Int64
	arrived         atomic.Int64
	registered      atomic.Int64
	posted          atomic.Int64
	adjusted        atomic.Int64
	invoiced        atomic.Int64
	adjustments     atomic.Int64
	settlements     atomic.Int64
	planMarkFailed  atomic.Int64
	ersSettled      atomic.Int64
	auditBlocked    atomic.Int64
	resolveFallback atomic.Int64
	auditReleased   atomic.Int64
	auditRejected   atomic.Int64
	payeesCreated   atomic.Int64
	groupMissing    atomic.Int64

	pendingPostings pendingTracker
	http            requestMetrics
}

func New(s store.Store, ports Ports) *API {
	if ports.Periods == nil || ports.Ledger == nil {
		panic("api.New: Ports.Periods and Ports.Ledger are required")
	}
	minAge := ports.ResolveMinAge
	if minAge == 0 {
		minAge = ledger.Timeout + httpx.RequestTimeout
	}
	lease := ports.ResolveLease
	if lease == 0 {
		lease = 2*ledger.Timeout + httpx.RequestTimeout
	}
	return &API{store: s, ports: ports, resolveMinAge: minAge, resolveLease: lease, http: newRequestMetrics()}
}

func (a *API) resolveFiscalPeriod(ctx context.Context, legalEntityID string, postingDate time.Time, supplied string) (string, error) {
	id, fellBack, err := domain.ResolveFiscalPeriod(ctx, a.ports.Periods, domain.PeriodQuery{LegalEntityID: legalEntityID, LedgerID: a.ports.Profile.LedgerID, PostingDate: postingDate}, supplied)
	if fellBack {
		a.resolveFallback.Add(1)
		slog.WarnContext(ctx, "close has no dated period; using the caller-supplied fiscal period", "fiscalPeriodId", id, "legalEntityId", legalEntityID, "postingDate", postingDate.Format(dateLayout), "correlationId", middleware.GetReqID(ctx))
	}
	return id, err
}

func (a *API) PostInvoice(ctx context.Context, in domain.VendorInvoice, profile domain.PostingProfile) (domain.JournalEntry, error) {
	subjects := []string{in.ID}
	for _, l := range in.Lines {
		subjects = append(subjects, l.Item.ReceiptID)
	}
	return domain.PostInvoice(ctx, in, a.ports.Periods, a.ledgerFor(subjects...), profile)
}

func (a *API) Register(r chi.Router) {
	r.Get("/readyz", a.ready)
	r.With(authz.RequireDuty(dutyMatchingPolicy)).Post("/v1/receiving/matching:evaluate", a.evaluate)
	r.With(authz.RequireDuty(dutyReceiving)).Post("/v1/receiving/arrivals", a.idempotent(a.arrive))
	r.With(authz.RequireDuty(dutyReceiving)).Post("/v1/receiving/receipts", a.idempotent(a.directReceipt))
	r.With(authz.RequireDuty(dutyReceiving)).Get("/v1/receiving/receipts", a.listReceipts)
	r.With(authz.RequireDuty(dutyReceiving)).Get("/v1/receiving/receipts/{id}", a.getReceipt)
	r.With(authz.RequireDuty(dutyReceiving)).Post("/v1/receiving/receipts/{id}:register", a.idempotent(a.registerReceipt))
	r.With(authz.RequireDuty(dutyReceivingPost)).Post("/v1/receiving/receipts/{id}:post", a.idempotent(a.postReceipt))
	r.With(authz.RequireDuty(dutyReceivingPost)).Post("/v1/receiving/receipts/{id}:correct", a.idempotent(a.correctReceipt))
	r.With(authz.RequireDuty(dutyReceivingPost)).Post("/v1/receiving/receipts/{id}:cancel", a.idempotent(a.cancelReceipt))
	r.With(authz.RequireDuty(dutyAPInquire)).Get("/v1/receiving/grir", a.listGrir)
	r.With(authz.RequireDuty(dutyReceivingAdmin)).Get("/v1/receiving/pending-postings", a.listPending)
	r.With(authz.RequireDuty(dutyReceivingAdmin)).Post("/v1/receiving/pending-postings/{key}/resolve", a.idempotent(a.resolvePending))
	r.With(authz.RequireDuty(dutyInvoicePost)).Post("/v1/receiving/invoices:post", a.idempotent(a.postVendorInvoice))
	r.With(authz.RequireDuty(dutyInvoicePost)).Post("/v1/receiving/one-time-payees", a.idempotent(a.createOneTimePayee))
	r.With(authz.RequireDuty(dutyInvoicePost)).Post("/v1/receiving/invoice-adjustments:post", a.idempotent(a.postInvoiceAdjustment))
	r.With(authz.RequireDuty(dutyInvoicePost)).Post("/v1/receiving/plan-settlements:run", a.idempotent(a.runPlanSettlement))
	r.With(authz.RequireDuty(dutyInvoicePost)).Post("/v1/receiving/consignment-settlements:run", a.idempotent(a.runConsignmentSettlement))
	r.With(authz.RequireDuty(dutyInvoicePost)).Post("/v1/receiving/ers-settlements:run", a.idempotent(a.runErsSettlement))
	r.With(authz.RequireDuty(dutyInvoiceAuditRelease)).Post("/v1/receiving/invoices/{id}:release-audit-block", a.idempotent(a.releaseAuditBlock))
	r.With(authz.RequireDuty(dutyInvoiceAuditRelease)).Post("/v1/receiving/invoices/{id}:reject-audit-block", a.idempotent(a.rejectAuditBlock))
	r.With(authz.RequireDuty(dutyErsPriceConfirm)).Put("/v1/receiving/receipts/{id}/lines/{lineNo}/ers-price-confirmation", a.confirmErsPrice)
	r.With(authz.RequireDuty(dutyMatchingPolicy)).Put("/v1/receiving/audit-policy", a.setAuditPolicy)
	r.With(authz.RequireDuty(dutyMatchingPolicy)).Put("/v1/receiving/ers-vendors/{vendorId}", a.setErsVendor)
}

func (a *API) Metrics() http.Handler {
	counters := []struct {
		name, help string
		value      *atomic.Int64
	}{
		{"receiving_plan_row_marks_failed_total", "Plan row settlements purchasing could not be told about; the next plan run retries them.", &a.planMarkFailed},
		{"receiving_matches_evaluated_total", "Invoices evaluated by the matching engine.", &a.evaluated},
		{"receiving_evaluate_rejected_total", "Evaluate requests rejected by validation.", &a.rejected},
		{"receiving_receipts_arrived_total", "Receipts recorded at arrival.", &a.arrived},
		{"receiving_receipts_registered_total", "Receipts registered, including direct receipts.", &a.registered},
		{"receiving_receipts_posted_total", "Receipts posted to the ledger.", &a.posted},
		{"receiving_receipts_adjusted_total", "Posted receipts corrected or cancelled.", &a.adjusted},
		{"receiving_invoices_posted_total", "Vendor invoices posted against GR/IR items.", &a.invoiced},
		{"receiving_invoice_adjustments_posted_total", "Subsequent debits and credits posted against vendor invoices.", &a.adjustments},
		{"receiving_settlements_posted_total", "Invoicing plan rows and consignment journals settled.", &a.settlements},
		{"receiving_ers_invoices_posted_total", "Self-billed invoices created by evaluated receipt settlement runs, including those held for audit.", &a.ersSettled},
		{"receiving_invoices_audit_blocked_total", "Invoices held by the stochastic audit block.", &a.auditBlocked},
		{"receiving_period_resolve_fallback_total", "Documents posted with the caller-supplied fiscal period because close has no dated period (temporary).", &a.resolveFallback},
		{"receiving_one_time_payees_created_total", "One-time payees recorded for vendor invoices.", &a.payeesCreated},
		{"receiving_invoices_audit_released_total", "Audit-blocked invoices released by an auditor and posted.", &a.auditReleased},
		{"receiving_vendor_group_lookups_failed_total", "Invoices posted without vendor_group_id because party was unreachable, refused the lookup or does not know the vendor.", &a.groupMissing},
		{"receiving_invoices_audit_rejected_total", "Audit-blocked invoices rejected by an auditor, freeing their GR/IR quantity.", &a.auditRejected},
	}
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		for _, c := range counters {
			_, _ = fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s counter\n%s %d\n", c.name, c.help, c.name, c.name, c.value.Load())
		}
		open, oldest := a.pendingPostings.stats()
		_, _ = fmt.Fprintf(w, "# HELP receiving_pending_postings_open Ledger postings this process recorded and has not seen complete.\n# TYPE receiving_pending_postings_open gauge\nreceiving_pending_postings_open %d\n", open)
		_, _ = fmt.Fprintf(w, "# HELP receiving_pending_postings_oldest_age_seconds Age of the oldest unfinished ledger posting.\n# TYPE receiving_pending_postings_oldest_age_seconds gauge\nreceiving_pending_postings_oldest_age_seconds %d\n", int64(oldest.Seconds()))
		a.http.write(w)
	})
}

func (a *API) ready(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), readyTimeout)
	defer cancel()
	if err := a.store.Ping(ctx); err != nil {
		slog.Error("readiness check failed", "err", err, "correlationId", middleware.GetReqID(r.Context()))
		httpx.JSON(w, http.StatusServiceUnavailable, map[string]string{"status": "unavailable"})
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]string{"status": "ready"})
}

var decimalText = regexp.MustCompile(`^-?\d+(\.\d+)?$`)

type wireDecimal struct{ decimal.Decimal }

func (d *wireDecimal) UnmarshalJSON(b []byte) error {
	text := string(b)
	if len(b) > 1 && b[0] == '"' {
		if err := json.Unmarshal(b, &text); err != nil || !decimalText.MatchString(text) {
			return fmt.Errorf("must be a decimal number or a decimal string")
		}
	} else if len(b) == 0 || (b[0] != '-' && (b[0] < '0' || b[0] > '9')) {
		return fmt.Errorf("must be a decimal number or a decimal string")
	}
	v, err := decimal.NewFromString(text)
	if err != nil {
		return err
	}
	if v.Exponent() > maxExponent || v.Exponent() < minExponent || v.Abs().GreaterThan(decimal.NewFromFloat(maxMagnitude)) {
		return fmt.Errorf("out of range")
	}
	d.Decimal = v
	return nil
}

type policyRequest struct {
	MatchLevel   string       `json:"matchLevel"`
	PriceTolPct  *wireDecimal `json:"priceTolPct"`
	PriceTolAbs  *wireDecimal `json:"priceTolAbs"`
	QtyTolPct    *wireDecimal `json:"qtyTolPct"`
	TotalTolPct  *wireDecimal `json:"totalTolPct"`
	TotalTolAbs  *wireDecimal `json:"totalTolAbs"`
	SmallDiffAbs *wireDecimal `json:"smallDiffAbs"`
	PriceTolMode string       `json:"priceTolMode"`
}

type lineRequest struct {
	Qty                   *wireDecimal `json:"qty"`
	Unit                  string       `json:"unit"`
	UnitPrice             *wireDecimal `json:"unitPrice"`
	POUnitPrice           *wireDecimal `json:"poUnitPrice"`
	POCurrency            string       `json:"poCurrency"`
	ReceiptState          string       `json:"receiptState"`
	ReceivedUninvoicedQty *wireDecimal `json:"receivedUninvoicedQty"`
	POQtyRemaining        *wireDecimal `json:"poQtyRemaining"`
	POLineNo              *int         `json:"poLineNo"`
}

type evaluateRequest struct {
	PurchaseOrderID string         `json:"purchaseOrderId"`
	LegalEntityID   string         `json:"legalEntityId"`
	Policy          *policyRequest `json:"policy"`
	Currency        string         `json:"currency"`
	HeaderTotal     *wireDecimal   `json:"headerTotal"`
	Charges         *wireDecimal   `json:"charges"`
	Tax             *wireDecimal   `json:"tax"`
	Lines           []lineRequest  `json:"lines"`
}

type moneyBody struct {
	Amount   string `json:"amount"`
	Currency string `json:"currency"`
}

type lineBody struct {
	Unit             string    `json:"unit,omitempty"`
	Status           string    `json:"status"`
	PriceVariance    moneyBody `json:"priceVariance"`
	CurrencyMismatch bool      `json:"currencyMismatch"`
}

type postingBody struct {
	Kind   string    `json:"kind"`
	Amount moneyBody `json:"amount"`
}

type evaluateResponse struct {
	Status        string        `json:"status"`
	Lines         []lineBody    `json:"lines"`
	TotalVariance moneyBody     `json:"totalVariance"`
	Postings      []postingBody `json:"postings"`
}

func (a *API) evaluate(w http.ResponseWriter, r *http.Request) {
	p, ok := tenant.FromContext(r.Context())
	if !ok || !ids.Valid(p.TenantID) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusUnauthorized, "AUTH-0001", "missing or malformed tenant"))
		return
	}
	var req evaluateRequest
	if err := httpx.Decode(r, &req); err != nil {
		a.rejected.Add(1)
		httpx.Fail(w, r, err)
		return
	}
	_, _, fields := buildInvoice(req)
	for i, line := range req.Lines {
		if line.POLineNo != nil && line.POUnitPrice == nil {
			delete(fields, fmt.Sprintf("lines[%d].poUnitPrice", i))
		}
	}
	mergeFields(fields, purchaseOrderReferenceFields(req))
	if len(fields) > 0 {
		a.rejectEvaluation(w, r, fields)
		return
	}
	if req.LegalEntityID != "" && !tenant.InLegalEntityScope(p, req.LegalEntityID) {
		a.rejected.Add(1)
		httpx.Fail(w, r, httpx.NewProblem(http.StatusForbidden, "AUTH-0003", "legal entity is outside the caller's access"))
		return
	}
	poFields, err := a.fillFromPurchaseOrder(r.Context(), &req)
	if err != nil {
		a.rejected.Add(1)
		httpx.Fail(w, r, err)
		return
	}
	policy, invoice, fields := buildInvoice(req)
	mergeFields(fields, poFields)
	if len(fields) > 0 {
		a.rejectEvaluation(w, r, fields)
		return
	}
	result := domain.Evaluate(policy, invoice)
	a.evaluated.Add(1)
	httpx.JSON(w, http.StatusOK, toResponse(result, req.Lines))
}

func (a *API) rejectEvaluation(w http.ResponseWriter, r *http.Request, fields map[string]string) {
	a.rejected.Add(1)
	problem := httpx.NewProblem(http.StatusBadRequest, "COM-0001", "request failed validation")
	problem.Errors = fields
	httpx.Fail(w, r, problem)
}

const poOverrideMessage = "is supplied by the purchase order and cannot be overridden"

func addField(fields map[string]string, key, message string) {
	if existing, ok := fields[key]; ok {
		message = existing + "; " + message
	}
	fields[key] = message
}

func mergeFields(dst, src map[string]string) {
	for key, message := range src {
		addField(dst, key, message)
	}
}

func purchaseOrderReferenceFields(req evaluateRequest) map[string]string {
	fields := map[string]string{}
	add := func(key, message string) { addField(fields, key, message) }
	if req.PurchaseOrderID != "" && !ids.Valid(req.PurchaseOrderID) {
		add("purchaseOrderId", "must be a UUID")
	}
	if req.LegalEntityID != "" && !ids.Valid(req.LegalEntityID) {
		add("legalEntityId", "must be a UUID")
	}
	lineIndexes := map[int]int{}
	for i, l := range req.Lines {
		if l.POLineNo == nil {
			continue
		}
		key := func(name string) string { return fmt.Sprintf("lines[%d].%s", i, name) }
		if first, exists := lineIndexes[*l.POLineNo]; exists {
			add(key("poLineNo"), fmt.Sprintf("duplicates lines[%d].poLineNo", first))
		} else {
			lineIndexes[*l.POLineNo] = i
		}
		if l.POUnitPrice != nil {
			add(key("poUnitPrice"), poOverrideMessage)
		}
		if l.POQtyRemaining != nil {
			add(key("poQtyRemaining"), poOverrideMessage)
		}
		if l.POCurrency != "" {
			add(key("poCurrency"), poOverrideMessage)
		}
		if req.PurchaseOrderID == "" {
			add(key("poLineNo"), "requires purchaseOrderId")
		}
	}
	return fields
}

func (a *API) loadPurchaseOrder(ctx context.Context, legalEntityID, id string) (domain.PurchaseOrder, error) {
	if a.ports.PurchaseOrders == nil {
		return domain.PurchaseOrder{}, httpx.NewProblem(http.StatusServiceUnavailable, "COM-0503", "purchase order lookup is not configured")
	}
	po, err := a.ports.PurchaseOrders.Get(ctx, legalEntityID, id)
	switch {
	case errors.Is(err, domain.ErrPurchaseOrderNotFound):
		return domain.PurchaseOrder{}, httpx.NewProblem(http.StatusNotFound, "RCV-NF-004", "purchase order not found")
	case errors.Is(err, domain.ErrPurchaseOrderForbidden):
		return domain.PurchaseOrder{}, httpx.NewProblem(http.StatusForbidden, "AUTH-0003", "purchase order is outside the caller's access")
	case err != nil:
		slog.ErrorContext(ctx, "purchase order lookup failed", "err", err, "purchaseOrderId", id, "correlationId", middleware.GetReqID(ctx))
		return domain.PurchaseOrder{}, httpx.NewProblem(http.StatusServiceUnavailable, "COM-0503", "purchase order lookup unavailable")
	}
	if !po.Usable() {
		return domain.PurchaseOrder{}, httpx.NewProblem(http.StatusUnprocessableEntity, "RCV-PO-001", "purchase order is not approved or confirmed")
	}
	return po, nil
}

func (a *API) fillFromPurchaseOrder(ctx context.Context, req *evaluateRequest) (map[string]string, error) {
	fields := map[string]string{}
	if !slices.ContainsFunc(req.Lines, func(l lineRequest) bool { return l.POLineNo != nil }) {
		return fields, nil
	}
	po, err := a.loadPurchaseOrder(ctx, req.LegalEntityID, req.PurchaseOrderID)
	if err != nil {
		return nil, err
	}
	if req.LegalEntityID != "" {
		named, _ := ids.Canonical(req.LegalEntityID)
		owner, _ := ids.Canonical(po.LegalEntityID)
		if named != owner {
			fields["legalEntityId"] = "does not match the purchase order's legal entity"
			return fields, nil
		}
	}
	byNo := make(map[int]domain.PurchaseOrderLine, len(po.Lines))
	for _, l := range po.Lines {
		byNo[l.LineNo] = l
	}
	for i := range req.Lines {
		l := &req.Lines[i]
		if l.POLineNo == nil {
			continue
		}
		line, ok := byNo[*l.POLineNo]
		if !ok {
			fields[fmt.Sprintf("lines[%d].poLineNo", i)] = "no such line on the purchase order"
			continue
		}
		if !line.Usable() {
			return nil, httpx.NewProblem(http.StatusUnprocessableEntity, "RCV-PO-002", fmt.Sprintf("purchase order line %d is cancelled", *l.POLineNo))
		}
		l.POUnitPrice = &wireDecimal{line.NetUnitPrice()}
		l.POQtyRemaining = &wireDecimal{line.QtyRemaining()}
		l.POCurrency = po.Currency
	}
	return fields, nil
}

func toMoney(a money.Amount) moneyBody {
	return moneyBody{Amount: a.Value.StringFixed(money.MinorUnits(a.Currency)), Currency: a.Currency}
}

func toResponse(res domain.Result, lines []lineRequest) evaluateResponse {
	out := evaluateResponse{Status: string(res.Status), TotalVariance: toMoney(res.TotalVariance), Lines: make([]lineBody, 0, len(res.Lines)), Postings: []postingBody{}}
	for i, l := range res.Lines {
		out.Lines = append(out.Lines, lineBody{Unit: lines[i].Unit, Status: string(l.Status), PriceVariance: toMoney(l.PriceVariance), CurrencyMismatch: l.CurrencyMismatch})
	}
	for _, p := range res.Postings {
		out.Postings = append(out.Postings, postingBody{Kind: p.Kind, Amount: toMoney(p.Amount)})
	}
	return out
}

func buildInvoice(req evaluateRequest) (domain.Policy, domain.Invoice, map[string]string) {
	fields := map[string]string{}
	policy := buildPolicy(req.Policy, fields)
	if len(req.Currency) != 3 || bytes.ContainsFunc([]byte(req.Currency), func(c rune) bool { return c < 'A' || c > 'Z' }) {
		fields["currency"] = "must be a 3-letter upper-case ISO 4217 code"
	}
	if len(req.Lines) == 0 || len(req.Lines) > maxLines {
		fields["lines"] = fmt.Sprintf("must hold 1 to %d lines", maxLines)
	}
	invoice := domain.Invoice{Currency: req.Currency}
	invoice.HeaderTotal = required(fields, "headerTotal", req.HeaderTotal)
	invoice.Charges = optionalNonNegative(fields, "charges", req.Charges)
	invoice.Tax = optionalNonNegative(fields, "tax", req.Tax)
	for i, l := range req.Lines {
		key := func(name string) string { return fmt.Sprintf("lines[%d].%s", i, name) }
		line := domain.InvoiceLine{
			POCurrency:         l.POCurrency,
			ReceiptState:       domain.ReceiptState(l.ReceiptState),
			Qty:                required(fields, key("qty"), l.Qty),
			UnitPrice:          required(fields, key("unitPrice"), l.UnitPrice),
			POUnitPrice:        required(fields, key("poUnitPrice"), l.POUnitPrice),
			ReceivedUninvoiced: optionalNonNegative(fields, key("receivedUninvoicedQty"), l.ReceivedUninvoicedQty),
			POQtyRemaining:     optionalNonNegative(fields, key("poQtyRemaining"), l.POQtyRemaining),
		}
		if l.Qty != nil && line.Qty.Sign() <= 0 {
			fields[key("qty")] = "must be greater than zero"
		}
		if l.UnitPrice != nil && line.UnitPrice.Sign() < 0 {
			fields[key("unitPrice")] = "must not be negative"
		}
		if l.POUnitPrice != nil && line.POUnitPrice.Sign() < 0 {
			fields[key("poUnitPrice")] = "must not be negative"
		}
		if l.POCurrency != "" && len(l.POCurrency) != 3 {
			fields[key("poCurrency")] = "must be a 3-letter ISO 4217 code"
		}
		if s := line.ReceiptState; s != "" && s != domain.Registered && s != domain.Posted {
			fields[key("receiptState")] = "must be REGISTERED or POSTED when present"
		}
		invoice.Lines = append(invoice.Lines, line)
	}
	return policy, invoice, fields
}

func buildPolicy(in *policyRequest, fields map[string]string) domain.Policy {
	p := domain.DefaultPolicy()
	if in == nil {
		return p
	}
	if in.MatchLevel != "" {
		p.MatchLevel = domain.MatchLevel(in.MatchLevel)
		if p.MatchLevel != domain.MatchNone && p.MatchLevel != domain.MatchTwoWay && p.MatchLevel != domain.MatchThreeWay {
			fields["policy.matchLevel"] = "must be NONE, TWO_WAY or THREE_WAY"
		}
	}
	if in.PriceTolMode != "" {
		p.PriceTolMode = domain.PriceMode(in.PriceTolMode)
		if p.PriceTolMode != domain.UpperOnly && p.PriceTolMode != domain.Both {
			fields["policy.priceTolMode"] = "must be UPPER_ONLY or BOTH"
		}
	}
	for name, target := range map[string]struct {
		in  *wireDecimal
		out *decimal.Decimal
	}{
		"priceTolPct": {in.PriceTolPct, &p.PriceTolPct}, "priceTolAbs": {in.PriceTolAbs, &p.PriceTolAbs},
		"qtyTolPct": {in.QtyTolPct, &p.QtyTolPct}, "totalTolPct": {in.TotalTolPct, &p.TotalTolPct},
		"totalTolAbs": {in.TotalTolAbs, &p.TotalTolAbs}, "smallDiffAbs": {in.SmallDiffAbs, &p.SmallDiffAbs},
	} {
		if target.in == nil {
			continue
		}
		if target.in.Sign() < 0 {
			fields["policy."+name] = "must not be negative"
			continue
		}
		*target.out = target.in.Decimal
	}
	return p
}

func required(fields map[string]string, name string, v *wireDecimal) decimal.Decimal {
	if v == nil {
		fields[name] = "is required"
		return decimal.Zero
	}
	return v.Decimal
}

func optionalNonNegative(fields map[string]string, name string, v *wireDecimal) decimal.Decimal {
	if v == nil {
		return decimal.Zero
	}
	if v.Sign() < 0 {
		fields[name] = "must not be negative"
	}
	return v.Decimal
}
