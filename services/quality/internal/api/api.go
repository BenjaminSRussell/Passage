// Package api holds the HTTP handlers of the quality service: decode, validate,
// check the tenant and duty, then call the domain and store.
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
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/quality/internal/domain"
	"github.com/BenjaminSRussell/passage/services/quality/internal/store"
)

const (
	readyTimeout       = 500 * time.Millisecond
	idempotencyKey     = "Idempotency-Key"
	dutySetup          = "quality.setup.maintain"
	dutyOrder          = "quality.order.maintain"
	dutyResults        = "quality.result.record"
	dutyDecision       = "quality.usage.decide"
	dutyView           = "quality.view"
	validationDetail   = "request failed validation"
	maxResultsPerBatch = 1000
	maxRefLength       = 200
	defaultListLimit   = 100
	maxListLimit       = 500
	codeKeyReuse       = store.CodeKeyReuse
)

// API wires the handlers to a store.
type API struct {
	store                store.Store
	ordersCreated        atomic.Int64
	resultsRecorded      atomic.Int64
	decisionsPosted      atomic.Int64
	instrumentRefuse     atomic.Int64
	evaluations          atomic.Int64
	studiesCreated       atomic.Int64
	ncrsCreated          atomic.Int64
	certificatesRecorded atomic.Int64
	samplesRegistered    atomic.Int64
	receiptChecks        atomic.Int64
	coasIssued           atomic.Int64
}

// New returns the API over s.
func New(s store.Store) *API { return &API{store: s} }

// Register mounts every route on r.
func (a *API) Register(r chi.Router) {
	r.Get("/readyz", a.ready)
	r.With(authz.RequireDuty(dutyView)).Post("/v1/quality/sampling-plans:size", a.sizeSample)
	r.With(authz.RequireDuty(dutySetup)).Post("/v1/quality/test-groups", a.createTestGroup)
	r.With(authz.RequireDuty(dutyView)).Get("/v1/quality/test-groups", a.listTestGroups)
	r.With(authz.RequireDuty(dutyView)).Get("/v1/quality/test-groups/{id}", a.getTestGroup)
	r.With(authz.RequireDuty(dutySetup)).Post("/v1/quality/instruments", a.createInstrument)
	r.With(authz.RequireDuty(dutyView)).Get("/v1/quality/instruments", a.listInstruments)
	r.With(authz.RequireDuty(dutySetup)).Post("/v1/quality/instruments/{id}:calibrate", a.calibrateInstrument)
	r.With(authz.RequireDuty(dutySetup)).Post("/v1/quality/associations", a.createAssociation)
	r.With(authz.RequireDuty(dutyView)).Get("/v1/quality/associations", a.listAssociations)
	r.With(authz.RequireDuty(dutyOrder)).Post("/v1/quality/quality-orders", a.createOrder)
	r.With(authz.RequireDuty(dutyView)).Get("/v1/quality/quality-orders", a.listOrders)
	r.With(authz.RequireDuty(dutyView)).Get("/v1/quality/quality-orders/{id}", a.getOrder)
	r.With(authz.RequireDuty(dutyResults)).Post("/v1/quality/quality-orders/{id}/results", a.recordResults)
	r.With(authz.RequireDuty(dutyDecision)).Post("/v1/quality/quality-orders/{id}:usage-decision", a.postDecision)
	r.With(authz.RequireDuty(dutyResults)).Post("/v1/quality/quality-orders/{id}/certificates", a.recordCertificate)
	r.With(authz.RequireDuty(dutyView)).Get("/v1/quality/quality-orders/{id}/certificates", a.listCertificates)
	r.With(authz.RequireDuty(dutyDecision)).Post("/v1/quality/quality-orders/{id}/certificates:waive", a.waiveCertificate)
	r.With(authz.RequireDuty(dutyResults)).Post("/v1/quality/samples", a.registerSample)
	r.With(authz.RequireDuty(dutyView)).Get("/v1/quality/samples", a.listSamples)
	r.With(authz.RequireDuty(dutyCompliance)).Post("/v1/quality/compliance-lists", a.createComplianceList)
	r.With(authz.RequireDuty(dutyView)).Get("/v1/quality/compliance-lists", a.listComplianceLists)
	r.With(authz.RequireDuty(dutyCompliance)).Post("/v1/quality/items/{id}/composition", a.saveComposition)
	r.With(authz.RequireDuty(dutyView)).Get("/v1/quality/items/{id}/composition", a.getComposition)
	r.With(authz.RequireDuty(dutyView)).Post("/v1/quality/items/{id}:evaluate-compliance", a.evaluateCompliance)
	r.With(authz.RequireDuty(dutyStability)).Post("/v1/quality/stability-studies", a.createStudy)
	r.With(authz.RequireDuty(dutyView)).Get("/v1/quality/stability-studies", a.listStudies)
	r.With(authz.RequireDuty(dutyView)).Get("/v1/quality/stability-studies/{id}", a.getStudy)
	r.With(authz.RequireDuty(dutyStability)).Post("/v1/quality/stability-studies/{id}:transition", a.transitionStudy)
	r.With(authz.RequireDuty(dutyOrder)).Post("/v1/quality/receipt-checks", a.checkReceipt)
	r.With(authz.RequireDuty(dutyView)).Get("/v1/quality/receipt-checks", a.listReceiptChecks)
	r.With(authz.RequireDuty(dutyCoA)).Post("/v1/quality/coa:generate", a.generateCoA)
	r.With(authz.RequireDuty(dutyView)).Get("/v1/quality/quality-orders/{id}/coas", a.listCoAs)
	r.With(authz.RequireDuty(dutyView)).Get("/v1/quality/spc", a.spcChart)
	r.With(authz.RequireDuty(dutyNCR)).Post("/v1/quality/nonconformances", a.createNonconformance)
	r.With(authz.RequireDuty(dutyView)).Get("/v1/quality/nonconformances", a.listNonconformances)
	r.With(authz.RequireAnyDuty(dutyView, dutyCAPAExecute)).Get("/v1/quality/nonconformances/{id}", a.getNonconformance)
	r.With(authz.RequireDuty(dutyNCR)).Post("/v1/quality/nonconformances/{id}:approve", a.approveNonconformance)
	r.With(authz.RequireDuty(dutyNCR)).Post("/v1/quality/nonconformances/{id}:refuse", a.refuseNonconformance)
	r.With(authz.RequireAnyDuty(dutyCAPA, dutyCAPAExecute)).Post("/v1/quality/nonconformances/{id}/corrections", a.startCorrection)
	r.With(authz.RequireAnyDuty(dutyCAPA, dutyCAPAExecute)).Post("/v1/quality/nonconformances/{id}:complete-step", a.completeStep)
	r.With(authz.RequireAnyDuty(dutyCAPA, dutyCAPAExecute)).Post("/v1/quality/nonconformances/{id}:check-effectiveness", a.checkEffectiveness)
}

