// Package api exposes the treasury HTTP surface under /v1/treasury.
package api

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/money"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/treasury/internal/bankconnect"
	"github.com/BenjaminSRussell/passage/services/treasury/internal/domain"
	"github.com/BenjaminSRussell/passage/services/treasury/internal/store"
)

const (
	readyTimeout         = 500 * time.Millisecond
	idempotencyKey       = "Idempotency-Key"
	dutyView             = "treasury.view"
	dutyAccountWrite     = "treasury.bank-account.write"
	dutyAccountApprove   = "treasury.bank-account.approve"
	dutyStatement        = "treasury.statement.import"
	dutyForecast         = "treasury.forecast.write"
	dutyCheckWrite       = "treasury.check.write"
	dutyFacilityWrite    = "treasury.facility.write"
	dutyFacilityOverride = "treasury.facility.override"
	dutyLoanWrite        = "treasury.loan.write"
	dutyReconcile        = "treasury.reconciliation.write"
	validationDetail     = "request failed validation"
	defaultMaxFileMB     = 50
	maxFileMBLimit       = 200
)

// API holds the handlers' dependencies and counters for /metrics.
type API struct {
	store        store.Store
	maxFileBytes int64
	now          func() time.Time
	ebics        EBICS
	channels     bankconnect.Channels

	accountsCreated      atomic.Int64
	accountsTransitioned atomic.Int64
	statementsImported   atomic.Int64
	statementsReplayed   atomic.Int64
	statementsRejected   atomic.Int64
	forecastEntries      atomic.Int64
	forecastsBuilt       atomic.Int64
	checksIssued         atomic.Int64
	checksVoided         atomic.Int64
	instrumentsBooked    atomic.Int64
	limitOverrides       atomic.Int64
	loansCreated         atomic.Int64
	rulesCreated         atomic.Int64
	reconRuns            atomic.Int64
	autoMatches          atomic.Int64
	manualMatches        atomic.Int64
	unmatches            atomic.Int64
}

// New returns the API over s with the default 50 MB statement upload limit.
func New(s store.Store) *API {
	return &API{store: s, maxFileBytes: defaultMaxFileMB << 20, now: time.Now}
}

// WithMaxFileMB sets the statement upload limit (1 to 200 MB, FR-36.2 statement.maxFileMb).
func (a *API) WithMaxFileMB(mb int) (*API, error) {
	if mb < 1 || mb > maxFileMBLimit {
		return nil, fmt.Errorf("TREASURY_MAX_FILE_MB must be 1 to %d, got %d", maxFileMBLimit, mb)
	}
	a.maxFileBytes = int64(mb) << 20
	return a, nil
}

// WithChannels sets the bank channels TREASURY_BANK_CHANNELS enabled, for the
// payment-file producers that send through them.
func (a *API) WithChannels(c bankconnect.Channels) *API {
	a.channels = c
	return a
}

