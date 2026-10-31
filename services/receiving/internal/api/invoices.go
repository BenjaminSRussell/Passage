package api

import (
	"context"
	"crypto/sha256"
	"fmt"
	"github.com/shopspring/decimal"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/money"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/domain"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/store"
)

const (
	maxInvoiceNumber   = 64
	auditReasonSample  = "stochastic sample"
	auditReasonReissue = "re-issue after audit rejection"
)

type invoiceLineRequest struct {
	ReceiptID     string        `json:"receiptId"`
	ReceiptLineNo int           `json:"receiptLineNo"`
	Qty           *fieldDecimal `json:"qty"`
	UnitPrice     *fieldDecimal `json:"unitPrice"`
	StandardCost  bool          `json:"standardCost"`
}

type invoiceRequest struct {
	VendorID       string               `json:"vendorId"`
	LegalEntityID  string               `json:"legalEntityId"`
	InvoiceNumber  string               `json:"invoiceNumber"`
	InvoiceDate    string               `json:"invoiceDate"`
	PostingDate    string               `json:"postingDate"`
	Currency       string               `json:"currency"`
	LedgerID       string               `json:"ledgerId"`
	FiscalPeriodID string               `json:"fiscalPeriodId"`
	OneTimePayeeID string               `json:"oneTimePayeeId"`
	Lines          []invoiceLineRequest `json:"lines"`
}

type invoiceLineBody struct {
	LineNo        int    `json:"lineNo"`
	ReceiptID     string `json:"receiptId"`
	ReceiptLineNo int    `json:"receiptLineNo"`
	Qty           string `json:"qty"`
	UnitPrice     string `json:"unitPrice"`
	Amount        string `json:"amount"`
	StandardCost  bool   `json:"standardCost"`
}

type invoiceBody struct {
	ID               string            `json:"id"`
	VendorID         string            `json:"vendorId"`
	LegalEntityID    string            `json:"legalEntityId"`
	InvoiceNumber    string            `json:"invoiceNumber"`
	InvoiceDate      string            `json:"invoiceDate"`
	PostingDate      string            `json:"postingDate"`
	Currency         string            `json:"currency"`
	LedgerID         string            `json:"ledgerId"`
	FiscalPeriodID   string            `json:"fiscalPeriodId"`
	OneTimePayeeID   string            `json:"oneTimePayeeId,omitempty"`
	Total            string            `json:"total"`
	LedgerEntryID    string            `json:"ledgerEntryId"`
	Source           string            `json:"source"`
	Status           string            `json:"status"`
	AuditReason      string            `json:"auditReason,omitempty"`
	AuditRuleVersion int               `json:"auditRuleVersion,omitempty"`
	ReleasedBy       string            `json:"releasedBy,omitempty"`
	ReleasedAt       string            `json:"releasedAt,omitempty"`
	RejectedBy       string            `json:"rejectedBy,omitempty"`
	RejectedAt       string            `json:"rejectedAt,omitempty"`
	RejectReason     string            `json:"rejectReason,omitempty"`
	CreatedAt        string            `json:"createdAt"`
	Lines            []invoiceLineBody `json:"lines"`
}

