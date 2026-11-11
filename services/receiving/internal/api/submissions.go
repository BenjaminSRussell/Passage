package api

import (
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/domain"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/store"
)

// Invoice submissions by received line (#511, BR-20.11). The vendor routes are the supplier portal's (the vendor is the verified
// token's); the buyer routes are accounts payable's review of them.

// WithSubmissions enables the invoice submission routes.
func (a *API) WithSubmissions(s store.SubmissionStore) *API {
	a.submissions = s
	return a
}

type submissionLineRequest struct {
	ReceiptID     string       `json:"receiptId"`
	ReceiptLineNo int          `json:"receiptLineNo"`
	Qty           *wireDecimal `json:"qty"`
	UnitPrice     *wireDecimal `json:"unitPrice"`
}

type submissionRequest struct {
	LegalEntityID string                  `json:"legalEntityId"`
	InvoiceNumber string                  `json:"invoiceNumber"`
	InvoiceDate   string                  `json:"invoiceDate"`
	Currency      string                  `json:"currency"`
	Note          string                  `json:"note"`
	Lines         []submissionLineRequest `json:"lines"`
}

type submissionLineBody struct {
	ReceiptID     string `json:"receiptId"`
	ReceiptLineNo int    `json:"receiptLineNo"`
	Qty           string `json:"qty"`
	UnitPrice     string `json:"unitPrice"`
	Amount        string `json:"amount"`
}

type submissionBody struct {
	ID            string               `json:"id"`
	LegalEntityID string               `json:"legalEntityId"`
	VendorID      string               `json:"vendorId,omitempty"`
	InvoiceNumber string               `json:"invoiceNumber"`
	InvoiceDate   string               `json:"invoiceDate"`
	Currency      string               `json:"currency"`
	Total         string               `json:"total"`
	Note          string               `json:"note,omitempty"`
	Status        string               `json:"status"`
	Lines         []submissionLineBody `json:"lines"`
	CreatedAt     string               `json:"createdAt"`
	DecidedAt     string               `json:"decidedAt,omitempty"`
	DecisionNote  string               `json:"decisionNote,omitempty"`
	InvoiceID     string               `json:"invoiceId,omitempty"`
}

func toSubmissionBody(s store.Submission, forVendor bool) submissionBody {
	b := submissionBody{ID: s.ID, LegalEntityID: s.LegalEntityID, InvoiceNumber: s.InvoiceNumber, InvoiceDate: s.InvoiceDate.Format(dateLayout), Currency: s.Currency,
		Total: s.Total.String(), Note: s.Note, Status: s.Status, CreatedAt: s.CreatedAt.UTC().Format(time.RFC3339), DecisionNote: s.DecisionNote, InvoiceID: s.InvoiceID,
		Lines: make([]submissionLineBody, 0, len(s.Lines))}
	if !forVendor {
		b.VendorID = s.VendorID
	}
	if s.DecidedAt != nil {
		b.DecidedAt = s.DecidedAt.UTC().Format(time.RFC3339)
	}
	for _, l := range s.Lines {
		b.Lines = append(b.Lines, submissionLineBody{ReceiptID: l.ReceiptID, ReceiptLineNo: l.ReceiptLineNo, Qty: l.Qty.String(), UnitPrice: l.UnitPrice.String(), Amount: l.Amount.String()})
	}
	return b
}

type submissionPage struct {
	Items      []submissionBody `json:"items"`
	NextCursor string           `json:"nextCursor,omitempty"`
}

type invoiceableLineBody struct {
	ReceiptID       string `json:"receiptId"`
	ReceiptLineNo   int    `json:"receiptLineNo"`
	PackingSlip     string `json:"packingSlip"`
	ReceiptDate     string `json:"receiptDate"`
	PurchaseOrderID string `json:"purchaseOrderId"`
	POLineNo        int    `json:"poLineNo,omitempty"`
	ProductID       string `json:"productId"`
	ReceivedQty     string `json:"receivedQty"`
	InvoicedQty     string `json:"invoicedQty"`
	ReservedQty     string `json:"reservedQty"`
	AvailableQty    string `json:"availableQty"`
	UnitPrice       string `json:"unitPrice"`
	Currency        string `json:"currency"`
}