// Metrics serves the admin port's Prometheus text output.
func (a *API) Metrics() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		fmt.Fprintf(w, "# HELP quality_orders_created_total Quality orders created; idempotent replays are not counted.\n# TYPE quality_orders_created_total counter\nquality_orders_created_total %d\n", a.ordersCreated.Load())
		fmt.Fprintf(w, "# HELP quality_result_batches_total Result batches recorded.\n# TYPE quality_result_batches_total counter\nquality_result_batches_total %d\n", a.resultsRecorded.Load())
		fmt.Fprintf(w, "# HELP quality_usage_decisions_total Usage decisions posted.\n# TYPE quality_usage_decisions_total counter\nquality_usage_decisions_total %d\n", a.decisionsPosted.Load())
		fmt.Fprintf(w, "# HELP quality_instrument_refusals_total Result batches refused for an unknown or overdue instrument.\n# TYPE quality_instrument_refusals_total counter\nquality_instrument_refusals_total %d\n", a.instrumentRefuse.Load())
		fmt.Fprintf(w, "# HELP quality_compliance_evaluations_total Item compliance evaluations run.\n# TYPE quality_compliance_evaluations_total counter\nquality_compliance_evaluations_total %d\n", a.evaluations.Load())
		fmt.Fprintf(w, "# HELP quality_stability_studies_created_total Stability studies created; idempotent replays are not counted.\n# TYPE quality_stability_studies_created_total counter\nquality_stability_studies_created_total %d\n", a.studiesCreated.Load())
		fmt.Fprintf(w, "# HELP quality_certificates_recorded_total Incoming certificate receipts and waivers recorded; idempotent replays are not counted.\n# TYPE quality_certificates_recorded_total counter\nquality_certificates_recorded_total %d\n", a.certificatesRecorded.Load())
		fmt.Fprintf(w, "# HELP quality_samples_registered_total Physical samples registered; idempotent replays are not counted.\n# TYPE quality_samples_registered_total counter\nquality_samples_registered_total %d\n", a.samplesRegistered.Load())
		fmt.Fprintf(w, "# HELP quality_receipt_checks_total Receipt lines checked against source inspection; idempotent replays are not counted.\n# TYPE quality_receipt_checks_total counter\nquality_receipt_checks_total %d\n", a.receiptChecks.Load())
		fmt.Fprintf(w, "# HELP quality_coas_issued_total Certificate of analysis revisions issued; idempotent replays are not counted.\n# TYPE quality_coas_issued_total counter\nquality_coas_issued_total %d\n", a.coasIssued.Load())
		fmt.Fprintf(w, "# HELP quality_nonconformances_created_total Nonconformances raised through the API; idempotent replays and NCRs raised by decisions are not counted.\n# TYPE quality_nonconformances_created_total counter\nquality_nonconformances_created_total %d\n", a.ncrsCreated.Load())
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

