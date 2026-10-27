// Package api exposes the reporting HTTP API under /api/v1/reporting.
// Every handler: decode -> validate -> tenant.FromContext -> store/domain ->
// httpx.JSON or httpx.Fail.
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
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"

	"github.com/BenjaminSRussell/passage/services/finreport/internal/config"
	"github.com/BenjaminSRussell/passage/services/finreport/internal/domain"
	"github.com/BenjaminSRussell/passage/services/finreport/internal/store"
)

// Privilege codes (PRD §2.15.17).
const (
	DutyMaintain = "reporting.definition.maintain"
	DutyRun      = "reporting.statement.run"
	DutyView     = "reporting.statement.view"
	DutySchedule = "reporting.statement.schedule"
	// DutyAudit is "View audit trail" (PRD §2.15.17): audit trail query and export.
	DutyAudit = "reporting.audit.view"
	// DutyLines is the ledger's "View ledger transactions" duty (wave 11,
	// ledger.journal.read): PRD §2.15.5 and §2.15.17 put drill level 2 under
	// it, separately from viewing statements.
	DutyLines = "ledger.journal.read"
)

const (
	maxBody            = 4 << 20
	maxCalendarPeriods = 600 // 50 fiscal years of 12 periods
)

// Store is the persistence surface the handlers need.
type Store interface {
	ListDefinitions(ctx context.Context) ([]store.DefinitionSummary, error)
	SaveDraft(ctx context.Context, def domain.Definition, ifMatch *int) (domain.Definition, int, error)
	GetDefinition(ctx context.Context, code string, version int) (domain.Definition, int, error)
	ImportDefinition(ctx context.Context, def domain.Definition, overwrite bool, key, requestHash string) (domain.Definition, int, bool, error)
	Publish(ctx context.Context, code, actor string) (domain.Definition, error)
	CreateRun(ctx context.Context, spec store.RunSpec) (store.Run, error)
	ListRuns(ctx context.Context, definition string, limit int) ([]store.Run, error)
	GetRun(ctx context.Context, id string) (store.Run, error)
	ListCells(ctx context.Context, runID string) ([]domain.Cell, error)
	GetCell(ctx context.Context, runID, row, column string) (domain.Cell, error)
	DrillLines(ctx context.Context, runID, row, column string, q store.LineQuery) (store.LinePage, error)
	Lag(ctx context.Context) ([]store.LagRow, error)
	RetireDefinition(ctx context.Context, code string) (int, error)
	InstallStandard(ctx context.Context, defs []domain.Definition, actor string) ([]store.InstallResult, error)
	ListHierarchies(ctx context.Context) ([]store.HierarchySummary, error)
	SaveHierarchy(ctx context.Context, h domain.Hierarchy, ifMatch *int, owner string) (domain.Hierarchy, int, error)
	GetHierarchy(ctx context.Context, code string, version int) (domain.Hierarchy, int, error)
	PublishHierarchy(ctx context.Context, code, actor string, members []domain.Member) (domain.Hierarchy, domain.Membership, error)
	HierarchyMembership(ctx context.Context, code string, version int) (domain.Membership, int, error)
	RunOutput(ctx context.Context, runID string) (store.Run, domain.Definition, []domain.Cell, error)
	ListSchedules(ctx context.Context) ([]domain.Schedule, error)
	GetSchedule(ctx context.Context, code string) (domain.Schedule, int, error)
	SaveSchedule(ctx context.Context, sc domain.Schedule, ifMatch *int) (domain.Schedule, int, error)
	AuditTrail(ctx context.Context, f domain.AuditFilter, after *domain.AuditCursor, limit int) (store.AuditPage, error)
	ExportAuditTrail(ctx context.Context, f domain.AuditFilter) (store.AuditExport, error)
}

// API holds the handlers.
type API struct {
	store     Store
	linePage  int                    // drill level 2 default page size (REPORTING_PAGE_SIZE)
	traceLink func(id string) string // trace viewer link of a line (REPORTING_TRACE_URL)
}

