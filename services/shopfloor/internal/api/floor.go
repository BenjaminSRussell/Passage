package api

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/shopfloor/internal/domain"
	"github.com/BenjaminSRussell/passage/services/shopfloor/internal/store"
)

const (
	maxIngest     = 500
	maxOEEResIDs  = 50
	maxOEESpan    = 7 * 24 * time.Hour
	maxMachineLen = 64
	maxReasonLen  = 32
	maxIdealSecs  = 86400
)

// oeeBuckets maps the bucket query parameter to its width (FR-25.10).
var oeeBuckets = map[string]time.Duration{"5m": 5 * time.Minute, "15m": 15 * time.Minute, "60m": time.Hour}

type machineRequest struct {
	MachineID         string  `json:"machineId"`
	ResourceID        string  `json:"resourceId"`
	SiteID            *string `json:"siteId"`
	IdealCycleSeconds string  `json:"idealCycleSeconds"`
	// IdleThresholdSeconds is optional; the default is 300 (BR-25.6).
	IdleThresholdSeconds *int `json:"idleThresholdSeconds"`
}

type measurementItem struct {
	Name  string          `json:"name"`
	Value decimal.Decimal `json:"value"`
	Unit  *string         `json:"unit"`
}

type machineEventItem struct {
	MachineID   string           `json:"machineId"`
	Seq         int64            `json:"seq"`
	Kind        string           `json:"kind"`
	TS          time.Time        `json:"ts"`
	GoodCount   int64            `json:"goodCount"`
	RejectCount int64            `json:"rejectCount"`
	State       string           `json:"state"`
	ReasonCode  *string          `json:"reasonCode"`
	Measurement *measurementItem `json:"measurement"`
	JobID       *string          `json:"jobId"`
}

type machineEventBatch struct {
	Events []machineEventItem `json:"events"`
}

type downtimeCodeRequest struct {
	ReasonCode string `json:"reasonCode"`
	Category   string `json:"category"`
}

type payExportRequest struct {
	From string `json:"from"`
	To   string `json:"to"`
}

type oeeItem struct {
	ResourceID   string          `json:"resourceId"`
	BucketStart  time.Time       `json:"bucketStart"`
	PlannedMin   decimal.Decimal `json:"plannedMinutes"`
	RunMin       decimal.Decimal `json:"runMinutes"`
	DownMin      decimal.Decimal `json:"downMinutes"`
	TotalCount   int64           `json:"totalCount"`
	GoodCount    int64           `json:"goodCount"`
	Availability decimal.Decimal `json:"availability"`
	Performance  decimal.Decimal `json:"performance"`
	Quality      decimal.Decimal `json:"quality"`
	OEE          decimal.Decimal `json:"oee"`
	OverRate     bool            `json:"overRate"`
	SourceEvents int             `json:"sourceEvents"`
}

// registerFloor mounts the machine, downtime, andon, OEE, live OEE stream, OEE rollup and payroll export routes.
func (a *API) registerFloor(r chi.Router) {
	r.With(authz.RequireDuty(DutySetup)).Post("/v1/shopfloor/machines", a.createMachine)
	r.With(authz.RequireDuty(DutyView)).Get("/v1/shopfloor/machines", a.listMachines)
	r.With(authz.RequireDuty(DutyIngest)).Post("/v1/shopfloor/machine-events", a.ingestMachineEvents)
	r.With(authz.RequireDuty(DutyView)).Get("/v1/shopfloor/downtimes", a.listDowntimes)
	r.With(authz.RequireDuty(DutyRegister)).Post("/v1/shopfloor/downtimes/{id}:code", a.codeDowntime)
	r.With(authz.RequireDuty(DutyView)).Get("/v1/shopfloor/andon", a.listAndon)
	r.With(authz.RequireDuty(DutyOEERollup)).Post("/v1/shopfloor/andon:sweep", a.sweepAndon)
	r.With(authz.RequireDuty(DutyCheckOverride)).Post("/v1/shopfloor/andon/{id}:clear", a.clearAndon)
	r.With(authz.RequireDuty(DutyView)).Get("/v1/shopfloor/oee", a.oee)
	r.With(authz.RequireDuty(DutyView)).Get("/v1/shopfloor/oee/stream", a.oeeStream)
	r.With(authz.RequireDuty(DutyOEERollup)).Post("/v1/shopfloor/oee:rollup", a.rollupOEE)
	r.With(authz.RequireDuty(DutyExport)).Post("/v1/shopfloor/pay:export", a.exportPay)
	r.With(authz.RequireDuty(DutyExport)).Get("/v1/shopfloor/pay-exports", a.listPayExports)
	r.With(authz.RequireDuty(DutyExport)).Get("/v1/shopfloor/pay-exports/{id}", a.getPayExport)
}

