// Package api exposes the production order HTTP API. Handlers decode, validate,
// read the tenant principal, then call the store, which applies the domain rules.
package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"regexp"
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
	"github.com/BenjaminSRussell/passage/services/production/internal/domain"
	"github.com/BenjaminSRussell/passage/services/production/internal/store"
)

// Duties checked on the routes (PRD 4.21.17). Bundles and OPA data live in policies/ (wave 2).
const (
	DutyRead        = "production.order.read"        // View production orders
	DutyWrite       = "production.order.write"       // Maintain production orders
	DutyRelease     = "production.order.release"     // Release production orders
	DutyJournalPost = "production.journal.post"      // Post production journals (report as finished)
	DutyEnd         = "production.order.end"         // End production orders
	DutyZeroOutput  = "production.order.zero-output" // End production order with zero output
	DutyCostRead    = "production.cost.read"         // View production costs
)

const (
	validationCode    = "COM-0001"
	validationText    = "request failed validation"
	unknownActionCode = "PRODUCTION_UNKNOWN_ACTION"
	idempotencyName   = "Idempotency-Key"
	maxIdempotencyKey = 255
	maxLineNo         = 20
	maxBOMDepth       = 12
	maxBOMLines       = 5000
	maxOperations     = 500
	defaultPageSize   = 50
	maxPageSize       = 200
	readyTimeout      = 500 * time.Millisecond
	maxCommandBody    = 64 << 10
)

var (
	currencyCode = regexp.MustCompile(`^[A-Z]{3}$`)
	originTypes  = map[string]bool{"PlannedOrder": true, "SalesOrderLine": true, "Manual": true, "Kanban": true}
	commands     = map[string]domain.Command{
		"estimate": domain.CmdEstimate, "schedule": domain.CmdSchedule, "release": domain.CmdRelease, "start": domain.CmdStart,
		"report-finished": domain.CmdReportFinished, "end": domain.CmdEnd, "cancel": domain.CmdCancel, "reset-status": domain.CmdResetStatus,
	}
	commandDuty = map[domain.Command]string{
		domain.CmdEstimate: DutyWrite, domain.CmdSchedule: DutyWrite,
		domain.CmdRelease: DutyRelease, domain.CmdStart: DutyRelease, domain.CmdCancel: DutyRelease, domain.CmdResetStatus: DutyRelease,
		domain.CmdReportFinished: DutyJournalPost, domain.CmdEnd: DutyEnd,
	}
	// maxQty bounds a quantity or amount to numeric(18,6): 12 integer digits.
	maxQty = decimal.New(1, 12)
)

// Config holds the service parameters the handlers apply (PRD 4.21.13).
type Config struct {
	// RAFTolerancePct is the over-delivery allowed on report as finished
	// (PRODUCTION_RAF_TOLERANCE_PCT, default 0).
	RAFTolerancePct decimal.Decimal
}

// API holds the handlers and their counters.
type API struct {
	store    store.Store
	cfg      Config
	created  atomic.Int64
	replayed atomic.Int64
	moved    atomic.Int64
	refused  atomic.Int64
}

// New returns the API over st.
func New(st store.Store, cfg Config) *API { return &API{store: st, cfg: cfg} }

// Register mounts the production routes on r. Duties are enforced by the
// platform authz middleware; a status command's duty depends on the command,
// so that route resolves it before applying the same middleware.
func (a *API) Register(r chi.Router) {
	r.Get("/readyz", a.ready)
	r.With(authz.RequireDuty(DutyRead)).Get("/v1/production/orders", a.listOrders)
	r.With(authz.RequireDuty(DutyWrite)).Post("/v1/production/orders", a.createOrder)
	r.With(authz.RequireDuty(DutyRead)).Get("/v1/production/orders/{id}", a.getOrder)
	r.With(authz.RequireDuty(DutyWrite)).Patch("/v1/production/orders/{id}", a.patchOrder)
	r.With(authz.RequireDuty(DutyRead)).Get("/v1/production/orders/{id}/status-log", a.getStatusLog)
	r.With(authz.RequireDuty(DutyCostRead)).Get("/v1/production/orders/{id}/costs", a.getCosts)
	r.Post("/v1/production/orders/{id}", a.transition)
}