type sizeRequest struct {
	LotQty decimal.Decimal `json:"lotQty"`
	domain.SamplingPlan
}

func (a *API) sizeSample(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var req sizeRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	draw, err := domain.DrawSample(req.SamplingPlan, req.LotQty)
	if err != nil {
		httpx.Fail(w, r, problemFrom(err))
		return
	}
	httpx.JSON(w, http.StatusOK, draw)
}

type testGroupRequest struct {
	Code  string            `json:"code"`
	Tests []domain.TestLine `json:"tests"`
}

func (a *API) createTestGroup(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	key, ok := requireKey(w, r)
	if !ok {
		return
	}
	var req testGroupRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if fields := domain.ValidateTestGroup(req.Code, req.Tests); len(fields) > 0 {
		httpx.Fail(w, r, validation(fields))
		return
	}
	group, inserted, err := a.store.CreateTestGroup(r.Context(), domain.TestGroup{Code: req.Code, Tests: req.Tests}, key)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, statusFor(inserted), group)
}

// list is the envelope of every list response.
type list[T any] struct {
	Items []T `json:"items"`
}

// limitParam reads ?limit= (1 to maxListLimit, default defaultListLimit).
func limitParam(r *http.Request) (int, error) {
	raw := r.URL.Query().Get("limit")
	if raw == "" {
		return defaultListLimit, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 || n > maxListLimit {
		return 0, validation(map[string]string{"limit": fmt.Sprintf("must be 1 to %d", maxListLimit)})
	}
	return n, nil
}

func (a *API) listTestGroups(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	limit, err := limitParam(r)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	groups, err := a.store.ListTestGroups(r.Context(), limit)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, list[domain.TestGroup]{Items: groups})
}

func (a *API) getTestGroup(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id := chi.URLParam(r, "id")
	if !ids.Valid(id) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, domain.CodeTestGroupMissing, "test group not found"))
		return
	}
	group, err := a.store.GetTestGroup(r.Context(), id)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, group)
}

type instrumentRequest struct {
	Code                    string    `json:"code"`
	Name                    string    `json:"name"`
	CalibrationIntervalDays int       `json:"calibrationIntervalDays"`
	LastCalibratedAt        time.Time `json:"lastCalibratedAt"`
}

func (a *API) createInstrument(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	key, ok := requireKey(w, r)
	if !ok {
		return
	}
	var req instrumentRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	in, err := domain.NewInstrument(req.Code, req.Name, req.CalibrationIntervalDays, req.LastCalibratedAt)
	if err != nil {
		httpx.Fail(w, r, problemFrom(err))
		return
	}
	inst, inserted, err := a.store.CreateInstrument(r.Context(), in, key)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	inst.CalibrationStatus = domain.CalibrationStatus(inst, time.Now().UTC())
	httpx.JSON(w, statusFor(inserted), inst)
}

// maxDueWithinDays bounds ?dueWithinDays= on the instrument list.
const maxDueWithinDays = 3650

// listInstruments answers ?dueWithinDays=&limit=, the soonest due first. With
// dueWithinDays it is the calibration due list (section 6.33.14): instruments due
// within that many days, overdue ones included.
func (a *API) listInstruments(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	limit, err := limitParam(r)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	now := time.Now().UTC()
	f := store.InstrumentFilter{Limit: limit}
	if raw := r.URL.Query().Get("dueWithinDays"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 0 || n > maxDueWithinDays {
			httpx.Fail(w, r, validation(map[string]string{"dueWithinDays": fmt.Sprintf("must be 0 to %d", maxDueWithinDays)}))
			return
		}
		due := now.AddDate(0, 0, n)
		f.DueBefore = &due
	}
	instruments, err := a.store.ListInstruments(r.Context(), f)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	for i := range instruments {
		instruments[i].CalibrationStatus = domain.CalibrationStatus(instruments[i], now)
	}
	httpx.JSON(w, http.StatusOK, list[domain.Instrument]{Items: instruments})
}

type calibrateRequest struct {
	CalibratedAt time.Time `json:"calibratedAt"`
}