func (a *API) createMachine(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	idem, ok := readIdem(w, r)
	if !ok {
		return
	}
	var req machineRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	code := strings.TrimSpace(req.MachineID)
	if n := len(code); n < 1 || n > maxMachineLen {
		fields["machineId"] = fmt.Sprintf("must be 1 to %d characters", maxMachineLen)
	}
	if !ids.Valid(req.ResourceID) {
		fields["resourceId"] = "must be a UUID"
	}
	if req.SiteID != nil && !ids.Valid(*req.SiteID) {
		fields["siteId"] = "must be a UUID"
	}
	ideal, err := decimal.NewFromString(req.IdealCycleSeconds)
	if err != nil || !ideal.IsPositive() || ideal.GreaterThan(decimal.NewFromInt(maxIdealSecs)) || ideal.Exponent() < -3 {
		fields["idealCycleSeconds"] = fmt.Sprintf("must be a decimal string above 0 and at most %d with up to 3 decimals", maxIdealSecs)
	}
	idle := int(domain.DefaultIdleThreshold / time.Second)
	if req.IdleThresholdSeconds != nil {
		idle = *req.IdleThresholdSeconds
	}
	if minIdle, maxIdle := int(domain.MinIdleThreshold/time.Second), int(domain.MaxIdleThreshold/time.Second); idle < minIdle || idle > maxIdle {
		fields["idleThresholdSeconds"] = fmt.Sprintf("must be %d to %d seconds", minIdle, maxIdle)
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	if a.replayed(w, r, idem) {
		return
	}
	m, err := a.store.RegisterMachine(r.Context(), store.Machine{
		Code: code, ResourceID: req.ResourceID, SiteID: req.SiteID, IdealCycleSeconds: ideal, IdleThresholdSeconds: idle,
	}, idem)
	if err != nil {
		a.fail(w, r, "register machine", err)
		return
	}
	httpx.JSON(w, http.StatusCreated, m)
}

func (a *API) listMachines(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	limit, ok := limitParam(w, r, 200, 1000)
	if !ok {
		return
	}
	items, err := a.store.ListMachines(r.Context(), limit)
	if err != nil {
		a.fail(w, r, "list machines", err)
		return
	}
	httpx.JSON(w, http.StatusOK, itemsResponse[store.Machine]{Items: items})
}

var (
	eventKinds    = map[string]bool{domain.EventCount: true, domain.EventState: true, domain.EventDowntime: true, domain.EventAlarm: true, domain.EventMeasurement: true}
	machineStates = map[string]bool{"": true, domain.MachineRunning: true, domain.MachineIdle: true, domain.MachineDown: true, domain.MachineSetup: true, domain.MachineOff: true}
)

// validateMachineEvent returns field errors keyed by JSON field name (BR-25.4).
func validateMachineEvent(it machineEventItem) map[string]string {
	fields := map[string]string{}
	if n := len(it.MachineID); n < 1 || n > maxMachineLen {
		fields["machineId"] = fmt.Sprintf("must be 1 to %d characters", maxMachineLen)
	}
	if it.Seq < 1 {
		fields["seq"] = "must be at least 1"
	}
	if !eventKinds[it.Kind] {
		fields["kind"] = "must be one of count, state, downtime, alarm, measurement"
	}
	if it.TS.IsZero() {
		fields["ts"] = "is required"
	}
	if it.GoodCount < 0 {
		fields["goodCount"] = "must not be negative"
	}
	if it.RejectCount < 0 {
		fields["rejectCount"] = "must not be negative"
	}
	if it.Kind != domain.EventCount && (it.GoodCount != 0 || it.RejectCount != 0) {
		fields["goodCount"] = "counts are only reported on count events"
	}
	switch {
	case !machineStates[it.State]:
		fields["state"] = "must be one of RUNNING, IDLE, DOWN, SETUP, OFF"
	case it.Kind == domain.EventState && it.State == "":
		fields["state"] = "is required for state events"
	}
	if it.ReasonCode != nil && (len(*it.ReasonCode) < 1 || len(*it.ReasonCode) > maxReasonLen) {
		fields["reasonCode"] = fmt.Sprintf("must be 1 to %d characters", maxReasonLen)
	}
	if it.Kind == domain.EventMeasurement && (it.Measurement == nil || it.Measurement.Name == "") {
		fields["measurement"] = "name is required for measurement events"
	}
	if it.JobID != nil && !ids.Valid(*it.JobID) {
		fields["jobId"] = "must be a UUID"
	}
	return fields
}

// ingestMachineEvents accepts a batch from the edge gateway. It needs no
// Idempotency-Key: each event is keyed by (machine, seq), so a replayed batch
// is reported as dropped rather than stored twice (BR-25.4).
func (a *API) ingestMachineEvents(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var req machineEventBatch
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if n := len(req.Events); n < 1 || n > maxIngest {
		validationFailed(w, r, map[string]string{"events": fmt.Sprintf("must hold 1 to %d events", maxIngest)})
		return
	}
	fields := map[string]string{}
	items := make([]store.MachineEventIn, 0, len(req.Events))
	for i, it := range req.Events {
		for field, rule := range validateMachineEvent(it) {
			fields[fmt.Sprintf("events[%d].%s", i, field)] = rule
		}
		in := store.MachineEventIn{
			MachineCode: it.MachineID,
			Event: domain.MachineEvent{Seq: it.Seq, Kind: it.Kind, At: it.TS, GoodCount: it.GoodCount,
				RejectCount: it.RejectCount, State: it.State},
			ReasonCode: it.ReasonCode,
			JobID:      it.JobID,
		}
		if it.Measurement != nil {
			name, v := it.Measurement.Name, it.Measurement.Value
			in.MeasurementName, in.MeasurementValue, in.Unit = &name, &v, it.Measurement.Unit
		}
		items = append(items, in)
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	res, err := a.store.IngestMachineEvents(r.Context(), time.Now().UTC(), items)
	if err != nil {
		a.fail(w, r, "ingest machine events", err)
		return
	}
	a.eventsAccepted.Add(int64(res.Accepted))
	a.eventsDropped.Add(int64(res.Dropped))
	a.seqGaps.Add(int64(len(res.Gaps)))
	a.andonsRaised.Add(int64(res.AndonsRaised))
	httpx.JSON(w, http.StatusOK, res)
}

func (a *API) listDowntimes(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	q := r.URL.Query()
	f := store.DowntimeFilter{ResourceID: q.Get("resourceId")}
	switch q.Get("open") {
	case "", "false":
	case "true":
		f.OpenOnly = true
	default:
		validationFailed(w, r, map[string]string{"open": "must be true or false"})
		return
	}
	if f.ResourceID != "" && !ids.Valid(f.ResourceID) {
		validationFailed(w, r, map[string]string{"resourceId": "must be a UUID"})
		return
	}
	limit, ok := limitParam(w, r, 200, 1000)
	if !ok {
		return
	}
	f.Limit = limit
	items, err := a.store.ListDowntimes(r.Context(), f)
	if err != nil {
		a.fail(w, r, "list downtimes", err)
		return
	}
	httpx.JSON(w, http.StatusOK, itemsResponse[store.Downtime]{Items: items})
}

var downtimeCategories = map[string]bool{"": true, "planned": true, "unplanned": true}

func (a *API) codeDowntime(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	idem, ok := readIdem(w, r)
	if !ok {
		return
	}
	id := chi.URLParam(r, "id")
	if !ids.Valid(id) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, store.CodeDowntimeNotFound, "downtime not found"))
		return
	}
	var req downtimeCodeRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	reason := strings.TrimSpace(req.ReasonCode)
	if n := len(reason); n < 1 || n > maxReasonLen {
		fields["reasonCode"] = fmt.Sprintf("must be 1 to %d characters", maxReasonLen)
	}
	if !downtimeCategories[req.Category] {
		fields["category"] = "must be planned or unplanned"
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	if a.replayed(w, r, idem) {
		return
	}
	d, err := a.store.CodeDowntime(r.Context(), id, reason, req.Category, idem)
	if err != nil {
		a.fail(w, r, "code downtime", err)
		return
	}
	httpx.JSON(w, http.StatusOK, d)
}