func (a *API) registerSubmissions(r chi.Router) {
	if a.submissions == nil {
		return
	}
	invoice, inquire, post := authz.RequireDuty(dutyVendorInvoice), authz.RequireDuty(dutyAPInquire), authz.RequireDuty(dutyInvoicePost)
	r.With(invoice).Get("/v1/receiving/vendor/invoiceable-lines", vendorScoped(a.vendorInvoiceableLines))
	r.With(invoice).Post("/v1/receiving/vendor/invoice-submissions", vendorScoped(a.idempotent(a.vendorCreateSubmission)))
	r.With(invoice).Get("/v1/receiving/vendor/invoice-submissions", vendorScoped(a.listSubmissions))
	r.With(invoice).Get("/v1/receiving/vendor/invoice-submissions/{id}", vendorScoped(a.getSubmission))
	r.With(invoice).Post("/v1/receiving/vendor/invoice-submissions/{id}:withdraw", vendorScoped(a.idempotent(a.vendorWithdrawSubmission)))
	r.With(inquire).Get("/v1/receiving/invoice-submissions", a.listSubmissions)
	r.With(inquire).Get("/v1/receiving/invoice-submissions/{id}", a.getSubmission)
	r.With(post).Post("/v1/receiving/invoice-submissions/{id}:reject", a.idempotent(a.decideSubmission(domain.SubmissionRejected)))
	r.With(post).Post("/v1/receiving/invoice-submissions/{id}:resolve", a.idempotent(a.decideSubmission(domain.SubmissionPosted)))
}

