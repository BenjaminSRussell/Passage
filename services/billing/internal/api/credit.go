package api

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/billing/internal/domain"
	"github.com/BenjaminSRussell/passage/services/billing/internal/store"
)

// creditLineDTO asks to credit qty of line lineNo of the original invoice.
type creditLineDTO struct {
	LineNo int    `json:"lineNo"`
	Qty    string `json:"qty"`
}

// creditNoteRequest proposes a credit note against a posted invoice (FR-29.5).
// Without lines the whole remainder of the original is credited, charges
// included. With lines only those quantities are credited, plus chargeAmount
// when given.
type creditNoteRequest struct {
	OriginalInvoiceID string          `json:"originalInvoiceId"`
	CreditDate        string          `json:"creditDate"`
	Reason            string          `json:"reason"`
	ChargeAmount      string          `json:"chargeAmount,omitempty"`
	Lines             []creditLineDTO `json:"lines,omitempty"`
}

// createCreditNote stores a credit note proposal. Lines copy the original's
// account, tax code and price; tax is the original's, pro rata, never a fresh
// quote (a credit reverses the tax that was charged). The store re-checks the
// remaining creditable amount under the original's row lock.
func (a *API) createCreditNote(w http.ResponseWriter, r *http.Request) {
	key := r.Header.Get(idempotencyKey)
	if invalidIdempotencyKey(key) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusBadRequest, "COM-0001", idempotencyKeyDetail))
		return
	}
	var req creditNoteRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	creditDate, request, charges, fields := a.validateCredit(req)
	if len(fields) > 0 {
		writeFields(w, r, fields)
		return
	}
	raw, _ := json.Marshal(req)
	sum := sha256.Sum256(raw)
	hash := hex.EncodeToString(sum[:])
	if existing, found, err := a.store.FindByIdempotencyKey(r.Context(), key); err != nil {
		httpx.Fail(w, r, err)
		return
	} else if found {
		if existing.RequestHash != hash {
			httpx.Fail(w, r, httpx.NewProblem(http.StatusUnprocessableEntity, "idempotency_key_reuse", "Idempotency-Key was already used with a different request body"))
			return
		}
		w.Header().Set("Idempotent-Replayed", "true")
		w.Header().Set("ETag", etag(existing.Version))
		httpx.JSON(w, http.StatusOK, toDTO(existing))
		return
	}
	orig, err := a.store.GetInvoice(r.Context(), req.OriginalInvoiceID)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if err := domain.CanBeCredited(orig.Status, orig.Type); err != nil {
		httpx.Fail(w, r, problemFrom(err))
		return
	}
	if creditDate.Before(orig.InvoiceDate) {
		writeFields(w, r, map[string]string{"creditDate": "must not be before the original invoice date " + orig.InvoiceDate.Format(dateLayout)})
		return
	}
	original := make([]domain.PostedLine, len(orig.Lines))
	for i, l := range orig.Lines {
		original[i] = domain.PostedLine{Line: l.Line, Net: l.Net, Tax: l.Tax}
	}
	planned, totals, err := domain.PlanCredit(orig.Currency, original, orig.Totals, orig.Credited, request, charges)
	if err != nil {
		httpx.Fail(w, r, problemFrom(err))
		return
	}
	cn := store.Invoice{ID: ids.New(), LegalEntityID: orig.LegalEntityID, CustomerID: orig.CustomerID, InvoiceAccountID: orig.InvoiceAccountID,
		Type: domain.TypeCreditNote, Status: domain.StatusProposal, Currency: orig.Currency, InvoiceDate: creditDate, DueDate: creditDate,
		Totals: totals, OriginalInvoiceID: orig.ID, OriginalNumber: orig.Number, Reason: strings.TrimSpace(req.Reason),
		IdempotencyKey: key, RequestHash: hash}
	for _, l := range planned {
		cn.Lines = append(cn.Lines, store.Line{Line: l.Line, Net: l.Net, Tax: l.Tax, OriginalLineNo: l.OriginalLineNo})
	}
	created, err := a.store.CreateCreditNote(r.Context(), cn)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	a.creditNotesCreated.Add(1)
	w.Header().Set("ETag", etag(created.Version))
	httpx.JSON(w, http.StatusCreated, toDTO(created))
}

// validateCredit checks the request shape and returns the credit date, the
// requested quantities (nil for a whole credit) and the charges, if given.
func (a *API) validateCredit(req creditNoteRequest) (time.Time, []domain.CreditQty, *decimal.Decimal, map[string]string) {
	fields := map[string]string{}
	if !ids.Valid(req.OriginalInvoiceID) {
		fields["originalInvoiceId"] = "must be a UUID"
	}
	creditDate, err := time.Parse(dateLayout, req.CreditDate)
	if err != nil {
		fields["creditDate"] = "must be a YYYY-MM-DD date"
	}
	if reason := strings.TrimSpace(req.Reason); reason == "" || len([]rune(reason)) > maxReasonLength {
		fields["reason"] = fmt.Sprintf("is required, at most %d characters", maxReasonLength)
	}
	if len(req.Lines) > a.maxLines {
		fields["lines"] = fmt.Sprintf("must hold at most %d lines", a.maxLines)
	}
	var charges *decimal.Decimal
	if req.ChargeAmount != "" {
		if !decimalText.MatchString(req.ChargeAmount) {
			fields["chargeAmount"] = "must be a non-negative decimal with at most 6 fraction digits"
		} else {
			c := decimal.RequireFromString(req.ChargeAmount)
			charges = &c
		}
	}
	var lines []domain.CreditQty
	for i, l := range req.Lines {
		field := func(name string) string { return fmt.Sprintf("lines[%d].%s", i, name) }
		if l.LineNo < 1 {
			fields[field("lineNo")] = "must be a line number of the original invoice"
		}
		if !decimalText.MatchString(l.Qty) || !decimal.RequireFromString(l.Qty).IsPositive() {
			fields[field("qty")] = "must be a positive decimal with at most 6 fraction digits"
			continue
		}
		lines = append(lines, domain.CreditQty{LineNo: l.LineNo, Qty: decimal.RequireFromString(l.Qty)})
	}
	return creditDate, lines, charges, fields
}

// postCreditNote posts a credit note proposal with the next CN- number.
// Posting an already posted credit note returns it unchanged. There is no tax
// re-quote: the credit carries the original's tax.
func (a *API) postCreditNote(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !ids.Valid(id) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, store.CodeNotFound, "credit note not found"))
		return
	}
	expected, ok := parseIfMatch(r.Header.Get("If-Match"))
	if !ok {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusBadRequest, "COM-0001", `If-Match must be the credit note version as an entity tag, for example "1"`))
		return
	}
	cn, err := a.store.GetInvoice(r.Context(), id)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if cn.Type != domain.TypeCreditNote {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusConflict, store.CodeNotProposal, "not a credit note; an invoice posts through /v1/billing/invoices/{id}:post"))
		return
	}
	if cn.Status == domain.StatusPosted {
		w.Header().Set("ETag", etag(cn.Version))
		httpx.JSON(w, http.StatusOK, toDTO(cn))
		return
	}
	posted, err := a.store.PostCreditNote(r.Context(), id, a.now().UTC().Truncate(24*time.Hour), expected)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	a.creditNotesPosted.Add(1)
	w.Header().Set("ETag", etag(posted.Version))
	httpx.JSON(w, http.StatusOK, toDTO(posted))
}
