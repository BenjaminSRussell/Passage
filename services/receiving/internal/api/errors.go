package api

import (
	"errors"
	"log/slog"
	"net/http"

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
}

func writeError(w http.ResponseWriter, r *http.Request, err error) {
	var problem *httpx.Problem
	var domainErr *domain.Error
	var duplicate *store.DuplicatePackingSlipError
	switch {
	case errors.As(err, &problem):
		httpx.Fail(w, r, problem)
	case errors.As(err, &domainErr) && domainStatus[domainErr.Code] != 0:
		httpx.Fail(w, r, httpx.NewProblem(domainStatus[domainErr.Code], domainErr.Code, domainErr.Detail))
	case errors.As(err, &duplicate):
		httpx.Fail(w, r, httpx.NewProblem(http.StatusConflict, "RCV-DUP-001", "Packing slip already used on receipt "+duplicate.ReceiptNumber))
	case errors.Is(err, store.ErrNotFound):
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, "RCV-NF-001", "receipt not found"))
	default:
		slog.Error("receipt request failed", "err", err, "correlationId", middleware.GetReqID(r.Context()))
		httpx.Fail(w, r, httpx.NewProblem(http.StatusInternalServerError, "COM-0500", "internal error"))
	}
}
