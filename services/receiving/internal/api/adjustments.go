package api

import (
	"fmt"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/money"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/domain"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/store"
)

type adjustmentLineRequest struct {
	InvoiceLineNo int           `json:"invoiceLineNo"`
	Amount        *fieldDecimal `json:"amount"`
}

type adjustmentRequest struct {
	OriginalInvoiceID string                  `json:"originalInvoiceId"`
	LegalEntityID     string                  `json:"legalEntityId"`
	Kind              string                  `json:"kind"`
	DocumentNumber    string                  `json:"documentNumber"`
	DocumentDate      string                  `json:"documentDate"`
	PostingDate       string                  `json:"postingDate"`
	LedgerID          string                  `json:"ledgerId"`
	FiscalPeriodID    string                  `json:"fiscalPeriodId"`
	Lines             []adjustmentLineRequest `json:"lines"`
}

type adjustmentLineBody struct {
	LineNo        int    `json:"lineNo"`
	InvoiceLineNo int    `json:"invoiceLineNo"`
	Amount        string `json:"amount"`
}

type adjustmentBody struct {
	ID                string               `json:"id"`
	OriginalInvoiceID string               `json:"originalInvoiceId"`
	LegalEntityID     string               `json:"legalEntityId"`
	VendorID          string               `json:"vendorId"`
	Kind              string               `json:"kind"`
	DocumentNumber    string               `json:"documentNumber"`
	DocumentDate      string               `json:"documentDate"`
	PostingDate       string               `json:"postingDate"`
	Currency          string               `json:"currency"`
	LedgerID          string               `json:"ledgerId"`
	FiscalPeriodID    string               `json:"fiscalPeriodId"`
	Total             string               `json:"total"`
	LedgerEntryID     string               `json:"ledgerEntryId"`
	CreatedAt         string               `json:"createdAt"`
	Lines             []adjustmentLineBody `json:"lines"`
}

func buildAdjustment(tenantID, ledgerID string, req adjustmentRequest) (store.InvoiceAdjustment, map[string]string) {
	fields := map[string]string{}
	checkUUID(fields, "originalInvoiceId", req.OriginalInvoiceID)
	checkUUID(fields, "legalEntityId", req.LegalEntityID)
	if req.FiscalPeriodID != "" {
		checkUUID(fields, "fiscalPeriodId", req.FiscalPeriodID)
	}
	if req.LedgerID != "" {
		checkUUID(fields, "ledgerId", req.LedgerID)
	}
	if req.Kind != domain.AdjustmentCredit && req.Kind != domain.AdjustmentDebit {
		fields["kind"] = "must be CREDIT or DEBIT"
	}
	number := strings.TrimSpace(req.DocumentNumber)
	if n := utf8.RuneCountInString(number); n < 1 || n > maxInvoiceNumber {
		fields["documentNumber"] = fmt.Sprintf("must be 1 to %d characters", maxInvoiceNumber)
	}
	documentDate, err := time.Parse(dateLayout, req.DocumentDate)
	if err != nil {
		fields["documentDate"] = "must be a YYYY-MM-DD date"
	}
	postingDate, err := time.Parse(dateLayout, req.PostingDate)
	if err != nil {
		fields["postingDate"] = "must be a YYYY-MM-DD date"
	}
	if len(req.Lines) == 0 || len(req.Lines) > maxLines {
		fields["lines"] = fmt.Sprintf("must hold 1 to %d lines", maxLines)
	}
	in := store.InvoiceAdjustment{
		ID: stableID(tenantID, "adjustment", req.OriginalInvoiceID, strings.ToLower(number)), OriginalInvoiceID: req.OriginalInvoiceID,
		LegalEntityID: req.LegalEntityID, Kind: req.Kind, DocumentNumber: number, DocumentDate: documentDate, PostingDate: postingDate,
		LedgerID: ledgerID, Lines: make([]domain.AdjustmentLine, 0, len(req.Lines)),
	}
	for i, l := range req.Lines {
		name := func(field string) string { return fmt.Sprintf("lines[%d].%s", i, field) }
		if l.InvoiceLineNo < 1 {
			fields[name("invoiceLineNo")] = "must be at least 1"
		}
		line := domain.AdjustmentLine{InvoiceLineNo: l.InvoiceLineNo}
		if v, ok := decimalField(fields, name("amount"), l.Amount, true); ok {
			if v.Sign() <= 0 {
				fields[name("amount")] = "must be greater than zero"
			}
			line.Amount = v
		}
		in.Lines = append(in.Lines, line)
	}
	return in, fields
}

