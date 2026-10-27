// Package api exposes the budgeting HTTP surface: decode, validate, tenant
// check, store, respond. Every command requires Idempotency-Key.
package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/budgeting/internal/domain"
	"github.com/BenjaminSRussell/passage/services/budgeting/internal/store"
)

const (
	idempotencyKey   = "Idempotency-Key"
	dutyConfigure    = "budget.configure"
	dutyMaintain     = "budget.maintain"
	dutyCheck        = "budget.check"
	dutyOverride     = "budget.override"
	dutyView         = "budget.view"
	dutyReserve      = "budget.reserve"
	validationDetail = "request failed validation"
	maxCheckLines    = 500
	maxBodyBytes     = 4 << 20
)

var (
	ruleCode     = regexp.MustCompile(`^[A-Z0-9_-]{2,40}$`)
	currencyCode = regexp.MustCompile(`^[A-Z]{3}$`)
	dateLayout   = "2006-01-02"
	docTypes     = map[string]bool{"Requisition": true, "PurchaseOrder": true, "Invoice": true, "Journal": true}
	statuses     = map[string]bool{"": true, domain.StatusOpen: true, domain.StatusPartiallyRelieved: true, domain.StatusRelieved: true, domain.StatusReleased: true}
	releaseWhy   = map[string]bool{store.ReleaseCancelled: true, store.ReleaseClosed: true}
)

// Store is the persistence surface the handlers need. Commands return the
// status and JSON body to replay for their Idempotency-Key.
type Store interface {
	CreateRule(ctx context.Context, key, hash string, in store.NewRule) (int, []byte, error)
	CheckBudget(ctx context.Context, key, hash string, req store.CheckRequest, override bool) (int, []byte, error)
	RelieveEncumbrance(ctx context.Context, key, hash string, in store.RelieveRequest) (int, []byte, error)
	ReleaseEncumbrance(ctx context.Context, key, hash string, in store.ReleaseRequest) (int, []byte, error)
	PostRegisterEntry(ctx context.Context, key, hash string, req store.RegisterRequest) (int, []byte, error)
	ListRules(ctx context.Context, legalEntityID string) ([]store.Rule, error)
	GetRule(ctx context.Context, id string) (store.Rule, error)
	ListRegisterEntries(ctx context.Context, ruleVersionID string) ([]store.RegisterEntry, error)
	Available(ctx context.Context, ruleVersionID, dim, span string) ([]store.BalanceView, error)
	ListEncumbrances(ctx context.Context, ruleVersionID, status string) ([]store.EncumbranceView, error)
	GetEncumbrance(ctx context.Context, id string) (store.EncumbranceView, error)
	ListChecks(ctx context.Context, ruleVersionID, documentID string) ([]store.CheckResponse, error)
	GetCheck(ctx context.Context, id string) (store.CheckResponse, error)
	CreateReservation(ctx context.Context, key, hash string, req store.ReservationRequest, override bool) (int, []byte, error)
	ConsumeReservation(ctx context.Context, key, hash string, req store.ConsumeRequest) (int, []byte, error)
	CloseReservation(ctx context.Context, key, hash, id string) (int, []byte, error)
	ExpireReservations(ctx context.Context, key, hash, asOf string) (int, []byte, error)
	ListReservations(ctx context.Context, f store.ReservationFilter) ([]store.Reservation, error)
	GetReservation(ctx context.Context, id string) (store.Reservation, error)
	CreateProgram(ctx context.Context, key, hash string, in store.NewProgram) (int, []byte, error)
	DistributePositions(ctx context.Context, key, hash, programID string, req store.DistributeRequest) (int, []byte, error)
	AssignObject(ctx context.Context, key, hash, programID string, req store.AssignRequest) (int, []byte, error)
	CarryForward(ctx context.Context, key, hash, programID string, toYear int) (int, []byte, error)
	ListPrograms(ctx context.Context, legalEntityID string) ([]store.Program, error)
	GetProgram(ctx context.Context, id string) (store.ProgramView, error)
	Ping(ctx context.Context) error
}

// API holds the handlers.
type API struct {
	store Store
}

// New returns the API over st.
func New(st Store) *API { return &API{store: st} }

