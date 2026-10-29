package api

import (
	"errors"
	"net/http"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/fx/internal/domain"
	"github.com/BenjaminSRussell/passage/services/fx/internal/importer"
	"github.com/BenjaminSRussell/passage/services/fx/internal/provider"
	"github.com/BenjaminSRussell/passage/services/fx/internal/store"
)

const (
	providerFailedDetail = "exchange-rate provider request failed"
	importInProgressCode = "FX_IMPORT_IN_PROGRESS"
	importInProgressMsg  = "an import for this provider, rate type and day is still running; retry shortly"
)

var sentinelProblems = []struct {
	sentinel error
	status   int
	code     string
	detail   string
}{
	{domain.ErrInvalidRate, http.StatusBadRequest, "FX_RATE_INVALID", ""},
	{domain.ErrFutureRate, http.StatusUnprocessableEntity, "FX_RATE_DATE_FUTURE", ""},
	{domain.ErrRateMissing, http.StatusUnprocessableEntity, "LEDGER_RATE_MISSING", ""},
	{domain.ErrRoundingExceeded, http.StatusUnprocessableEntity, "LEDGER_ROUNDING_EXCEEDED", ""},
	{domain.ErrUnbalanced, http.StatusUnprocessableEntity, "LEDGER_UNBALANCED", ""},
	{provider.ErrDisabled, http.StatusUnprocessableEntity, "FX_PROVIDER_UNAVAILABLE", ""},
	{importer.ErrNoLastKnownRate, http.StatusBadGateway, "FX_PROVIDER_FAILED", providerFailedDetail},
	{importer.ErrDateMismatch, http.StatusBadGateway, "FX_PROVIDER_FAILED", providerFailedDetail},
	{store.ErrLostOwnership, http.StatusConflict, importInProgressCode, importInProgressMsg},
}

func toProblem(err error) error {
	var p *httpx.Problem
	if errors.As(err, &p) {
		return p
	}
	for _, m := range sentinelProblems {
		if errors.Is(err, m.sentinel) {
			detail := m.detail
			if detail == "" {
				detail = err.Error()
			}
			return httpx.NewProblem(m.status, m.code, detail)
		}
	}
	return err
}