func (a *API) postInvoiceAdjustment(w http.ResponseWriter, r *http.Request) {
	p, ok := tenant.FromContext(r.Context())
	if !ok || !ids.Valid(p.TenantID) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusUnauthorized, "AUTH-0001", "missing or malformed tenant"))
		return
	}
	var req adjustmentRequest
	if err := httpx.Decode(r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	in, fields := buildAdjustment(p.TenantID, a.ports.Profile.LedgerID, req)
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
	ctx := r.Context()
	if in.FiscalPeriodID, err = a.resolveFiscalPeriod(ctx, in.LegalEntityID, in.PostingDate, req.FiscalPeriodID); err != nil {
		writeError(w, r, err)
		return
	}
	posted, err := a.store.PostInvoiceAdjustment(ctx, in, func(base store.InvoiceBase) (store.AdjustmentOutcome, error) {
		entry, err := domain.PostInvoiceAdjustment(ctx, domain.InvoiceAdjustment{
			ID: in.ID, Kind: in.Kind, LegalEntityID: in.LegalEntityID, LedgerID: in.LedgerID, FiscalPeriodID: in.FiscalPeriodID,
			PostingDate: in.PostingDate, DocumentDate: in.DocumentDate, Currency: base.Invoice.Currency, Original: base.Lines, Lines: in.Lines,
		}, a.ports.Periods, a.ledgerFor(in.ID, in.OriginalInvoiceID), a.ports.Profile)
		if err != nil {
			return store.AdjustmentOutcome{}, err
		}
		total := decimalSum(in.Lines)
		evt, err := invoiceAdjustedEvent(p.TenantID, in, base.Invoice.VendorID, base.Invoice.Currency, entry.LedgerEntryID, total)
		return store.AdjustmentOutcome{LedgerEntryID: entry.LedgerEntryID, Total: total, Event: evt}, err
	})
	if err != nil {
		writeError(w, r, err)
		return
	}
	a.pendingPostings.finish(posted.ID, posted.OriginalInvoiceID)
	a.adjustments.Add(1)
	httpx.JSON(w, http.StatusCreated, toAdjustmentBody(posted))
}

func toAdjustmentBody(in store.InvoiceAdjustment) adjustmentBody {
	scale := money.MinorUnits(in.Currency)
	out := adjustmentBody{
		ID: in.ID, OriginalInvoiceID: in.OriginalInvoiceID, LegalEntityID: in.LegalEntityID, VendorID: in.VendorID, Kind: in.Kind,
		DocumentNumber: in.DocumentNumber, DocumentDate: in.DocumentDate.Format(dateLayout), PostingDate: in.PostingDate.Format(dateLayout),
		Currency: in.Currency, LedgerID: in.LedgerID, FiscalPeriodID: in.FiscalPeriodID, Total: in.Total.StringFixed(scale),
		LedgerEntryID: in.LedgerEntryID, CreatedAt: in.CreatedAt.UTC().Format(time.RFC3339), Lines: make([]adjustmentLineBody, 0, len(in.Lines)),
	}
	for i, l := range in.Lines {
		out.Lines = append(out.Lines, adjustmentLineBody{LineNo: i + 1, InvoiceLineNo: l.InvoiceLineNo, Amount: l.Amount.StringFixed(scale)})
	}
	return out
}

func decimalSum(lines []domain.AdjustmentLine) decimal.Decimal {
	total := decimal.Zero
	for _, l := range lines {
		total = total.Add(l.Amount)
	}
	return total
}