func (a *API) listAndon(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	siteID := r.URL.Query().Get("siteId")
	if siteID != "" && !ids.Valid(siteID) {
		validationFailed(w, r, map[string]string{"siteId": "must be a UUID"})
		return
	}
	items, err := a.store.OpenAndon(r.Context(), siteID)
	if err != nil {
		a.fail(w, r, "list andon", err)
		return
	}
	httpx.JSON(w, http.StatusOK, itemsResponse[store.Andon]{Items: items})
}

// parseOEEQuery validates resourceIds, from, to and bucket. The window must be
// a whole number of buckets so every bucket covers its full width.
func parseOEEQuery(r *http.Request) (resourceIDs []string, from, to time.Time, width time.Duration, fields map[string]string) {
	fields = map[string]string{}
	q := r.URL.Query()
	seen := map[string]bool{}
	for _, s := range strings.Split(q.Get("resourceIds"), ",") {
		if !ids.Valid(s) {
			fields["resourceIds"] = "must be a comma-separated list of UUIDs"
			break
		}
		if !seen[s] {
			seen[s] = true
			resourceIDs = append(resourceIDs, s)
		}
	}
	if _, bad := fields["resourceIds"]; !bad && len(resourceIDs) > maxOEEResIDs {
		fields["resourceIds"] = fmt.Sprintf("must hold 1 to %d UUIDs", maxOEEResIDs)
	}
	from, errFrom := time.Parse(time.RFC3339, q.Get("from"))
	to, errTo := time.Parse(time.RFC3339, q.Get("to"))
	if errFrom != nil {
		fields["from"] = "must be an RFC 3339 timestamp"
	}
	if errTo != nil {
		fields["to"] = "must be an RFC 3339 timestamp"
	}
	bucket := q.Get("bucket")
	if bucket == "" {
		bucket = "15m"
	}
	width, ok := oeeBuckets[bucket]
	if !ok {
		fields["bucket"] = "must be 5m, 15m or 60m"
	}
	if errFrom == nil && errTo == nil {
		switch {
		case !to.After(from):
			fields["to"] = "must be after from"
		case to.Sub(from) > maxOEESpan:
			fields["to"] = "window must be at most 7 days"
		case ok && to.Sub(from)%width != 0:
			fields["to"] = "window must be a whole number of buckets"
		}
	}
	return resourceIDs, from.UTC(), to.UTC(), width, fields
}

