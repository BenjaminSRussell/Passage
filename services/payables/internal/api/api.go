package api

import (
	"context"
	"encoding/json"
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
	"github.com/BenjaminSRussell/passage/services/payables/internal/domain"
	"github.com/BenjaminSRussell/passage/services/payables/internal/store"
)

const (
	readyTimeout   = 500 * time.Millisecond
	dutySettleRead = "settlement.read"
)

type Pinger interface {
	Ping(ctx context.Context) error
}

type API struct {
	db           Pinger
	files        FileValidator
	now          func() time.Time
	filePreviews atomic.Int64
	statusParses atomic.Int64
	previews     atomic.Int64
	clientErrors atomic.Int64
	serverFaults atomic.Int64

	fileClientErrors    atomic.Int64
	fileServerFaults    atomic.Int64
	http                requestMetrics
	aging               *agingDeps
	proposals           *proposalDeps
	runs                *runDeps
	withholding         *withholdingDeps
	withholdingRecorder WithholdingRecorder
	employeeVendors     *employeeVendorDeps
	opening             *openingDeps
	vendorPayments      store.VendorPaymentStore
	// vendorGroupDim is the ledger dimension attribute that carries the vendor group code on the AP control account; empty posts and compares none.
	vendorGroupDim string
}

type problemBody struct {
	Type        string            `json:"type"`
	Title       string            `json:"title"`
	Status      int               `json:"status"`
	Detail      string            `json:"detail,omitempty"`
	Instance    string            `json:"instance,omitempty"`
	Code        string            `json:"code"`
	ServiceCode string            `json:"serviceCode"`
	TraceID     string            `json:"traceId,omitempty"`
	Errors      map[string]string `json:"errors,omitempty"`
}

var problemSlugs = map[string]string{
	domain.CodeValidation:  "validation_failed",
	domain.CodeCurrency:    "validation_failed",
	"COM-0001":             "validation_failed",
	"AUTH-0001":            "forbidden_tenant",
	domain.CodeRunInvalid:  "validation_failed",
	domain.CodeFileInvalid: "validation_failed",
	codeBodyTooLarge:       "payload_too_large",
}

var unprocessableCodes = map[string]bool{
	domain.CodeCurrency:    true,
	domain.CodeRunInvalid:  true,
	domain.CodeFileInvalid: true,
	domain.CodeRunConfig:   true,
	domain.CodeRunPayee:    true,
	domain.CodeWithholding: true,
}

func New(db Pinger, files FileValidator, now func() time.Time) *API {
	return &API{db: db, files: files, now: now, http: newRequestMetrics(), vendorGroupDim: DefaultVendorGroupDimension}
}

// DefaultVendorGroupDimension is the ledger dimension attribute for the vendor group code (issue #421).
const DefaultVendorGroupDimension = "VENDGRP"

// WithVendorGroupDimension names the ledger dimension attribute that carries the vendor group code; "" turns it off.
func (a *API) WithVendorGroupDimension(attr string) *API {
	a.vendorGroupDim = attr
	return a
}

func (a *API) Register(r chi.Router) {
	r.Get("/readyz", a.ready)
	r.With(authz.RequireDuty(dutySettleRead)).Post("/v1/payables/settlements:preview", a.preview)
	r.With(authz.RequireDuty(dutyPaymentFilePreview)).Post(pathPaymentFilePreview, a.previewPaymentFile)
	r.With(authz.RequireDuty(dutyPaymentStatusParse)).Post(pathPaymentStatusParse, a.parsePaymentStatus)
	a.registerAging(r)
	a.registerProposals(r)
	a.registerRuns(r)
	a.registerEmployeeVendors(r)
	a.registerOpening(r)
	a.registerVendorPayments(r)
}

func (a *API) ready(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), readyTimeout)
	defer cancel()
	if err := a.db.Ping(ctx); err != nil {
		slog.Error("readiness check failed", "err", err, "correlationId", middleware.GetReqID(r.Context()))
		httpx.JSON(w, http.StatusServiceUnavailable, map[string]string{"status": "unavailable"})
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]string{"status": "ready"})
}

func (a *API) preview(w http.ResponseWriter, r *http.Request) {
	if !a.validTenant(w, r) {
		return
	}
	var dto domain.RequestDTO
	if err := httpx.Decode(r, &dto); err != nil {
		a.reject(w, r, err)
		return
	}
	req, err := domain.ParseRequest(dto, a.now())
	if err != nil {
		a.reject(w, r, err)
		return
	}
	res, err := domain.Settle(req)
	if err != nil {
		a.serverFaults.Add(1)
		slog.Error("settlement preview failed", "err", err, "correlationId", middleware.GetReqID(r.Context()))
		fail(w, r, err)
		return
	}
	a.previews.Add(1)
	httpx.JSON(w, http.StatusOK, res)
}

