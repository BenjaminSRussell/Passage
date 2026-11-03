// Package api exposes the sales HTTP surface under /v1/sales.
package api

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/sales/internal/store"
)

const (
	readyTimeout     = 500 * time.Millisecond
	idempotencyKey   = "Idempotency-Key"
	validationDetail = "request failed validation"
	maxLines         = 2000
	maxPeriods       = 1000
	defaultPageSize  = 50
	maxPageSize      = 200

	dutyRead           = "sales.order.read"
	dutyWrite          = "sales.order.write"
	dutyIntake         = "sales.channel.intake"
	dutyConfirm        = "sales.order.confirm"
	dutyHold           = "sales.order.hold"
	dutyCreditRelease  = "sales.credit.release"
	dutyCreditMaintain = "sales.credit.maintain"
	dutyParameter      = "sales.parameter.maintain"
	dutyAgreement      = "sales.agreement.maintain"
	dutyBackorder      = "sales.backorder.run"
	dutyListing        = "sales.listing.maintain"
	dutyOrderApprove   = "sales.order.approve"
	dutyQuotation      = "sales.quotation.maintain"
	dutyAllocation     = "sales.allocation.maintain"

	orders          = "/v1/sales/orders"
	creditProfiles  = "/v1/sales/credit-profiles"
	releaseCheck    = "/v1/sales/schedule-releases:check"
	backorderRun    = "/v1/sales/backorder-runs:preview"
	productListings = "/v1/sales/product-listings"
	agreements      = "/v1/sales/agreements"
	quotations      = "/v1/sales/quotations"
	holdQueue       = "/v1/sales/holds"
	suppRules       = "/v1/sales/supplementary-item-rules"
	suppEvaluate    = "/v1/sales/supplementary-items:evaluate"
	proposalRules   = "/v1/sales/product-proposal-rules"
	proposalEval    = "/v1/sales/product-proposals:evaluate"
	proposalRespond = "/v1/sales/product-proposals:respond"
	allocationQuota = "/v1/sales/allocation-quotas"
	parameters      = "/v1/sales/parameters"
)

var (
	currencyCode = regexp.MustCompile(`^[A-Z]{3}$`)
	channelName  = regexp.MustCompile(`^[A-Za-z0-9_-]{1,40}$`)
	orderTypes   = map[string]bool{"JOURNAL": true, "ORDER": true, "SUBSCRIPTION": true, "RETURNED": true}
)

// API bundles the handlers and their counters.
type API struct {
	store    store.Store
	now      func() time.Time
	created  atomic.Int64
	confirms atomic.Int64
	held     atomic.Int64
	applied  atomic.Int64
	released atomic.Int64
	atp      atomic.Int64
	changed  atomic.Int64
	canceled atomic.Int64
	failed   atomic.Int64
	planned  atomic.Int64

	configs    ConfigurationChecker
	production LinePlanner
}

// New returns the API over s.
func New(s store.Store) *API { return &API{store: s, now: time.Now} }

// anyDuty allows a request holding at least one of its duties.
type anyDuty []string

// Allow implements authz.Policy.
func (a anyDuty) Allow(_ context.Context, p tenant.Principal, _, _ string) (bool, error) {
	for _, d := range a {
		if p.HasDuty(d) {
			return true, nil
		}
	}
	return false, nil
}

func requireAny(duties ...string) func(http.Handler) http.Handler {
	return authz.Require(anyDuty(duties), "", "")
}

