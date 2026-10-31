// Package api is the HTTP layer of the lean service: decode, validate, check
// the tenant, call the store or a domain rule, and write JSON or an RFC 9457
// problem.
package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/lean/internal/domain"
	"github.com/BenjaminSRussell/passage/services/lean/internal/store"
)

// Duties (PRD 4.24.17): "Maintain production flows / kanban rules", "Work
// kanban board", "Settle production flows" (open and settle flow costing
// periods), and generate under "Maintain kanbans" (PRD 4.24.5). Reads of
// flows, rules, cards and costing periods need DutyRead.
const (
	DutyRead        = "lean.read"
	DutyFlowWrite   = "lean.flow.write"
	DutyRuleWrite   = "lean.kanban-rule.write"
	DutyKanbanWrite = "lean.kanban.write"
	DutyKanbanWork  = "lean.kanban.work"
	DutyFlowSettle  = "lean.flow.settle"
)

const (
	idempotencyHeader = "Idempotency-Key"
	maxBody           = 1 << 20
	defaultLimit      = 50
	readyTimeout      = 500 * time.Millisecond
	validationDetail  = "request failed validation"
	maxRefLen         = 80
)

var ruleCode = regexp.MustCompile(`^[A-Z0-9][A-Z0-9_.-]{0,31}$`)

// maxQty is the largest quantity numeric(18,6) holds.
func maxQty() decimal.Decimal { return decimal.New(1, 12) }

// API holds the store.
type API struct{ store store.Store }

// New returns the handler set.
func New(s store.Store) *API { return &API{store: s} }

// Register mounts every route on r. /healthz comes from httpx.NewRouter.
func (a *API) Register(r chi.Router) {
	read := authz.RequireDuty(DutyRead)
	work := authz.RequireDuty(DutyKanbanWork)
	r.Get("/readyz", a.ready)
	r.With(authz.RequireDuty(DutyFlowWrite)).Post("/v1/lean/flows", a.createFlow)
	r.With(read).Get("/v1/lean/flows", a.listFlows)
	r.With(read).Get("/v1/lean/flows/{id}", a.getFlow)
	r.With(authz.RequireDuty(DutyFlowWrite)).Post("/v1/lean/flows/{id}:activate", a.activateFlow)
	r.With(authz.RequireDuty(DutyFlowWrite)).Post("/v1/lean/flows/{id}:new-version", a.newFlowVersion)
	r.With(authz.RequireDuty(DutyFlowSettle)).Post("/v1/lean/flows/{id}/costing-periods", a.openPeriod)
	r.With(read).Get("/v1/lean/flows/{id}/costing-periods", a.listPeriods)
	r.With(authz.RequireDuty(DutyFlowSettle)).Post("/v1/lean/flows/{id}/costing-periods/{periodId}:settle", a.settlePeriod)
	r.With(authz.RequireDuty(DutyFlowSettle)).Post("/v1/lean/flows/{id}/costing-periods/{periodId}:backflush", a.backflush)
	r.With(read).Get("/v1/lean/flows/{id}/costing-periods:export", a.exportPeriods)
	r.With(authz.RequireDuty(DutyRuleWrite)).Post("/v1/lean/kanban-rules", a.createRule)
	r.With(read).Get("/v1/lean/kanban-rules", a.listRules)
	r.With(read).Get("/v1/lean/kanban-rules/{id}", a.getRule)
	r.With(read).Get("/v1/lean/kanban-rules/{id}/analysis", a.ruleAnalysis)
	r.With(read).Get("/v1/lean/kanban-rules/{id}/count-check", a.countCheck)
	r.With(authz.RequireDuty(DutyRuleWrite)).Post("/v1/lean/kanban-rules:fixed-count", a.fixedCount)
	r.With(authz.RequireDuty(DutyKanbanWrite)).Post("/v1/lean/kanbans:generate", a.generate)
	r.With(read).Get("/v1/lean/kanbans/{id}", a.getKanban)
	r.With(work).Post("/v1/lean/kanbans/{id}:move", a.move)
	r.With(work).Get("/v1/lean/board", a.board)
	r.With(work).Post("/v1/lean/kanban-jobs/{id}:start", a.startJob)
	r.With(work).Post("/v1/lean/kanban-jobs/{id}:complete", a.completeJob)
	r.With(authz.RequireDuty(DutyFlowWrite)).Post("/v1/lean/subcontracting:preview", handle(subcontractLines))
}