// Metrics serves Prometheus text for the admin port.
func (a *API) Metrics() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		fmt.Fprintf(w, "# HELP production_orders_created_total Production orders created; idempotent replays are not counted.\n# TYPE production_orders_created_total counter\nproduction_orders_created_total %d\n", a.created.Load())
		fmt.Fprintf(w, "# HELP production_transitions_total Status commands applied.\n# TYPE production_transitions_total counter\nproduction_transitions_total %d\n", a.moved.Load())
		fmt.Fprintf(w, "# HELP production_idempotent_replays_total Requests answered from a stored Idempotency-Key.\n# TYPE production_idempotent_replays_total counter\nproduction_idempotent_replays_total %d\n", a.replayed.Load())
		fmt.Fprintf(w, "# HELP production_transitions_refused_total Commands refused by the state machine, a guard or a precondition.\n# TYPE production_transitions_refused_total counter\nproduction_transitions_refused_total %d\n", a.refused.Load())
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

// ---- create ----

type createRequest struct {
	LegalEntityID string                `json:"legalEntityId"`
	ProductID     string                `json:"productId"`
	SiteID        string                `json:"siteId"`
	WarehouseID   string                `json:"warehouseId"`
	Currency      string                `json:"currency"`
	Quantity      decimal.Decimal       `json:"quantity"`
	DeliveryDate  *string               `json:"deliveryDate"`
	OriginType    string                `json:"originType"`
	OriginID      *string               `json:"originId"`
	BOM           []domain.BOMComponent `json:"bom"`
	Operations    []domain.Operation    `json:"operations"`
}

func (a *API) createOrder(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	var req createRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if req.LegalEntityID == "" {
		req.LegalEntityID = p.LegalEntityID
	}
	if req.OriginType == "" {
		req.OriginType = "Manual"
	}
	key, fields := idempotencyKey(r)
	validateCreate(req, fields)
	if p.LegalEntityID != "" && req.LegalEntityID != p.LegalEntityID {
		fields["legalEntityId"] = "must be the legal entity of the caller"
	}
	if len(fields) > 0 {
		invalid(w, r, fields)
		return
	}
	lines, err := domain.Explode(req.BOM, req.Quantity)
	if err != nil {
		httpx.Fail(w, r, problemFrom(err))
		return
	}
	for _, l := range lines {
		if !l.QtyRequired.LessThan(maxQty) {
			httpx.Fail(w, r, httpx.NewProblem(http.StatusUnprocessableEntity, domain.CodeInvalidInput, "line "+l.LineNo+": required quantity is too large"))
			return
		}
	}
	ops, err := domain.PlanOperations(req.Operations, req.Quantity)
	if err != nil {
		httpx.Fail(w, r, problemFrom(err))
		return
	}
	order, created, err := a.store.CreateOrder(r.Context(), store.NewOrder{
		LegalEntityID: req.LegalEntityID, ProductID: req.ProductID, SiteID: req.SiteID, WarehouseID: req.WarehouseID,
		Currency: req.Currency, Quantity: req.Quantity, DeliveryDate: req.DeliveryDate, OriginType: req.OriginType, OriginID: req.OriginID,
		Lines: lines, Operations: ops, IdempotencyKey: key, RequestHash: hashOf(req),
	})
	if err != nil {
		a.fail(w, r, "create production order", err)
		return
	}
	w.Header().Set("ETag", etag(order.Version))
	w.Header().Set("Location", "/v1/production/orders/"+order.ID)
	if !created {
		a.replayed.Add(1)
		w.Header().Set("Idempotent-Replayed", "true")
		httpx.JSON(w, http.StatusOK, order)
		return
	}
	a.created.Add(1)
	httpx.JSON(w, http.StatusCreated, order)
}

// ---- reads ----

