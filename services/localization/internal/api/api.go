// Package api exposes the localization HTTP surface: pack catalogue, install
// checks, tax-id validation and statutory fixture validation (FR-48.1, FR-48.2).
package api

import (
	"errors"
	"fmt"
	"net/http"
	"sort"
	"sync/atomic"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/localization/internal/domain"
)

const (
	dutyAdmin    = "LocalizationAdmin"
	codeNotFound = "LOC-0506"
)

// API serves the pack catalogue loaded at start-up. It is stateless in slice 1:
// installs per legal entity need the store (later slice).
type API struct {
	packs   map[string]domain.Pack
	checks  atomic.Int64
	rejects atomic.Int64
}

// New builds the API over the loaded packs.
func New(packs map[string]domain.Pack) *API {
	return &API{packs: packs}
}

// InstallCheckRequest is the body of POST /v1/localization/packs/{code}/install:check.
type InstallCheckRequest struct {
	EntityCountry        string `json:"entityCountry"`
	PrimaryInstalled     int    `json:"primaryInstalled"`
	CalendarOpenPeriodOK bool   `json:"calendarOpenPeriodsOk"`
}

// TaxIDRequest is the body of POST /v1/localization/tax-ids:validate.
type TaxIDRequest struct {
	Scheme string `json:"scheme"`
	Value  string `json:"value"`
}

// Register mounts the routes on r.
func (a *API) Register(r chi.Router) {
	r.Get("/readyz", func(w http.ResponseWriter, _ *http.Request) {
		httpx.JSON(w, http.StatusOK, map[string]any{"status": "ready", "packs": len(a.packs)})
	})
	r.With(authz.RequireDuty(dutyAdmin)).Get("/v1/localization/packs", a.listPacks)
	r.With(authz.RequireDuty(dutyAdmin)).Get("/v1/localization/packs/{code}", a.getPack)
	r.With(authz.RequireDuty(dutyAdmin)).Post("/v1/localization/packs/{code}/install:check", a.checkInstall)
	r.With(authz.RequireDuty(dutyAdmin)).Post("/v1/localization/tax-ids:validate", a.validateTaxID)
	r.With(authz.RequireDuty(dutyAdmin)).Post("/v1/localization/statutory/uk-mtd-vat:validate", a.validateMTDVAT)
}

func (a *API) listPacks(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	codes := make([]string, 0, len(a.packs))
	for c := range a.packs {
		codes = append(codes, c)
	}
	sort.Strings(codes)
	items := make([]domain.Pack, 0, len(codes))
	for _, c := range codes {
		items = append(items, a.packs[c])
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
}

func (a *API) getPack(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	p, ok := a.packs[chi.URLParam(r, "code")]
	if !ok {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, codeNotFound, "no country pack with that code"))
		return
	}
	httpx.JSON(w, http.StatusOK, p)
}

func (a *API) checkInstall(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	p, ok := a.packs[chi.URLParam(r, "code")]
	if !ok {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, codeNotFound, "no country pack with that code"))
		return
	}
	var req InstallCheckRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if req.PrimaryInstalled < 0 || len(req.EntityCountry) != 2 {
		httpx.Fail(w, r, validation("entityCountry must be an ISO 3166 alpha-2 code and primaryInstalled >= 0"))
		return
	}
	a.checks.Add(1)
	if err := domain.CheckInstall(p, req.EntityCountry, req.PrimaryInstalled, req.CalendarOpenPeriodOK); err != nil {
		a.rejects.Add(1)
		httpx.Fail(w, r, domainProblem(err))
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"installable": true, "pack": p.Code, "version": p.Version})
}

func (a *API) validateTaxID(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var req TaxIDRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if err := domain.ValidateTaxID(req.Scheme, req.Value); err != nil {
		httpx.Fail(w, r, domainProblem(err))
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"valid": true, "scheme": req.Scheme})
}

func (a *API) validateMTDVAT(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var req domain.VATReturn
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	failures := domain.ValidateMTDVAT(req)
	if len(failures) == 0 {
		httpx.JSON(w, http.StatusOK, map[string]any{"valid": true, "errors": 0})
		return
	}
	p := httpx.NewProblem(http.StatusUnprocessableEntity, domain.CodeSchema,
		fmt.Sprintf("output fails schema UK_MTD_VAT: %d errors (first: %s %s)", len(failures), failures[0].Field, failures[0].Detail))
	p.Errors = map[string]string{}
	for _, f := range failures {
		p.Errors[f.Field] = f.Detail
	}
	httpx.Fail(w, r, p)
}

func validation(detail string) error {
	return httpx.NewProblem(http.StatusBadRequest, "COM-0002", detail)
}

func domainProblem(err error) error {
	var de *domain.Error
	if errors.As(err, &de) {
		return httpx.NewProblem(http.StatusUnprocessableEntity, de.Code, de.Detail)
	}
	return err
}

func validTenant(w http.ResponseWriter, r *http.Request) bool {
	p, ok := tenant.FromContext(r.Context())
	if !ok || !ids.Valid(p.TenantID) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusUnauthorized, "AUTH-0001", "missing or malformed tenant"))
		return false
	}
	return true
}

// Metrics serves the admin-port counters.
func (a *API) Metrics() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		fmt.Fprintf(w, "# TYPE localization_install_checks_total counter\nlocalization_install_checks_total %d\n", a.checks.Load())
		fmt.Fprintf(w, "# TYPE localization_install_rejections_total counter\nlocalization_install_rejections_total %d\n", a.rejects.Load())
	})
}