func (a *API) ready(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), readyTimeout)
	defer cancel()
	if err := a.store.Ping(ctx); err != nil {
		slog.Error("readiness check failed", "err", err, "requestId", middleware.GetReqID(r.Context()))
		httpx.JSON(w, http.StatusServiceUnavailable, map[string]string{"status": "unavailable"})
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]string{"status": "ready"})
}

// fail maps domain errors to problems and writes the result. Malformed input
// is 400; a card or job move, capacity or settlement rule the card, job or rule
// state refuses is 409 (BR-24.6, BR-24.7, BR-24.9: LEAN-0003 lists the open
// cards); a flow that is not connected (FR-24.8) or a
// service job without a purchase agreement (FR-24.20, LEAN-0005) is 422.
func fail(w http.ResponseWriter, r *http.Request, err error) {
	var de *domain.Error
	if errors.As(err, &de) {
		status := http.StatusConflict
		switch de.Code {
		case domain.CodeInvalid:
			status = http.StatusBadRequest
		case domain.CodeNoOutput, domain.CodeNoAgreement:
			status = http.StatusUnprocessableEntity
		}
		err = httpx.NewProblem(status, de.Code, de.Detail)
	}
	var p *httpx.Problem
	if !errors.As(err, &p) {
		slog.Error("request failed", "err", err, "path", r.URL.Path, "requestId", middleware.GetReqID(r.Context()))
	}
	httpx.Fail(w, r, err)
}

func invalid(w http.ResponseWriter, r *http.Request, fields map[string]string) {
	p := httpx.NewProblem(http.StatusBadRequest, "COM-0001", validationDetail)
	p.Errors = fields
	httpx.Fail(w, r, p)
}

// principal returns the caller; the tenant must be a UUID for RLS.
func principal(w http.ResponseWriter, r *http.Request) (tenant.Principal, bool) {
	p, ok := tenant.FromContext(r.Context())
	if !ok || !ids.Valid(p.TenantID) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusUnauthorized, "AUTH-0001", "missing or malformed tenant"))
		return p, false
	}
	return p, true
}

// decodeCommand reads the body once, decodes it and fingerprints method, path
// and body for the required Idempotency-Key.
func decodeCommand(w http.ResponseWriter, r *http.Request, v any) (store.Idem, bool) {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxBody))
	if err != nil {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusBadRequest, "COM-0001", "request body could not be read"))
		return store.Idem{}, false
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	if err := httpx.Decode(r, v); err != nil {
		httpx.Fail(w, r, err)
		return store.Idem{}, false
	}
	key := r.Header.Get(idempotencyHeader)
	if key == "" || len(key) > 200 {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusBadRequest, "COM-0001", "Idempotency-Key header is required (1 to 200 characters)"))
		return store.Idem{}, false
	}
	sum := sha256.Sum256(append([]byte(r.Method+" "+r.URL.Path+"\n"), body...))
	return store.Idem{Key: key, Fingerprint: hex.EncodeToString(sum[:])}, true
}

// writeCommand writes freshStatus for a new result or 200 with
// Idempotent-Replayed for a replay.
func writeCommand(w http.ResponseWriter, fresh bool, freshStatus int, v any) {
	status := freshStatus
	if !fresh {
		status = http.StatusOK
		w.Header().Set("Idempotent-Replayed", "true")
	}
	httpx.JSON(w, status, v)
}

