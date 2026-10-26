package api

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/party/internal/domain"
	"github.com/BenjaminSRussell/passage/services/party/internal/store"
)

const (
	checkTimeout = 5 * time.Second
	readyTimeout = 500 * time.Millisecond
	dutyWrite    = "party.write"
	dutyRead     = "party.read"
	dutyPII      = "pii.read"
	codeInvalid  = "PTY-0001"
)

var reportedStatuses = []domain.Status{domain.StatusValid, domain.StatusInvalid, domain.StatusUnverified, domain.StatusFormatOnly}

type Pinger interface{ Ping(context.Context) error }

type Sequencer interface {
	Next(ctx context.Context, code, idempotencyKey string) (string, error)
}

type API struct {
	pinger   Pinger
	parties  store.Store
	sequence Sequencer
	created  atomic.Int64
	checker  loggedChecker
	statuses map[domain.Status]*atomic.Int64
	rejected atomic.Int64
	failures atomic.Int64
}

type validateRequest struct {
	Type    string `json:"type"`
	Country string `json:"country"`
	Number  string `json:"number"`
}

type validateResponse struct {
	Type             domain.TaxType `json:"type"`
	Country          string         `json:"country,omitempty"`
	Number           string         `json:"number"`
	ValidationStatus domain.Status  `json:"validationStatus"`
}

type loggedChecker struct {
	inner    domain.Checker
	failures *atomic.Int64
}

func (c loggedChecker) Check(ctx context.Context, country, number string) (domain.Status, error) {
	status, err := c.inner.Check(ctx, country, number)
	if err != nil {
		c.failures.Add(1)
		slog.Error("tax checker failed, registration left unverified", "err", err, "country", country, "correlationId", middleware.GetReqID(ctx))
	}
	return status, err
}

func New(checker domain.Checker, pinger Pinger, parties store.Store, sequence Sequencer) *API {
	a := &API{pinger: pinger, parties: parties, sequence: sequence, statuses: map[domain.Status]*atomic.Int64{}}
	for _, s := range reportedStatuses {
		a.statuses[s] = new(atomic.Int64)
	}
	a.checker = loggedChecker{inner: checker, failures: &a.failures}
	return a
}

func (a *API) Register(r chi.Router) {
	r.Get("/readyz", a.ready)
	r.With(authz.RequireDuty(dutyWrite)).Post("/v1/party/tax-registrations:validate", a.validate)
	r.With(authz.RequireDuty(dutyWrite)).Post("/v1/party/parties", a.createParty)
	r.With(authz.RequireDuty(dutyRead)).Get("/v1/party/parties/{id}", a.getParty)
	r.With(authz.RequireDuty(dutyWrite)).Patch("/v1/party/parties/{id}", a.patchParty)
}

func (a *API) Metrics() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		fmt.Fprint(w, "# HELP party_tax_validations_total Tax registration validations by resulting status.\n# TYPE party_tax_validations_total counter\n")
		for _, s := range reportedStatuses {
			fmt.Fprintf(w, "party_tax_validations_total{status=%q} %d\n", s, a.statuses[s].Load())
		}
		fmt.Fprintf(w, "# HELP party_tax_validations_rejected_total Tax validation requests rejected for malformed requests, tenant context, or tax format.\n# TYPE party_tax_validations_rejected_total counter\nparty_tax_validations_rejected_total %d\n", a.rejected.Load())
		fmt.Fprintf(w, "# HELP party_tax_checker_failures_total Checker calls that failed and left the registration unverified.\n# TYPE party_tax_checker_failures_total counter\nparty_tax_checker_failures_total %d\n", a.failures.Load())
		fmt.Fprintf(w, "# HELP party_parties_created_total Parties created; an idempotent replay is not counted.\n# TYPE party_parties_created_total counter\nparty_parties_created_total %d\n", a.created.Load())
	})
}

func (a *API) ready(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), readyTimeout)
	defer cancel()
	if err := a.pinger.Ping(ctx); err != nil {
		slog.Error("readiness check failed", "err", err, "correlationId", middleware.GetReqID(r.Context()))
		httpx.JSON(w, http.StatusServiceUnavailable, map[string]string{"status": "unavailable"})
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]string{"status": "ready"})
}

func (a *API) validate(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		a.rejected.Add(1)
		return
	}
	var req validateRequest
	if err := httpx.Decode(r, &req); err != nil {
		a.rejected.Add(1)
		httpx.Fail(w, r, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), checkTimeout)
	defer cancel()
	taxType := domain.TaxType(req.Type)
	status, err := domain.Validate(ctx, a.checker, taxType, req.Country, req.Number)
	if err != nil {
		a.rejected.Add(1)
		httpx.Fail(w, r, formatProblem(err))
		return
	}
	if _, ok := a.statuses[status]; !ok {
		slog.Error("tax checker returned unknown status", "status", status)
		status = domain.StatusUnverified
	}
	a.statuses[status].Add(1)
	httpx.JSON(w, http.StatusOK, validateResponse{
		Type:             taxType,
		Country:          domain.VATCountry(req.Country),
		Number:           domain.NormalizeTaxNumber(req.Number),
		ValidationStatus: status,
	})
}

func formatProblem(err error) error {
	var formatErr *domain.FormatError
	if !errors.As(err, &formatErr) {
		return err
	}
	p := httpx.NewProblem(http.StatusUnprocessableEntity, codeInvalid, "tax registration failed validation")
	p.Errors = map[string]string{formatErr.Field: formatErr.Reason}
	return p
}

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
