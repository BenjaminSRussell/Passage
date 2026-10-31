// Package api is the HTTP layer of the processmfg service: decode, validate,
// check the tenant, call the store or a domain rule, and write JSON or an RFC
// 9457 problem.
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
	"github.com/BenjaminSRussell/passage/services/processmfg/internal/domain"
	"github.com/BenjaminSRussell/passage/services/processmfg/internal/store"
)

// Duties (PRD 4.24.17). Reads and the calculators need DutyRead; the PRD's
// "Post production journals" duty is the wave-21 production.journal.post.
const (
	DutyRead              = "production.batch-order.read"
	DutyFormulaWrite      = "production.formula.write"
	DutyBatchOrderWrite   = "production.batch-order.write"
	DutyCoProductAllocate = "production.journal.post"
)

const (
	idempotencyHeader = "Idempotency-Key"
	maxBody           = 4 << 20
	defaultLimit      = 50
	readyTimeout      = 500 * time.Millisecond
	validationDetail  = "request failed validation"
)

var (
	formulaCode  = regexp.MustCompile(`^[A-Z0-9][A-Z0-9_.-]{0,39}$`)
	currencyCode = regexp.MustCompile(`^[A-Z]{3}$`)
)

// validStatus reports whether s is a batch-order status.
func validStatus(s string) bool {
	switch s {
	case "planned", "released", "started", "reported", "ended", "closed":
		return true
	}
	return false
}

// maxQty is the largest quantity numeric(18,6) holds.
func maxQty() decimal.Decimal { return decimal.New(1, 12) }

// API holds the store.
type API struct{ store store.Store }

// New returns the handler set.
func New(s store.Store) *API { return &API{store: s} }