// calibrateInstrument records a calibration (BR-33.8). It is idempotent by
// value: recording the same calibration time again changes nothing.
func (a *API) calibrateInstrument(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id := chi.URLParam(r, "id")
	if !ids.Valid(id) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, domain.CodeInstrumentMissing, "instrument not found"))
		return
	}
	var req calibrateRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if req.CalibratedAt.IsZero() {
		httpx.Fail(w, r, validation(map[string]string{"calibratedAt": "is required"}))
		return
	}
	now := time.Now().UTC()
	inst, err := a.store.CalibrateInstrument(r.Context(), id, func(cur domain.Instrument) (domain.Instrument, error) {
		next, err := cur.Recalibrate(req.CalibratedAt, now)
		return next, problemFrom(err)
	})
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	inst.CalibrationStatus = domain.CalibrationStatus(inst, now)
	httpx.JSON(w, http.StatusOK, inst)
}

func (a *API) listAssociations(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	assocs, err := a.store.ListAssociations(r.Context())
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, list[domain.Association]{Items: assocs})
}

func (a *API) createAssociation(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	key, ok := requireKey(w, r)
	if !ok {
		return
	}
	var req domain.Association
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	req.ID = ""
	if fields := domain.ValidateAssociation(req); len(fields) > 0 {
		httpx.Fail(w, r, validation(fields))
		return
	}
	req.Plan = req.Plan.Normalized()
	if req.CertificateRequired == "" {
		req.CertificateRequired = domain.CertNone
	}
	if _, err := a.store.GetTestGroup(r.Context(), req.TestGroupID); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	assoc, inserted, err := a.store.CreateAssociation(r.Context(), req, key)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, statusFor(inserted), assoc)
}

// orderRequest is the body of POST /v1/quality/quality-orders. itemGroupId and
// partyId only feed association matching (BR-33.3); testGroupId and samplingPlan
// are used, and required, for a MANUAL order only. certificateRequired is used
// for a MANUAL order only; a triggered order takes its association's (FR-33.15).
type orderRequest struct {
	Trigger      string               `json:"trigger"`
	SourceRef    domain.SourceRef     `json:"sourceRef"`
	ItemID       string               `json:"itemId"`
	ItemGroupID  string               `json:"itemGroupId"`
	PartyID      string               `json:"partyId"`
	SiteID       string               `json:"siteId"`
	WarehouseID  string               `json:"warehouseId"`
	Dims         map[string]string    `json:"dims"`
	LotQty       decimal.Decimal      `json:"lotQty"`
	UOM          string               `json:"uom"`
	TestGroupID  string               `json:"testGroupId"`
	SamplingPlan *domain.SamplingPlan `json:"samplingPlan"`

	CertificateRequired string `json:"certificateRequired"`
}

// validateOrder returns field errors for an order request.
func validateOrder(req orderRequest) map[string]string {
	fields := map[string]string{}
	if !domain.ValidTrigger(req.Trigger) {
		fields["trigger"] = "is not a known trigger"
	}
	if req.SourceRef.Type == "" || len(req.SourceRef.Type) > 40 || !ids.Valid(req.SourceRef.ID) {
		fields["sourceRef"] = "needs a type and a UUID id"
	}
	for name, v := range map[string]string{"itemId": req.ItemID, "siteId": req.SiteID, "warehouseId": req.WarehouseID} {
		if !ids.Valid(v) {
			fields[name] = "must be a UUID"
		}
	}
	for name, v := range map[string]string{"itemGroupId": req.ItemGroupID, "partyId": req.PartyID} {
		if v != "" && !ids.Valid(v) {
			fields[name] = "must be a UUID when given"
		}
	}
	if !req.LotQty.IsPositive() {
		fields["lotQty"] = "must be positive"
	}
	if req.CertificateRequired != "" && !domain.ValidCertRequirement(req.CertificateRequired) {
		fields["certificateRequired"] = "must be NONE, COA, COC or EN10204_31"
	}
	if req.UOM == "" || len(req.UOM) > 10 {
		fields["uom"] = "must be 1 to 10 characters"
	}
	for k, v := range dimsFields(req.Dims) {
		fields[k] = v
	}
	return fields
}

// maxDims, maxDimKey and maxDimValue bound the inventory dimensions an order or
// NCR carries. A batch is at most 60 characters, the limit the receipt check
// matches source inspections on (BR-33.16), so a longer one could never be found.
const (
	maxDims     = 10
	maxDimKey   = 40
	maxDimValue = 60
)

// dimsFields reports the dims map when it is too large or holds an empty or
// over-long name or value.
func dimsFields(dims map[string]string) map[string]string {
	if len(dims) > maxDims {
		return map[string]string{"dims": "must have at most 10 entries"}
	}
	for k, v := range dims {
		if k == "" || len(k) > maxDimKey || strings.TrimSpace(v) == "" || len(v) > maxDimValue {
			return map[string]string{"dims": "names must be 1 to 40 characters and values 1 to 60"}
		}
	}
	return nil
}