func (a *API) countClientError(r *http.Request) {
	if r.URL.Path == pathPaymentFilePreview || r.URL.Path == pathPaymentStatusParse {
		a.fileClientErrors.Add(1)
		return
	}
	a.clientErrors.Add(1)
}

func (a *API) reject(w http.ResponseWriter, r *http.Request, err error) {
	a.countClientError(r)
	slog.Warn("request rejected", "err", err, "path", r.URL.Path, "correlationId", middleware.GetReqID(r.Context()))
	var de *domain.Error
	if errors.As(err, &de) {
		status := http.StatusBadRequest
		if unprocessableCodes[de.Code] {
			status = http.StatusUnprocessableEntity
		}
		err = httpx.NewProblem(status, de.Code, de.Detail)
	}
	fail(w, r, err)
}

func fail(w http.ResponseWriter, r *http.Request, err error) {
	var p *httpx.Problem
	if !errors.As(err, &p) {
		slog.Error("unhandled error", "err", err, "path", r.URL.Path, "correlationId", middleware.GetReqID(r.Context()))
		p = httpx.NewProblem(http.StatusInternalServerError, "COM-0500", "internal error")
	}
	code := problemSlugs[p.Code]
	if code == "" {
		code = p.Code
	}
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(p.Status)
	_ = json.NewEncoder(w).Encode(problemBody{
		Type: p.Type, Title: p.Title, Status: p.Status, Detail: p.Detail, Instance: r.URL.Path,
		Code: code, ServiceCode: p.Code, TraceID: middleware.GetReqID(r.Context()), Errors: p.Errors,
	})
}

func (a *API) validTenant(w http.ResponseWriter, r *http.Request) bool {
	p, ok := tenant.FromContext(r.Context())
	if !ok || !ids.Valid(p.TenantID) {
		a.countClientError(r)
		fail(w, r, httpx.NewProblem(http.StatusUnauthorized, "AUTH-0001", "missing or malformed tenant"))
		return false
	}
	if p.LegalEntityID != "" && !ids.Valid(p.LegalEntityID) {
		a.countClientError(r)
		fail(w, r, httpx.NewProblem(http.StatusBadRequest, "COM-0001", "legal entity must be a single UUID"))
		return false
	}
	return true
}

func (a *API) Metrics() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		tenant.WriteReplayMetrics(w)
		fmt.Fprintf(w, "# HELP payables_settlement_previews_total Successful settlement previews.\n# TYPE payables_settlement_previews_total counter\npayables_settlement_previews_total %d\n", a.previews.Load())
		fmt.Fprintf(w, "# HELP payables_settlement_preview_client_errors_total Previews rejected as invalid input (4xx).\n# TYPE payables_settlement_preview_client_errors_total counter\npayables_settlement_preview_client_errors_total %d\n", a.clientErrors.Load())
		fmt.Fprintf(w, "# HELP payables_settlement_preview_server_faults_total Previews that failed on a server fault (5xx).\n# TYPE payables_settlement_preview_server_faults_total counter\npayables_settlement_preview_server_faults_total %d\n", a.serverFaults.Load())
		fmt.Fprintf(w, "# HELP payables_payment_file_previews_total Successful pain.001 previews.\n# TYPE payables_payment_file_previews_total counter\npayables_payment_file_previews_total %d\n", a.filePreviews.Load())
		fmt.Fprintf(w, "# HELP payables_payment_status_parses_total Successful pain.002 status parses.\n# TYPE payables_payment_status_parses_total counter\npayables_payment_status_parses_total %d\n", a.statusParses.Load())
		fmt.Fprintf(w, "# HELP payables_payment_file_client_errors_total Preview and status-parse requests rejected as invalid input (4xx).\n# TYPE payables_payment_file_client_errors_total counter\npayables_payment_file_client_errors_total %d\n", a.fileClientErrors.Load())
		fmt.Fprintf(w, "# HELP payables_payment_file_server_faults_total Preview and status-parse requests that failed on a server fault (5xx).\n# TYPE payables_payment_file_server_faults_total counter\npayables_payment_file_server_faults_total %d\n", a.fileServerFaults.Load())
		a.writeAgingMetrics(w)
		a.writeProposalMetrics(w)
		a.writeRunMetrics(w)
		a.writeWithholdingMetrics(w)
		a.writeOpeningMetrics(w)
		if a.employeeVendors != nil {
			fmt.Fprintf(w, "# HELP payables_employee_vendors_registered_total Workers newly mapped to a vendor in the employee-vendor register.\n# TYPE payables_employee_vendors_registered_total counter\npayables_employee_vendors_registered_total %d\n", a.employeeVendors.registered.Load())
		}
		a.http.write(w)
	})
}
