package api

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5/middleware"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/payables/internal/domain"
)

const (
	dutyPaymentFilePreview = "payment-file.preview"
	dutyPaymentStatusParse = "payment-status.parse"

	maxPreviewBodyBytes    = 4 << 20
	maxPreviewPayments     = 2000
	maxStatusBodyBytes     = 16 << 20
	codeBodyTooLarge       = "COM-0413"
	pathPaymentFilePreview = "/v1/payables/payment-files:preview"
	pathPaymentStatusParse = "/v1/payables/payments/status:parse"
)

type FileValidator interface {
	Validate(ctx context.Context, content []byte) ([]domain.XSDError, error)
}

type xsdIssue struct {
	Code   string `json:"code"`
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

type fileSummary struct {
	PaymentCount int    `json:"paymentCount"`
	ControlSum   string `json:"controlSum"`
	SHA256       string `json:"sha256"`
}

type fileValidation struct {
	Valid  bool       `json:"valid"`
	Errors []xsdIssue `json:"errors"`
}

type filePreviewResponse struct {
	Format     string         `json:"format"`
	FileNumber string         `json:"fileNumber"`
	Content    string         `json:"content"`
	Summary    fileSummary    `json:"summary"`
	Validation fileValidation `json:"validation"`
}

type knownPaymentDTO struct {
	Status string `json:"status"`
	Method string `json:"method"`
}

type statusParseRequest struct {
	Content  string                     `json:"content"`
	Messages map[string][]string        `json:"messages"`
	Payments map[string]knownPaymentDTO `json:"payments"`
}

type statusIssue struct {
	Code   string `json:"code"`
	Detail string `json:"detail"`
}

type statusWarning struct {
	Payment string `json:"payment"`
	Code    string `json:"code"`
	Text    string `json:"text"`
}

type statusOutcome struct {
	EndToEndID      string         `json:"endToEndId"`
	BankStatus      string         `json:"bankStatus"`
	ReasonCode      string         `json:"reasonCode,omitempty"`
	ReasonText      string         `json:"reasonText,omitempty"`
	From            string         `json:"from,omitempty"`
	To              string         `json:"to,omitempty"`
	Hold            string         `json:"hold,omitempty"`
	TransitionError *statusIssue   `json:"transitionError,omitempty"`
	Warning         *statusWarning `json:"warning,omitempty"`
}

type unknownRef struct {
	Code string `json:"code"`
	Kind string `json:"kind"`
	ID   string `json:"id"`
}

type statusParseResponse struct {
	MessageID         string          `json:"messageId"`
	OriginalMessageID string          `json:"originalMessageId"`
	Outcomes          []statusOutcome `json:"outcomes"`
	Unknown           []unknownRef    `json:"unknown"`
}

func (a *API) failDomain(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, context.Canceled) {
		return
	}
	if errors.Is(err, context.DeadlineExceeded) {
		a.fileServerFaults.Add(1)
		w.Header().Set("Retry-After", "1")
		httpx.Fail(w, r, httpx.NewProblem(http.StatusServiceUnavailable, "COM-0503", "payment file validation is temporarily unavailable"))
		return
	}
	var de *domain.Error
	var p *httpx.Problem
	if errors.As(err, &de) || errors.As(err, &p) {
		a.reject(w, r, err)
		return
	}
	a.fileServerFaults.Add(1)
	slog.Error("payment file request failed", "err", err, "path", r.URL.Path, "correlationId", middleware.GetReqID(r.Context()))
	fail(w, r, err)
}

func (a *API) previewPaymentFile(w http.ResponseWriter, r *http.Request) {
	if !a.validTenant(w, r) {
		return
	}
	var dto domain.Pain001RequestDTO
	if err := httpx.DecodeLimit(r, &dto, maxPreviewBodyBytes); err != nil {
		a.failDomain(w, r, err)
		return
	}
	if len(dto.Payments) > maxPreviewPayments {
		a.failDomain(w, r, &domain.Error{Code: domain.CodeValidation, Detail: "a preview holds at most 2000 payments"})
		return
	}
	if dto.Created == "" {
		dto.Created = a.now().UTC().Format(time.RFC3339)
	}
	in, err := domain.ParsePain001Request(dto)
	if err != nil {
		a.failDomain(w, r, err)
		return
	}
	file, err := domain.GeneratePain001(in)
	if err != nil {
		a.failDomain(w, r, err)
		return
	}
	found, err := a.files.Validate(r.Context(), file.Content)
	if err != nil {
		a.failDomain(w, r, err)
		return
	}
	issues := make([]xsdIssue, len(found))
	for i, e := range found {
		issues[i] = xsdIssue{Code: domain.CodeFileXSD, Path: e.Path, Reason: e.Reason}
	}
	a.filePreviews.Add(1)
	httpx.JSON(w, http.StatusOK, filePreviewResponse{
		Format: file.Format, FileNumber: file.FileNumber, Content: string(file.Content),
		Summary:    fileSummary{PaymentCount: file.PaymentCount, ControlSum: file.ControlSum.String(), SHA256: file.SHA256},
		Validation: fileValidation{Valid: len(issues) == 0, Errors: issues},
	})
}

func (a *API) parsePaymentStatus(w http.ResponseWriter, r *http.Request) {
	if !a.validTenant(w, r) {
		return
	}
	var req statusParseRequest
	if err := httpx.DecodeLimit(r, &req, maxStatusBodyBytes); err != nil {
		a.failDomain(w, r, err)
		return
	}
	if len(req.Content) > domain.MaxPain002Bytes {
		a.failDomain(w, r, httpx.NewProblem(http.StatusRequestEntityTooLarge, codeBodyTooLarge, "status file exceeds the size limit"))
		return
	}
	known := domain.KnownIDs{Messages: req.Messages, Payments: make(map[string]domain.KnownPayment, len(req.Payments))}
	for id, p := range req.Payments {
		known.Payments[id] = domain.KnownPayment{Status: p.Status, Method: p.Method}
	}
	report, err := domain.ParsePain002([]byte(req.Content), known)
	if err != nil {
		a.failDomain(w, r, err)
		return
	}
	a.statusParses.Add(1)
	httpx.JSON(w, http.StatusOK, toStatusResponse(report))
}

func toStatusResponse(report domain.StatusReport) statusParseResponse {
	out := statusParseResponse{
		MessageID: report.MessageID, OriginalMessageID: report.OriginalMessageID,
		Outcomes: make([]statusOutcome, len(report.Outcomes)), Unknown: make([]unknownRef, len(report.Unknown)),
	}
	for i, o := range report.Outcomes {
		item := statusOutcome{
			EndToEndID: o.EndToEndID, BankStatus: o.BankStatus, ReasonCode: o.ReasonCode, ReasonText: o.ReasonText,
			From: o.From, To: o.To, Hold: o.Hold,
		}
		if o.TransitionError != nil {
			item.TransitionError = &statusIssue{Code: o.TransitionError.Code, Detail: o.TransitionError.Detail}
		}
		if o.Warning != nil {
			item.Warning = &statusWarning{Payment: o.Warning.Payment, Code: o.Warning.Code, Text: o.Warning.Text}
		}
		out.Outcomes[i] = item
	}
	for i, u := range report.Unknown {
		out.Unknown[i] = unknownRef{Code: u.Code, Kind: u.Kind, ID: u.ID}
	}
	return out
}