func limitParam(w http.ResponseWriter, r *http.Request) (int, bool) {
	raw := r.URL.Query().Get("limit")
	if raw == "" {
		return defaultLimit, true
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 || n > store.MaxList {
		invalid(w, r, map[string]string{"limit": fmt.Sprintf("must be an integer from 1 to %d", store.MaxList)})
		return 0, false
	}
	return n, true
}

func pathID(w http.ResponseWriter, r *http.Request) (string, bool) {
	id := chi.URLParam(r, "id")
	if !ids.Valid(id) {
		invalid(w, r, map[string]string{"id": "must be a UUID"})
		return "", false
	}
	return id, true
}

// uuidQuery reads optional UUID query parameters into dst, reporting bad ones.
func uuidQuery(r *http.Request, fields map[string]string, names ...string) map[string]string {
	out := map[string]string{}
	for _, name := range names {
		v := r.URL.Query().Get(name)
		if v != "" && !ids.Valid(v) {
			fields[name] = "must be a UUID"
		}
		out[name] = v
	}
	return out
}

func validQty(d decimal.Decimal) bool {
	return d.IsPositive() && d.LessThan(maxQty()) && d.Exponent() >= -6
}

func optionalUUID(fields map[string]string, name string, v *string) {
	if v != nil && !ids.Valid(*v) {
		fields[name] = "must be a UUID"
	}
}

func parseDate(fields map[string]string, name, v string) (time.Time, bool) {
	t, err := time.Parse(store.DateLayout, v)
	if err != nil {
		fields[name] = "must be a date (YYYY-MM-DD)"
		return t, false
	}
	return t, true
}

type activityRequest struct {
	Sequence           int     `json:"sequence"`
	Name               string  `json:"name"`
	Kind               string  `json:"kind"`
	CycleTimeS         int     `json:"cycleTimeS"`
	WorkCellResourceID *string `json:"workCellResourceId"`
	InputLocationID    *string `json:"inputLocationId"`
	OutputLocationID   *string `json:"outputLocationId"`
	Predecessors       []int   `json:"predecessors"`
	PickingRequired    bool    `json:"pickingRequired"`
}

type flowRequest struct {
	ValueStreamID string            `json:"valueStreamId"`
	SiteID        string            `json:"siteId"`
	Name          string            `json:"name"`
	TaktTimeS     *int              `json:"taktTimeS"`
	Activities    []activityRequest `json:"activities"`
}

// buildActivities validates the activity list of a flow request and returns
// the store activities; problems are added to fields.
func buildActivities(fields map[string]string, reqs []activityRequest) []store.Activity {
	if len(reqs) < 1 || len(reqs) > domain.MaxActivities {
		fields["activities"] = fmt.Sprintf("must hold 1 to %d activities", domain.MaxActivities)
	}
	seqs := map[int]bool{}
	for i, act := range reqs {
		if seqs[act.Sequence] {
			fields[fmt.Sprintf("activities[%d].sequence", i)] = "is used by another activity"
		}
		seqs[act.Sequence] = true
	}
	acts := make([]store.Activity, len(reqs))
	for i, act := range reqs {
		at := func(f string) string { return fmt.Sprintf("activities[%d].%s", i, f) }
		if act.Sequence < 1 {
			fields[at("sequence")] = "must be 1 or more"
		}
		actName := strings.TrimSpace(act.Name)
		if n := len([]rune(actName)); n < 1 || n > 80 {
			fields[at("name")] = "must be 1 to 80 characters"
		}
		if !domain.ValidActivityKind(act.Kind) {
			fields[at("kind")] = "must be process, transfer or service"
		}
		if act.CycleTimeS < 0 {
			fields[at("cycleTimeS")] = "must not be negative"
		}
		optionalUUID(fields, at("workCellResourceId"), act.WorkCellResourceID)
		optionalUUID(fields, at("inputLocationId"), act.InputLocationID)
		optionalUUID(fields, at("outputLocationId"), act.OutputLocationID)
		for _, p := range act.Predecessors {
			if !seqs[p] || p == act.Sequence {
				fields[at("predecessors")] = "must name the sequence numbers of other activities of this flow"
			}
		}
		preds := act.Predecessors
		if preds == nil {
			preds = []int{}
		}
		acts[i] = store.Activity{Sequence: act.Sequence, Name: actName, Kind: act.Kind, CycleTimeS: act.CycleTimeS, WorkCellResourceID: act.WorkCellResourceID,
			InputLocationID: act.InputLocationID, OutputLocationID: act.OutputLocationID, Predecessors: preds, PickingRequired: act.PickingRequired}
	}
	return acts
}

func (a *API) createFlow(w http.ResponseWriter, r *http.Request) {
	if _, ok := principal(w, r); !ok {
		return
	}
	var req flowRequest
	idem, ok := decodeCommand(w, r, &req)
	if !ok {
		return
	}
	fields := map[string]string{}
	if !ids.Valid(req.ValueStreamID) {
		fields["valueStreamId"] = "must be a UUID (the wave-6 value-stream operating unit)"
	}
	if !ids.Valid(req.SiteID) {
		fields["siteId"] = "must be a UUID"
	}
	name := strings.TrimSpace(req.Name)
	if n := len([]rune(name)); n < 1 || n > 120 {
		fields["name"] = "must be 1 to 120 characters"
	}
	if req.TaktTimeS != nil && *req.TaktTimeS < 1 {
		fields["taktTimeS"] = "must be a positive number of seconds"
	}
	acts := buildActivities(fields, req.Activities)
	if len(fields) > 0 {
		invalid(w, r, fields)
		return
	}
	f, fresh, err := a.store.CreateFlow(r.Context(), store.Flow{ValueStreamID: req.ValueStreamID, SiteID: req.SiteID, Name: name,
		TaktTimeS: req.TaktTimeS, Activities: acts}, idem)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeCommand(w, fresh, http.StatusCreated, f)
}

func (a *API) listFlows(w http.ResponseWriter, r *http.Request) {
	if _, ok := principal(w, r); !ok {
		return
	}
	limit, ok := limitParam(w, r)
	if !ok {
		return
	}
	status := r.URL.Query().Get("status")
	if status != "" && status != "draft" && status != "active" && status != "expired" {
		invalid(w, r, map[string]string{"status": "must be draft, active or expired"})
		return
	}
	items, err := a.store.ListFlows(r.Context(), status, limit)
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
}

func (a *API) getFlow(w http.ResponseWriter, r *http.Request) {
	if _, ok := principal(w, r); !ok {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	f, err := a.store.GetFlow(r.Context(), id)
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, f)
}

type activateRequest struct {
	Version    int     `json:"version"`
	ActiveFrom *string `json:"activeFrom"`
	ActiveTo   *string `json:"activeTo"`
}

func (a *API) activateFlow(w http.ResponseWriter, r *http.Request) {
	if _, ok := principal(w, r); !ok {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var req activateRequest
	idem, ok := decodeCommand(w, r, &req)
	if !ok {
		return
	}
	fields := map[string]string{}
	if req.Version < 1 {
		fields["version"] = "must be the version you read (1 or more)"
	}
	from := time.Now().UTC().Truncate(24 * time.Hour)
	if req.ActiveFrom != nil {
		from, _ = parseDate(fields, "activeFrom", *req.ActiveFrom)
	}
	var to *time.Time
	if req.ActiveTo != nil {
		if t, ok := parseDate(fields, "activeTo", *req.ActiveTo); ok {
			to = &t
			if t.Before(from) {
				fields["activeTo"] = "must not be before activeFrom"
			}
		}
	}
	if len(fields) > 0 {
		invalid(w, r, fields)
		return
	}
	f, fresh, err := a.store.ActivateFlow(r.Context(), id, req.Version, from, to, idem)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeCommand(w, fresh, http.StatusOK, f)
}

type newVersionRequest struct {
	Version    int               `json:"version"`
	TaktTimeS  *int              `json:"taktTimeS"`
	Activities []activityRequest `json:"activities"`
}

func (a *API) newFlowVersion(w http.ResponseWriter, r *http.Request) {
	if _, ok := principal(w, r); !ok {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var req newVersionRequest
	idem, ok := decodeCommand(w, r, &req)
	if !ok {
		return
	}
	fields := map[string]string{}
	if req.Version < 1 {
		fields["version"] = "must be the version you read (1 or more)"
	}
	if req.TaktTimeS != nil && *req.TaktTimeS < 1 {
		fields["taktTimeS"] = "must be a positive number of seconds"
	}
	in := store.NewVersion{SetTakt: req.TaktTimeS != nil, TaktTimeS: req.TaktTimeS}
	if req.Activities != nil {
		in.Activities = buildActivities(fields, req.Activities)
	}
	if len(fields) > 0 {
		invalid(w, r, fields)
		return
	}
	f, fresh, err := a.store.NewFlowVersion(r.Context(), id, req.Version, in, idem)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeCommand(w, fresh, http.StatusCreated, f)
}

type ruleRequest struct {
	Code            string          `json:"code"`
	ItemID          string          `json:"itemId"`
	SiteID          string          `json:"siteId"`
	FlowID          *string         `json:"flowId"`
	RuleType        string          `json:"ruleType"`
	Replenishment   string          `json:"replenishment"`
	FixedQty        decimal.Decimal `json:"fixedQty"`
	NumberOfKanbans *int            `json:"numberOfKanbans"`
	LeadTimeH       int             `json:"leadTimeH"`
	EventSource     *string         `json:"eventSource"`
	ExactQty        bool            `json:"exactQty"`
	EffectiveFrom   *string         `json:"effectiveFrom"`
	EffectiveTo     *string         `json:"effectiveTo"`
}

func (a *API) createRule(w http.ResponseWriter, r *http.Request) {
	if _, ok := principal(w, r); !ok {
		return
	}
	var req ruleRequest
	idem, ok := decodeCommand(w, r, &req)
	if !ok {
		return
	}
	fields := map[string]string{}
	if !ruleCode.MatchString(req.Code) {
		fields["code"] = "must be 1 to 32 characters of A-Z, 0-9, _, . or -, starting with a letter or digit"
	}
	if !ids.Valid(req.ItemID) {
		fields["itemId"] = "must be a UUID"
	}
	if !ids.Valid(req.SiteID) {
		fields["siteId"] = "must be a UUID"
	}
	optionalUUID(fields, "flowId", req.FlowID)
	if !validQty(req.FixedQty) {
		fields["fixedQty"] = "must be a positive quantity below 10^12 with at most 6 decimals"
	}
	if req.LeadTimeH < 0 || req.LeadTimeH > 8760 {
		fields["leadTimeH"] = "must be 0 to 8760 hours"
	}
	from := time.Now().UTC().Format(store.DateLayout)
	if req.EffectiveFrom != nil {
		from = *req.EffectiveFrom
	}
	fromDate, fromOK := parseDate(fields, "effectiveFrom", from)
	if req.EffectiveTo != nil {
		if to, ok := parseDate(fields, "effectiveTo", *req.EffectiveTo); ok && fromOK && to.Before(fromDate) {
			fields["effectiveTo"] = "must not be before effectiveFrom"
		}
	}
	// Only fixed rules carry a number of kanbans, only event rules an event source.
	if req.Replenishment != string(domain.ReplenishFixed) && req.NumberOfKanbans != nil {
		fields["numberOfKanbans"] = "is only allowed on fixed rules"
	}
	if req.Replenishment != string(domain.ReplenishEvent) && req.EventSource != nil {
		fields["eventSource"] = "is only allowed on event rules"
	}
	if len(fields) > 0 {
		invalid(w, r, fields)
		return
	}
	rule, fresh, err := a.store.CreateRule(r.Context(), store.Rule{Code: req.Code, ItemID: req.ItemID, SiteID: req.SiteID, FlowID: req.FlowID,
		RuleType: req.RuleType, Replenishment: req.Replenishment, FixedQty: req.FixedQty, NumberOfKanbans: req.NumberOfKanbans, LeadTimeH: req.LeadTimeH,
		EventSource: req.EventSource, ExactQty: req.ExactQty, EffectiveFrom: from, EffectiveTo: req.EffectiveTo}, idem)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeCommand(w, fresh, http.StatusCreated, rule)
}

func (a *API) listRules(w http.ResponseWriter, r *http.Request) {
	if _, ok := principal(w, r); !ok {
		return
	}
	limit, ok := limitParam(w, r)
	if !ok {
		return
	}
	fields := map[string]string{}
	q := uuidQuery(r, fields, "flowId")
	if len(fields) > 0 {
		invalid(w, r, fields)
		return
	}
	items, err := a.store.ListRules(r.Context(), q["flowId"], limit)
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
}

func (a *API) getRule(w http.ResponseWriter, r *http.Request) {
	if _, ok := principal(w, r); !ok {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	rule, err := a.store.GetRule(r.Context(), id)
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, rule)
}

type fixedCountRequest struct {
	DailyDemand  decimal.Decimal `json:"dailyDemand"`
	LeadTimeDays decimal.Decimal `json:"leadTimeDays"`
	SafetyPct    decimal.Decimal `json:"safetyPct"`
	FixedQty     decimal.Decimal `json:"fixedQty"`
}

// fixedCount is the BR-24.7 suggestion for a fixed rule's number of kanbans.
// It writes nothing; the suggestion is never applied automatically.
func (a *API) fixedCount(w http.ResponseWriter, r *http.Request) {
	if _, ok := principal(w, r); !ok {
		return
	}
	var req fixedCountRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	n, err := domain.FixedKanbanCount(req.DailyDemand, req.LeadTimeDays, req.SafetyPct, req.FixedQty)
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]int64{"numberOfKanbans": n})
}

type generateRequest struct {
	RuleID       string           `json:"ruleId"`
	Count        int              `json:"count"`
	DemandQty    *decimal.Decimal `json:"demandQty"`
	EventRefType *string          `json:"eventRefType"`
	EventRefID   *string          `json:"eventRefId"`
	Due          *time.Time       `json:"due"`
}

func (a *API) generate(w http.ResponseWriter, r *http.Request) {
	if _, ok := principal(w, r); !ok {
		return
	}
	var req generateRequest
	idem, ok := decodeCommand(w, r, &req)
	if !ok {
		return
	}
	fields := map[string]string{}
	if !ids.Valid(req.RuleID) {
		fields["ruleId"] = "must be a UUID"
	}
	if req.Count < 0 || req.Count > domain.MaxKanbansPerRule {
		fields["count"] = fmt.Sprintf("must be 0 (fill the rule) to %d", domain.MaxKanbansPerRule)
	}
	demand := decimal.Zero
	if req.DemandQty != nil {
		demand = *req.DemandQty
		if !validQty(demand) {
			fields["demandQty"] = "must be a positive quantity below 10^12 with at most 6 decimals"
		}
	}
	for name, v := range map[string]*string{"eventRefType": req.EventRefType, "eventRefId": req.EventRefID} {
		if v != nil && (strings.TrimSpace(*v) == "" || len(*v) > maxRefLen) {
			fields[name] = fmt.Sprintf("must be 1 to %d characters", maxRefLen)
		}
	}
	if (req.EventRefType == nil) != (req.EventRefID == nil) {
		fields["eventRefId"] = "eventRefType and eventRefId go together"
	}
	if len(fields) > 0 {
		invalid(w, r, fields)
		return
	}
	cards, fresh, err := a.store.GenerateKanbans(r.Context(), store.Generate{RuleID: req.RuleID, Count: req.Count, DemandQty: demand,
		EventRefType: req.EventRefType, EventRefID: req.EventRefID, Due: req.Due}, idem)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeCommand(w, fresh, http.StatusCreated, map[string]any{"items": cards})
}

func (a *API) getKanban(w http.ResponseWriter, r *http.Request) {
	if _, ok := principal(w, r); !ok {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	k, err := a.store.GetKanban(r.Context(), id)
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, k)
}

type moveRequest struct {
	To      domain.State `json:"to"`
	Version int          `json:"version"`
}

func (a *API) move(w http.ResponseWriter, r *http.Request) {
	if _, ok := principal(w, r); !ok {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var req moveRequest
	idem, ok := decodeCommand(w, r, &req)
	if !ok {
		return
	}
	fields := map[string]string{}
	if !domain.ValidState(req.To) {
		fields["to"] = "must be not-planned, planned, prepared, in-progress, completed or emptied"
	}
	if req.Version < 1 {
		fields["version"] = "must be the version you read (1 or more)"
	}
	if len(fields) > 0 {
		invalid(w, r, fields)
		return
	}
	k, fresh, err := a.store.MoveKanban(r.Context(), id, req.To, req.Version, idem)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeCommand(w, fresh, http.StatusOK, k)
}

// column is one board column (FR-24.10): the cards in one state.
type column struct {
	State   domain.State   `json:"state"`
	Kanbans []store.Kanban `json:"kanbans"`
}

// board serves the kanban board: one column per card state in BR-24.6 order
// or, with workCellId, the work-cell board of jobs (jobBoard).
func (a *API) board(w http.ResponseWriter, r *http.Request) {
	if _, ok := principal(w, r); !ok {
		return
	}
	fields := map[string]string{}
	q := uuidQuery(r, fields, "siteId", "flowId", "ruleId", "workCellId")
	if len(fields) > 0 {
		invalid(w, r, fields)
		return
	}
	if q["workCellId"] != "" {
		a.jobBoard(w, r, store.BoardFilter{SiteID: q["siteId"], FlowID: q["flowId"], RuleID: q["ruleId"], WorkCellID: q["workCellId"]})
		return
	}
	cards, err := a.store.Board(r.Context(), store.BoardFilter{SiteID: q["siteId"], FlowID: q["flowId"], RuleID: q["ruleId"]})
	if err != nil {
		fail(w, r, err)
		return
	}
	var takt *taktDTO
	if q["flowId"] != "" {
		if takt, err = a.flowTakt(r.Context(), q["flowId"]); err != nil {
			fail(w, r, err)
			return
		}
	}
	cols := []column{}
	for _, s := range []domain.State{domain.NotPlanned, domain.Planned, domain.Prepared, domain.InProgress, domain.Completed, domain.Emptied} {
		c := column{State: s, Kanbans: []store.Kanban{}}
		for _, k := range cards {
			if k.State == s {
				c.Kanbans = append(c.Kanbans, k)
			}
		}
		cols = append(cols, c)
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"columns": cols, "truncated": len(cards) == store.MaxBoard, "takt": takt})
}

// taktDTO is the board's takt vs cycle figure (FR-24.14).
type taktDTO struct {
	TaktTimeS          int    `json:"taktTimeS"`
	CycleTimeS         int    `json:"cycleTimeS"`
	BottleneckActivity string `json:"bottleneckActivity"`
	WithinTakt         bool   `json:"withinTakt"`
}

// flowTakt compares the flow's takt time with its slowest activity; nil when
// the flow has no takt time or no activities.
func (a *API) flowTakt(ctx context.Context, flowID string) (*taktDTO, error) {
	f, err := a.store.GetFlow(ctx, flowID)
	if err != nil {
		return nil, err
	}
	if f.TaktTimeS == nil {
		return nil, nil
	}
	acts := make([]domain.ActivityCycle, len(f.Activities))
	for i, act := range f.Activities {
		acts[i] = domain.ActivityCycle{Name: act.Name, CycleTimeS: act.CycleTimeS}
	}
	c, ok := domain.TaktVsCycle(*f.TaktTimeS, acts)
	if !ok {
		return nil, nil
	}
	return &taktDTO{TaktTimeS: c.TaktTimeS, CycleTimeS: c.CycleTimeS, BottleneckActivity: c.Bottleneck, WithinTakt: c.WithinTakt}, nil
}
