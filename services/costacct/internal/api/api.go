// Package api exposes the cost accounting HTTP surface (PRD 7.38.5).
// Each handler: check duty -> decode -> validate -> store -> httpx response.
package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/costacct/internal/domain"
	"github.com/BenjaminSRussell/passage/services/costacct/internal/store"
)

// Duties (PRD 7.38.5 CostAccountingView / Maintain / Run), named like every
// other service's duties.
const (
	DutyView     = "controlling.view"
	DutyMaintain = "controlling.maintain"
	DutyRun      = "controlling.run"
)

const (
	idempotencyKey = "Idempotency-Key"
	maxBody        = 4 << 20
	maxMirrorLines = 5000
	maxObjects     = 500
	maxAccount     = 40
	codeValidation = "COM-0001"
)

// Store is the persistence surface the handlers need.
type Store interface {
	CreateCostObject(ctx context.Context, key, hash string, in store.CostObject) (int, []byte, error)
	CreateCostElement(ctx context.Context, key, hash string, in store.CostElement) (int, []byte, error)
	MirrorJournalLines(ctx context.Context, key, hash string, in store.MirrorRequest) (int, []byte, error)
	RunAllocation(ctx context.Context, key, hash string, in store.RunRequest) (int, []byte, error)
	EntriesFor(ctx context.Context, costObjectID, period string) ([]store.Entry, error)
	Actuals(ctx context.Context, legalEntityID, from, to, kind string) ([]store.ActualRow, error)
	ListCostObjects(ctx context.Context, legalEntityID, kind string) ([]store.CostObject, error)
	GetCostObject(ctx context.Context, id string) (store.CostObject, error)
	ListCostElements(ctx context.Context, legalEntityID string) ([]store.CostElement, error)
	ListRuns(ctx context.Context, legalEntityID string) ([]store.Run, error)
	GetRun(ctx context.Context, id string) (store.Run, error)
	ReconcileToGL(ctx context.Context, gl store.GLBalances, in store.GLReconcileRequest) (domain.Reconciliation, error)
	RunTopDown(ctx context.Context, key, hash string, in store.TopDownRequest) (int, []byte, error)
	ListTopDownRuns(ctx context.Context, legalEntityID string) ([]store.TopDownRun, error)
	GetTopDownRun(ctx context.Context, id string) (store.TopDownRun, error)
	SettleInternalOrder(ctx context.Context, key, hash string, in store.SettlementRequest) (int, []byte, error)
	ListSettlements(ctx context.Context, orderID, period string) ([]store.Settlement, error)
	CreateMeasure(ctx context.Context, key, hash string, in store.StatisticalMeasure) (int, []byte, error)
	LoadMeasureValues(ctx context.Context, key, hash string, in store.MeasureValuesRequest) (int, []byte, error)
	ListMeasures(ctx context.Context, legalEntityID string) ([]store.StatisticalMeasure, error)
	MeasureValues(ctx context.Context, measureID, period string) ([]store.MeasureValue, error)
	SaveTemplate(ctx context.Context, key, hash string, in store.AllocationTemplate, expectedVersion int) (int, []byte, error)
	ListTemplates(ctx context.Context, legalEntityID string) ([]store.AllocationTemplate, error)
	GetTemplate(ctx context.Context, id string) (store.AllocationTemplate, error)
	DryRunTemplate(ctx context.Context, in store.TemplateRunRequest) (store.DryRunResult, error)
	RunTemplate(ctx context.Context, key, hash string, in store.TemplateRunRequest) (int, []byte, error)
	ListTemplateRuns(ctx context.Context, legalEntityID, templateID string) ([]store.TemplateRun, error)
	GetTemplateRun(ctx context.Context, id string) (store.TemplateRun, error)
	CreateBehaviorRule(ctx context.Context, key, hash string, in store.BehaviorRule) (int, []byte, error)
	ListBehaviorRules(ctx context.Context, legalEntityID, elementID string) ([]store.BehaviorRule, error)
	CreateHierarchy(ctx context.Context, key, hash string, in store.Hierarchy) (int, []byte, error)
	CreateHierarchyNode(ctx context.Context, key, hash string, in store.HierarchyNode) (int, []byte, error)
	CollectAtNode(ctx context.Context, key, hash string, in store.CollectRequest) (int, []byte, error)
	RunHierarchyDistribution(ctx context.Context, key, hash string, in store.DistributionRequest) (int, []byte, error)
	ListHierarchies(ctx context.Context, legalEntityID string) ([]store.Hierarchy, error)
	GetHierarchy(ctx context.Context, id string) (store.Hierarchy, error)
	HierarchyBalances(ctx context.Context, hierarchyID, period string) ([]store.NodeBalance, error)
	ListDistributionRuns(ctx context.Context, legalEntityID, hierarchyID string) ([]store.DistributionRun, error)
	GetDistributionRun(ctx context.Context, id string) (store.DistributionRun, error)
	Ping(ctx context.Context) error
}