// New returns the API over st with the default reporting tunables.
func New(st Store) *API {
	return &API{store: st, linePage: domain.DefaultLinePage, traceLink: config.Reporting{}.TraceLink}
}

// WithConfig applies the reporting tunables the handlers use.
func (a *API) WithConfig(cfg config.Reporting) *API {
	if cfg.PageSize > 0 {
		a.linePage = cfg.PageSize
	}
	a.traceLink = cfg.TraceLink
	return a
}

// Register mounts the reporting routes on r.
func (a *API) Register(r chi.Router) {
	r.Route("/api/v1/reporting", func(r chi.Router) {
		r.With(authz.RequireDuty(DutyView)).Get("/definitions", a.listDefinitions)
		r.With(authz.RequireDuty(DutyView)).Get("/definitions/{code}", a.getDefinition)
		r.With(authz.RequireDuty(DutyMaintain)).Put("/definitions/{code}", a.putDefinition)
		r.With(authz.RequireDuty(DutyMaintain)).Get("/definitions/{code}/export", a.exportDefinition)
		r.With(authz.RequireDuty(DutyMaintain)).Post("/definitions/import", a.importDefinition)
		r.With(authz.RequireDuty(DutyMaintain)).Post("/definitions/*", a.definitionAction)
		r.With(authz.RequireDuty(DutyRun)).Post("/runs", a.createRun)
		r.With(authz.RequireDuty(DutyView)).Get("/runs", a.listRuns)
		r.With(authz.RequireDuty(DutyView)).Get("/runs/{id}", a.getRun)
		r.With(authz.RequireDuty(DutyView)).Get("/runs/{id}/cells", a.listCells)
		r.With(authz.RequireDuty(DutyView)).Get("/runs/{id}/cells/{row}/{column}/balances", a.drillBalances)
		r.With(authz.RequireDuty(DutyLines)).Get("/runs/{id}/cells/{row}/{column}/lines", a.drillLines)
		r.With(authz.RequireDuty(DutyView)).Get("/runs/{id}/outputs/{format}", a.runOutput)
		r.With(authz.RequireDuty(DutyView)).Get("/lag", a.lag)
		r.With(authz.RequireDuty(DutyMaintain)).Get("/standard-definitions", a.listStandard)
		r.With(authz.RequireDuty(DutyMaintain)).Post("/standard-definitions:install", a.installStandard)
		r.With(authz.RequireDuty(DutyMaintain)).Get("/hierarchies", a.listHierarchies)
		r.With(authz.RequireDuty(DutyMaintain)).Get("/hierarchies/{code}", a.getHierarchy)
		r.With(authz.RequireDuty(DutyMaintain)).Put("/hierarchies/{code}", a.putHierarchy)
		r.With(authz.RequireDuty(DutyMaintain)).Get("/hierarchies/{code}/members", a.hierarchyMembers)
		r.With(authz.RequireDuty(DutyMaintain)).Post("/hierarchies/*", a.hierarchyAction)
		r.With(authz.RequireDuty(DutySchedule)).Get("/schedules", a.listSchedules)
		r.With(authz.RequireDuty(DutySchedule)).Get("/schedules/{code}", a.getSchedule)
		r.With(authz.RequireDuty(DutySchedule)).Put("/schedules/{code}", a.putSchedule)
		r.With(authz.RequireDuty(DutyAudit)).Get("/audit-trail", a.auditTrail)
		r.With(authz.RequireDuty(DutyAudit)).Get("/audit-trail:export", a.exportAuditTrail)
	})
}

// --- definitions -----------------------------------------------------------

func (a *API) listDefinitions(w http.ResponseWriter, r *http.Request) {
	if !principal(r) {
		httpx.Fail(w, r, unauthenticated())
		return
	}
	items, err := a.store.ListDefinitions(r.Context())
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
}