// Register mounts every route on r.
func (a *API) Register(r chi.Router) {
	r.Get("/readyz", a.ready)
	r.With(requireAny(dutyWrite, dutyIntake)).Post(orders, a.createOrder)
	r.With(authz.RequireDuty(dutyRead)).Get(orders, a.listOrders)
	r.With(authz.RequireAnyDuty(dutyRead, "sales.order.lookup")).Get(orders+"/{id}", a.getOrder)
	r.With(authz.RequireDuty(dutyConfirm)).Post(orders+"/{id}:confirm", a.confirmOrder)
	r.With(authz.RequireDuty(dutyWrite)).Patch(orders+"/{id}", a.patchOrder)
	r.With(authz.RequireDuty(dutyConfirm)).Post(orders+"/{id}:cancel", a.cancelOrder)
	r.With(authz.RequireDuty(dutyWrite)).Post(orders+"/{id}:cancel-remainder", a.cancelRemainder)
	r.With(authz.RequireDuty(dutyWrite)).Post(orders+"/{id}/lines/{lineNo}:cancel-remainder", a.cancelLineRemainder)
	r.With(authz.RequireDuty(dutyPlanProduction)).Post(orders+"/{id}:plan-production", a.planProduction)
	r.With(authz.RequireDuty(dutyRead)).Get(orders+"/{id}/versions", a.listOrderVersions)
	r.With(authz.RequireAnyDuty(dutyRecordDelivery, dutyRecordInvoice)).Post(orders+"/{id}:record-progress", a.recordProgress)
	r.With(authz.RequireDuty(dutyRead)).Get(orderProgress, a.listProgress)
	r.With(authz.RequireDuty(dutyWrite)).Post(orders+"/{id}/lines/{lineNo}:promise", a.promiseLine)
	r.With(authz.RequireDuty(dutyRead)).Get(orders+"/{id}/schedule-lines", a.listScheduleLines)
	r.With(authz.RequireDuty(dutyRead)).Get(backorders, a.listBackorders)
	r.With(authz.RequireDuty(dutyAgreement)).Post(releaseCheck, a.checkRelease)
	r.With(authz.RequireDuty(dutyBackorder)).Post(backorderRun, a.previewBackorder)
	r.With(authz.RequireDuty(dutyBackorder)).Post(backorderRuns, a.createBackorderRun)
	r.With(authz.RequireDuty(dutyRead)).Get(backorderRuns, a.listBackorderRuns)
	r.With(authz.RequireDuty(dutyRead)).Get(backorderRuns+"/{id}", a.getBackorderRun)
	r.With(authz.RequireDuty(dutyBackorder)).Post(backorderRuns+"/{id}:commit", a.commitBackorderRun)
	r.With(authz.RequireDuty(dutyHold)).Post(orders+"/{id}/holds", a.applyHold)
	r.With(requireAny(dutyCreditRelease, dutyHold)).Post(orders+"/{id}/holds/{holdId}:release", a.releaseHold)
	r.With(authz.RequireDuty(dutyListing)).Post(productListings, a.createListing)
	r.With(authz.RequireDuty(dutyRead)).Get(productListings, a.listListings)
	r.With(authz.RequireDuty(dutyListing)).Delete(productListings+"/{id}", a.deleteListing)
	r.With(authz.RequireDuty(dutyAgreement)).Post(agreements, a.createAgreement)
	r.With(authz.RequireDuty(dutyRead)).Get(agreements, a.listAgreements)
	r.With(authz.RequireDuty(dutyRead)).Get(agreements+"/{id}", a.getAgreement)
	r.With(authz.RequireDuty(dutyAgreement)).Patch(agreements+"/{id}", a.reviseAgreement)
	r.With(authz.RequireDuty(dutyAgreement)).Post(agreements+"/{id}:close", a.closeAgreement)
	r.With(authz.RequireDuty(dutyRead)).Get(agreements+"/{id}/versions", a.listAgreementVersions)
	r.With(authz.RequireDuty(dutyAgreement)).Post(agreements+"/{id}:release-order", a.releaseOrder)
	r.With(authz.RequireDuty(dutyAgreement)).Post(agreements+"/{id}/schedule-releases", a.createScheduleRelease)
	r.With(authz.RequireDuty(dutyRead)).Get(agreements+"/{id}/schedule-releases", a.listScheduleReleases)
	r.With(authz.RequireDuty(dutyRead)).Get(agreements+"/{id}/schedule-releases/{releaseId}", a.getScheduleRelease)
	r.With(authz.RequireDuty(dutyAgreement)).Post(agreements+"/{id}/schedule-releases/{releaseId}:resolve", a.resolveScheduleRelease)
	r.With(authz.RequireDuty(dutyAgreement)).Post(agreements+"/{id}:record-shipped", a.recordShipped)
	r.With(authz.RequireDuty(dutyRead)).Post("/v1/sales/atp:check", a.atpCheck)
	r.With(authz.RequireDuty(dutyRead)).Get(creditProfiles, a.listCreditProfiles)
	r.With(authz.RequireDuty(dutyRead)).Get(creditProfiles+"/{customerId}", a.getCreditProfile)
	r.With(authz.RequireDuty(dutyCreditMaintain)).Put(creditProfiles+"/{customerId}", a.putCreditProfile)
	r.With(authz.RequireDuty(dutyRead)).Get(changeRequests, a.listChangeRequests)
	r.With(authz.RequireDuty(dutyRead)).Get(changeRequests+"/{id}", a.getChangeRequest)
	r.With(authz.RequireDuty(dutyOrderApprove)).Post(changeRequests+"/{id}:approve", a.approveChangeRequest)
	r.With(authz.RequireDuty(dutyOrderApprove)).Post(changeRequests+"/{id}:reject", a.rejectChangeRequest)
	r.With(authz.RequireDuty(dutyWrite)).Post(changeRequests+"/{id}:withdraw", a.withdrawChangeRequest)
	r.With(authz.RequireDuty(dutyRead)).Get(changePolicy, a.getChangePolicy)
	r.With(authz.RequireDuty(dutyOrderApprove)).Put(changePolicy, a.putChangePolicy)
	r.With(authz.RequireDuty(dutyRead)).Get(parameters, a.getParameters)
	r.With(authz.RequireDuty(dutyParameter)).Put(parameters, a.putParameters)
	r.With(authz.RequireDuty(dutyQuotation)).Post(quotations, a.createQuotation)
	r.With(authz.RequireDuty(dutyRead)).Get(quotations, a.listQuotations)
	r.With(authz.RequireDuty(dutyRead)).Get(quotations+"/{id}", a.getQuotation)
	r.With(authz.RequireDuty(dutyQuotation)).Post(quotations+"/{id}:send", a.sendQuotation)
	r.With(authz.RequireDuty(dutyQuotation)).Post(quotations+"/{id}:lose", a.loseQuotation)
	r.With(authz.RequireDuty(dutyQuotation)).Post(quotations+"/{id}:convert", a.convertQuotation)
	r.With(authz.RequireDuty(dutyQuotation)).Post(expirationRun, a.expireQuotations)
	r.With(authz.RequireDuty(dutyRead)).Get(summaryPath, a.getSummary)
	r.With(requireAny(dutyCreditRelease, dutyHold)).Get(holdQueue, a.listHolds)
	r.With(authz.RequireDuty(dutyListing)).Post(suppRules, a.createSupplementaryRule)
	r.With(authz.RequireDuty(dutyRead)).Get(suppRules, a.listSupplementaryRules)
	r.With(authz.RequireDuty(dutyListing)).Delete(suppRules+"/{id}", a.deleteSupplementaryRule)
	r.With(authz.RequireDuty(dutyRead)).Post(suppEvaluate, a.evaluateSupplementary)
	r.With(authz.RequireDuty(dutyListing)).Post(proposalRules, a.createProposalRule)
	r.With(authz.RequireDuty(dutyRead)).Get(proposalRules, a.listProposalRules)
	r.With(authz.RequireDuty(dutyListing)).Delete(proposalRules+"/{id}", a.deleteProposalRule)
	r.With(authz.RequireDuty(dutyRead)).Post(proposalEval, a.evaluateProposals)
	r.With(requireAny(dutyWrite, dutyIntake)).Post(proposalRespond, a.respondToProposal)
	a.registerAlternatives(r)
	a.registerIntakeErrors(r)
	r.With(authz.RequireDuty(dutyAllocation)).Post(allocationQuota, a.createAllocationQuota)
	r.With(authz.RequireDuty(dutyRead)).Get(allocationQuota, a.listAllocationQuotas)
	r.With(authz.RequireDuty(dutyRead)).Get(allocationQuota+"/{id}", a.getAllocationQuota)
	r.With(authz.RequireDuty(dutyAllocation)).Patch(allocationQuota+"/{id}", a.changeAllocationQuota)
	r.With(authz.RequireDuty(dutyParameter)).Post(deliveryTerms, a.createDeliveryTerms)
	r.With(authz.RequireDuty(dutyRead)).Get(deliveryTerms, a.listDeliveryTerms)
	r.With(authz.RequireDuty(dutyRead)).Get(deliveryTerms+"/{id}", a.getDeliveryTerms)
	r.With(authz.RequireDuty(dutyParameter)).Put(deliveryTerms+"/{id}", a.replaceDeliveryTerms)
}