func (a *API) listOrders(w http.ResponseWriter, r *http.Request) {
	if _, ok := principal(w, r); !ok {
		return
	}
	q := r.URL.Query()
	f := store.ListFilter{Status: domain.Status(q.Get("status")), LegalEntityID: q.Get("legalEntityId"), SiteID: q.Get("siteId"),
		ProductID: q.Get("productId"), Cursor: q.Get("cursor"), Limit: defaultPageSize}
	fields := map[string]string{}
	if f.Status != "" && !f.Status.Valid() {
		fields["status"] = "must be one of Created, Estimated, Scheduled, Released, Started, ReportedAsFinished, Ended, Cancelled"
	}
	for name, v := range map[string]string{"legalEntityId": f.LegalEntityID, "siteId": f.SiteID, "productId": f.ProductID, "cursor": f.Cursor} {
		if v != "" && !ids.Valid(v) {
			fields[name] = "must be a UUID"
		}
	}
	if raw := q.Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > maxPageSize {
			fields["limit"] = fmt.Sprintf("must be a whole number from 1 to %d", maxPageSize)
		}
		f.Limit = n
	}
	if len(fields) > 0 {
		invalid(w, r, fields)
		return
	}
	page, err := a.store.ListOrders(r.Context(), f)
	if err != nil {
		a.fail(w, r, "list production orders", err)
		return
	}
	httpx.JSON(w, http.StatusOK, page)
}

func (a *API) getOrder(w http.ResponseWriter, r *http.Request) {
	order, ok := a.loadByID(w, r)
	if !ok {
		return
	}
	w.Header().Set("ETag", etag(order.Version))
	httpx.JSON(w, http.StatusOK, order)
}

func (a *API) getStatusLog(w http.ResponseWriter, r *http.Request) {
	id, ok := orderID(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	entries, err := a.store.StatusLog(r.Context(), id)
	if err != nil {
		a.fail(w, r, "read production status log", err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"orderId": id, "items": entries})
}

// costView is the planned-cost read (FR-21.11): totals by cost group to cents,
// the grand total, and the 4-decimal rows behind them. Actual cost and
// variances arrive with journal posting and settlement.
type costView struct {
	OrderID  string                      `json:"orderId"`
	Currency string                      `json:"currency"`
	Totals   map[domain.CostGroup]string `json:"totals"`
	Total    string                      `json:"total"`
	Rows     []domain.CostRow            `json:"rows"`
}

func (a *API) getCosts(w http.ResponseWriter, r *http.Request) {
	order, ok := a.loadByID(w, r)
	if !ok {
		return
	}
	sums := map[domain.CostGroup]decimal.Decimal{}
	total := decimal.Zero
	for _, c := range order.Costs {
		sums[c.Group] = sums[c.Group].Add(c.Amount)
		total = total.Add(c.Amount)
	}
	totals := map[domain.CostGroup]string{}
	for g, v := range sums {
		totals[g] = v.StringFixed(2)
	}
	httpx.JSON(w, http.StatusOK, costView{OrderID: order.ID, Currency: order.Currency, Totals: totals, Total: total.StringFixed(2), Rows: order.Costs})
}

// ---- PATCH ----

type patchRequest struct {
	Quantity     *decimal.Decimal `json:"quantity"`
	DeliveryDate json.RawMessage  `json:"deliveryDate"`
}

func (a *API) patchOrder(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	id, ok := orderID(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	ifMatch, err := parseIfMatch(r.Header.Get("If-Match"))
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	var req patchRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	u := store.OrderUpdate{OrderID: id, IfMatch: ifMatch, Quantity: req.Quantity, Actor: p.UserID}
	fields := map[string]string{}
	if req.Quantity != nil {
		checkQuantity("quantity", *req.Quantity, true, fields)
	}
	if len(req.DeliveryDate) > 0 {
		u.SetDeliveryDate = true
		if string(req.DeliveryDate) != "null" {
			var date string
			if json.Unmarshal(req.DeliveryDate, &date) != nil || !isDate(date) {
				fields["deliveryDate"] = "must be a date (YYYY-MM-DD) or null"
			}
			u.DeliveryDate = &date
		}
	}
	if req.Quantity == nil && !u.SetDeliveryDate {
		fields["body"] = "set quantity or deliveryDate"
	}
	if len(fields) > 0 {
		invalid(w, r, fields)
		return
	}
	order, err := a.store.UpdateOrder(r.Context(), u)
	if err != nil {
		a.fail(w, r, "update production order", err)
		return
	}
	w.Header().Set("ETag", etag(order.Version))
	httpx.JSON(w, http.StatusOK, order)
}

// ---- status commands ----

// commandRequest is the optional body of a `:command`. Quantity and
// ErrorQuantity belong to :report-finished, Status to :reset-status.
type commandRequest struct {
	Quantity      *decimal.Decimal `json:"quantity"`
	ErrorQuantity *decimal.Decimal `json:"errorQuantity"`
	Status        domain.Status    `json:"status"`
}

func (a *API) transition(w http.ResponseWriter, r *http.Request) {
	raw, name, found := strings.Cut(chi.URLParam(r, "id"), ":")
	cmd, known := commands[name]
	if !found || !ids.Valid(raw) || !known {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, unknownActionCode,
			"use :estimate, :schedule, :release, :start, :report-finished, :end, :cancel or :reset-status on an order"))
		return
	}
	authz.RequireDuty(commandDuty[cmd])(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a.applyCommand(w, r, raw, cmd)
	})).ServeHTTP(w, r)
}

