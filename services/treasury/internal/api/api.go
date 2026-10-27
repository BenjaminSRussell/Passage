// Package api exposes the treasury HTTP surface under /v1/treasury.
package api

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/treasury/internal/domain"
	"github.com/BenjaminSRussell/passage/services/treasury/internal/store"
)

const (
	readyTimeout       = 500 * time.Millisecond
	idempotencyKey     = "Idempotency-Key"
	dutyView           = "treasury.view"
	dutyAccountWrite   = "treasury.bank-account.write"
	dutyAccountApprove = "treasury.bank-account.approve"
	dutyStatement      = "treasury.statement.import"
	dutyForecast       = "treasury.forecast.write"
	validationDetail   = "request failed validation"
	defaultMaxFileMB   = 50
	maxFileMBLimit     = 200
)

// API holds the handlers' dependencies and counters for /metrics.
type API struct {
	store        store.Store
	maxFileBytes int64
	now          func() time.Time

	accountsCreated      atomic.Int64
	accountsTransitioned atomic.Int64
	statementsImported   atomic.Int64
	statementsReplayed   atomic.Int64
	statementsRejected   atomic.Int64
	forecastEntries      atomic.Int64
	forecastsBuilt       atomic.Int64
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
	if de.Line > 0 {
		p.Errors = map[string]string{"line": strconv.Itoa(de.Line)}
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