// Metrics serves the admin port's Prometheus text.
func (a *API) Metrics() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		fmt.Fprintf(w, "# HELP sales_orders_created_total Sales orders stored; idempotent replays are not counted.\n# TYPE sales_orders_created_total counter\nsales_orders_created_total %d\n", a.created.Load())
		fmt.Fprintf(w, "# HELP sales_orders_confirm_requests_total Confirm requests that passed validation.\n# TYPE sales_orders_confirm_requests_total counter\nsales_orders_confirm_requests_total %d\n", a.confirms.Load())
		fmt.Fprintf(w, "# HELP sales_credit_holds_applied_total CREDIT holds placed by the credit check.\n# TYPE sales_credit_holds_applied_total counter\nsales_credit_holds_applied_total %d\n", a.held.Load())
		fmt.Fprintf(w, "# HELP sales_holds_applied_total MANUAL, FRAUD and PRICE_REVIEW holds applied through the holds API.\n# TYPE sales_holds_applied_total counter\nsales_holds_applied_total %d\n", a.applied.Load())
		fmt.Fprintf(w, "# HELP sales_holds_released_total Holds released.\n# TYPE sales_holds_released_total counter\nsales_holds_released_total %d\n", a.released.Load())
		fmt.Fprintf(w, "# HELP sales_atp_checks_total Stateless ATP promise checks.\n# TYPE sales_atp_checks_total counter\nsales_atp_checks_total %d\n", a.atp.Load())
		fmt.Fprintf(w, "# HELP sales_orders_changed_total Order changes that produced a new version.\n# TYPE sales_orders_changed_total counter\nsales_orders_changed_total %d\n", a.changed.Load())
		fmt.Fprintf(w, "# HELP sales_orders_cancelled_total Orders cancelled.\n# TYPE sales_orders_cancelled_total counter\nsales_orders_cancelled_total %d\n", a.canceled.Load())
		fmt.Fprintf(w, "# HELP sales_production_orders_requested_total Production orders newly created for configured order lines.\n# TYPE sales_production_orders_requested_total counter\nsales_production_orders_requested_total %d\n", a.planned.Load())
		fmt.Fprintf(w, "# HELP sales_request_failures_total Requests that failed in the store.\n# TYPE sales_request_failures_total counter\nsales_request_failures_total %d\n", a.failed.Load())
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