// Register mounts every route on r.
func (a *API) Register(r chi.Router) {
	// /readyz is not registered here: httpx.NewRouter already owns it and the
	// entrypoint serves Ready() in front of the router (see cmd/treasury).
	r.With(authz.RequireDuty(dutyAccountWrite)).Post("/v1/treasury/bank-accounts", a.createBankAccount)
	r.With(authz.RequireDuty(dutyView)).Get("/v1/treasury/bank-accounts", a.listBankAccounts)
	r.With(authz.RequireDuty(dutyView)).Get("/v1/treasury/bank-accounts/{id}", a.getBankAccount)
	r.With(authz.RequireDuty(dutyAccountApprove)).Post("/v1/treasury/bank-accounts/{id}:activate", a.transitionBankAccount(domain.AccountActive))
	r.With(authz.RequireDuty(dutyAccountApprove)).Post("/v1/treasury/bank-accounts/{id}:deactivate", a.transitionBankAccount(domain.AccountInactive))
	r.With(authz.RequireDuty(dutyAccountApprove)).Post("/v1/treasury/bank-accounts/{id}:close", a.transitionBankAccount(domain.AccountClosed))
	r.With(authz.RequireDuty(dutyView)).Get("/v1/treasury/bank-accounts/{id}/statements", a.listStatements)
	r.With(authz.RequireDuty(dutyStatement)).Post("/v1/treasury/statements/import", a.importStatement)
	r.With(authz.RequireDuty(dutyView)).Get("/v1/treasury/statements/{id}", a.getStatement)
	r.With(authz.RequireDuty(dutyView)).Get("/v1/treasury/statements/{id}/lines", a.listStatementLines)
	r.With(authz.RequireDuty(dutyForecast)).Post("/v1/treasury/cash-forecast-entries", a.createForecastEntry)
	r.With(authz.RequireDuty(dutyView)).Get("/v1/treasury/cash-forecast", a.cashForecast)
	r.With(authz.RequireDuty(dutyCheckWrite)).Post("/v1/treasury/checks", a.issueCheck)
	r.With(authz.RequireDuty(dutyView)).Get("/v1/treasury/checks", a.listChecks)
	r.With(authz.RequireDuty(dutyView)).Get("/v1/treasury/checks/{id}", a.getCheck)
	r.With(authz.RequireDuty(dutyCheckWrite)).Post("/v1/treasury/checks/{id}:print", a.printCheck)
	r.With(authz.RequireDuty(dutyCheckWrite)).Post("/v1/treasury/checks/{id}:send", a.sendCheck)
	r.With(authz.RequireDuty(dutyCheckWrite)).Post("/v1/treasury/checks/{id}:clear", a.clearCheck)
	r.With(authz.RequireDuty(dutyCheckWrite)).Post("/v1/treasury/checks/{id}:void", a.voidCheck)
	r.With(authz.RequireDuty(dutyCheckWrite)).Post("/v1/treasury/checks/{id}:reprint", a.reprintCheck)
	r.With(authz.RequireDuty(dutyCheckWrite)).Post("/v1/treasury/checks/{id}:stop", a.stopCheck)
	r.With(authz.RequireDuty(dutyFacilityWrite)).Post("/v1/treasury/facilities", a.createFacility)
	r.With(authz.RequireDuty(dutyView)).Get("/v1/treasury/facilities", a.listFacilities)
	r.With(authz.RequireDuty(dutyView)).Get("/v1/treasury/facilities/{id}", a.getFacility)
	r.With(authz.RequireDuty(dutyFacilityWrite)).Post("/v1/treasury/facilities/{id}/instruments", a.createInstrument)
	r.With(authz.RequireDuty(dutyView)).Get("/v1/treasury/facilities/{id}/instruments", a.listInstruments)
	r.With(authz.RequireDuty(dutyFacilityWrite)).Post("/v1/treasury/facility-instruments/{id}:close", a.closeInstrument)
	r.With(authz.RequireDuty(dutyLoanWrite)).Post("/v1/treasury/loans", a.createLoan)
	r.With(authz.RequireDuty(dutyView)).Get("/v1/treasury/loans", a.listLoans)
	r.With(authz.RequireDuty(dutyView)).Get("/v1/treasury/loans/{id}", a.getLoan)
	r.With(authz.RequireDuty(dutyReconcile)).Post("/v1/treasury/reconciliation-rules", a.createReconRule)
	r.With(authz.RequireDuty(dutyView)).Get("/v1/treasury/reconciliation-rules", a.listReconRules)
	r.With(authz.RequireDuty(dutyView)).Get("/v1/treasury/reconciliation-rules/{id}", a.getReconRule)
	r.With(authz.RequireDuty(dutyReconcile)).Post("/v1/treasury/reconciliation-rules/{id}:deactivate", a.deactivateReconRule)
	r.With(authz.RequireDuty(dutyReconcile)).Post("/v1/treasury/reconciliation-runs", a.runReconciliation)
	r.With(authz.RequireDuty(dutyReconcile)).Post("/v1/treasury/reconciliation-matches", a.createManualMatch)
	r.With(authz.RequireDuty(dutyView)).Get("/v1/treasury/reconciliation-matches", a.listMatches)
	r.With(authz.RequireDuty(dutyView)).Get("/v1/treasury/reconciliation-matches/{id}", a.getMatch)
	r.With(authz.RequireDuty(dutyReconcile)).Post("/v1/treasury/reconciliation-matches/{id}:unmatch", a.unmatch)
	a.registerEBICS(r)
}

// Metrics serves the counters in Prometheus text format on the admin port.
func (a *API) Metrics() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		for _, m := range []struct {
			name, help string
			value      int64
		}{
			{"treasury_bank_accounts_created_total", "Bank account create requests answered (includes idempotent replays).", a.accountsCreated.Load()},
			{"treasury_bank_account_transitions_total", "Bank account status changes (activate, deactivate, close).", a.accountsTransitioned.Load()},
			{"treasury_statements_imported_total", "Statements stored as new.", a.statementsImported.Load()},
			{"treasury_statements_replayed_total", "Statement uploads answered with an existing statement.", a.statementsReplayed.Load()},
			{"treasury_statements_rejected_total", "Statement uploads rejected by parsing or rules.", a.statementsRejected.Load()},
			{"treasury_forecast_entries_total", "Cash forecast entries created.", a.forecastEntries.Load()},
			{"treasury_forecasts_built_total", "Cash forecasts computed.", a.forecastsBuilt.Load()},
			{"treasury_checks_issued_total", "Checks issued or reprinted on a new number.", a.checksIssued.Load()},
			{"treasury_checks_voided_total", "Checks voided, including the old check of a reprint.", a.checksVoided.Load()},
			{"treasury_facility_instruments_total", "Letters of credit and guarantees booked on a facility.", a.instrumentsBooked.Load()},
			{"treasury_facility_limit_overrides_total", "Instrument requests that asked to override the facility limit.", a.limitOverrides.Load()},
			{"treasury_loans_created_total", "Loan contracts created with their schedule.", a.loansCreated.Load()},
			{"treasury_reconciliation_rules_created_total", "Reconciliation rules created.", a.rulesCreated.Load()},
			{"treasury_reconciliation_runs_total", "Reconciliation runs completed.", a.reconRuns.Load()},
			{"treasury_reconciliation_auto_matches_total", "Matches stored by reconciliation runs.", a.autoMatches.Load()},
			{"treasury_reconciliation_manual_matches_total", "Manual matches stored.", a.manualMatches.Load()},
			{"treasury_reconciliation_unmatches_total", "Matches dissolved.", a.unmatches.Load()},
		} {
			fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s counter\n%s %d\n", m.name, m.help, m.name, m.name, m.value)
		}
	})
}