func (a *API) vendorInvoiceableLines(w http.ResponseWriter, r *http.Request) {
	q := query{values: r.URL.Query(), fields: map[string]string{}}
	po, limit := q.uuid("purchaseOrderId"), q.limit()
	if len(q.fields) > 0 {
		httpx.Fail(w, r, validationProblem(q.fields))
		return
	}
	rows, err := a.submissions.InvoiceableLines(r.Context(), vendorFrom(r.Context()), po, limit)
	if err != nil {
		writeError(w, r, err)
		return
	}
	items := make([]invoiceableLineBody, 0, len(rows))
	for _, l := range rows {
		items = append(items, invoiceableLineBody{ReceiptID: l.ReceiptID, ReceiptLineNo: l.ReceiptLineNo, PackingSlip: l.PackingSlip, ReceiptDate: l.ReceiptDate.Format(dateLayout),
			PurchaseOrderID: l.PurchaseOrderID, POLineNo: l.POLineNo, ProductID: l.ProductID, ReceivedQty: l.ReceivedQty.String(), InvoicedQty: l.InvoicedQty.String(),
			ReservedQty: l.ReservedQty.String(), AvailableQty: l.AvailableQty.String(), UnitPrice: l.UnitPrice.String(), Currency: l.Currency})
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
}

func (a *API) vendorCreateSubmission(w http.ResponseWriter, r *http.Request) {
	p, ok := principalWithUser(w, r)
	if !ok {
		return
	}
	var req submissionRequest
	if err := httpx.DecodeLimit(r, &req, maxBodyBytes); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	checkUUID(fields, "legalEntityId", req.LegalEntityID)
	date, err := time.Parse(dateLayout, req.InvoiceDate)
	if err != nil {
		fields["invoiceDate"] = "must be a YYYY-MM-DD date"
	}
	if len(req.Currency) != 3 || strings.ToUpper(req.Currency) != req.Currency {
		fields["currency"] = "must be a 3-letter upper-case ISO 4217 code"
	}
	lines := make([]domain.SubmissionLine, 0, len(req.Lines))
	total := decimal.Zero
	for _, l := range req.Lines {
		if !ids.Valid(l.ReceiptID) {
			fields["lines"] = "every line needs a receiptId"
			break
		}
		if l.Qty == nil || l.UnitPrice == nil {
			fields["lines"] = "every line needs a qty and a unitPrice"
			break
		}
		amount := l.Qty.Mul(l.UnitPrice.Decimal).Round(6)
		total = total.Add(amount)
		lines = append(lines, domain.SubmissionLine{ReceiptID: strings.ToLower(l.ReceiptID), ReceiptLineNo: l.ReceiptLineNo, Qty: l.Qty.Decimal, UnitPrice: l.UnitPrice.Decimal, Amount: amount})
	}
	if _, bad := fields["lines"]; !bad {
		for k, v := range domain.CheckSubmission(req.InvoiceNumber, lines, req.Note) {
			fields[k] = v
		}
	}
	if len(fields) > 0 {
		httpx.Fail(w, r, validationProblem(fields))
		return
	}
	if err := inScope(p, req.LegalEntityID); err != nil {
		writeError(w, r, err)
		return
	}
	saved, err := a.submissions.CreateSubmission(r.Context(), store.Submission{ID: ids.New(), LegalEntityID: req.LegalEntityID, VendorID: vendorFrom(r.Context()),
		InvoiceNumber: strings.TrimSpace(req.InvoiceNumber), InvoiceDate: date, Currency: req.Currency, Total: total, Note: strings.TrimSpace(req.Note), Lines: lines, CreatedBy: p.UserID})
	if err != nil {
		writeError(w, r, err)
		return
	}
	a.submissionsCreated.Add(1)
	w.Header().Set("Location", "/v1/receiving/vendor/invoice-submissions/"+saved.ID)
	httpx.JSON(w, http.StatusCreated, toSubmissionBody(saved, true))
}

// getSubmission serves both the vendor's read (a row of another vendor reads as missing) and the buyer's; row security keeps the
// buyer to its legal entities.
func (a *API) getSubmission(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !ids.Valid(id) {
		httpx.Fail(w, r, validationProblem(map[string]string{"id": "must be a UUID"}))
		return
	}
	s, err := a.submissions.GetSubmission(r.Context(), id)
	vendor := vendorFrom(r.Context())
	if err == nil && vendor != "" && !strings.EqualFold(s.VendorID, vendor) {
		err = store.ErrSubmissionNotFound
	}
	if err != nil {
		writeError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toSubmissionBody(s, vendor != ""))
}

func (a *API) listSubmissions(w http.ResponseWriter, r *http.Request) {
	q := query{values: r.URL.Query(), fields: map[string]string{}}
	vendor := vendorFrom(r.Context())
	f := store.SubmissionFilter{VendorID: vendor, Status: q.values.Get("status"), Limit: q.limit()}
	if vendor == "" {
		f.VendorID = q.uuid("vendorId")
	}
	f.CursorCreatedAt, f.CursorID = q.cursor()
	switch f.Status {
	case "", domain.SubmissionSubmitted, domain.SubmissionWithdrawn, domain.SubmissionRejected, domain.SubmissionPosted:
	default:
		q.fields["status"] = "must be SUBMITTED, WITHDRAWN, REJECTED or POSTED"
	}
	if len(q.fields) > 0 {
		httpx.Fail(w, r, validationProblem(q.fields))
		return
	}
	rows, err := a.submissions.ListSubmissions(r.Context(), f)
	if err != nil {
		writeError(w, r, err)
		return
	}
	page := submissionPage{Items: make([]submissionBody, 0, len(rows))}
	if len(rows) > f.Limit {
		rows = rows[:f.Limit]
		page.NextCursor = encodeCursor(rows[len(rows)-1].CreatedAt, rows[len(rows)-1].ID)
	}
	for _, s := range rows {
		page.Items = append(page.Items, toSubmissionBody(s, vendor != ""))
	}
	httpx.JSON(w, http.StatusOK, page)
}

func (a *API) vendorWithdrawSubmission(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !ids.Valid(id) {
		httpx.Fail(w, r, validationProblem(map[string]string{"id": "must be a UUID"}))
		return
	}
	s, err := a.submissions.WithdrawSubmission(r.Context(), id, vendorFrom(r.Context()))
	if err != nil {
		writeError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toSubmissionBody(s, true))
}

type decideSubmissionRequest struct {
	Reason    string `json:"reason"`
	InvoiceID string `json:"invoiceId"`
}

// decideSubmission is accounts payable's close of a submission: rejected with a reason the vendor reads, or resolved with the
// invoice that was keyed from it.
func (a *API) decideSubmission(status string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p, ok := principalWithUser(w, r)
		if !ok {
			return
		}
		id := chi.URLParam(r, "id")
		var req decideSubmissionRequest
		if err := httpx.DecodeLimit(r, &req, 8<<10); err != nil {
			httpx.Fail(w, r, err)
			return
		}
		fields := map[string]string{}
		if !ids.Valid(id) {
			fields["id"] = "must be a UUID"
		}
		req.Reason = strings.TrimSpace(req.Reason)
		if status == domain.SubmissionRejected && (req.Reason == "" || len([]rune(req.Reason)) > 1000) {
			fields["reason"] = "is required, at most 1000 characters"
		}
		if status == domain.SubmissionPosted && !ids.Valid(req.InvoiceID) {
			fields["invoiceId"] = "must be the id of the posted invoice"
		}
		if len(fields) > 0 {
			httpx.Fail(w, r, validationProblem(fields))
			return
		}
		s, err := a.submissions.DecideSubmission(r.Context(), id, p.UserID, status, req.Reason, strings.ToLower(req.InvoiceID))
		if err != nil {
			writeError(w, r, err)
			return
		}
		httpx.JSON(w, http.StatusOK, toSubmissionBody(s, false))
	}
}