func (a *API) applyCommand(w http.ResponseWriter, r *http.Request, id string, cmd domain.Command) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	ifMatch, err := parseIfMatch(r.Header.Get("If-Match"))
	if err != nil {
		a.refuse(w, r, err)
		return
	}
	var body commandRequest
	if err := decodeOptional(r, &body); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	key, fields := idempotencyKey(r)
	t := store.Transition{OrderID: id, IfMatch: ifMatch, Command: cmd, ZeroOutputDuty: p.HasDuty(DutyZeroOutput),
		RAFTolerancePct: a.cfg.RAFTolerancePct, Actor: p.UserID, TraceID: middleware.GetReqID(r.Context()), IdempotencyKey: key}
	validateCommand(cmd, body, &t, fields)
	if len(fields) > 0 {
		invalid(w, r, fields)
		return
	}
	t.RequestHash = hashOf(struct {
		Command domain.Command
		ResetTo domain.Status
		Good    string
		Error   string
	}{cmd, t.ResetTo, t.Good.String(), t.Error.String()})
	order, replayed, err := a.store.Transition(r.Context(), t)
	if err != nil {
		a.refuse(w, r, err)
		return
	}
	w.Header().Set("ETag", etag(order.Version))
	if replayed {
		a.replayed.Add(1)
		w.Header().Set("Idempotent-Replayed", "true")
	} else {
		a.moved.Add(1)
	}
	httpx.JSON(w, http.StatusOK, order)
}

func validateCommand(cmd domain.Command, body commandRequest, t *store.Transition, fields map[string]string) {
	if cmd == domain.CmdReportFinished {
		if body.Quantity == nil {
			fields["quantity"] = "is required: the good quantity reported as finished"
		} else {
			checkQuantity("quantity", *body.Quantity, true, fields)
			t.Good = *body.Quantity
		}
		if body.ErrorQuantity != nil {
			checkQuantity("errorQuantity", *body.ErrorQuantity, false, fields)
			t.Error = *body.ErrorQuantity
		}
	} else if body.Quantity != nil || body.ErrorQuantity != nil {
		fields["quantity"] = "is accepted only by :report-finished"
	}
	if body.Status != "" {
		switch {
		case cmd != domain.CmdResetStatus:
			fields["status"] = "is accepted only by :reset-status"
		case !body.Status.Valid():
			fields["status"] = "must be a production order status"
		default:
			t.ResetTo = body.Status
		}
	}
}

// ---- helpers ----

func (a *API) loadByID(w http.ResponseWriter, r *http.Request) (store.Order, bool) {
	id, ok := orderID(w, r, chi.URLParam(r, "id"))
	if !ok {
		return store.Order{}, false
	}
	order, err := a.store.GetOrder(r.Context(), id)
	if err != nil {
		a.fail(w, r, "read production order", err)
		return store.Order{}, false
	}
	return order, true
}