func (a *API) createOrder(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	key, ok := requireKey(w, r)
	if !ok {
		return
	}
	var req orderRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := validateOrder(req)
	if req.Trigger == domain.TriggerManual {
		if !ids.Valid(req.TestGroupID) {
			fields["testGroupId"] = "is required for a MANUAL order"
		}
		if req.SamplingPlan == nil {
			fields["samplingPlan"] = "is required for a MANUAL order"
		} else {
			for k, v := range domain.ValidatePlan("samplingPlan", *req.SamplingPlan) {
				fields[k] = v
			}
		}
	}
	if len(fields) > 0 {
		httpx.Fail(w, r, validation(fields))
		return
	}
	groupID, plan, certReq, err := a.resolvePlan(r, req)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	group, err := a.store.GetTestGroup(r.Context(), groupID)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	draw, err := domain.DrawSample(plan, req.LotQty)
	if err != nil {
		httpx.Fail(w, r, problemFrom(err))
		return
	}
	if req.Dims == nil {
		req.Dims = map[string]string{}
	}
	order := domain.Order{ID: ids.New(), Trigger: req.Trigger, SourceRef: req.SourceRef, ItemID: req.ItemID, SiteID: req.SiteID,
		WarehouseID: req.WarehouseID, Dims: req.Dims, LotQty: req.LotQty, SampleQty: draw.SampleQty, UOM: req.UOM,
		TestGroupID: group.ID, TestGroupVersion: group.Version, SamplingMethod: plan.Method, Severity: draw.Severity,
		CodeLetter: draw.CodeLetter, AcceptNo: draw.AcceptNo, RejectNo: draw.RejectNo, CertificateRequired: certReq}
	created, inserted, err := a.store.CreateOrder(r.Context(), order, key)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if inserted {
		a.ordersCreated.Add(1)
	}
	setETag(w, created.Version)
	httpx.JSON(w, statusFor(inserted), created)
}

// resolvePlan picks the test group, sampling plan and certificate requirement
// (FR-33.15): the request's own for a MANUAL order, otherwise the best matching
// association's (BR-33.3).
func (a *API) resolvePlan(r *http.Request, req orderRequest) (string, domain.SamplingPlan, string, error) {
	if req.Trigger == domain.TriggerManual {
		return req.TestGroupID, req.SamplingPlan.Normalized(), certRequirement(req.CertificateRequired), nil
	}
	assocs, err := a.store.ListAssociations(r.Context())
	if err != nil {
		return "", domain.SamplingPlan{}, "", err
	}
	best, ok := domain.BestAssociation(assocs, domain.TriggerContext{Trigger: req.Trigger, ItemID: req.ItemID,
		ItemGroupID: req.ItemGroupID, PartyID: req.PartyID, SiteID: req.SiteID})
	if !ok {
		return "", domain.SamplingPlan{}, "", httpx.NewProblem(http.StatusUnprocessableEntity, domain.CodeNoAssociation, "no quality association matches this trigger")
	}
	return best.TestGroupID, best.Plan, certRequirement(best.CertificateRequired), nil
}

// certRequirement maps an empty certificate requirement to NONE.
func certRequirement(t string) string {
	if t == "" {
		return domain.CertNone
	}
	return t
}

// orderView is a quality order with its test group version, recorded results,
// computed evaluation (FR-33.4), its certificate gate status (BR-33.13) and,
// once closed, its usage decision.
type orderView struct {
	domain.Order
	TestGroup         domain.TestGroup  `json:"testGroup"`
	Results           []domain.Result   `json:"results"`
	Evaluation        domain.Evaluation `json:"evaluation"`
	CertificateStatus string            `json:"certificateStatus"`
	Decision          *domain.Decision  `json:"decision,omitempty"`
}

func (a *API) loadView(r *http.Request, id string) (orderView, error) {
	order, err := a.store.GetOrder(r.Context(), id)
	if err != nil {
		return orderView{}, err
	}
	group, err := a.store.GetTestGroup(r.Context(), order.TestGroupID)
	if err != nil {
		return orderView{}, err
	}
	results, err := a.store.ListResults(r.Context(), id)
	if err != nil {
		return orderView{}, err
	}
	view := orderView{Order: order, TestGroup: group, Results: results, Evaluation: domain.Evaluate(group, order, results),
		CertificateStatus: domain.CertNotRequired}
	if order.CertificateRequired != domain.CertNone {
		recs, err := a.store.ListCertificates(r.Context(), id)
		if err != nil {
			return orderView{}, err
		}
		view.CertificateStatus = domain.CertificateStatusOf(order.CertificateRequired, recs)
	}
	if order.Status == domain.StatusClosed {
		d, found, err := a.store.GetDecision(r.Context(), id)
		if err != nil {
			return orderView{}, err
		}
		if found {
			view.Decision = &d
		}
	}
	return view, nil
}