// API serves the controlling endpoints.
type API struct {
	st        Store
	gl        store.GLBalances
	ledgerIDs map[string]string
}

// New returns the handler set over st.
func New(st Store) *API { return &API{st: st} }

// WithLedger gives the reconciliation its general ledger and the ledger of each legal entity.
func (a *API) WithLedger(gl store.GLBalances, ledgerIDs map[string]string) *API {
	a.gl, a.ledgerIDs = gl, ledgerIDs
	return a
}

// Register mounts the routes on r.
func (a *API) Register(r chi.Router) {
	r.With(authz.RequireDuty(DutyMaintain)).Post("/v1/controlling/cost-objects", a.createCostObject)
	r.With(authz.RequireDuty(DutyMaintain)).Post("/v1/controlling/cost-elements", a.createCostElement)
	r.With(authz.RequireDuty(DutyMaintain)).Post("/v1/controlling/cost-entries:mirror", a.mirror)
	r.With(authz.RequireDuty(DutyRun)).Post("/v1/controlling/allocation-runs", a.runAllocation)
	r.With(authz.RequireDuty(DutyRun)).Post("/v1/controlling/top-down-runs", a.runTopDown)
	r.With(authz.RequireDuty(DutyRun)).Post("/v1/controlling/internal-orders/{id}:settle", a.settle)
	view := r.With(authz.RequireDuty(DutyView))
	view.Get("/v1/controlling/cost-objects/{id}/entries", a.entries)
	view.Get("/v1/controlling/cost-actuals", a.actuals)
	view.Get("/v1/controlling/cost-objects", a.listCostObjects)
	view.Get("/v1/controlling/cost-objects/{id}", a.getCostObject)
	view.Get("/v1/controlling/cost-elements", a.listCostElements)
	view.Get("/v1/controlling/allocation-runs", a.listRuns)
	view.Get("/v1/controlling/allocation-runs/{id}", a.getRun)
	view.Post("/v1/controlling/reconciliations", a.reconcile)
	view.Get("/v1/controlling/top-down-runs", a.listTopDownRuns)
	view.Get("/v1/controlling/top-down-runs/{id}", a.getTopDownRun)
	view.Get("/v1/controlling/internal-orders/{id}/settlements", a.listSettlements)
	a.registerTemplates(r)
	a.registerBehavior(r)
	a.registerHierarchies(r)
}

// prepare checks the Idempotency-Key, decodes the body into v and returns the
// body hash. Duties are enforced by route middleware. On failure it has
// already written the problem.
func prepare(w http.ResponseWriter, r *http.Request, v any) (key, hash string, ok bool) {
	if !validTenant(w, r) {
		return "", "", false
	}
	key = r.Header.Get(idempotencyKey)
	if key == "" || len(key) > 255 {
		invalid(w, r, "Idempotency-Key header is required and at most 255 characters")
		return "", "", false
	}
	raw, ok := readJSON(w, r, v)
	if !ok {
		return "", "", false
	}
	sum := sha256.Sum256(raw)
	return key, hex.EncodeToString(sum[:]), true
}

// readJSON decodes a body of at most 4 MiB, rejecting unknown fields.
func readJSON(w http.ResponseWriter, r *http.Request, v any) ([]byte, bool) {
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBody))
	if err != nil {
		invalid(w, r, "body must be a JSON object of at most 4 MiB")
		return nil, false
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		invalid(w, r, "malformed JSON body: "+err.Error())
		return nil, false
	}
	return raw, true
}