func stableID(parts ...string) string {
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	sum[6] = sum[6]&0x0f | 0x50
	sum[8] = sum[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", sum[0:4], sum[4:6], sum[6:8], sum[8:10], sum[10:16])
}

func buildVendorInvoice(tenantID, ledgerID string, req invoiceRequest) (store.Invoice, map[string]string) {
	fields := map[string]string{}
	checkUUID(fields, "vendorId", req.VendorID)
	checkUUID(fields, "legalEntityId", req.LegalEntityID)
	if req.LedgerID != "" {
		checkUUID(fields, "ledgerId", req.LedgerID)
	}
	if req.FiscalPeriodID != "" {
		checkUUID(fields, "fiscalPeriodId", req.FiscalPeriodID)
	}
	payeeID := ""
	if req.OneTimePayeeID != "" {
		var ok bool
		if payeeID, ok = ids.Canonical(req.OneTimePayeeID); !ok {
			fields["oneTimePayeeId"] = "must be a UUID"
		}
	}
	number := strings.TrimSpace(req.InvoiceNumber)
	if n := utf8.RuneCountInString(number); n < 1 || n > maxInvoiceNumber {
		fields["invoiceNumber"] = fmt.Sprintf("must be 1 to %d characters", maxInvoiceNumber)
	}
	invoiceDate, err := time.Parse(dateLayout, req.InvoiceDate)
	if err != nil {
		fields["invoiceDate"] = "must be a YYYY-MM-DD date"
	}
	postingDate, err := time.Parse(dateLayout, req.PostingDate)
	if err != nil {
		fields["postingDate"] = "must be a YYYY-MM-DD date"
	}
	if len(req.Currency) != 3 || !isUpperAlpha(req.Currency) {
		fields["currency"] = "must be a 3-letter upper-case ISO 4217 code"
	}
	if len(req.Lines) == 0 || len(req.Lines) > maxLines {
		fields["lines"] = fmt.Sprintf("must hold 1 to %d lines", maxLines)
	}
	in := store.Invoice{
		ID: stableID(tenantID, req.VendorID, strings.ToLower(number)), LegalEntityID: req.LegalEntityID, VendorID: req.VendorID, InvoiceNumber: number, OneTimePayeeID: payeeID,
		InvoiceDate: invoiceDate, PostingDate: postingDate, Currency: req.Currency, LedgerID: ledgerID,
		Source: domain.InvoiceSourceManual, Lines: make([]store.InvoiceLine, 0, len(req.Lines)),
	}
	for i, l := range req.Lines {
		name := func(field string) string { return fmt.Sprintf("lines[%d].%s", i, field) }
		checkUUID(fields, name("receiptId"), l.ReceiptID)
		if l.ReceiptLineNo < 1 {
			fields[name("receiptLineNo")] = "must be at least 1"
		}
		line := store.InvoiceLine{ReceiptID: l.ReceiptID, ReceiptLineNo: l.ReceiptLineNo, StandardCost: l.StandardCost}
		if v, ok := decimalField(fields, name("qty"), l.Qty, true); ok {
			if v.Sign() <= 0 {
				fields[name("qty")] = "must be greater than zero"
			}
			line.Qty = v
		}
		if v, ok := decimalField(fields, name("unitPrice"), l.UnitPrice, true); ok {
			if v.Sign() < 0 {
				fields[name("unitPrice")] = "must not be negative"
			}
			line.UnitPrice = v
		}
		line.Amount = line.Qty.Mul(line.UnitPrice).RoundBank(money.MinorUnits(req.Currency))
		in.Lines = append(in.Lines, line)
	}
	return in, fields
}

func invoiceSubjects(in store.Invoice) []string {
	subjects := []string{in.ID}
	for _, l := range in.Lines {
		subjects = append(subjects, l.ReceiptID)
	}
	return subjects
}

func (a *API) postVendorInvoice(w http.ResponseWriter, r *http.Request) {
	p, ok := principalWithUser(w, r)
	if !ok {
		return
	}
	var req invoiceRequest
	if err := httpx.Decode(r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	in, fields := buildVendorInvoice(p.TenantID, a.ports.Profile.LedgerID, req)
	if len(fields) > 0 {
		httpx.Fail(w, r, validationProblem(fields))
		return
	}
	if err := inScope(p, req.LegalEntityID); err != nil {
		writeError(w, r, err)
		return
	}
	if req.LedgerID != "" && req.LedgerID != a.ports.Profile.LedgerID {
		writeError(w, r, &domain.Error{Code: domain.CodeInvalidReceipt, Detail: "ledgerId differs from the ledger this service posts to"})
		return
	}
	var err error
	if in.FiscalPeriodID, err = a.resolveFiscalPeriod(r.Context(), in.LegalEntityID, in.PostingDate, req.FiscalPeriodID); err != nil {
		writeError(w, r, err)
		return
	}
	posted, err := a.submitInvoice(r.Context(), p, in)
	if err != nil {
		writeError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, toInvoiceBody(posted))
}

func (a *API) submitInvoice(ctx context.Context, p tenant.Principal, in store.Invoice) (store.Invoice, error) {
	in.PostedBy = p.UserID
	policy, err := a.store.AuditPolicy(ctx, in.LegalEntityID)
	if err != nil {
		return store.Invoice{}, err
	}
	ratePct := policy.RatePct
	if in.AuditReason == "" && a.ports.AuditSampler.Decide(in.ID, policy, invoiceAmount(in), in.Currency) {
		in.AuditReason = auditReasonSample
	}
	if in.AuditReason != "" || policy.RatePct.IsPositive() {
		in.AuditRuleVersion = policy.RuleVersion
	}
	blocked := in.AuditReason != ""
	posted, err := a.store.PostInvoice(ctx, in, func(items []*domain.GrirItem) (store.InvoiceOutcome, error) {
		lines := make([]domain.PostLine, len(in.Lines))
		for i, l := range in.Lines {
			lines[i] = domain.PostLine{Item: items[i], Qty: l.Qty, UnitPrice: l.UnitPrice, StandardCost: l.StandardCost}
		}
		doc := domain.VendorInvoice{
			ID: in.ID, LegalEntityID: in.LegalEntityID, LedgerID: in.LedgerID, FiscalPeriodID: in.FiscalPeriodID,
			PostingDate: in.PostingDate, InvoiceDate: in.InvoiceDate, Currency: in.Currency, Lines: lines,
		}
		if blocked {
			entry, err := domain.HoldInvoice(ctx, doc, a.ports.Periods, a.ports.Profile)
			if err != nil {
				return store.InvoiceOutcome{}, err
			}
			total := entry.Lines[len(entry.Lines)-1].Amount.Value.Neg()
			evt, err := invoiceSampledEvent(p.TenantID, in, total, ratePct)
			return store.InvoiceOutcome{Total: total, Blocked: true, Clearings: entry.GrirCleared, Event: evt}, err
		}
		entry, err := a.PostInvoice(ctx, doc, a.ports.Profile)
		if err != nil {
			return store.InvoiceOutcome{}, err
		}
		total := entry.Lines[len(entry.Lines)-1].Amount.Value.Neg()
		evt, err := invoicePostedEvent(p.TenantID, in, entry.LedgerEntryID, total, entry.GrirCleared)
		return store.InvoiceOutcome{LedgerEntryID: entry.LedgerEntryID, Total: total, Clearings: entry.GrirCleared, Event: evt}, err
	})
	if err != nil {
		return store.Invoice{}, err
	}
	if blocked {
		a.auditBlocked.Add(1)
		return posted, nil
	}
	a.pendingPostings.finish(invoiceSubjects(posted)...)
	a.invoiced.Add(1)
	return posted, nil
}

func invoiceAmount(in store.Invoice) decimal.Decimal {
	total := decimal.Zero
	for _, l := range in.Lines {
		total = total.Add(l.Amount)
	}
	return total
}

func toInvoiceBody(in store.Invoice) invoiceBody {
	scale := money.MinorUnits(in.Currency)
	out := invoiceBody{
		ID: in.ID, VendorID: in.VendorID, LegalEntityID: in.LegalEntityID, InvoiceNumber: in.InvoiceNumber, InvoiceDate: in.InvoiceDate.Format(dateLayout),
		PostingDate: in.PostingDate.Format(dateLayout), Currency: in.Currency, LedgerID: in.LedgerID, FiscalPeriodID: in.FiscalPeriodID,
		OneTimePayeeID: in.OneTimePayeeID, Total: in.Total.StringFixed(scale), LedgerEntryID: in.LedgerEntryID, Source: in.Source, Status: in.Status, AuditReason: in.AuditReason, AuditRuleVersion: in.AuditRuleVersion, ReleasedBy: in.ReleasedBy, RejectedBy: in.RejectedBy, RejectReason: in.RejectReason, CreatedAt: in.CreatedAt.UTC().Format(time.RFC3339), Lines: make([]invoiceLineBody, 0, len(in.Lines)),
	}
	if in.ReleasedAt != nil {
		out.ReleasedAt = in.ReleasedAt.UTC().Format(time.RFC3339)
	}
	if in.RejectedAt != nil {
		out.RejectedAt = in.RejectedAt.UTC().Format(time.RFC3339)
	}
	for i, l := range in.Lines {
		out.Lines = append(out.Lines, invoiceLineBody{
			LineNo: i + 1, ReceiptID: l.ReceiptID, ReceiptLineNo: l.ReceiptLineNo, Qty: l.Qty.String(), UnitPrice: l.UnitPrice.String(),
			Amount: l.Amount.StringFixed(scale), StandardCost: l.StandardCost,
		})
	}
	return out
}
