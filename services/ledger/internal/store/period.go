package store

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/go-chi/chi/v5/middleware"
	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
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
	statusClosed          = "closed"
)

var ErrNoFiscalPeriod = errors.New("no fiscal period registered for the posting date")

type PeriodGate interface {
	Status(ctx context.Context, legalEntityID, ledgerID string, postingDate time.Time, sourceSystem string) (string, error)
}

type periodGateKey struct{}

func WithPeriodGate(ctx context.Context, g PeriodGate) context.Context {
	return context.WithValue(ctx, periodGateKey{}, &requestGate{inner: g, open: map[string]bool{}})
}

type requestGate struct {
	inner PeriodGate
	mu    sync.Mutex
	open  map[string]bool
}

func (r *requestGate) Status(ctx context.Context, legalEntityID, ledgerID string, postingDate time.Time, sourceSystem string) (string, error) {
	key := ledgerID + "|" + sourceSystem + "|" + postingDate.Format(time.DateOnly)
	r.mu.Lock()
	known := r.open[key]
	r.mu.Unlock()
	if known {
		return statusOpen, nil
	}
	status, err := r.inner.Status(ctx, legalEntityID, ledgerID, postingDate, sourceSystem)
	if err == nil && status == statusOpen {
		r.mu.Lock()
		r.open[key] = true
		r.mu.Unlock()
	}
	return status, err
}

func PeriodGateStatus(ctx context.Context) (PeriodGate, error) {
	if g := gateFrom(ctx); g != nil {
		return g, nil
	}
	return nil, errors.New("no period gate in context")
}

func gateFrom(ctx context.Context) PeriodGate {
	g, _ := ctx.Value(periodGateKey{}).(PeriodGate)
	return g
}

func overridable(status string) bool { return status == statusClosed || status == statusOnHold }

func periodViolations(ctx context.Context, ledger Ledger, postingDate time.Time, sourceSystem string) ([]domain.Violation, error) {
	gate := gateFrom(ctx)
	if gate == nil {
		return nil, nil
	}
	status, err := gate.Status(ctx, ledger.LegalEntityID, ledger.ID, postingDate, sourceSystem)
	switch {
	case errors.Is(err, ErrNoFiscalPeriod):
		return nil, nil
	case err != nil:
		slog.ErrorContext(ctx, "close period status unavailable", "err", err, "ledgerId", ledger.ID, "correlationId", middleware.GetReqID(ctx))
		return nil, httpx.NewProblem(http.StatusServiceUnavailable, CodePeriodUnavailable, "the close service could not confirm the fiscal period status; nothing was posted")
	case status == statusOpen:
		return nil, nil
	}
	if p, ok := tenant.FromContext(ctx); ok && overridable(status) && p.HasDuty(DutyPostClosedPeriod) {
		return nil, nil
	}
	return []domain.Violation{{Code: codePeriodClosed, Field: "postingDate",
		Message: "fiscal period of " + postingDate.Format(time.DateOnly) + " is " + status + " in the close service; only an open period accepts postings" + holderHint(status)}}, nil
}

func holderHint(status string) string {
	if overridable(status) {
		return ", or a caller with duty " + DutyPostClosedPeriod
	}
	return ""
}

func rejectWithPeriod(ctx context.Context, ledger Ledger, postingDate time.Time, sourceSystem string, found []domain.Violation) error {
	if len(found) > 0 {
		return &domain.RejectedError{Violations: found}
	}
	closed, err := periodViolations(ctx, ledger, postingDate, sourceSystem)
	if err != nil {
		return err
	}
	if len(closed) > 0 {
		return &domain.RejectedError{Violations: closed}
	}
	return nil
}

// warmPeriods asks the gate before the posting transaction takes its ledger
// row locks, so a caching gate answers the in-transaction check without a call
// made under the lock. The query returns (ledger id, posting date, source
// system) rows for tenant $1; failures are ignored because the in-transaction
// check is the authority.
func (s *Postgres) warmPeriods(ctx context.Context, query string, args ...any) {
	gate := gateFrom(ctx)
	if gate == nil {
		return
	}
	p, err := principalOf(ctx)
	if err != nil {
		return
	}
	type ref struct {
		ledger Ledger
		date   time.Time
		source string
	}
	var refs []ref
	_ = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, query, append([]any{p.TenantID}, args...)...)
		if err != nil {
			return err
		}
		type raw struct{ ledgerID, date, source string }
		var found []raw
		for rows.Next() {
			var r raw
			if err := rows.Scan(&r.ledgerID, &r.date, &r.source); err != nil {
				rows.Close()
				return err
			}
			found = append(found, r)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		for _, r := range found {
			date, err := time.Parse(time.DateOnly, r.date)
			if err != nil {
				continue
			}
			ledger, err := loadLedger(ctx, tx, p.TenantID, r.ledgerID, false)
			if err != nil {
				continue
			}
			refs = append(refs, ref{ledger, date, r.source})
		}
		return nil
	})
	for _, r := range refs {
		_, _ = gate.Status(ctx, r.ledger.LegalEntityID, r.ledger.ID, r.date, r.source)
	}
}

const (
	warmEntryQuery    = `select $2::text, $3::text, $4::text where $1::text <> ''`
	warmSetQuery      = `select ledger_id::text, $3::text, $4::text from ledger_set_member where tenant_id = $1 and ledger_set_id::text = $2`
	warmReversalQuery = `select ledger_id::text, coalesce(nullif($3::text, ''), posting_date::text), source_system from journal_entry where tenant_id = $1 and id::text = $2`
	warmBatchQuery    = `select ledger_id::text, posting_date::text, $3::text from journal_batch where tenant_id = $1 and id::text = $2`
)