// Register mounts the routes on r.
func (a *API) Register(r chi.Router) {
	r.With(authz.RequireDuty(dutyConfigure)).Post("/v1/budget/control-rules", a.createRule)
	r.With(authz.RequireDuty(dutyMaintain)).Post("/v1/budget/register-entries", a.postRegisterEntry)
	r.With(authz.RequireDuty(dutyCheck)).Post("/v1/budget/checks", a.checkBudget)
	r.With(authz.RequireDuty(dutyCheck)).Post("/v1/budget/encumbrances/{id}:relieve", a.relieve)
	r.With(authz.RequireDuty(dutyCheck)).Post("/v1/budget/encumbrances/{id}:release", a.release)
	r.With(authz.RequireDuty(dutyReserve)).Post("/v1/budget/reservations", a.createReservation)
	r.With(authz.RequireDuty(dutyReserve)).Post("/v1/budget/reservations:expire", a.expireReservations)
	r.With(authz.RequireDuty(dutyReserve)).Post("/v1/budget/reservations/{id}:close", a.closeReservation)
	r.With(authz.RequireDuty(dutyCheck)).Post("/v1/budget/reservations/{id}:consume", a.consumeReservation)

	view := r.With(authz.RequireDuty(dutyView))
	view.Get("/v1/budget/control-rules", a.listRules)
	view.Get("/v1/budget/control-rules/{id}", a.getRule)
	view.Get("/v1/budget/register-entries", a.listRegisterEntries)
	view.Get("/v1/budget/available", a.available)
	view.Get("/v1/budget/encumbrances", a.listEncumbrances)
	view.Get("/v1/budget/encumbrances/{id}", a.getEncumbrance)
	view.Get("/v1/budget/checks", a.listChecks)
	view.Get("/v1/budget/checks/{id}", a.getCheck)
	view.Get("/v1/budget/reservations", a.listReservations)
	view.Get("/v1/budget/reservations/{id}", a.getReservation)
	a.registerPrograms(r)
}

type createRuleRequest struct {
	LegalEntityID string          `json:"legalEntityId"`
	Code          string          `json:"code"`
	WarnPct       decimal.Decimal `json:"warnPct"`
	BlockPct      decimal.Decimal `json:"blockPct"`
	Currency      string          `json:"currency"`
	EffectiveFrom string          `json:"effectiveFrom"`
}

type checkLineRequest struct {
	LineID       string          `json:"lineId"`
	DimensionKey string          `json:"dimensionKey"`
	SpanKey      string          `json:"spanKey"`
	Amount       decimal.Decimal `json:"amount"`
}

type checkRequest struct {
	RuleVersionID string             `json:"ruleVersionId"`
	DocumentID    string             `json:"documentId"`
	DocumentType  string             `json:"documentType"`
	Lines         []checkLineRequest `json:"lines"`
}

type relieveRequest struct {
	InvoiceID       string          `json:"invoiceId"`
	InvoiceAmount   decimal.Decimal `json:"invoiceAmount"`
	InvoiceQuantity decimal.Decimal `json:"invoiceQuantity"`
	POUnitPrice     decimal.Decimal `json:"poUnitPrice"`
}

type releaseRequest struct {
	DocumentID string `json:"documentId"`
	Reason     string `json:"reason"`
}

type registerLineRequest struct {
	DimensionKey string          `json:"dimensionKey"`
	SpanKey      string          `json:"spanKey"`
	Amount       decimal.Decimal `json:"amount"`
}

type registerRequest struct {
	RuleVersionID string                `json:"ruleVersionId"`
	EntryType     string                `json:"entryType"`
	Lines         []registerLineRequest `json:"lines"`
}

// readCommand reads the body, its hash and the Idempotency-Key; false means a
// problem was already written.
func readCommand(w http.ResponseWriter, r *http.Request) ([]byte, string, bool) {
	if !validTenant(w, r) {
		return nil, "", false
	}
	key := r.Header.Get(idempotencyKey)
	if key == "" || utf8.RuneCountInString(key) > 255 {
		validationFailed(w, r, map[string]string{idempotencyKey: "is required and must be at most 255 characters"})
		return nil, "", false
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxBodyBytes+1))
	if err != nil || len(body) > maxBodyBytes {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusBadRequest, "COM-0001", "body must be a JSON object of at most 4 MiB"))
		return nil, "", false
	}
	sum := sha256.Sum256(body)
	return body, key + "\x00" + hex.EncodeToString(sum[:]), true
}