var orderStatuses = map[string]bool{
	domain.StatusCreated: true, domain.StatusOpen: true, domain.StatusInProgress: true,
	domain.StatusPending: true, domain.StatusClosed: true, domain.StatusCancelled: true,
}

// listOrders is the quality order worklist: ?status=&siteId=&limit= (section 6.33.14).
func (a *API) listOrders(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	q := r.URL.Query()
	f := store.OrderFilter{Status: q.Get("status"), SiteID: q.Get("siteId")}
	fields := map[string]string{}
	if f.Status != "" && !orderStatuses[f.Status] {
		fields["status"] = "is not a quality order status"
	}
	if f.SiteID != "" && !ids.Valid(f.SiteID) {
		fields["siteId"] = "must be a UUID"
	}
	limit, err := limitParam(r)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if len(fields) > 0 {
		httpx.Fail(w, r, validation(fields))
		return
	}
	f.Limit = limit
	orders, err := a.store.ListOrders(r.Context(), f)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, list[domain.Order]{Items: orders})
}

func (a *API) getOrder(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id := chi.URLParam(r, "id")
	if !ids.Valid(id) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, domain.CodeOrderMissing, "quality order not found"))
		return
	}
	a.writeView(w, r, id, http.StatusOK)
}

type resultsRequest struct {
	Results []resultInput `json:"results"`
}

type resultInput struct {
	TestID       string           `json:"testId"`
	SampleNo     int              `json:"sampleNo"`
	RetestNo     int              `json:"retestNo"`
	Value        *decimal.Decimal `json:"value"`
	OutcomeID    string           `json:"outcomeId"`
	InstrumentID string           `json:"instrumentId"`
}

func (a *API) recordResults(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	key, ok := requireKey(w, r)
	if !ok {
		return
	}
	id := chi.URLParam(r, "id")
	if !ids.Valid(id) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, domain.CodeOrderMissing, "quality order not found"))
		return
	}
	var req resultsRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if n := len(req.Results); n < 1 || n > maxResultsPerBatch {
		httpx.Fail(w, r, validation(map[string]string{"results": fmt.Sprintf("must hold 1 to %d results", maxResultsPerBatch)}))
		return
	}
	// A replayed batch answers with the order as it is now, before any check that
	// the first attempt's own status change would now fail.
	if replayed, err := a.store.HasResultBatch(r.Context(), id, key); err != nil {
		httpx.Fail(w, r, err)
		return
	} else if replayed {
		a.writeView(w, r, id, http.StatusOK)
		return
	}
	order, err := a.store.GetOrder(r.Context(), id)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	group, err := a.store.GetTestGroup(r.Context(), order.TestGroupID)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	existing, err := a.store.ListResults(r.Context(), id)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	retestOnly := true
	for _, in := range req.Results {
		retestOnly = retestOnly && in.RetestNo > 0
	}
	if _, err := domain.AfterResults(order.Status, false, retestOnly); err != nil {
		httpx.Fail(w, r, problemFrom(err))
		return
	}
	now := time.Now().UTC()
	samples := domain.SampleCount(order.SampleQty)
	batch, err := a.buildResults(r, req.Results, group, samples, now)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if fields := domain.CheckRetests(existing, batch); len(fields) > 0 {
		httpx.Fail(w, r, validation(fields))
		return
	}
	all := append(append([]domain.Result{}, existing...), batch...)
	next, err := domain.AfterResults(order.Status, len(domain.MissingRequired(group, samples, all)) == 0, retestOnly)
	if err != nil {
		httpx.Fail(w, r, problemFrom(err))
		return
	}
	_, applied, err := a.store.RecordResults(r.Context(), id, order.Version, next, batch, key)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if applied {
		a.resultsRecorded.Add(1)
	}
	a.writeView(w, r, id, statusFor(applied))
}

func (a *API) writeView(w http.ResponseWriter, r *http.Request, id string, status int) {
	view, err := a.loadView(r, id)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	setETag(w, view.Version)
	httpx.JSON(w, status, view)
}