func (a *API) getDefinition(w http.ResponseWriter, r *http.Request) {
	if !principal(r) {
		httpx.Fail(w, r, unauthenticated())
		return
	}
	version := store.Latest
	if v := r.URL.Query().Get("version"); v == "draft" {
		version = store.Draft
	} else if v != "" && v != "latest" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			httpx.Fail(w, r, httpx.NewProblem(http.StatusBadRequest, "COM-0002", "version must be draft, latest or a positive integer"))
			return
		}
		version = n
	}
	def, rev, err := a.store.GetDefinition(r.Context(), chi.URLParam(r, "code"), version)
	if err != nil {
		fail(w, r, err)
		return
	}
	if version == store.Draft {
		w.Header().Set("ETag", etag(rev))
	}
	httpx.JSON(w, http.StatusOK, def)
}

func (a *API) putDefinition(w http.ResponseWriter, r *http.Request) {
	code := chi.URLParam(r, "code")
	def, ok := decodeDefinition(w, r)
	if !ok {
		return
	}
	if def.Code == "" {
		def.Code = code
	}
	if def.Code != code {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusUnprocessableEntity, "REPORT-0002", "body code does not match the path"))
		return
	}
	if issues := def.Validate(); len(issues) > 0 {
		httpx.Fail(w, r, issueProblem(issues))
		return
	}
	ifMatch, err := parseIfMatch(r)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	saved, rev, err := a.store.SaveDraft(r.Context(), def, ifMatch)
	if err != nil {
		fail(w, r, err)
		return
	}
	w.Header().Set("ETag", etag(rev))
	httpx.JSON(w, http.StatusOK, saved)
}

// definitionAction dispatches the colon custom methods on a definition:
// POST /definitions/{code}:publish, :retire and :validate.
func (a *API) definitionAction(w http.ResponseWriter, r *http.Request) {
	if !principal(r) {
		httpx.Fail(w, r, unauthenticated())
		return
	}
	rest := chi.URLParam(r, "*")
	i := strings.LastIndex(rest, ":")
	if i < 0 {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, "COM-0404", "unknown definition action"))
		return
	}
	code, action := rest[:i], rest[i+1:]
	switch action {
	case "publish":
		p, _ := tenant.FromContext(r.Context())
		def, err := a.store.Publish(r.Context(), code, p.UserID)
		if err != nil {
			fail(w, r, err)
			return
		}
		httpx.JSON(w, http.StatusOK, def)
	case "retire":
		n, err := a.store.RetireDefinition(r.Context(), code)
		if err != nil {
			fail(w, r, err)
			return
		}
		httpx.JSON(w, http.StatusOK, map[string]any{"code": code, "status": "retired", "retiredVersions": n})
	case "validate":
		def, ok := decodeDefinition(w, r)
		if !ok {
			return
		}
		if def.Code == "" {
			def.Code = code
		}
		issues := def.Validate()
		out := make([]issueDTO, 0, len(issues))
		for _, is := range issues {
			out = append(out, issueDTO{Field: is.Field, Code: is.Code, Detail: is.Detail})
		}
		httpx.JSON(w, http.StatusOK, map[string]any{"valid": len(out) == 0, "issues": out})
	default:
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, "COM-0404", "unknown definition action"))
	}
}

func (a *API) exportDefinition(w http.ResponseWriter, r *http.Request) {
	code := chi.URLParam(r, "code")
	def, _, err := a.store.GetDefinition(r.Context(), code, store.Latest)
	if err != nil {
		fail(w, r, err)
		return
	}
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s-v%d.json"`, code, def.Version))
	httpx.JSON(w, http.StatusOK, def)
}