// splitKey separates the key from the hash built by readCommand.
func splitKey(composite string) (string, string) {
	i := strings.IndexByte(composite, 0)
	return composite[:i], composite[i+1:]
}

func decodeBody(body []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return httpx.NewProblem(http.StatusBadRequest, "COM-0001", "malformed JSON body: "+err.Error())
	}
	return nil
}

func (a *API) createRule(w http.ResponseWriter, r *http.Request) {
	body, composite, ok := readCommand(w, r)
	if !ok {
		return
	}
	var req createRuleRequest
	if err := decodeBody(body, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	if !ids.Valid(req.LegalEntityID) {
		fields["legalEntityId"] = "must be a UUID"
	}
	if !ruleCode.MatchString(req.Code) {
		fields["code"] = "must be 2 to 40 characters of A-Z, 0-9, _ or -"
	}
	if req.WarnPct.Sign() < 0 || req.WarnPct.GreaterThan(decimal.NewFromInt(100)) {
		fields["warnPct"] = "must be between 0 and 100"
	}
	if req.BlockPct.Sign() < 0 || req.BlockPct.GreaterThan(decimal.NewFromInt(100)) {
		fields["blockPct"] = "must be between 0 and 100"
	} else if req.BlockPct.LessThan(req.WarnPct) {
		fields["blockPct"] = "must not be below warnPct"
	}
	if !currencyCode.MatchString(req.Currency) {
		fields["currency"] = "must be an ISO 4217 alpha-3 code"
	}
	if _, err := time.Parse(dateLayout, req.EffectiveFrom); err != nil {
		fields["effectiveFrom"] = "must be a date as YYYY-MM-DD"
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	key, hash := splitKey(composite)
	in := store.NewRule{LegalEntityID: req.LegalEntityID, Code: req.Code, WarnPct: req.WarnPct, BlockPct: req.BlockPct, Currency: req.Currency, EffectiveFrom: req.EffectiveFrom}
	a.replay(w, r)(a.store.CreateRule(r.Context(), key, hash, in))
}

func (a *API) checkBudget(w http.ResponseWriter, r *http.Request) {
	body, composite, ok := readCommand(w, r)
	if !ok {
		return
	}
	var req checkRequest
	if err := decodeBody(body, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	if !ids.Valid(req.RuleVersionID) {
		fields["ruleVersionId"] = "must be a UUID"
	}
	if !ids.Valid(req.DocumentID) {
		fields["documentId"] = "must be a UUID"
	}
	if !docTypes[req.DocumentType] {
		fields["documentType"] = "must be Requisition, PurchaseOrder, Invoice or Journal"
	}
	if len(req.Lines) == 0 || len(req.Lines) > maxCheckLines {
		fields["lines"] = fmt.Sprintf("must hold 1 to %d lines", maxCheckLines)
	}
	lines := make([]store.CheckLine, 0, len(req.Lines))
	seen := make(map[string]bool, len(req.Lines))
	for i, l := range req.Lines {
		prefix := fmt.Sprintf("lines[%d].", i)
		if !ids.Valid(l.LineID) {
			fields[prefix+"lineId"] = "must be a UUID"
		} else if seen[l.LineID] {
			fields[prefix+"lineId"] = "appears twice in the document"
		}
		seen[l.LineID] = true
		if l.DimensionKey == "" || len(l.DimensionKey) > 200 {
			fields[prefix+"dimensionKey"] = "must be 1 to 200 characters"
		}
		if l.SpanKey == "" || len(l.SpanKey) > 40 {
			fields[prefix+"spanKey"] = "must be 1 to 40 characters"
		}
		if l.Amount.Sign() < 0 {
			fields[prefix+"amount"] = "must not be negative"
		}
		if l.Amount.Exponent() < -4 {
			fields[prefix+"amount"] = "must have at most 4 decimal places"
		}
		lines = append(lines, store.CheckLine{LineID: l.LineID, DimensionKey: l.DimensionKey, SpanKey: l.SpanKey, Amount: l.Amount})
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	p, _ := tenant.FromContext(r.Context())
	key, hash := splitKey(composite)
	in := store.CheckRequest{RuleVersionID: req.RuleVersionID, DocumentID: req.DocumentID, DocumentType: req.DocumentType, Lines: lines}
	a.replay(w, r)(a.store.CheckBudget(r.Context(), key, hash, in, p.HasDuty(dutyOverride)))
}

func (a *API) relieve(w http.ResponseWriter, r *http.Request) {
	body, composite, ok := readCommand(w, r)
	if !ok {
		return
	}
	id := chi.URLParam(r, "id")
	if !ids.Valid(id) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, store.CodeEncumbranceNotFound, "encumbrance not found"))
		return
	}
	var req relieveRequest
	if err := decodeBody(body, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	if !ids.Valid(req.InvoiceID) {
		fields["invoiceId"] = "must be a UUID"
	}
	for name, v := range map[string]decimal.Decimal{"invoiceAmount": req.InvoiceAmount, "invoiceQuantity": req.InvoiceQuantity, "poUnitPrice": req.POUnitPrice} {
		if v.Sign() < 0 {
			fields[name] = "must not be negative"
		}
	}
	if req.InvoiceAmount.Exponent() < -4 {
		fields["invoiceAmount"] = "must have at most 4 decimal places"
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	key, hash := splitKey(composite)
	in := store.RelieveRequest{EncumbranceID: id, InvoiceID: req.InvoiceID, InvoiceAmount: req.InvoiceAmount,
		MatchedShare: req.InvoiceQuantity.Mul(req.POUnitPrice)}
	a.replay(w, r)(a.store.RelieveEncumbrance(r.Context(), key, hash, in))
}

// release gives an encumbrance's open remainder back on a PO line
// cancellation or a PO close (FR-38.5).
func (a *API) release(w http.ResponseWriter, r *http.Request) {
	body, composite, ok := readCommand(w, r)
	if !ok {
		return
	}
	id := chi.URLParam(r, "id")
	if !ids.Valid(id) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, store.CodeEncumbranceNotFound, "encumbrance not found"))
		return
	}
	var req releaseRequest
	if err := decodeBody(body, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	if !ids.Valid(req.DocumentID) {
		fields["documentId"] = "must be a UUID (the cancelled or closed purchase order)"
	}
	if !releaseWhy[req.Reason] {
		fields["reason"] = "must be Cancelled or Closed"
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	key, hash := splitKey(composite)
	in := store.ReleaseRequest{EncumbranceID: id, DocumentID: req.DocumentID, Reason: req.Reason}
	a.replay(w, r)(a.store.ReleaseEncumbrance(r.Context(), key, hash, in))
}

func (a *API) postRegisterEntry(w http.ResponseWriter, r *http.Request) {
	body, composite, ok := readCommand(w, r)
	if !ok {
		return
	}
	var req registerRequest
	if err := decodeBody(body, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	if !ids.Valid(req.RuleVersionID) {
		fields["ruleVersionId"] = "must be a UUID"
	}
	if !domain.ValidEntryType(domain.EntryType(req.EntryType)) {
		fields["entryType"] = "must be Original, Revision, Transfer or CarryForward"
	}
	if len(req.Lines) == 0 || len(req.Lines) > maxCheckLines {
		fields["lines"] = fmt.Sprintf("must hold 1 to %d lines", maxCheckLines)
	}
	lines := make([]domain.RegisterLine, 0, len(req.Lines))
	for i, l := range req.Lines {
		if l.DimensionKey == "" || len(l.DimensionKey) > 200 || l.SpanKey == "" || len(l.SpanKey) > 40 {
			fields[fmt.Sprintf("lines[%d]", i)] = "dimensionKey (1 to 200 characters) and spanKey (1 to 40 characters) are required"
		} else if l.Amount.Exponent() < -4 {
			fields[fmt.Sprintf("lines[%d].amount", i)] = "must have at most 4 decimal places"
		}
		lines = append(lines, domain.RegisterLine{DimensionKey: l.DimensionKey, SpanKey: l.SpanKey, Amount: l.Amount})
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	key, hash := splitKey(composite)
	a.replay(w, r)(a.store.PostRegisterEntry(r.Context(), key, hash, store.RegisterRequest{RuleVersionID: req.RuleVersionID, EntryType: domain.EntryType(req.EntryType), Lines: lines}))
}

// replay writes a stored or fresh command result, or the problem it failed with.
func (a *API) replay(w http.ResponseWriter, r *http.Request) func(int, []byte, error) {
	return func(status int, raw []byte, err error) {
		if err != nil {
			slog.Error("budget command failed", "err", err, "path", r.URL.Path)
			httpx.Fail(w, r, err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write(raw)
	}
}

func validTenant(w http.ResponseWriter, r *http.Request) bool {
	p, ok := tenant.FromContext(r.Context())
	if !ok || !ids.Valid(p.TenantID) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusUnauthorized, "AUTH-0001", "missing or malformed tenant"))
		return false
	}
	return true
}

func validationFailed(w http.ResponseWriter, r *http.Request, fields map[string]string) {
	p := httpx.NewProblem(http.StatusBadRequest, "COM-0001", validationDetail)
	p.Errors = fields
	httpx.Fail(w, r, p)
}

// list writes {"items": rows} or the problem the read failed with.
func list[T any](w http.ResponseWriter, r *http.Request, rows []T, err error) {
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": rows})
}

func one[T any](w http.ResponseWriter, r *http.Request, v T, err error) {
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, v)
}

// ruleQuery reads the required rule query parameter; false means a problem was written.
func ruleQuery(w http.ResponseWriter, r *http.Request) (string, bool) {
	if !validTenant(w, r) {
		return "", false
	}
	rule := r.URL.Query().Get("rule")
	if !ids.Valid(rule) {
		validationFailed(w, r, map[string]string{"rule": "is required and must be a rule version UUID"})
		return "", false
	}
	return rule, true
}

func (a *API) listRules(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	le := r.URL.Query().Get("legalEntityId")
	if le != "" && !ids.Valid(le) {
		validationFailed(w, r, map[string]string{"legalEntityId": "must be a UUID"})
		return
	}
	rows, err := a.store.ListRules(r.Context(), le)
	list(w, r, rows, err)
}

func (a *API) getRule(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id := chi.URLParam(r, "id")
	if !ids.Valid(id) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, store.CodeRuleNotFound, "budget control rule version not found"))
		return
	}
	rule, err := a.store.GetRule(r.Context(), id)
	one(w, r, rule, err)
}