// fail writes the problem. Expected outcomes (4xx problems such as a stale
// If-Match or a missing order) are written as they are; anything else is
// counted and logged as a store failure.
func (a *API) fail(w http.ResponseWriter, r *http.Request, op string, err error) {
	var p *httpx.Problem
	if !errors.As(err, &p) || p.Status >= http.StatusInternalServerError {
		a.failed.Add(1)
		slog.Error(op+" failed", "err", err, "correlationId", middleware.GetReqID(r.Context()))
	}
	httpx.Fail(w, r, err)
}

func validationFailed(w http.ResponseWriter, r *http.Request, fields map[string]string) {
	p := httpx.NewProblem(http.StatusBadRequest, "COM-0001", validationDetail)
	p.Errors = fields
	httpx.Fail(w, r, p)
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

// setETag sets the strong ETag of a versioned resource.
func setETag(w http.ResponseWriter, version int) {
	w.Header().Set("ETag", fmt.Sprintf("\"%d\"", version))
}

// ifMatch reads the required If-Match version. A missing header is 428 and a
// stale or malformed one is reported by the store or as 400.
func ifMatch(w http.ResponseWriter, r *http.Request) (int, bool) {
	raw := r.Header.Get("If-Match")
	if raw == "" {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusPreconditionRequired, store.CodeVersionConflict, "If-Match with the current ETag is required"))
		return 0, false
	}
	v, err := strconv.Atoi(strings.Trim(raw, `"`))
	if err != nil || v < 0 {
		validationFailed(w, r, map[string]string{"If-Match": "must be an ETag such as \"3\""})
		return 0, false
	}
	return v, true
}