// importDefinition stores an exported definition as a draft (FR-15.11). An
// existing code is refused unless overwrite=true (REPORT-0011). An optional
// Idempotency-Key makes a retried import return the stored result.
func (a *API) importDefinition(w http.ResponseWriter, r *http.Request) {
	if !principal(r) {
		httpx.Fail(w, r, unauthenticated())
		return
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBody))
	if err != nil {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusBadRequest, "COM-0002", "unreadable body"))
		return
	}
	var def domain.Definition
	if err := decodeStrict(raw, &def); err != nil {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusBadRequest, "COM-0002", err.Error()))
		return
	}
	if def.SchemaVersion != domain.SchemaVersion {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusUnprocessableEntity, domain.CodeImportSchema,
			fmt.Sprintf("Schema version %s is not supported (supported: %s).", def.SchemaVersion, domain.SchemaVersion)))
		return
	}
	if issues := def.Validate(); len(issues) > 0 {
		httpx.Fail(w, r, issueProblem(issues))
		return
	}
	overwrite := r.URL.Query().Get("overwrite") == "true"
	sum := sha256.Sum256(append([]byte(strconv.FormatBool(overwrite)+"\n"), raw...))
	saved, rev, replayed, err := a.store.ImportDefinition(r.Context(), def, overwrite, r.Header.Get("Idempotency-Key"), hex.EncodeToString(sum[:]))
	if err != nil {
		fail(w, r, err)
		return
	}
	w.Header().Set("ETag", etag(rev))
	status := http.StatusCreated
	if replayed {
		status = http.StatusOK
	}
	httpx.JSON(w, status, saved)
}

// --- runs ------------------------------------------------------------------

type periodDTO struct {
	Year       int  `json:"year"`
	No         int  `json:"no"`
	Quarter    int  `json:"quarter"`
	Adjustment bool `json:"adjustment"`
}

type runRequest struct {
	Definition        string      `json:"definition"`
	DefinitionVersion int         `json:"definitionVersion"`
	LedgerID          string      `json:"ledgerId"`
	CurrencyType      string      `json:"currencyType"`
	Period            periodDTO   `json:"period"`
	Calendar          []periodDTO `json:"calendar"`
	IncludeAdjustment bool        `json:"includeAdjustmentPeriods"`
}

func (a *API) createRun(w http.ResponseWriter, r *http.Request) {
	p, ok := tenant.FromContext(r.Context())
	if !ok {
		httpx.Fail(w, r, unauthenticated())
		return
	}
	key := r.Header.Get("Idempotency-Key")
	if key == "" {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusBadRequest, "COM-0003", "Idempotency-Key header is required"))
		return
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBody))
	if err != nil {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusBadRequest, "COM-0002", "unreadable body"))
		return
	}
	var req runRequest
	if err := decodeStrict(raw, &req); err != nil {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusBadRequest, "COM-0002", err.Error()))
		return
	}
	if err := validateRun(req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	cal := make([]domain.Period, 0, len(req.Calendar))
	for _, c := range req.Calendar {
		cal = append(cal, domain.Period{Year: c.Year, No: c.No, Quarter: c.Quarter, Adjustment: c.Adjustment})
	}
	sum := sha256.Sum256(raw)
	run, err := a.store.CreateRun(r.Context(), store.RunSpec{
		Code: req.Definition, Version: req.DefinitionVersion, LedgerID: req.LedgerID, CurrencyType: req.CurrencyType,
		PeriodYear: req.Period.Year, PeriodNo: req.Period.No, IncludeAdjustment: req.IncludeAdjustment, Calendar: cal,
		RequestedBy: p.UserID, IdempotencyKey: key, RequestHash: hex.EncodeToString(sum[:]),
	})
	if err != nil {
		fail(w, r, err)
		return
	}
	w.Header().Set("Location", "/api/v1/reporting/runs/"+run.ID)
	status := http.StatusCreated
	if run.Replayed {
		status = http.StatusOK
	}
	httpx.JSON(w, status, run)
}

