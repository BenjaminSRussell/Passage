package store

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"time"

	"github.com/go-chi/chi/v5/middleware"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/money"
	"github.com/BenjaminSRussell/passage/services/ledger/internal/domain"
)

const (
	RateTypeSpot       = "spot"
	CodeRateMissing    = "LEDGER_RATE_MISSING"
	CodeFXUnavailable  = "LEDGER_FX_UNAVAILABLE"
	CodeFXMisconfigure = "LEDGER_FX_MISCONFIGURED"
)

type RateRejected struct{ Code, Detail string }

func (e *RateRejected) Error() string { return e.Code + ": " + e.Detail }

type RateRefused struct{ Status int }

func (e *RateRefused) Error() string {
	return fmt.Sprintf("fx refused the lookup with status %d", e.Status)
}

type RateSource interface {
	Rate(ctx context.Context, legalEntityID, from, to string, date time.Time) (decimal.Decimal, error)
}

type rateSourceKey struct{}

type resolvedRatesKey struct{}

type resolvedRate struct {
	rate decimal.Decimal
	err  error
}

func WithRateSource(ctx context.Context, r RateSource) context.Context {
	return context.WithValue(ctx, rateSourceKey{}, r)
}

func rateKey(from, to string, date time.Time) string {
	return from + ">" + to + "@" + date.Format(time.DateOnly)
}

// resolveRates looks up the rate of every other currency in the set before the posting transaction
// takes its locks; the in-transaction translation reads the answers (or the lookup error) from ctx.
func (s *Postgres) resolveRates(ctx context.Context, setID string, date time.Time) context.Context {
	src, _ := ctx.Value(rateSourceKey{}).(RateSource)
	p, err := principalOf(ctx)
	if src == nil || err != nil {
		return ctx
	}
	type member struct {
		currency, legalEntity string
		leading               bool
	}
	var members []member
	_ = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select l.currency::text, l.legal_entity_id::text, l.id = s.leading_ledger_id
			from ledger_set s join ledger_set_member m on m.tenant_id = s.tenant_id and m.ledger_set_id = s.id
			join ledger l on l.tenant_id = m.tenant_id and l.id = m.ledger_id
			where s.tenant_id = $1 and s.id::text = $2`, p.TenantID, setID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var m member
			if err := rows.Scan(&m.currency, &m.legalEntity, &m.leading); err != nil {
				return err
			}
			members = append(members, m)
		}
		return rows.Err()
	})
	document := ""
	for _, m := range members {
		if m.leading {
			document = m.currency
		}
	}
	resolved := map[string]resolvedRate{}
	for _, m := range members {
		key := rateKey(document, m.currency, date)
		if _, done := resolved[key]; done || m.currency == document {
			continue
		}
		rate, err := src.Rate(ctx, m.legalEntity, document, m.currency, date)
		resolved[key] = resolvedRate{rate, err}
	}
	return context.WithValue(ctx, resolvedRatesKey{}, resolved)
}

func rateFor(ctx context.Context, ledger Ledger, from string, date time.Time) (decimal.Decimal, error) {
	var got resolvedRate
	if resolved, ok := ctx.Value(resolvedRatesKey{}).(map[string]resolvedRate); ok {
		if got, ok = resolved[rateKey(from, ledger.Currency, date)]; !ok {
			got.err = &RateRejected{Code: CodeRateMissing, Detail: "no rate was resolved"}
		}
	} else if src, _ := ctx.Value(rateSourceKey{}).(RateSource); src != nil {
		got.rate, got.err = src.Rate(ctx, ledger.LegalEntityID, from, ledger.Currency, date)
	} else {
		got.err = &RateRejected{Code: CodeRateMissing, Detail: "no exchange rate source is configured"}
	}
	if got.err == nil {
		return got.rate, nil
	}
	pair := "ledger " + ledger.Code + ": " + RateTypeSpot + " rate from " + from + " to " + ledger.Currency + " on " + date.Format(time.DateOnly)
	var rejected *RateRejected
	var refused *RateRefused
	switch {
	case errors.As(got.err, &rejected):
		return decimal.Decimal{}, httpx.NewProblem(http.StatusUnprocessableEntity, rejected.Code, pair+": "+rejected.Detail)
	case errors.As(got.err, &refused):
		slog.ErrorContext(ctx, "exchange rate lookup refused", "status", refused.Status, "from", from, "to", ledger.Currency, "correlationId", middleware.GetReqID(ctx))
		return decimal.Decimal{}, httpx.NewProblem(http.StatusBadGateway, CodeFXMisconfigure, "the exchange rate service refused the lookup; check the ledger's fx grant and request; nothing was posted")
	}
	slog.ErrorContext(ctx, "exchange rate lookup failed", "err", got.err, "from", from, "to", ledger.Currency, "correlationId", middleware.GetReqID(ctx))
	return decimal.Decimal{}, httpx.NewProblem(http.StatusServiceUnavailable, CodeFXUnavailable, "the exchange rate service could not be reached; nothing was posted")
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
	sourceNet, translatedNet := decimal.Zero, decimal.Zero
	out := slices.Clone(lines)
	for i, l := range out {
		if l.Currency != from {
			continue
		}
		out[i].Amount = l.Amount.Mul(rate).Round(minor)
		out[i].Currency = ledger.Currency
		sourceNet = sourceNet.Add(signed(l.Side, l.Amount))
		translatedNet = translatedNet.Add(signed(l.Side, out[i].Amount))
	}
	absorbRounding(out, ledger.Currency, sourceNet.Mul(rate).Round(minor).Sub(translatedNet), minor)
	return out, nil
}

func signed(side string, amount decimal.Decimal) decimal.Decimal {
	if side == domain.SideDebit {
		return amount
	}
	return amount.Neg()
}

// absorbRounding moves the translated net by diff (wanted net minus actual net) onto the largest line
// that can take it: reducing the heavier side first, else enlarging the lighter side (BR-12.4).
func absorbRounding(lines []domain.LineInput, currency string, diff decimal.Decimal, minor int32) {
	if diff.IsZero() || diff.Abs().GreaterThan(decimal.New(int64(len(lines)), -minor)) {
		return
	}
	reduce, enlarge := domain.SideDebit, domain.SideCredit
	if diff.IsPositive() {
		reduce, enlarge = domain.SideCredit, domain.SideDebit
	}
	amount := diff.Abs()
	if i := largestLine(lines, currency, reduce); i >= 0 && lines[i].Amount.Sub(amount).IsPositive() {
		lines[i].Amount, lines[i].RoundingAdjustment = lines[i].Amount.Sub(amount), true
	} else if j := largestLine(lines, currency, enlarge); j >= 0 {
		lines[j].Amount, lines[j].RoundingAdjustment = lines[j].Amount.Add(amount), true
	}
}

func largestLine(lines []domain.LineInput, currency, side string) int {
	target := -1
	for i, l := range lines {
		if l.Currency == currency && l.Side == side && l.Amount.IsPositive() && (target < 0 || l.Amount.GreaterThan(lines[target].Amount)) {
			target = i
		}
	}
	return target
}