// Register mounts every route on r. /healthz comes from httpx.NewRouter.
func (a *API) Register(r chi.Router) {
	read := authz.RequireDuty(DutyRead)
	r.Get("/readyz", a.ready)
	r.With(authz.RequireDuty(DutyFormulaWrite)).Post("/v1/production/formulas", a.createFormula)
	r.With(read).Get("/v1/production/formulas", a.listFormulas)
	r.With(read).Get("/v1/production/formulas/{id}", a.getFormula)
	r.With(read).Post("/v1/production/formulas/{id}:scale", a.previewScale)
	r.With(authz.RequireDuty(DutyBatchOrderWrite)).Post("/v1/production/batch-orders", a.createBatchOrder)
	r.With(read).Get("/v1/production/batch-orders", a.listBatchOrders)
	r.With(read).Get("/v1/production/batch-orders/{id}", a.getBatchOrder)
	r.With(authz.RequireDuty(DutyBatchOrderWrite)).Post("/v1/production/batch-orders/{id}:rescale", a.rescale)
	a.registerLifecycle(r)
	r.With(read).Post("/v1/production/potency:consume", handle(consume))
	r.With(read).Post("/v1/production/catch-weight:validate", handle(validateCatchWeight))
	r.With(authz.RequireDuty(DutyCoProductAllocate)).Post("/v1/production/co-products:allocate", handle(allocateCoProducts))
	a.registerBatchRecords(r)
	registerBatches(r)
	registerQuality(r)
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

// fail maps domain errors to problems (malformed input 400, a rule violation
// 422 with its PMF- code, the same-batch shortfall 409 as FR-24.19 asks) and
// writes the result.
func fail(w http.ResponseWriter, r *http.Request, err error) {
	var de *domain.Error
	if errors.As(err, &de) {
		status := http.StatusUnprocessableEntity
		switch de.Code {
		case domain.CodeInvalid:
			status = http.StatusBadRequest
		case domain.CodeSameBatch:
			status = http.StatusConflict
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

// decodeCommand reads the body once, decodes it and, when the command is
// idempotent, fingerprints method, path and body for the Idempotency-Key.
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

// writeCommand writes 201 for a new resource (200 for a changed one) or 200
// with Idempotent-Replayed for a replay.
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

func validQty(d decimal.Decimal) bool {
	return d.IsPositive() && d.LessThan(maxQty()) && d.Exponent() >= -domain.QtyPrecision
}

func pathID(w http.ResponseWriter, r *http.Request) (string, bool) {
	id := chi.URLParam(r, "id")
	if !ids.Valid(id) {
		invalid(w, r, map[string]string{"id": "must be a UUID"})
		return "", false
	}
	return id, true
}

type formulaLineRequest struct {
	LineNumber        int             `json:"lineNumber"`
	ItemID            string          `json:"itemId"`
	Type              domain.LineType `json:"type"`
	Qty               decimal.Decimal `json:"qty"`
	Percent           decimal.Decimal `json:"percent"`
	Scalable          *bool           `json:"scalable"`
	PercentControlled bool            `json:"percentControlled"`
}

type formulaRequest struct {
	Code         string               `json:"code"`
	Name         string               `json:"name"`
	ItemID       string               `json:"itemId"`
	BomVersionID *string              `json:"bomVersionId"`
	Size         decimal.Decimal      `json:"size"`
	Unit         string               `json:"unit"`
	Lines        []formulaLineRequest `json:"lines"`
}

func (a *API) createFormula(w http.ResponseWriter, r *http.Request) {
	if _, ok := principal(w, r); !ok {
		return
	}
	var req formulaRequest
	idem, ok := decodeCommand(w, r, &req)
	if !ok {
		return
	}
	fields := map[string]string{}
	if !formulaCode.MatchString(req.Code) {
		fields["code"] = "must be 1 to 40 characters of A-Z, 0-9, _, . or -, starting with a letter or digit"
	}
	if n := len(strings.TrimSpace(req.Name)); n < 1 || n > 120 {
		fields["name"] = "must be 1 to 120 characters"
	}
	if !ids.Valid(req.ItemID) {
		fields["itemId"] = "must be a UUID"
	}
	if req.BomVersionID != nil && !ids.Valid(*req.BomVersionID) {
		fields["bomVersionId"] = "must be a UUID"
	}
	if !validQty(req.Size) {
		fields["size"] = "must be a positive quantity below 10^12 with at most 6 decimals"
	}
	if n := len(strings.TrimSpace(req.Unit)); n < 1 || n > 10 {
		fields["unit"] = "must be 1 to 10 characters"
	}
	lines := make([]store.FormulaLine, len(req.Lines))
	for i, l := range req.Lines {
		if !ids.Valid(l.ItemID) {
			fields[fmt.Sprintf("lines[%d].itemId", i)] = "must be a UUID"
		}
		if l.Qty.GreaterThanOrEqual(maxQty()) || l.Qty.Exponent() < -domain.QtyPrecision {
			fields[fmt.Sprintf("lines[%d].qty", i)] = "must be below 10^12 with at most 6 decimals"
		}
		if l.Percent.Exponent() < -4 {
			fields[fmt.Sprintf("lines[%d].percent", i)] = "must have at most 4 decimals"
		}
		scalable := true
		if l.Scalable != nil {
			scalable = *l.Scalable
		}
		lines[i] = store.FormulaLine{LineNumber: l.LineNumber, ItemID: l.ItemID, Type: l.Type, Qty: l.Qty, Percent: l.Percent,
			Scalable: scalable, PercentControlled: l.PercentControlled}
	}
	if len(fields) > 0 {
		invalid(w, r, fields)
		return
	}
	f, fresh, err := a.store.CreateFormula(r.Context(), store.Formula{Code: req.Code, Name: strings.TrimSpace(req.Name), ItemID: req.ItemID,
		BomVersionID: req.BomVersionID, Size: req.Size, Unit: strings.TrimSpace(req.Unit), Lines: lines}, idem)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeCommand(w, fresh, http.StatusCreated, f)
}

func (a *API) listFormulas(w http.ResponseWriter, r *http.Request) {
	if _, ok := principal(w, r); !ok {
		return
	}
	limit, ok := limitParam(w, r)
	if !ok {
		return
	}
	items, err := a.store.ListFormulas(r.Context(), limit)
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
}

func (a *API) getFormula(w http.ResponseWriter, r *http.Request) {
	if _, ok := principal(w, r); !ok {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	f, err := a.store.GetFormula(r.Context(), id)
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, f)
}

type scaleRequest struct {
	BatchSize decimal.Decimal `json:"batchSize"`
}

type scaledLineDTO struct {
	LineNumber int             `json:"lineNumber"`
	ItemID     string          `json:"itemId"`
	Type       domain.LineType `json:"type"`
	FormulaQty decimal.Decimal `json:"formulaQty"`
	Qty        decimal.Decimal `json:"qty"`
}

type scaleResponse struct {
	BatchSize   decimal.Decimal `json:"batchSize"`
	ScaleFactor decimal.Decimal `json:"scaleFactor"`
	Lines       []scaledLineDTO `json:"lines"`
}

// previewScale scales a stored formula to a batch size without writing
// (BR-24.1; the "side-by-side preview" of 4.24.6).
func (a *API) previewScale(w http.ResponseWriter, r *http.Request) {
	if _, ok := principal(w, r); !ok {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var req scaleRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if !validQty(req.BatchSize) {
		invalid(w, r, map[string]string{"batchSize": "must be a positive quantity below 10^12 with at most 6 decimals"})
		return
	}
	f, err := a.store.GetFormula(r.Context(), id)
	if err != nil {
		fail(w, r, err)
		return
	}
	lines := make([]domain.FormulaLine, len(f.Lines))
	for i, l := range f.Lines {
		lines[i] = domain.FormulaLine{LineNumber: l.LineNumber, ItemID: l.ItemID, Type: l.Type, Qty: l.Qty, Percent: l.Percent,
			Scalable: l.Scalable, PercentControlled: l.PercentControlled}
	}
	res, err := domain.Scale(domain.Formula{Size: f.Size, Lines: lines}, req.BatchSize)
	if err != nil {
		fail(w, r, err)
		return
	}
	out := scaleResponse{BatchSize: req.BatchSize, ScaleFactor: res.ScaleFactor, Lines: make([]scaledLineDTO, len(res.Lines))}
	for i, l := range res.Lines {
		out.Lines[i] = scaledLineDTO{LineNumber: l.LineNumber, ItemID: l.ItemID, Type: l.Type, FormulaQty: l.FormulaQty, Qty: l.Qty}
	}
	httpx.JSON(w, http.StatusOK, out)
}

type batchOrderRequest struct {
	FormulaID     string          `json:"formulaId"`
	BatchSize     decimal.Decimal `json:"batchSize"`
	LegalEntityID string          `json:"legalEntityId"`
}

func (a *API) createBatchOrder(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	var req batchOrderRequest
	idem, ok := decodeCommand(w, r, &req)
	if !ok {
		return
	}
	if req.LegalEntityID == "" {
		req.LegalEntityID = p.LegalEntityID
	}
	fields := map[string]string{}
	if !ids.Valid(req.FormulaID) {
		fields["formulaId"] = "must be a UUID"
	}
	if !validQty(req.BatchSize) {
		fields["batchSize"] = "must be a positive quantity below 10^12 with at most 6 decimals"
	}
	if !ids.Valid(req.LegalEntityID) {
		fields["legalEntityId"] = "must be a UUID (defaults to the caller's legal entity)"
	}
	if len(fields) > 0 {
		invalid(w, r, fields)
		return
	}
	if !tenant.InLegalEntityScope(p, req.LegalEntityID) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusForbidden, "AUTH-0003", "legal entity "+req.LegalEntityID+" is outside your scope"))
		return
	}
	o, fresh, err := a.store.CreateBatchOrder(r.Context(), store.NewBatchOrder{LegalEntityID: req.LegalEntityID, FormulaID: req.FormulaID,
		BatchSize: req.BatchSize}, idem)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeCommand(w, fresh, http.StatusCreated, o)
}

func (a *API) listBatchOrders(w http.ResponseWriter, r *http.Request) {
	if _, ok := principal(w, r); !ok {
		return
	}
	limit, ok := limitParam(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	f := store.OrderFilter{Status: q.Get("status"), FormulaID: q.Get("formulaId"), Limit: limit}
	if f.Status != "" && !validStatus(f.Status) {
		invalid(w, r, map[string]string{"status": "must be planned, released, started, reported, ended or closed"})
		return
	}
	if f.FormulaID != "" && !ids.Valid(f.FormulaID) {
		invalid(w, r, map[string]string{"formulaId": "must be a UUID"})
		return
	}
	items, err := a.store.ListBatchOrders(r.Context(), f)
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
}

func (a *API) getBatchOrder(w http.ResponseWriter, r *http.Request) {
	if _, ok := principal(w, r); !ok {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	o, err := a.store.GetBatchOrder(r.Context(), id)
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, o)
}

type rescaleRequest struct {
	BatchSize decimal.Decimal `json:"batchSize"`
	Version   int             `json:"version"`
}

func (a *API) rescale(w http.ResponseWriter, r *http.Request) {
	if _, ok := principal(w, r); !ok {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var req rescaleRequest
	idem, ok := decodeCommand(w, r, &req)
	if !ok {
		return
	}
	fields := map[string]string{}
	if !validQty(req.BatchSize) {
		fields["batchSize"] = "must be a positive quantity below 10^12 with at most 6 decimals"
	}
	if req.Version < 1 {
		fields["version"] = "must be the version you read (1 or more)"
	}
	if len(fields) > 0 {
		invalid(w, r, fields)
		return
	}
	o, fresh, err := a.store.RescaleBatchOrder(r.Context(), id, req.BatchSize, req.Version, idem)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeCommand(w, fresh, http.StatusOK, o)
}

// handle serves a stateless calculator: decode the body, run fn, write the
// result or the rule's problem.
func handle[In any, Out any](fn func(In) (Out, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var in In
		if err := httpx.Decode(r, &in); err != nil {
			httpx.Fail(w, r, err)
			return
		}
		out, err := fn(in)
		if err != nil {
			fail(w, r, err)
			return
		}
		httpx.JSON(w, http.StatusOK, out)
	}
}

type potencyRequest struct {
	Planned             decimal.Decimal `json:"planned"`
	StandardPotency     decimal.Decimal `json:"standardPotency"`
	BatchPotency        decimal.Decimal `json:"batchPotency"`
	CompensatingPlanned decimal.Decimal `json:"compensatingPlanned"`
}

type potencyResponse struct {
	Qty             decimal.Decimal `json:"qty"`
	CompensatingQty decimal.Decimal `json:"compensatingQty"`
}

func consume(in potencyRequest) (potencyResponse, error) {
	res, err := domain.ConsumePotency(in.Planned, in.StandardPotency, in.BatchPotency, in.CompensatingPlanned)
	if err != nil {
		return potencyResponse{}, err
	}
	return potencyResponse{Qty: res.Qty, CompensatingQty: res.CompensatingQty}, nil
}

type catchWeightRequest struct {
	Nominal decimal.Decimal `json:"nominal"`
	Min     decimal.Decimal `json:"min"`
	Max     decimal.Decimal `json:"max"`
	CWQty   int             `json:"cwQty"`
	Weight  decimal.Decimal `json:"weight"`
}

type validResponse struct {
	Valid bool `json:"valid"`
}

func validateCatchWeight(in catchWeightRequest) (validResponse, error) {
	spec := domain.CatchWeight{Nominal: in.Nominal, Min: in.Min, Max: in.Max}
	if err := domain.ValidateCatchWeight(spec, in.CWQty, in.Weight); err != nil {
		return validResponse{}, err
	}
	return validResponse{Valid: true}, nil
}

type productDTO struct {
	ItemID       string          `json:"itemId"`
	StandardCost decimal.Decimal `json:"standardCost"`
	RafQty       decimal.Decimal `json:"rafQty"`
	Percent      decimal.Decimal `json:"percent"`
}

type byProductDTO struct {
	ItemID   string          `json:"itemId"`
	CostType string          `json:"costType"`
	Qty      decimal.Decimal `json:"qty"`
	Rate     decimal.Decimal `json:"rate"`
}

type allocateRequest struct {
	BatchCost  decimal.Decimal `json:"batchCost"`
	Currency   string          `json:"currency"`
	Method     string          `json:"method"`
	Main       productDTO      `json:"main"`
	CoProducts []productDTO    `json:"coProducts"`
	ByProducts []byProductDTO  `json:"byProducts"`
}

type allocatedDTO struct {
	ItemID  string          `json:"itemId"`
	Amount  decimal.Decimal `json:"amount"`
	Percent decimal.Decimal `json:"percent"`
}

type allocateResponse struct {
	Currency   string          `json:"currency"`
	Main       allocatedDTO    `json:"main"`
	CoProducts []allocatedDTO  `json:"coProducts"`
	Burdens    []allocatedDTO  `json:"byProductBurdens"`
	Total      decimal.Decimal `json:"total"`
}

func toProduct(p productDTO) domain.Product {
	return domain.Product{ItemID: p.ItemID, StandardCost: p.StandardCost, RafQty: p.RafQty, Percent: p.Percent}
}

func toAllocated(a domain.Allocated) allocatedDTO {
	return allocatedDTO{ItemID: a.ItemID, Amount: a.Amount, Percent: a.Percent}
}

// allocateCoProducts previews the BR-24.3 allocation. Amounts are in Currency
// (ISO 4217); the rule itself is currency-neutral decimal arithmetic.
func allocateCoProducts(in allocateRequest) (allocateResponse, error) {
	if !currencyCode.MatchString(in.Currency) {
		return allocateResponse{}, &domain.Error{Code: domain.CodeInvalid, Detail: "currency must be an ISO 4217 code such as USD"}
	}
	if len(in.CoProducts) > domain.MaxFormulaLines || len(in.ByProducts) > domain.MaxFormulaLines {
		return allocateResponse{}, &domain.Error{Code: domain.CodeInvalid, Detail: fmt.Sprintf("at most %d co-products and %d by-products", domain.MaxFormulaLines, domain.MaxFormulaLines)}
	}
	co := make([]domain.Product, len(in.CoProducts))
	for i, p := range in.CoProducts {
		co[i] = toProduct(p)
	}
	by := make([]domain.ByProduct, len(in.ByProducts))
	for i, b := range in.ByProducts {
		by[i] = domain.ByProduct{ItemID: b.ItemID, CostType: b.CostType, Qty: b.Qty, Rate: b.Rate}
	}
	res, err := domain.AllocateCost(in.BatchCost, domain.AllocationMethod(in.Method), toProduct(in.Main), co, by)
	if err != nil {
		return allocateResponse{}, err
	}
	out := allocateResponse{Currency: in.Currency, Main: toAllocated(res.Main), CoProducts: []allocatedDTO{}, Burdens: []allocatedDTO{}, Total: res.Total}
	for _, c := range res.CoProducts {
		out.CoProducts = append(out.CoProducts, toAllocated(c))
	}
	for _, b := range res.Burdens {
		out.Burdens = append(out.Burdens, toAllocated(b))
	}
	return out, nil
}