// listRuns is the report library list: newest runs first, optionally for one
// definition code, at most limit (default and maximum 200).
func (a *API) listRuns(w http.ResponseWriter, r *http.Request) {
	if !principal(r) {
		httpx.Fail(w, r, unauthenticated())
		return
	}
	limit := store.MaxRunPage
	if v := r.URL.Query().Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > store.MaxRunPage {
			httpx.Fail(w, r, httpx.NewProblem(http.StatusBadRequest, "COM-0002", fmt.Sprintf("limit must be 1 to %d", store.MaxRunPage)))
			return
		}
		limit = n
	}
	runs, err := a.store.ListRuns(r.Context(), r.URL.Query().Get("definition"), limit)
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": runs})
}

func validateRun(req runRequest) error {
	bad := func(detail string) error {
		return httpx.NewProblem(http.StatusUnprocessableEntity, "COM-0002", detail)
	}
	switch {
	case req.Definition == "":
		return bad("definition is required")
	case req.DefinitionVersion < 0:
		return bad("definitionVersion must be 0 (latest published) or a published version")
	case !ids.Valid(req.LedgerID):
		return bad("ledgerId must be a UUID")
	case req.Period.Year <= 0 || req.Period.No < 0:
		return bad("period.year and period.no are required")
	case len(req.Calendar) == 0:
		return bad("calendar must list the ledger's periods")
	case len(req.Calendar) > maxCalendarPeriods:
		return bad(fmt.Sprintf("calendar may list at most %d periods", maxCalendarPeriods))
	}
	for i, c := range req.Calendar {
		if c.Year <= 0 || c.No < 0 || c.Quarter < 1 || c.Quarter > 4 {
			return bad(fmt.Sprintf("calendar[%d] needs year > 0, no >= 0 and quarter 1 to 4", i))
		}
	}
	switch req.CurrencyType {
	case "txn", "acct", "rpt":
	default:
		return bad("currencyType must be txn, acct or rpt")
	}
	return nil
}

func (a *API) getRun(w http.ResponseWriter, r *http.Request) {
	id, ok := runID(w, r)
	if !ok {
		return
	}
	run, err := a.store.GetRun(r.Context(), id)
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, run)
}

type cellDTO struct {
	Row      string `json:"row"`
	Column   string `json:"column"`
	Value    string `json:"value"`
	Blank    bool   `json:"blank"`
	Computed bool   `json:"computed"`
	Note     string `json:"note,omitempty"`
	KeyCount int    `json:"keyCount"`
}

func (a *API) listCells(w http.ResponseWriter, r *http.Request) {
	id, ok := runID(w, r)
	if !ok {
		return
	}
	if _, err := a.store.GetRun(r.Context(), id); err != nil {
		fail(w, r, err)
		return
	}
	cells, err := a.store.ListCells(r.Context(), id)
	if err != nil {
		fail(w, r, err)
		return
	}
	out := make([]cellDTO, 0, len(cells))
	for _, c := range cells {
		out = append(out, cellDTO{Row: c.Row, Column: c.Column, Value: c.Value.StringFixed(6), Blank: c.Blank, Computed: c.Computed, Note: c.Note, KeyCount: len(c.Keys)})
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": out})
}

type keyDTO struct {
	LedgerID     string `json:"ledgerId"`
	Period       string `json:"period"`
	Account      string `json:"account"`
	Combination  string `json:"combination"`
	CurrencyType string `json:"currencyType"`
	Amount       string `json:"amount"`
}

// drillBalances is drill level 1 (FR-15.7, BR-15.4): the balance keys of one
// cell. The keys must sum to the cell value exactly; a mismatch is an invariant
// failure and is never served. Computed cells (calculation rows, CALC columns)
// answer with computed=true and no keys: their inputs are the referenced cells.
func (a *API) drillBalances(w http.ResponseWriter, r *http.Request) {
	id, ok := runID(w, r)
	if !ok {
		return
	}
	cell, err := a.store.GetCell(r.Context(), id, chi.URLParam(r, "row"), chi.URLParam(r, "column"))
	if err != nil {
		fail(w, r, err)
		return
	}
	if err := cell.CheckDrill(); err != nil {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusInternalServerError, "REPORT-0099", err.Error()))
		return
	}
	keys := make([]keyDTO, 0, len(cell.Keys))
	sum := decimal.Zero
	for _, k := range cell.Keys {
		sum = sum.Add(k.Amount)
		keys = append(keys, keyDTO{LedgerID: k.LedgerID, Period: k.Period, Account: k.Account, Combination: k.Combination,
			CurrencyType: k.CurrencyType, Amount: k.Amount.StringFixed(6)})
	}
	httpx.JSON(w, http.StatusOK, map[string]any{
		"runId": id, "row": cell.Row, "column": cell.Column, "value": cell.Value.StringFixed(6),
		"blank": cell.Blank, "computed": cell.Computed, "sumOfKeys": sum.StringFixed(6), "keys": keys,
	})
}

