package api

import (
	"errors"
	"net/http"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/fx/internal/domain"
	"github.com/BenjaminSRussell/passage/services/fx/internal/importer"
	"github.com/BenjaminSRussell/passage/services/fx/internal/provider"
)

var sentinelProblems = []struct {
	sentinel error
	status   int
	code     string
}{
	{domain.ErrInvalidRate, http.StatusBadRequest, "FX_RATE_INVALID"},
	{domain.ErrFutureRate, http.StatusUnprocessableEntity, "FX_RATE_DATE_FUTURE"},
	{domain.ErrRateMissing, http.StatusUnprocessableEntity, "LEDGER_RATE_MISSING"},
	{domain.ErrRoundingExceeded, http.StatusUnprocessableEntity, "LEDGER_ROUNDING_EXCEEDED"},
	{domain.ErrUnbalanced, http.StatusUnprocessableEntity, "LEDGER_UNBALANCED"},
	{provider.ErrDisabled, http.StatusUnprocessableEntity, "FX_PROVIDER_UNAVAILABLE"},
	{importer.ErrNoLastKnownRate, http.StatusBadGateway, "FX_PROVIDER_FAILED"},
}

func toProblem(err error) error {
	var p *httpx.Problem
	if errors.As(err, &p) {
		return p
	}
	for _, m := range sentinelProblems {
		if errors.Is(err, m.sentinel) {
			return httpx.NewProblem(m.status, m.code, err.Error())
		}
	}
	return err
}
