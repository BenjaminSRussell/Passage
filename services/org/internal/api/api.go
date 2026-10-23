package api

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/org/internal/domain"
	"github.com/BenjaminSRussell/passage/services/org/internal/store"
)

const (
	readyTimeout     = 500 * time.Millisecond
	idempotencyKey   = "Idempotency-Key"
	dutyRead         = "org.read"
	dutyLegalEntity  = "org.legal-entity.write"
	maxNameLength    = 120
	maxQuantityExp   = 18
	minQuantityExp   = -24
	validationDetail = "request failed validation"
)

var (
	legalEntityCode = regexp.MustCompile(`^[A-Z0-9]{2,8}$`)
	countryCode     = regexp.MustCompile(`^[A-Z]{2}$`)
	currencyCode    = regexp.MustCompile(`^[A-Z]{3}$`)
	reservedCodes   = map[string]bool{"DAT": true, "SYS": true, "ALL": true}
	maxQuantity     = decimal.New(1, maxQuantityExp)
)

type API struct {
	store       store.Store
	conversions []domain.Conversion
	created     atomic.Int64
	failed      atomic.Int64
}

type createLegalEntityRequest struct {
	Code     string `json:"code"`
	Name     string `json:"name"`
	Country  string `json:"country"`
	Currency string `json:"currency"`
}

type convertResponse struct {
	From     string          `json:"from"`
	To       string          `json:"to"`
	Quantity decimal.Decimal `json:"quantity"`
}

func New(s store.Store) *API { return &API{store: s, conversions: domain.StandardConversions()} }

func (a *API) Register(r chi.Router) {
	r.Get("/readyz", a.ready)
	r.With(authz.RequireDuty(dutyLegalEntity)).Post("/v1/org/legal-entities", a.createLegalEntity)
	r.With(authz.RequireDuty(dutyRead)).Get("/v1/org/legal-entities", a.listLegalEntities)
	r.Get("/v1/org/uom/convert", a.convert)
}

func (a *API) Metrics() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		fmt.Fprintf(w, "# HELP org_legal_entities_created_total Legal entities created.\n# TYPE org_legal_entities_created_total counter\norg_legal_entities_created_total %d\n", a.created.Load())
		fmt.Fprintf(w, "# HELP org_legal_entities_failed_total Legal entity creations that failed in the store.\n# TYPE org_legal_entities_failed_total counter\norg_legal_entities_failed_total %d\n", a.failed.Load())
	})
}

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

func (a *API) createLegalEntity(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var req createLegalEntityRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	in := store.LegalEntity{Code: req.Code, Name: strings.TrimSpace(req.Name), Country: req.Country, Currency: req.Currency}
	if fields := validateLegalEntity(in); len(fields) > 0 {
		p := httpx.NewProblem(http.StatusBadRequest, "COM-0001", validationDetail)
		p.Errors = fields
		httpx.Fail(w, r, p)
		return
	}
	entity, err := a.store.CreateLegalEntity(r.Context(), in, r.Header.Get(idempotencyKey))
	if err != nil {
		a.failed.Add(1)
		slog.Error("create legal entity failed", "err", err, "correlationId", middleware.GetReqID(r.Context()))
		httpx.Fail(w, r, err)
		return
	}
	a.created.Add(1)
	httpx.JSON(w, http.StatusCreated, entity)
}

func (a *API) listLegalEntities(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	entities, err := a.store.ListLegalEntities(r.Context())
	if err != nil {
		slog.Error("list legal entities failed", "err", err, "correlationId", middleware.GetReqID(r.Context()))
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, entities)
}

func (a *API) convert(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	q := r.URL.Query()
	from, to := q.Get("from"), q.Get("to")
	qty, err := decimal.NewFromString(q.Get("qty"))
	if from == "" || to == "" || err != nil || qty.Exponent() > maxQuantityExp || qty.Exponent() < minQuantityExp || qty.Abs().GreaterThan(maxQuantity) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusBadRequest, "COM-0001", "from, to and a finite decimal qty are required"))
		return
	}
	result, err := domain.Convert(a.conversions, from, to, qty)
	if err != nil {
		httpx.Fail(w, r, problemFrom(err))
		return
	}
	httpx.JSON(w, http.StatusOK, convertResponse{From: from, To: to, Quantity: domain.Display(result, domain.DisplayPrecision)})
}

func problemFrom(err error) error {
	var de *domain.Error
	if errors.As(err, &de) {
		return httpx.NewProblem(http.StatusUnprocessableEntity, de.Code, de.Detail)
	}
	return err
}

func validateLegalEntity(in store.LegalEntity) map[string]string {
	fields := map[string]string{}
	if !legalEntityCode.MatchString(in.Code) || reservedCodes[in.Code] {
		fields["code"] = "must be 2 to 8 characters of A-Z or 0-9 and not DAT, SYS or ALL"
	}
	if n := utf8.RuneCountInString(in.Name); n == 0 || n > maxNameLength {
		fields["name"] = fmt.Sprintf("must be 1 to %d characters", maxNameLength)
	}
	if !countryCode.MatchString(in.Country) {
		fields["country"] = "must be an ISO 3166-1 alpha-2 code"
	}
	if !currencyCode.MatchString(in.Currency) {
		fields["currency"] = "must be an ISO 4217 alpha-3 code"
	}
	return fields
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