type lineDTO struct {
	Key             int    `json:"key"`
	PostingDate     string `json:"postingDate"`
	PostingSequence int64  `json:"postingSequence"`
	LineNumber      int    `json:"lineNumber"`
	Voucher         string `json:"voucher"`
	Period          string `json:"period"`
	Account         string `json:"account"`
	Combination     string `json:"combination"`
	CurrencyType    string `json:"currencyType"`
	Debit           string `json:"debit"`
	Credit          string `json:"credit"`
	Amount          string `json:"amount"`
	ClosingEntry    bool   `json:"closingEntry"`
	SourceURI       string `json:"sourceUri,omitempty"`
	TraceID         string `json:"traceId,omitempty"`
	TraceURL        string `json:"traceUrl,omitempty"`
}

// drillLines is drill level 2 (FR-15.7, BR-15.4): the journal lines behind one
// cell, or behind one of its balance keys (?key=, the position in the level 1
// list), keyset paged in (posting date, posting sequence, line number) order.
// Every page carries its own sum, the running sum and the reconciliation of
// all the lines against the balance keys; when they do not tie the response
// says so (reconciled false, difference) instead of hiding it.
func (a *API) drillLines(w http.ResponseWriter, r *http.Request) {
	id, ok := runID(w, r)
	if !ok {
		return
	}
	q := store.LineQuery{Key: -1, Limit: a.linePage}
	query := r.URL.Query()
	if v := query.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > domain.MaxLinePage {
			httpx.Fail(w, r, httpx.NewProblem(http.StatusBadRequest, "COM-0002", fmt.Sprintf("limit must be 1 to %d", domain.MaxLinePage)))
			return
		}
		q.Limit = n
	}
	if v := query.Get("key"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			httpx.Fail(w, r, httpx.NewProblem(http.StatusBadRequest, "COM-0002", "key must be a balance key position (0 or more)"))
			return
		}
		q.Key = n
	}
	if v := query.Get("cursor"); v != "" {
		c, err := domain.DecodeLineCursor(v)
		if err != nil {
			httpx.Fail(w, r, httpx.NewProblem(http.StatusBadRequest, "COM-0002", "cursor is not one this endpoint returned"))
			return
		}
		q.After = &c
	}
	page, err := a.store.DrillLines(r.Context(), id, chi.URLParam(r, "row"), chi.URLParam(r, "column"), q)
	if err != nil {
		fail(w, r, err)
		return
	}
	items := make([]lineDTO, 0, len(page.Items))
	for _, l := range page.Items {
		items = append(items, lineDTO{Key: l.Key, PostingDate: l.PostingDate, PostingSequence: l.PostingSequence, LineNumber: l.LineNumber,
			Voucher: l.Voucher, Period: l.Period(), Account: l.Account, Combination: l.Combination, CurrencyType: l.CurrencyType,
			Debit: l.Debit.StringFixed(6), Credit: l.Credit.StringFixed(6), Amount: l.Amount.StringFixed(6), ClosingEntry: l.ClosingEntry,
			SourceURI: l.SourceURI, TraceID: l.TraceID, TraceURL: a.traceLink(l.TraceID)})
	}
	var key, next any
	if page.Key >= 0 {
		key = page.Key
	}
	if page.Next != nil {
		next = page.Next.Encode()
	}
	rec := page.Reconciliation
	httpx.JSON(w, http.StatusOK, map[string]any{
		"runId": id, "row": page.Cell.Row, "column": page.Cell.Column, "key": key, "blank": page.Cell.Blank, "computed": page.Cell.Computed,
		"value": page.Value.StringFixed(6), "hwm": page.HWM, "limit": q.Limit, "items": items,
		"sumOfLines": page.SumOfLines.StringFixed(6), "cumulativeSum": page.CumulativeSum.StringFixed(6), "nextCursor": next,
		"linesTotal": rec.LinesTotal.StringFixed(6), "difference": rec.Difference.StringFixed(6),
		"unreconciledKeys": rec.UnreconciledKeys, "reconciled": rec.Reconciled(),
	})
}