// fail writes err; unexpected errors are logged with the correlation id.
func (a *API) fail(w http.ResponseWriter, r *http.Request, op string, err error) {
	var prob *httpx.Problem
	if !errors.As(err, &prob) {
		slog.Error(op+" failed", "err", err, "correlationId", middleware.GetReqID(r.Context()))
	}
	httpx.Fail(w, r, err)
}

// refuse writes a command failure and counts the refusals (4xx).
func (a *API) refuse(w http.ResponseWriter, r *http.Request, err error) {
	var prob *httpx.Problem
	if errors.As(err, &prob) && prob.Status >= 400 && prob.Status < 500 {
		a.refused.Add(1)
	}
	a.fail(w, r, "production order command", err)
}

func principal(w http.ResponseWriter, r *http.Request) (tenant.Principal, bool) {
	p, ok := tenant.FromContext(r.Context())
	if !ok || !ids.Valid(p.TenantID) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusUnauthorized, "AUTH-0001", "missing or malformed tenant"))
		return tenant.Principal{}, false
	}
	if p.LegalEntityID != "" && !ids.Valid(p.LegalEntityID) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusBadRequest, validationCode, "legal entity must be a single UUID"))
		return tenant.Principal{}, false
	}
	return p, true
}

func orderID(w http.ResponseWriter, r *http.Request, raw string) (string, bool) {
	if !ids.Valid(raw) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, store.CodeOrderNotFound, "production order not found"))
		return "", false
	}
	return raw, true
}

func idempotencyKey(r *http.Request) (string, map[string]string) {
	fields := map[string]string{}
	key := strings.TrimSpace(r.Header.Get(idempotencyName))
	if key == "" || len(key) > maxIdempotencyKey {
		fields[idempotencyName] = fmt.Sprintf("is required, at most %d characters", maxIdempotencyKey)
	}
	return key, fields
}

func parseIfMatch(raw string) (int, error) {
	if raw == "" {
		return 0, httpx.NewProblem(http.StatusPreconditionRequired, "precondition_required", "If-Match with the order version is required")
	}
	v, err := strconv.Atoi(strings.Trim(strings.TrimPrefix(raw, "W/"), `"`))
	if err != nil || v < 1 {
		return 0, httpx.NewProblem(http.StatusBadRequest, validationCode, "If-Match must be the order version")
	}
	return v, nil
}

func etag(version int) string { return fmt.Sprintf(`"%d"`, version) }

// decodeOptional decodes a JSON body when there is one; an empty body is fine.
func decodeOptional(r *http.Request, v any) error {
	body, err := io.ReadAll(http.MaxBytesReader(nil, r.Body, maxCommandBody))
	if err != nil {
		return httpx.NewProblem(http.StatusBadRequest, validationCode, "unreadable body: "+err.Error())
	}
	if len(bytes.TrimSpace(body)) == 0 {
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return httpx.NewProblem(http.StatusBadRequest, validationCode, "malformed JSON body: "+err.Error())
	}
	return nil
}