func validTenant(w http.ResponseWriter, r *http.Request) bool {
	p, ok := tenant.FromContext(r.Context())
	if !ok || !ids.Valid(p.TenantID) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusUnauthorized, "AUTH-0001", "missing or malformed tenant"))
		return false
	}
	return true
}

// writeCommand relays a store result. Stored bodies are already JSON.
func writeCommand(w http.ResponseWriter, r *http.Request, status int, raw []byte, err error) {
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(raw)
}

func invalid(w http.ResponseWriter, r *http.Request, detail string) {
	httpx.Fail(w, r, httpx.NewProblem(http.StatusBadRequest, codeValidation, detail))
}

func (a *API) createCostObject(w http.ResponseWriter, r *http.Request) {
	var in store.CostObject
	key, hash, ok := prepare(w, r, &in)
	if !ok {
		return
	}
	switch {
	case !ids.Valid(in.LegalEntityID):
		invalid(w, r, "legalEntityId must be a UUID")
	case !domain.ValidObjectKind(in.Kind):
		invalid(w, r, "kind must be CostCenter, InternalOrder, Product or Project")
	case !domain.ValidCode(in.Code):
		invalid(w, r, "code must be 2-40 characters of A-Z, 0-9, _ or -")
	case in.ID != "":
		invalid(w, r, "id is assigned by the service")
	case in.Name == "" || len(in.Name) > 200:
		invalid(w, r, "name is required and at most 200 characters")
	default:
		status, raw, err := a.st.CreateCostObject(r.Context(), key, hash, in)
		writeCommand(w, r, status, raw, err)
	}
}

func (a *API) createCostElement(w http.ResponseWriter, r *http.Request) {
	var in store.CostElement
	key, hash, ok := prepare(w, r, &in)
	if !ok {
		return
	}
	if !ids.Valid(in.LegalEntityID) || !domain.ValidCode(in.Code) || in.Name == "" || len(in.Name) > 200 || in.ID != "" {
		invalid(w, r, "legalEntityId (UUID), code (2-40 characters of A-Z, 0-9, _ or -) and name (at most 200 characters) are required; id is assigned by the service")
		return
	}
	if in.DefaultBehavior != "" && !domain.ValidBehavior(in.DefaultBehavior) {
		invalid(w, r, "defaultBehavior must be Fixed or Variable when set")
		return
	}
	switch in.Kind {
	case domain.ElementPrimary:
		rng := domain.AccountRange{From: in.MainAccountFrom, To: in.MainAccountTo}
		if len(in.MainAccountFrom) > maxAccount || len(in.MainAccountTo) > maxAccount {
			invalid(w, r, "main account bounds are at most 40 characters")
			return
		}
		if err := domain.ValidateRange(rng); err != nil {
			invalid(w, r, err.Error())
			return
		}
	case domain.ElementSecondary:
		if in.MainAccountFrom != "" || in.MainAccountTo != "" {
			invalid(w, r, "secondary elements carry no main account range")
			return
		}
	default:
		invalid(w, r, "kind must be Primary or Secondary")
		return
	}
	status, raw, err := a.st.CreateCostElement(r.Context(), key, hash, in)
	writeCommand(w, r, status, raw, err)
}

// mirrorBody is the wire form of a mirror request.
type mirrorBody struct {
	LegalEntityID string             `json:"legalEntityId"`
	Lines         []store.MirrorLine `json:"lines"`
}

func (a *API) mirror(w http.ResponseWriter, r *http.Request) {
	var b mirrorBody
	key, hash, ok := prepare(w, r, &b)
	if !ok {
		return
	}
	if !ids.Valid(b.LegalEntityID) || len(b.Lines) == 0 || len(b.Lines) > maxMirrorLines {
		invalid(w, r, "legalEntityId is required and lines must hold 1-5000 entries")
		return
	}
	for i, l := range b.Lines {
		switch {
		case !ids.Valid(l.JournalLineID) || !ids.Valid(l.CostObjectID):
			invalid(w, r, lineErr(i, "journalLineId and costObjectId must be UUIDs"))
			return
		case l.MainAccount == "" || len(l.MainAccount) > maxAccount:
			invalid(w, r, lineErr(i, "mainAccount is required"))
			return
		case !domain.ValidPeriod(l.Period):
			invalid(w, r, lineErr(i, "period must be YYYY-MM"))
			return
		case !validCurrency(l.Currency):
			invalid(w, r, lineErr(i, "currency must be a 3-letter ISO code"))
			return
		case l.Amount.Exponent() < -4:
			invalid(w, r, lineErr(i, "amount has more than 4 decimal places"))
			return
		}
	}
	status, raw, err := a.st.MirrorJournalLines(r.Context(), key, hash, store.MirrorRequest{LegalEntityID: b.LegalEntityID, Lines: b.Lines})
	writeCommand(w, r, status, raw, err)
}