// buildResults evaluates each input against its test line: variable values are
// rounded and compared to limits; attribute outcomes are looked up. An instrument
// must be registered and in calibration (BR-33.8).
func (a *API) buildResults(r *http.Request, inputs []resultInput, group domain.TestGroup, sampleCount int, now time.Time) ([]domain.Result, error) {
	p, _ := tenant.FromContext(r.Context())
	lines := map[string]domain.TestLine{}
	for _, l := range group.Tests {
		lines[l.TestID] = l
	}
	fields := map[string]string{}
	var instrumentIDs []string
	for i, in := range inputs {
		key := fmt.Sprintf("results[%d]", i)
		if _, ok := lines[in.TestID]; !ok {
			fields[key+".testId"] = "is not a test of this order's test group"
		}
		if in.SampleNo < 1 || in.SampleNo > sampleCount {
			fields[key+".sampleNo"] = fmt.Sprintf("must be 1 to %d", sampleCount)
		}
		if in.RetestNo < 0 {
			fields[key+".retestNo"] = "must not be negative"
		}
		if in.InstrumentID != "" {
			if !ids.Valid(in.InstrumentID) {
				fields[key+".instrumentId"] = "must be a UUID"
			}
			instrumentIDs = append(instrumentIDs, in.InstrumentID)
		}
	}
	if len(fields) > 0 {
		return nil, validation(fields)
	}
	instruments, err := a.store.GetInstruments(r.Context(), instrumentIDs)
	if err != nil {
		return nil, err
	}
	for _, id := range instrumentIDs {
		inst, ok := instruments[id]
		if !ok {
			return nil, httpx.NewProblem(http.StatusUnprocessableEntity, domain.CodeInstrumentUnknown, "instrument "+id+" is not registered")
		}
		if err := domain.CheckInstrument(inst, now); err != nil {
			a.instrumentRefuse.Add(1)
			return nil, problemFrom(err)
		}
	}
	out := make([]domain.Result, 0, len(inputs))
	for i, in := range inputs {
		line := lines[in.TestID]
		res := domain.Result{TestID: in.TestID, SampleNo: in.SampleNo, RetestNo: in.RetestNo, OutcomeID: in.OutcomeID,
			InstrumentID: in.InstrumentID, RecordedBy: p.UserID, RecordedAt: now}
		switch line.Kind {
		case domain.KindVariable:
			if in.Value == nil {
				return nil, validation(map[string]string{fmt.Sprintf("results[%d].value", i): "is required for a variable test"})
			}
			rounded, passed := domain.EvaluateVariable(line, *in.Value)
			res.Value, res.Passed = &rounded, passed
		case domain.KindAttribute:
			if !ids.Valid(in.OutcomeID) {
				return nil, validation(map[string]string{fmt.Sprintf("results[%d].outcomeId", i): "is required for an attribute test"})
			}
			res.Passed = domain.EvaluateAttribute(line, in.OutcomeID)
		}
		out = append(out, res)
	}
	return out, nil
}

type decisionRequest struct {
	Lines []domain.DecisionLine `json:"lines"`
}

type decisionResponse struct {
	Order    orderView       `json:"order"`
	Decision domain.Decision `json:"decision"`
}

func (a *API) postDecision(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	key, ok := requireKey(w, r)
	if !ok {
		return
	}
	id := chi.URLParam(r, "id")
	if !ids.Valid(id) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, domain.CodeOrderMissing, "quality order not found"))
		return
	}
	var req decisionRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if stored, found, err := a.store.FindDecisionByKey(r.Context(), key); err != nil {
		httpx.Fail(w, r, err)
		return
	} else if found {
		if stored.OrderID != id {
			httpx.Fail(w, r, httpx.NewProblem(http.StatusUnprocessableEntity, codeKeyReuse, "Idempotency-Key was already used for a decision on another order"))
			return
		}
		a.replayDecision(w, r, stored)
		return
	}
	version, err := ifMatch(r)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	order, err := a.store.GetOrder(r.Context(), id)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if order.Version != version {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusPreconditionFailed, store.CodeVersionConflict, fmt.Sprintf("current version is %d, not %d", order.Version, version)))
		return
	}
	if order.Status == domain.StatusClosed {
		httpx.Fail(w, r, problemFrom(&domain.Error{Code: domain.CodeClosed, Detail: "Order is closed; amend through a nonconformance."}))
		return
	}
	if order.Status != domain.StatusPending {
		httpx.Fail(w, r, problemFrom(&domain.Error{Code: domain.CodeStatus, Detail: "a usage decision needs an order in PENDING_DECISION, not " + order.Status}))
		return
	}
	split, err := domain.SplitDecision(req.Lines, order.LotQty)
	if err != nil {
		httpx.Fail(w, r, problemFrom(err))
		return
	}
	group, err := a.store.GetTestGroup(r.Context(), order.TestGroupID)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	results, err := a.store.ListResults(r.Context(), id)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if missing := domain.MissingRequired(group, domain.SampleCount(order.SampleQty), results); len(missing) > 0 {
		httpx.Fail(w, r, problemFrom(&domain.Error{Code: domain.CodeRequiredNoResult,
			Detail: "Required test " + missing[0] + " has no result for every sample."}))
		return
	}
	if order.CertificateRequired != domain.CertNone {
		recs, err := a.store.ListCertificates(r.Context(), id)
		if err != nil {
			httpx.Fail(w, r, err)
			return
		}
		if err := domain.CertificateGate(order, split, recs); err != nil {
			httpx.Fail(w, r, problemFrom(err))
			return
		}
	}
	p, _ := tenant.FromContext(r.Context())
	decision := domain.Decision{Lines: req.Lines, Split: split, DecidedBy: p.UserID}
	posted, stored, err := a.store.PostUsageDecision(r.Context(), id, version, decision, key)
	if err != nil {
		httpx.Fail(w, r, problemFrom(err))
		return
	}
	a.decisionsPosted.Add(1)
	a.writeDecision(w, r, posted, stored, http.StatusCreated)
}