// oee computes the ISO 22400 series per resource and bucket from the stored
// machine events (FR-25.10, BR-25.5). Resources without a machine have no rows.
func (a *API) oee(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	resourceIDs, from, to, width, fields := parseOEEQuery(r)
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	streams, err := a.store.LoadOEEStreams(r.Context(), resourceIDs, from, to)
	if err != nil {
		a.fail(w, r, "load oee streams", err)
		return
	}
	now := a.now().UTC()
	items := []oeeItem{}
	for _, rid := range resourceIDs {
		s, ok := streams[rid]
		if !ok {
			continue
		}
		buckets, err := domain.BuildBuckets(withHorizon(s, now), from, to, width)
		if err != nil {
			a.fail(w, r, "build oee buckets", err)
			return
		}
		for _, b := range buckets {
			items = append(items, toOEEItem(rid, b))
		}
	}
	httpx.JSON(w, http.StatusOK, itemsResponse[oeeItem]{Items: items})
}

func (a *API) exportPay(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	p, _ := tenant.FromContext(r.Context())
	if p.UserID == "" {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusUnauthorized, "AUTH-0001", "export needs an authenticated user"))
		return
	}
	idem, ok := readIdem(w, r)
	if !ok {
		return
	}
	var req payExportRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	win, fields := parseRange(payRangeRequest{From: req.From, To: req.To}, maxListRangeDays, false)
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	if a.replayed(w, r, idem) {
		return
	}
	out, err := a.store.ExportPay(r.Context(), win.fromDate, win.toDate, p.UserID, idem)
	if err != nil {
		a.fail(w, r, "export pay", err)
		return
	}
	a.exportedLines.Add(int64(out.LineCount))
	httpx.JSON(w, http.StatusCreated, out)
}

func (a *API) listPayExports(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	limit, ok := limitParam(w, r, 100, 1000)
	if !ok {
		return
	}
	items, err := a.store.ListPayExports(r.Context(), limit)
	if err != nil {
		a.fail(w, r, "list pay exports", err)
		return
	}
	httpx.JSON(w, http.StatusOK, itemsResponse[store.PayExport]{Items: items})
}

func (a *API) getPayExport(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id := chi.URLParam(r, "id")
	if !ids.Valid(id) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, store.CodeExportNotFound, "export batch not found"))
		return
	}
	out, err := a.store.GetPayExport(r.Context(), id)
	if err != nil {
		a.fail(w, r, "get pay export", err)
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}