func lineErr(i int, msg string) string {
	return "line " + strconv.Itoa(i) + ": " + msg
}

func validCurrency(c string) bool {
	if len(c) != 3 {
		return false
	}
	for _, r := range c {
		if r < 'A' || r > 'Z' {
			return false
		}
	}
	return true
}

// runBody is the wire form of an allocation run request.
type runBody struct {
	LegalEntityID      string              `json:"legalEntityId"`
	Period             string              `json:"period"`
	Currency           string              `json:"currency"`
	SecondaryElementID string              `json:"secondaryElementId"`
	CostObjectIDs      []string            `json:"costObjectIds"`
	Basis              [][]decimal.Decimal `json:"basis"`
}

func (a *API) runAllocation(w http.ResponseWriter, r *http.Request) {
	var b runBody
	key, hash, ok := prepare(w, r, &b)
	if !ok {
		return
	}
	switch {
	case !ids.Valid(b.LegalEntityID) || !ids.Valid(b.SecondaryElementID):
		invalid(w, r, "legalEntityId and secondaryElementId must be UUIDs")
		return
	case !domain.ValidPeriod(b.Period):
		invalid(w, r, "period must be YYYY-MM")
		return
	case !validCurrency(b.Currency):
		invalid(w, r, "currency must be a 3-letter ISO code")
		return
	case len(b.CostObjectIDs) < 2 || len(b.CostObjectIDs) > maxObjects:
		invalid(w, r, "costObjectIds must hold 2-500 cost objects")
		return
	}
	seen := make(map[string]bool, len(b.CostObjectIDs))
	for _, id := range b.CostObjectIDs {
		if !ids.Valid(id) || seen[id] {
			invalid(w, r, "costObjectIds must be distinct UUIDs")
			return
		}
		seen[id] = true
	}
	n := len(b.CostObjectIDs)
	if len(b.Basis) != n {
		invalid(w, r, "basis must be a square matrix matching costObjectIds")
		return
	}
	for _, row := range b.Basis {
		if len(row) != n {
			invalid(w, r, "basis must be a square matrix matching costObjectIds")
			return
		}
		for _, v := range row {
			if v.Sign() < 0 {
				invalid(w, r, "basis values must not be negative")
				return
			}
		}
	}
	status, raw, err := a.st.RunAllocation(r.Context(), key, hash, store.RunRequest{
		LegalEntityID: b.LegalEntityID, Period: b.Period, Currency: b.Currency,
		SecondaryElementID: b.SecondaryElementID, CostObjectIDs: b.CostObjectIDs, Basis: b.Basis,
	})
	writeCommand(w, r, status, raw, err)
}

func (a *API) entries(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	period := r.URL.Query().Get("period")
	if !ids.Valid(id) || !domain.ValidPeriod(period) {
		invalid(w, r, "cost object id must be a UUID and period must be YYYY-MM")
		return
	}
	items, err := a.st.EntriesFor(r.Context(), id, period)
	list(w, r, items, err)
}

// actuals handles GET /cost-actuals?legalEntityId=&from=&to=&kind=: the cost ledger's actual cost per cost object and period,
// which cost control (budgeting) sets beside the plan of the same cost center (FR-38.10).
func (a *API) actuals(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	q := r.URL.Query()
	le, from, to, kind := q.Get("legalEntityId"), q.Get("from"), q.Get("to"), q.Get("kind")
	if !ids.Valid(le) || !domain.ValidPeriod(from) || !domain.ValidPeriod(to) || from > to {
		invalid(w, r, "legalEntityId must be a UUID, from and to must be YYYY-MM periods and from must not be after to")
		return
	}
	if kind != "" && !domain.ValidObjectKind(kind) {
		invalid(w, r, "kind must be CostCenter, InternalOrder, Product or Project")
		return
	}
	items, err := a.st.Actuals(r.Context(), le, from, to, kind)
	list(w, r, items, err)
}