// replayDecision answers a repeated Idempotency-Key with the stored decision.
func (a *API) replayDecision(w http.ResponseWriter, r *http.Request, stored domain.Decision) {
	order, err := a.store.GetOrder(r.Context(), stored.OrderID)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	a.writeDecision(w, r, order, stored, http.StatusOK)
}

func (a *API) writeDecision(w http.ResponseWriter, r *http.Request, order domain.Order, d domain.Decision, status int) {
	view, err := a.loadView(r, order.ID)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	setETag(w, view.Version)
	httpx.JSON(w, status, decisionResponse{Order: view, Decision: d})
}

// ifMatch reads the order version from If-Match: 428 when absent, 400 when malformed.
func ifMatch(r *http.Request) (int, error) {
	raw := r.Header.Get("If-Match")
	if raw == "" {
		return 0, httpx.NewProblem(http.StatusPreconditionRequired, "COM-0428", "If-Match with the resource's ETag is required")
	}
	v, err := strconv.Atoi(strings.Trim(strings.TrimPrefix(raw, "W/"), `"`))
	if err != nil || v < 1 {
		return 0, validation(map[string]string{"If-Match": "must be the resource's ETag"})
	}
	return v, nil
}

func setETag(w http.ResponseWriter, version int) {
	w.Header().Set("ETag", fmt.Sprintf(`"%d"`, version))
}

func statusFor(inserted bool) int {
	if inserted {
		return http.StatusCreated
	}
	return http.StatusOK
}

func requireKey(w http.ResponseWriter, r *http.Request) (string, bool) {
	key := r.Header.Get(idempotencyKey)
	if key == "" || len(key) > maxRefLength {
		httpx.Fail(w, r, validation(map[string]string{idempotencyKey: "is required and must be at most 200 characters"}))
		return "", false
	}
	return key, true
}

func validation(fields map[string]string) *httpx.Problem {
	p := httpx.NewProblem(http.StatusBadRequest, domain.CodeValidation, validationDetail)
	p.Errors = fields
	return p
}

// problemFrom maps a domain error to its HTTP problem. Unknown errors pass through.
func problemFrom(err error) error {
	var de *domain.Error
	if !errors.As(err, &de) {
		return err
	}
	p := httpx.NewProblem(httpStatus(de.Code), de.Code, de.Detail)
	if de.Reason != "" {
		p.Errors = map[string]string{"reason": de.Reason}
	}
	return p
}

// httpStatus gives the HTTP status of a domain error code.
func httpStatus(code string) int {
	switch code {
	case domain.CodeValidation:
		return http.StatusBadRequest
	case domain.CodeTestGroupMissing, domain.CodeOrderMissing, domain.CodeInstrumentMissing, domain.CodeStudyMissing, domain.CodeCompositionMissing,
		domain.CodeNCRMissing:
		return http.StatusNotFound
	case domain.CodeClosed, domain.CodeResultDuplicate, domain.CodeInstrumentDupe, domain.CodeStatus, domain.CodeSourceLineDupe,
		domain.CodeListRace, domain.CodeCompositionRace, domain.CodeStudyRace, domain.CodeNCRStatus, domain.CodeNCRRace, domain.CodeCAPAStep,
		domain.CodeCertNotRequired, domain.CodeCertSampleRace, domain.CodeStudyStatus, domain.CodeReceiptCheckDupe, domain.CodeWriteRace:
		return http.StatusConflict
	}
	return http.StatusUnprocessableEntity
}

func validTenant(w http.ResponseWriter, r *http.Request) bool {
	p, ok := tenant.FromContext(r.Context())
	if !ok || !ids.Valid(p.TenantID) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusUnauthorized, "AUTH-0001", "missing or malformed tenant"))
		return false
	}
	return true
}