// Ready answers GET /readyz with a real Postgres check (500 ms budget).
func (a *API) Ready() http.HandlerFunc { return a.ready }

func (a *API) ready(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), readyTimeout)
	defer cancel()
	if err := a.store.Ping(ctx); err != nil {
		slog.Error("readiness check failed", "err", err, "correlationId", middleware.GetReqID(r.Context()))
		httpx.JSON(w, http.StatusServiceUnavailable, map[string]string{"status": "unavailable"})
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]string{"status": "ready"})
}

// validTenant rejects requests without a well-formed tenant (401) or legal entity scope (400).
func validTenant(w http.ResponseWriter, r *http.Request) bool {
	p, ok := tenant.FromContext(r.Context())
	if !ok || !ids.Valid(p.TenantID) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusUnauthorized, "AUTH-0001", "missing or malformed tenant"))
		return false
	}
	if p.LegalEntityID != "" && !ids.Valid(p.LegalEntityID) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusBadRequest, "COM-0001", "legal entity must be a single UUID"))
		return false
	}
	return true
}

// outsideScope refuses (403) a create for a legal entity other than the one a
// scoped caller works in. Row-level security would refuse the insert anyway,
// but as an unhandled SQLSTATE 42501 that reached the client as a 500.
func outsideScope(w http.ResponseWriter, r *http.Request, legalEntityID string) bool {
	if p, _ := tenant.FromContext(r.Context()); !tenant.InLegalEntityScope(p, legalEntityID) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusForbidden, "AUTH-0003", "the legal entity is outside the caller's scope"))
		return true
	}
	return false
}

// maxAmount bounds amounts to the 14 integer digits of numeric(20,6).
var maxAmount = decimal.New(1, 14)

// parseAmount reads a positive decimal string with no more places than the
// currency's minor unit (two for EUR, none for JPY), so a check or facility
// amount is never silently rounded by the database. An unknown currency is
// checked against two places; the currency itself is validated separately.
func parseAmount(raw, currency string) (decimal.Decimal, bool) {
	d, err := decimal.NewFromString(strings.TrimSpace(raw))
	if err != nil || !d.IsPositive() || !d.LessThan(maxAmount) {
		return decimal.Zero, false
	}
	return d, d.Equal(d.Round(money.MinorUnits(currency)))
}

func validationFailed(w http.ResponseWriter, r *http.Request, fields map[string]string) {
	p := httpx.NewProblem(http.StatusBadRequest, "COM-0001", validationDetail)
	p.Errors = fields
	httpx.Fail(w, r, p)
}

// problemFrom turns domain rule violations into problem details; other errors pass through.
func problemFrom(err error) error {
	var de *domain.Error
	if !errors.As(err, &de) {
		return err
	}
	status := http.StatusUnprocessableEntity
	if de.Code == domain.CodeStatementConflict {
		status = http.StatusConflict
	}
	p := httpx.NewProblem(status, de.Code, de.Detail)
	switch {
	case de.Line > 0:
		p.Errors = map[string]string{"line": strconv.Itoa(de.Line)}
	case de.Entry > 0:
		p.Errors = map[string]string{"entry": strconv.Itoa(de.Entry)}
	}
	return p
}

// pathID returns the {id} segment or writes a 404 when it is not a UUID.
func pathID(w http.ResponseWriter, r *http.Request, code, detail string) (string, bool) {
	id := chi.URLParam(r, "id")
	if !ids.Valid(id) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, code, detail))
		return "", false
	}
	return id, true
}

func logFailure(r *http.Request, op string, err error) {
	slog.Error(op+" failed", "err", err, "correlationId", middleware.GetReqID(r.Context()))
}