func list[T any](w http.ResponseWriter, r *http.Request, rows []T, err error) {
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if rows == nil {
		rows = []T{}
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

// legalEntityFilter reads the optional legalEntityId query parameter.
func legalEntityFilter(w http.ResponseWriter, r *http.Request) (string, bool) {
	if !validTenant(w, r) {
		return "", false
	}
	le := r.URL.Query().Get("legalEntityId")
	if le != "" && !ids.Valid(le) {
		invalid(w, r, "legalEntityId must be a UUID")
		return "", false
	}
	return le, true
}

func (a *API) listCostObjects(w http.ResponseWriter, r *http.Request) {
	le, ok := legalEntityFilter(w, r)
	if !ok {
		return
	}
	kind := r.URL.Query().Get("kind")
	if kind != "" && !domain.ValidObjectKind(kind) {
		invalid(w, r, "kind must be CostCenter, InternalOrder, Product or Project")
		return
	}
	rows, err := a.st.ListCostObjects(r.Context(), le, kind)
	list(w, r, rows, err)
}

func (a *API) getCostObject(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !validTenant(w, r) {
		return
	}
	if !ids.Valid(id) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, store.CodeObjectNotFound, "cost object not found"))
		return
	}
	o, err := a.st.GetCostObject(r.Context(), id)
	one(w, r, o, err)
}

func (a *API) listCostElements(w http.ResponseWriter, r *http.Request) {
	le, ok := legalEntityFilter(w, r)
	if !ok {
		return
	}
	rows, err := a.st.ListCostElements(r.Context(), le)
	list(w, r, rows, err)
}

func (a *API) listRuns(w http.ResponseWriter, r *http.Request) {
	le, ok := legalEntityFilter(w, r)
	if !ok {
		return
	}
	rows, err := a.st.ListRuns(r.Context(), le)
	list(w, r, rows, err)
}

func (a *API) getRun(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !validTenant(w, r) {
		return
	}
	if !ids.Valid(id) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, store.CodeRunNotFound, "allocation run not found"))
		return
	}
	run, err := a.st.GetRun(r.Context(), id)
	one(w, r, run, err)
}

// reconcileBody is the wire form of a reconciliation request. The journal side is read from the
// general ledger; a body that names balances is rejected as an unknown field.
type reconcileBody struct {
	LegalEntityID string `json:"legalEntityId"`
	Period        string `json:"period"`
	Currency      string `json:"currency"`
}

// reconcile is a read: it compares and reports, and changes nothing.
func (a *API) reconcile(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var b reconcileBody
	if _, ok := readJSON(w, r, &b); !ok {
		return
	}
	switch {
	case !ids.Valid(b.LegalEntityID):
		invalid(w, r, "legalEntityId must be a UUID")
		return
	case !domain.ValidPeriod(b.Period):
		invalid(w, r, "period must be YYYY-MM")
		return
	case !validCurrency(b.Currency):
		invalid(w, r, "currency must be a 3-letter ISO code")
		return
	case a.gl == nil:
		httpx.Fail(w, r, httpx.NewProblem(http.StatusServiceUnavailable, store.CodeLedgerUnavailable, "the general ledger is not configured for this service"))
		return
	}
	le, _ := ids.Canonical(b.LegalEntityID)
	if p, _ := tenant.FromContext(r.Context()); !tenant.InLegalEntityScope(p, le) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusForbidden, "AUTH-0003", "the legal entity is outside the caller's scope"))
		return
	}
	ledgerID, ok := a.ledgerIDs[le]
	if !ok {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusUnprocessableEntity, store.CodeLedgerNotMapped, "no ledger is configured for this legal entity"))
		return
	}
	res, err := a.st.ReconcileToGL(r.Context(), a.gl, store.GLReconcileRequest{LegalEntityID: le, LedgerID: ledgerID, Period: b.Period, Currency: b.Currency})
	one(w, r, res, err)
}
