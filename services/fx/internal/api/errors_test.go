package api

import (
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/fx/internal/domain"
	"github.com/BenjaminSRussell/passage/services/fx/internal/provider"
	"github.com/BenjaminSRussell/passage/services/fx/internal/store"
)

func TestToProblem(t *testing.T) {
	cases := []struct {
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
	}
	for _, tc := range cases {
		t.Run(tc.code, func(t *testing.T) {
			var p *httpx.Problem
			if !errors.As(toProblem(fmt.Errorf("item 2: %w", tc.sentinel)), &p) || p.Status != tc.status || p.Code != tc.code {
				t.Fatalf("got %+v, want %d %s", p, tc.status, tc.code)
			}
		})
	}
}

func TestToProblemPassThroughAndUnknown(t *testing.T) {
	conflict := httpx.NewProblem(http.StatusConflict, store.CodeConflict, "conflict")
	if got := toProblem(fmt.Errorf("wrapped: %w", conflict)); got != error(conflict) {
		t.Fatalf("store problem not passed through: %v", got)
	}
	plain := errors.New("pg down")
	if got := toProblem(plain); got != plain {
		t.Fatalf("unknown error mapped: %v", got)
	}
}
