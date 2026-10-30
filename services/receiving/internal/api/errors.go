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
	domain.CodeOverCredit:        http.StatusUnprocessableEntity,
	domain.CodePlanSettled:       http.StatusUnprocessableEntity,
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
	case errors.Is(err, store.ErrGrirNotFound):
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, "RCV-NF-002", "receipt line has no open GR/IR item"))
	default:
		slog.Error("receipt request failed", "err", err, "correlationId", middleware.GetReqID(r.Context()))
		httpx.Fail(w, r, httpx.NewProblem(http.StatusInternalServerError, "COM-0500", "internal error"))
	}
}
