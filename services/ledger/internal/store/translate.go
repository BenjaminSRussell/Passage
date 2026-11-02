package store

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"slices"
	"time"

	"github.com/go-chi/chi/v5/middleware"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/money"
	"github.com/BenjaminSRussell/passage/services/ledger/internal/domain"
)

const (
	CodeFXRateMissing = "LEDGER_FX_RATE_MISSING"
	CodeFXUnavailable = "LEDGER_FX_UNAVAILABLE"
)

var ErrRateUnavailable = errors.New("no exchange rate for the pair and date")

type RateSource interface {
	Rate(ctx context.Context, legalEntityID, from, to string, date time.Time) (decimal.Decimal, error)
}

type rateSourceKey struct{}

func WithRateSource(ctx context.Context, r RateSource) context.Context {
	return context.WithValue(ctx, rateSourceKey{}, r)
}

func rateFor(ctx context.Context, ledger Ledger, from string, date time.Time) (decimal.Decimal, error) {
	src, _ := ctx.Value(rateSourceKey{}).(RateSource)
	if src == nil {
		return decimal.Decimal{}, httpx.NewProblem(http.StatusUnprocessableEntity, CodeFXRateMissing,
			"ledger "+ledger.Code+" is kept in "+ledger.Currency+" and no exchange rate source is configured to translate from "+from)
	}
	rate, err := src.Rate(ctx, ledger.LegalEntityID, from, ledger.Currency, date)
	switch {
	case errors.Is(err, ErrRateUnavailable):
		return decimal.Decimal{}, httpx.NewProblem(http.StatusUnprocessableEntity, CodeFXRateMissing,
			"ledger "+ledger.Code+": no exchange rate from "+from+" to "+ledger.Currency+" on "+date.Format(time.DateOnly))
	case err != nil:
		slog.ErrorContext(ctx, "exchange rate lookup failed", "err", err, "from", from, "to", ledger.Currency, "correlationId", middleware.GetReqID(ctx))
		return decimal.Decimal{}, httpx.NewProblem(http.StatusServiceUnavailable, CodeFXUnavailable, "the exchange rate service could not be reached; nothing was posted")
	}
	return rate, nil
}

func translateLines(ctx context.Context, ledger Ledger, from string, date time.Time, lines []domain.LineInput) ([]domain.LineInput, error) {
	if ledger.Currency == from || len(lines) == 0 {
		return lines, nil
	}
	rate, err := rateFor(ctx, ledger, from, date)
	if err != nil {
		return nil, err
	}
	minor := money.MinorUnits(ledger.Currency)
	sourceDebit, sourceCredit := decimal.Zero, decimal.Zero
	out := slices.Clone(lines)
	for i, l := range out {
		if l.Currency != from {
			continue
		}
		if l.Side == domain.SideDebit {
			sourceDebit = sourceDebit.Add(l.Amount)
		} else {
			sourceCredit = sourceCredit.Add(l.Amount)
		}
		out[i].Amount = l.Amount.Mul(rate).Round(minor)
		out[i].Currency = ledger.Currency
	}
	if sourceDebit.Equal(sourceCredit) {
		absorbRounding(out, ledger.Currency, minor)
	}
	return out, nil
}

func absorbRounding(lines []domain.LineInput, currency string, minor int32) {
	debit, credit := decimal.Zero, decimal.Zero
	for _, l := range lines {
		if l.Currency != currency {
			continue
		}
		if l.Side == domain.SideDebit {
			debit = debit.Add(l.Amount)
		} else {
			credit = credit.Add(l.Amount)
		}
	}
	diff := debit.Sub(credit)
	if diff.IsZero() || diff.Abs().GreaterThan(decimal.New(int64(len(lines)), -minor)) {
		return
	}
	heavier, adjust := domain.SideDebit, diff.Neg()
	if diff.IsNegative() {
		heavier, adjust = domain.SideCredit, diff
	}
	target := -1
	for i, l := range lines {
		if l.Currency == currency && l.Side == heavier && (target < 0 || l.Amount.GreaterThan(lines[target].Amount)) {
			target = i
		}
	}
	if target >= 0 && lines[target].Amount.Add(adjust).IsPositive() {
		lines[target].Amount = lines[target].Amount.Add(adjust)
	}
}
