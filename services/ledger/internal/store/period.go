package store

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5/middleware"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/ledger/internal/domain"
)

const (
	DutyPostClosedPeriod  = "ledger.period.postclosed"
	CodePeriodUnavailable = "LEDGER_PERIOD_STATUS_UNAVAILABLE"
	codePeriodClosed      = "LEDGER_PERIOD_CLOSED"
	statusOpen            = "open"
	statusOnHold          = "on_hold"
	reversalSourceSystem  = "gl"
)

var ErrNoFiscalPeriod = errors.New("no fiscal period registered for the posting date")

type PeriodGate interface {
	Status(ctx context.Context, legalEntityID, ledgerID string, postingDate time.Time, sourceSystem string) (string, error)
}

type periodGateKey struct{}

func WithPeriodGate(ctx context.Context, g PeriodGate) context.Context {
	return context.WithValue(ctx, periodGateKey{}, g)
}

func PeriodGateStatus(ctx context.Context) (PeriodGate, error) {
	if g, ok := ctx.Value(periodGateKey{}).(PeriodGate); ok && g != nil {
		return g, nil
	}
	return nil, errors.New("no period gate in context")
}

func periodViolations(ctx context.Context, ledger Ledger, postingDate time.Time, sourceSystem string) ([]domain.Violation, error) {
	gate, ok := ctx.Value(periodGateKey{}).(PeriodGate)
	if !ok || gate == nil {
		return nil, nil
	}
	if p, ok := tenant.FromContext(ctx); ok && p.HasDuty(DutyPostClosedPeriod) {
		return nil, nil
	}
	status, err := gate.Status(ctx, ledger.LegalEntityID, ledger.ID, postingDate, sourceSystem)
	switch {
	case errors.Is(err, ErrNoFiscalPeriod):
		return nil, nil
	case err != nil:
		slog.ErrorContext(ctx, "close period status unavailable", "err", err, "ledgerId", ledger.ID, "correlationId", middleware.GetReqID(ctx))
		return nil, httpx.NewProblem(http.StatusServiceUnavailable, CodePeriodUnavailable, "the close service could not confirm the fiscal period status; nothing was posted")
	case status == statusOpen || status == statusOnHold:
		return nil, nil
	}
	return []domain.Violation{{Code: codePeriodClosed, Field: "postingDate",
		Message: "fiscal period of " + postingDate.Format(time.DateOnly) + " is " + status + " in the close service; posting needs duty " + DutyPostClosedPeriod}}, nil
}

func rejectWithPeriod(ctx context.Context, ledger Ledger, postingDate time.Time, sourceSystem string, found []domain.Violation) error {
	closed, err := periodViolations(ctx, ledger, postingDate, sourceSystem)
	if err != nil {
		return err
	}
	if found = append(found, closed...); len(found) > 0 {
		return &domain.RejectedError{Violations: found}
	}
	return nil
}