func (a *API) lag(w http.ResponseWriter, r *http.Request) {
	rows, err := a.store.Lag(r.Context())
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": rows})
}

// --- helpers ---------------------------------------------------------------

type issueDTO struct {
	Field  string `json:"field"`
	Code   string `json:"code"`
	Detail string `json:"detail"`
}

func decodeDefinition(w http.ResponseWriter, r *http.Request) (domain.Definition, bool) {
	var def domain.Definition
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBody))
	if err != nil {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusBadRequest, "COM-0002", "unreadable body"))
		return def, false
	}
	if err := decodeStrict(raw, &def); err != nil {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusBadRequest, "COM-0002", err.Error()))
		return def, false
	}
	return def, true
}

// decodeStrict rejects unknown fields so a typo in a definition fails loudly.
func decodeStrict(raw []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	return dec.Decode(v)
}

func parseIfMatch(r *http.Request) (*int, error) {
	h := strings.Trim(r.Header.Get("If-Match"), `"`)
	if h == "" {
		return nil, nil
	}
	n, err := strconv.Atoi(h)
	if err != nil {
		return nil, httpx.NewProblem(http.StatusBadRequest, "COM-0002", "If-Match must be the draft ETag")
	}
	return &n, nil
}

// runID reads the run id path parameter; anything but a UUID cannot name a run.
func runID(w http.ResponseWriter, r *http.Request) (string, bool) {
	id := chi.URLParam(r, "id")
	if !ids.Valid(id) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, store.CodeRunNotFound, "statement run "+id+" does not exist"))
		return "", false
	}
	return id, true
}

func etag(rev int) string { return fmt.Sprintf(`"%d"`, rev) }

func principal(r *http.Request) bool {
	_, ok := tenant.FromContext(r.Context())
	return ok
}

func unauthenticated() error {
	return httpx.NewProblem(http.StatusUnauthorized, "AUTH-0001", "missing tenant")
}

func issueProblem(issues []domain.Issue) error {
	return httpx.NewProblem(http.StatusUnprocessableEntity, issues[0].Code, issues[0].Field+": "+issues[0].Detail)
}

// fail maps domain errors to their REPORT-nnnn problems and passes every other
// error (already a problem from the store, or unexpected) to httpx.Fail.
func fail(w http.ResponseWriter, r *http.Request, err error) {
	var de *domain.Error
	if errors.As(err, &de) {
		status := http.StatusUnprocessableEntity
		if de.Code == store.CodeImportDuplicate {
			status = http.StatusConflict
		}
		httpx.Fail(w, r, httpx.NewProblem(status, de.Code, de.Detail))
		return
	}
	httpx.Fail(w, r, err)
}
