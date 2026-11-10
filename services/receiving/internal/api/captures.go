package api

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/domain"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/store"
)

var ocrMediaTypes = []string{"application/pdf", "image/png", "image/jpeg", "image/tiff"}

type captureRequest struct {
	LegalEntityID string `json:"legalEntityId"`
	Format        string `json:"format"`
	MediaType     string `json:"mediaType"`
	Content       []byte `json:"content"`
}

type captureLineBody struct {
	Description string `json:"description"`
	Unit        string `json:"unit"`
	Qty         string `json:"qty"`
	UnitPrice   string `json:"unitPrice"`
	Amount      string `json:"amount"`
}

type captureBody struct {
	ID            string            `json:"id"`
	LegalEntityID string            `json:"legalEntityId"`
	Source        string            `json:"source"`
	Status        string            `json:"status"`
	ReviewReasons []string          `json:"reviewReasons"`
	VendorTaxID   string            `json:"vendorTaxId"`
	InvoiceNumber string            `json:"invoiceNumber"`
	InvoiceDate   string            `json:"invoiceDate"`
	Currency      string            `json:"currency"`
	Total         string            `json:"total"`
	Confidence    string            `json:"confidence"`
	Lines         []captureLineBody `json:"lines"`
	CreatedAt     string            `json:"createdAt"`
}

func toCaptureBody(c store.Capture) captureBody {
	out := captureBody{
		ID: c.ID, LegalEntityID: c.LegalEntityID, Source: c.Source, Status: c.Status, ReviewReasons: c.Reasons, VendorTaxID: c.Invoice.VendorTaxID,
		InvoiceNumber: c.Invoice.InvoiceNumber, InvoiceDate: c.Invoice.InvoiceDate.Format(dateLayout), Currency: c.Invoice.Currency,
		Total: c.Invoice.Total.String(), Confidence: c.Invoice.Confidence.String(), CreatedAt: c.CreatedAt.UTC().Format(time.RFC3339),
		Lines: make([]captureLineBody, 0, len(c.Invoice.Lines)),
	}
	for _, l := range c.Invoice.Lines {
		out.Lines = append(out.Lines, captureLineBody{Description: l.Description, Unit: l.Unit, Qty: l.Qty.String(), UnitPrice: l.UnitPrice.String(), Amount: l.Amount.String()})
	}
	return out
}

func (a *API) extract(r *http.Request, req captureRequest) (domain.ExtractedInvoice, error) {
	if req.Format == domain.CaptureSourceUBL {
		return domain.ParseUBL(req.Content)
	}
	if a.ports.Extractor == nil {
		return domain.ExtractedInvoice{}, httpx.NewProblem(http.StatusServiceUnavailable, "COM-0503", "invoice OCR is not configured")
	}
	extracted, err := a.ports.Extractor.Extract(r.Context(), req.Content, req.MediaType)
	if err != nil {
		slog.ErrorContext(r.Context(), "invoice document extraction failed", "err", err, "legalEntityId", req.LegalEntityID, "correlationId", middleware.GetReqID(r.Context()))
		return domain.ExtractedInvoice{}, &domain.Error{Code: domain.CodeCaptureExtraction, Detail: "the document could not be read"}
	}
	return extracted, nil
}