// hashOf fingerprints a decoded request so an Idempotency-Key replay with a
// different body is refused rather than answered with the first result.
func hashOf(v any) string {
	raw, _ := json.Marshal(v)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func isDate(s string) bool {
	_, err := time.Parse(time.DateOnly, s)
	return err == nil
}

// checkQuantity keeps a quantity inside numeric(18,6): at most 6 decimals and
// 12 integer digits; positive when required, otherwise not negative.
func checkQuantity(name string, d decimal.Decimal, positive bool, fields map[string]string) {
	switch {
	case positive && !d.IsPositive():
		fields[name] = "must be positive"
	case d.IsNegative():
		fields[name] = "must not be negative"
	case d.Exponent() < -domain.QtyScale && !d.Equal(d.Round(domain.QtyScale)):
		fields[name] = fmt.Sprintf("must have at most %d decimals", domain.QtyScale)
	case !d.LessThan(maxQty):
		fields[name] = "is too large"
	}
}

func validateCreate(req createRequest, fields map[string]string) {
	for name, v := range map[string]string{"legalEntityId": req.LegalEntityID, "productId": req.ProductID,
		"siteId": req.SiteID, "warehouseId": req.WarehouseID} {
		if !ids.Valid(v) {
			fields[name] = "must be a UUID"
		}
	}
	if !currencyCode.MatchString(req.Currency) {
		fields["currency"] = "must be an ISO 4217 alpha-3 code"
	}
	checkQuantity("quantity", req.Quantity, true, fields)
	if !originTypes[req.OriginType] {
		fields["originType"] = "must be PlannedOrder, SalesOrderLine, Manual or Kanban"
	}
	if req.OriginID != nil && !ids.Valid(*req.OriginID) {
		fields["originId"] = "must be a UUID"
	}
	if req.DeliveryDate != nil && !isDate(*req.DeliveryDate) {
		fields["deliveryDate"] = "must be a date (YYYY-MM-DD)"
	}
	switch {
	case len(req.Operations) == 0:
		fields["operations"] = "at least one operation is required"
	case len(req.Operations) > maxOperations:
		fields["operations"] = fmt.Sprintf("at most %d operations", maxOperations)
	}
	for i, op := range req.Operations {
		at := fmt.Sprintf("operations[%d]", i)
		if op.OperNumber < 1 {
			fields[at+".operNumber"] = "must be a positive whole number"
		}
		if !ids.Valid(op.ResourceID) {
			fields[at+".resourceId"] = "must be a UUID"
		}
		for name, v := range map[string]decimal.Decimal{"setupTimeH": op.SetupH, "runTimeHPer": op.RunHPer, "scrapPct": op.ScrapPct,
			"laborRate": op.LaborRate, "overheadRate": op.OverheadRate, "subcontractPrice": op.SubcontractPrice} {
			checkQuantity(at+"."+name, v, false, fields)
		}
		if !op.ProcessQty.IsZero() {
			fields[at+".processQty"] = "is computed by the service"
		}
	}
	count := 0
	seen := map[string]bool{}
	for i, c := range req.BOM {
		at := fmt.Sprintf("bom[%d]", i)
		switch {
		case c.LineNo == "" || len(c.LineNo) > maxLineNo:
			fields[at+".lineNo"] = fmt.Sprintf("is required, at most %d characters", maxLineNo)
		case seen[c.LineNo]:
			fields[at+".lineNo"] = "must be unique in the BOM"
		}
		seen[c.LineNo] = true
	}
	validateBOM(req.BOM, "bom", 1, &count, fields)
	if count > maxBOMLines {
		fields["bom"] = fmt.Sprintf("at most %d lines", maxBOMLines)
	}
}

// validateBOM checks item ids and quantities at every level. Line numbers of
// phantom children are derived from the parent, so only the top level needs them.
func validateBOM(comps []domain.BOMComponent, path string, depth int, count *int, fields map[string]string) {
	if depth > maxBOMDepth {
		fields[path] = fmt.Sprintf("phantoms nest at most %d levels", maxBOMDepth)
		return
	}
	for i, c := range comps {
		*count++
		at := fmt.Sprintf("%s[%d]", path, i)
		checkQuantity(at+".qtyPer", c.QtyPer, false, fields)
		checkQuantity(at+".scrapPct", c.ScrapPct, false, fields)
		if c.Phantom {
			if c.ItemID != "" && !ids.Valid(c.ItemID) {
				fields[at+".itemId"] = "must be a UUID"
			}
			validateBOM(c.Children, at+".children", depth+1, count, fields)
			continue
		}
		if len(c.Children) > 0 {
			fields[at+".children"] = "only a phantom line has children"
		}
		if !ids.Valid(c.ItemID) {
			fields[at+".itemId"] = "must be a UUID"
		}
		checkQuantity(at+".seriesSize", c.SeriesSize, false, fields)
		checkQuantity(at+".unitCost", c.UnitCost, false, fields)
	}
}

func invalid(w http.ResponseWriter, r *http.Request, fields map[string]string) {
	p := httpx.NewProblem(http.StatusBadRequest, validationCode, validationText)
	p.Errors = fields
	httpx.Fail(w, r, p)
}

// problemFrom maps a domain rule violation to a 422 problem with its stable code.
func problemFrom(err error) error {
	var de *domain.Error
	if errors.As(err, &de) {
		return httpx.NewProblem(http.StatusUnprocessableEntity, de.Code, de.Detail)
	}
	return err
}