func (a *API) listRegisterEntries(w http.ResponseWriter, r *http.Request) {
	rule, ok := ruleQuery(w, r)
	if !ok {
		return
	}
	rows, err := a.store.ListRegisterEntries(r.Context(), rule)
	list(w, r, rows, err)
}

func (a *API) available(w http.ResponseWriter, r *http.Request) {
	rule, ok := ruleQuery(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	rows, err := a.store.Available(r.Context(), rule, q.Get("dims"), q.Get("span"))
	list(w, r, rows, err)
}

func (a *API) listEncumbrances(w http.ResponseWriter, r *http.Request) {
	rule, ok := ruleQuery(w, r)
	if !ok {
		return
	}
	status := r.URL.Query().Get("status")
	if !statuses[status] {
		validationFailed(w, r, map[string]string{"status": "must be Open, PartiallyRelieved, Relieved or Released"})
		return
	}
	rows, err := a.store.ListEncumbrances(r.Context(), rule, status)
	list(w, r, rows, err)
}

func (a *API) getEncumbrance(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id := chi.URLParam(r, "id")
	if !ids.Valid(id) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, store.CodeEncumbranceNotFound, "encumbrance not found"))
		return
	}
	e, err := a.store.GetEncumbrance(r.Context(), id)
	one(w, r, e, err)
}

func (a *API) listChecks(w http.ResponseWriter, r *http.Request) {
	rule, ok := ruleQuery(w, r)
	if !ok {
		return
	}
	doc := r.URL.Query().Get("documentId")
	if doc != "" && !ids.Valid(doc) {
		validationFailed(w, r, map[string]string{"documentId": "must be a UUID"})
		return
	}
	rows, err := a.store.ListChecks(r.Context(), rule, doc)
	list(w, r, rows, err)
}

func (a *API) getCheck(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id := chi.URLParam(r, "id")
	if !ids.Valid(id) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, store.CodeCheckNotFound, "budget check not found"))
		return
	}
	c, err := a.store.GetCheck(r.Context(), id)
	one(w, r, c, err)
}