func (a *API) createInvoiceCapture(w http.ResponseWriter, r *http.Request) {
	p, ok := principalWithUser(w, r)
	if !ok {
		return
	}
	var req captureRequest
	if err := httpx.DecodeLimit(r, &req, maxBodyBytes); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	checkUUID(fields, "legalEntityId", req.LegalEntityID)
	if req.Format != domain.CaptureSourceUBL && req.Format != domain.CaptureSourceOCR {
		fields["format"] = "must be UBL or OCR"
	}
	if req.Format == domain.CaptureSourceOCR && !slices.Contains(ocrMediaTypes, req.MediaType) {
		fields["mediaType"] = "must be application/pdf, image/png, image/jpeg or image/tiff for OCR"
	}
	if len(req.Content) == 0 {
		fields["content"] = "is required (base64)"
	}
	if len(fields) > 0 {
		httpx.Fail(w, r, validationProblem(fields))
		return
	}
	if err := inScope(p, req.LegalEntityID); err != nil {
		writeError(w, r, err)
		return
	}
	digest := sha256.Sum256(req.Content)
	hexDigest := hex.EncodeToString(digest[:])
	known, found, err := a.store.FindCapture(r.Context(), req.LegalEntityID, hexDigest)
	if err != nil {
		writeError(w, r, err)
		return
	}
	vendor := vendorFrom(r.Context())
	if found && vendor != "" && !strings.EqualFold(known.VendorID, vendor) {
		// The same document reached us before, from someone else; say nothing about whose.
		httpx.Fail(w, r, httpx.NewProblem(http.StatusConflict, "RCV-CAP-004", "this document was already submitted"))
		return
	}
	if found {
		httpx.JSON(w, http.StatusOK, toCaptureBody(known))
		return
	}
	extracted, err := a.extract(r, req)
	if err == nil {
		err = extracted.Validate()
	}
	if err != nil {
		writeError(w, r, err)
		return
	}
	reasons := extracted.ReviewReasons()
	status := domain.CaptureCaptured
	if len(reasons) > 0 {
		status = domain.CaptureNeedsReview
	}
	saved, created, err := a.store.CreateCapture(r.Context(), store.Capture{
		ID: ids.New(), LegalEntityID: req.LegalEntityID, Source: req.Format, Status: status, DocumentSHA256: hexDigest,
		Reasons: reasons, CreatedBy: p.UserID, Invoice: extracted, VendorID: vendor,
	})
	if err != nil {
		writeError(w, r, err)
		return
	}
	if !created {
		if vendor != "" && !strings.EqualFold(saved.VendorID, vendor) {
			httpx.Fail(w, r, httpx.NewProblem(http.StatusConflict, "RCV-CAP-004", "this document was already submitted"))
			return
		}
		httpx.JSON(w, http.StatusOK, toCaptureBody(saved))
		return
	}
	a.capturesCreated.Add(1)
	if status == domain.CaptureNeedsReview {
		a.capturesReview.Add(1)
	}
	w.Header().Set("Location", "/v1/receiving/invoice-captures/"+saved.ID)
	httpx.JSON(w, http.StatusCreated, toCaptureBody(saved))
}

func (a *API) getInvoiceCapture(w http.ResponseWriter, r *http.Request) {
	p, ok := principalWithUser(w, r)
	if !ok {
		return
	}
	id := chi.URLParam(r, "id")
	if !ids.Valid(id) {
		httpx.Fail(w, r, validationProblem(map[string]string{"id": "must be a UUID"}))
		return
	}
	c, err := a.store.GetCapture(r.Context(), id)
	if err == nil && vendorFrom(r.Context()) != "" && !strings.EqualFold(c.VendorID, vendorFrom(r.Context())) {
		err = store.ErrCaptureNotFound
	}
	if err == nil {
		err = inScope(p, c.LegalEntityID)
	}
	if errors.Is(err, store.ErrCaptureNotFound) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, "RCV-NF-007", "invoice capture not found"))
		return
	}
	if err != nil {
		writeError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toCaptureBody(c))
}

type capturePage struct {
	Items      []captureBody `json:"items"`
	NextCursor string        `json:"nextCursor,omitempty"`
}

func (a *API) listInvoiceCaptures(w http.ResponseWriter, r *http.Request) {
	q := query{values: r.URL.Query(), fields: map[string]string{}}
	f := store.CaptureFilter{Status: q.values.Get("status"), Limit: q.limit()}
	f.CursorCreatedAt, f.CursorID = q.cursor()
	f.VendorID = vendorFrom(r.Context())
	if f.Status != "" && f.Status != domain.CaptureCaptured && f.Status != domain.CaptureNeedsReview {
		q.fields["status"] = "must be CAPTURED or NEEDS_REVIEW"
	}
	if len(q.fields) > 0 {
		httpx.Fail(w, r, validationProblem(q.fields))
		return
	}
	rows, err := a.store.ListCaptures(r.Context(), f)
	if err != nil {
		writeError(w, r, err)
		return
	}
	page := capturePage{Items: make([]captureBody, 0, len(rows))}
	if len(rows) > f.Limit {
		rows = rows[:f.Limit]
		page.NextCursor = encodeCursor(rows[len(rows)-1].CreatedAt, rows[len(rows)-1].ID)
	}
	for _, c := range rows {
		page.Items = append(page.Items, toCaptureBody(c))
	}
	httpx.JSON(w, http.StatusOK, page)
}
