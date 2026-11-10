package api

import (
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5/middleware"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/domain"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/store"
)

var domainStatus = map[string]int{
	domain.CodeOverDelivery:      http.StatusUnprocessableEntity,
	domain.CodeCatchWeight:       http.StatusUnprocessableEntity,
	domain.CodeCatchWeightUnit:   http.StatusUnprocessableEntity,
	domain.CodeDuplicateSerial:   http.StatusUnprocessableEntity,
	domain.CodeInvalidReceipt:    http.StatusUnprocessableEntity,
	domain.CodeInvalidTransition: http.StatusConflict,
	domain.CodePeriodClosed:      http.StatusUnprocessableEntity,
	domain.CodePeriodUnresolved:  http.StatusUnprocessableEntity,
	domain.CodeOverInvoice:       http.StatusUnprocessableEntity,
	domain.CodeDuplicateInvoice:  http.StatusConflict,
	domain.CodeCorrectionBelow:   http.StatusConflict,
	domain.CodePostingPending:    http.StatusConflict,
	domain.CodeLedgerRejected:    http.StatusUnprocessableEntity,
	domain.CodeServiceReceipt:    http.StatusConflict,
	domain.CodeOverCredit:        http.StatusUnprocessableEntity,
	domain.CodePlanSettled:       http.StatusUnprocessableEntity,
	domain.CodeAuditBlocked:      http.StatusConflict,
	domain.CodeNotAuditBlocked:   http.StatusConflict,
	domain.CodeAuditSelfRelease:  http.StatusForbidden,
	domain.CodeInvalidBank:       http.StatusUnprocessableEntity,
	domain.CodeInvalidCapture:    http.StatusUnprocessableEntity,
	domain.CodeSheetOverLine:     http.StatusUnprocessableEntity,
	domain.CodeSheetSelfAccept:   http.StatusForbidden,
	domain.CodeSheetNotPending:   http.StatusConflict,
	domain.CodeCaptureExtraction: http.StatusUnprocessableEntity,
	domain.CodeAsnOverQty:        http.StatusUnprocessableEntity,
	domain.CodeAsnTracking:       http.StatusConflict,
	domain.CodeAsnConsumed:       http.StatusConflict,
}

func writeError(w http.ResponseWriter, r *http.Request, err error) {
	var problem *httpx.Problem
	var domainErr *domain.Error
	var duplicate *store.DuplicatePackingSlipError
	var pending *store.PendingError
	switch {
	case errors.As(err, &problem):
		httpx.Fail(w, r, problem)
	case errors.As(err, &domainErr) && domainStatus[domainErr.Code] != 0:
		httpx.Fail(w, r, httpx.NewProblem(domainStatus[domainErr.Code], domainErr.Code, domainErr.Detail))
	case errors.As(err, &duplicate):
		httpx.Fail(w, r, httpx.NewProblem(http.StatusConflict, "RCV-DUP-001", "Packing slip already used on receipt "+duplicate.ReceiptNumber))
	case errors.As(err, &pending):
		problem := httpx.NewProblem(http.StatusConflict, domain.CodePostingPending, pending.Error()+"; repeat the original request first")
		problem.Errors = map[string]string{"pendingKey": pending.Key, "createdAt": pending.CreatedAt.UTC().Format(time.RFC3339)}
		httpx.Fail(w, r, problem)
	case errors.Is(err, store.ErrPostingSuperseded) || errors.Is(err, store.ErrPendingNotClaimed):
		httpx.Fail(w, r, httpx.NewProblem(http.StatusConflict, domain.CodePostingPending, err.Error()+"; nothing was committed; review the pending postings before repeating the request"))
	case errors.Is(err, store.ErrNotFound):
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, "RCV-NF-001", "receipt not found"))
	case errors.Is(err, store.ErrInvoiceNotFound):
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, "RCV-NF-005", "vendor invoice not found"))
	case errors.Is(err, store.ErrPayeeNotFound):
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, "RCV-NF-006", "one-time payee not found"))
	case errors.Is(err, store.ErrNoFieldCrypt):
		slog.Error("one-time payee request refused: field encryption keys are not configured", "correlationId", middleware.GetReqID(r.Context()))
		httpx.Fail(w, r, httpx.NewProblem(http.StatusServiceUnavailable, "RCV-CRY-001", "field encryption is not configured; one-time payees cannot be stored"))
	case errors.Is(err, store.ErrGrirNotFound):
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, "RCV-NF-002", "receipt line has no open GR/IR item"))
	case errors.Is(err, domain.ErrSelfBillPartyIncomplete):
		httpx.Fail(w, r, httpx.NewProblem(http.StatusUnprocessableEntity, "AP-ERS-005", "the self-billing document cannot be issued: "+err.Error()))
	case errors.Is(err, store.ErrAsnNotFound):
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, "RCV-NF-010", "advance ship notice not found"))
	case errors.Is(err, store.ErrSelfBillNotFound):
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, "RCV-NF-009", "self-billing document not found"))
	default:
		slog.Error("receipt request failed", "err", err, "correlationId", middleware.GetReqID(r.Context()))
		httpx.Fail(w, r, httpx.NewProblem(http.StatusInternalServerError, "COM-0500", "internal error"))
	}
}
