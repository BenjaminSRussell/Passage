// Package api is the HTTP layer of the ledger: decode, validate, call the
// store, render. Errors are RFC 9457 problems; a rejected entry carries every
// violation in a violations array (422).
package api

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/money"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/ledger/internal/domain"
	ledgerevents "github.com/BenjaminSRussell/passage/services/ledger/internal/events"
	"github.com/BenjaminSRussell/passage/services/ledger/internal/store"
)

const (
	idempotencyHeader  = "Idempotency-Key"
	validationDetail   = "request failed validation"
	readyTimeout       = 500 * time.Millisecond
	defaultFutureDays  = 366
	maxFutureDays      = 730
	defaultAccountPage = 100
	maxAccountPage     = 1000
	maxTextLength      = 255
	maxSourceRefLength = 200
	maxIdempotencyKey  = 128

	dutyLedgerWrite  = "ledger.ledger.write"
	dutyAccountWrite = "ledger.account.write"
	dutyRead         = "ledger.journal.read"
	dutyBalanceRead  = "ledger.balance.read"
	dutyPost         = "ledger.journal.post"
	dutyReverse      = "ledger.journal.reverse"
	dutyRebuild      = "ledger.balance.rebuild"
	dutyReasonWrite  = "ledger.reason.write"

	// Narrow duties a route also accepts beside its umbrella duty above, so a
	// service is granted one route instead of a family.
	dutyEntryPost     = "ledger.entry.post"
	dutyEntryValidate = "ledger.entry.validate"
	dutyBatchPost     = "ledger.batch.post"
	dutySetPost       = "ledger.set.post"
	dutyEntryLookup   = "ledger.entry.lookup"
	dutyLedgerLookup  = "ledger.ledger.lookup"
	dutyAccountLookup = "ledger.account.lookup"

	// Maintain account structures (§2.11.17) and the dimension register.
	dutyStructureWrite = "ledger.structure.write"
	dutyDimensionWrite = "ledger.dimension.write"

	// Maintain ledger setup (§2.11.17): description templates, splitting rules.
	dutySetupWrite = "ledger.setup.write"
)

var (
	codeRe          = regexp.MustCompile(`^[A-Z0-9]{1,10}$`)
	accountNumberRe = regexp.MustCompile(`^[A-Za-z0-9._-]{1,20}$`)
	currencyRe      = regexp.MustCompile(`^[A-Z]{3}$`)
	periodRe        = regexp.MustCompile(`^[0-9]{4}-[0-9]{2}$`)
	dimAttrRe       = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,19}$`)
	traceParentRe   = regexp.MustCompile(`^[0-9a-f]{2}-([0-9a-f]{32})-[0-9a-f]{16}-[0-9a-f]{2}$`)
	accountTypes    = map[string]bool{"asset": true, "liability": true, "equity": true, "revenue": true, "expense": true,
		"profit_and_loss_total": true, "balance_sheet_total": true, "reporting": true}
	reconTypes = map[string]bool{"none": true, "customer": true, "vendor": true, "bank": true, "inventory": true, "fixed_asset": true}
)

// API serves the ledger routes over a store.Store.
type API struct {
	store    store.Store
	periods  store.PeriodGate
	rates    store.RateSource
	now      func() time.Time
	posted   atomic.Int64
	replayed atomic.Int64
	reversed atomic.Int64
	rejected atomic.Int64
}

// New returns an API over s.
func New(s store.Store) *API { return &API{store: s, now: time.Now} }

func (a *API) WithPeriodGate(g store.PeriodGate) *API {
	a.periods = g
	return a
}

func (a *API) WithRateSource(src store.RateSource) *API {
	a.rates = src
	return a
}

func (a *API) periodGate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if a.periods != nil {
			r = r.WithContext(store.WithPeriodGate(r.Context(), a.periods))
		}
		if a.rates != nil {
			r = r.WithContext(store.WithRateSource(r.Context(), a.rates))
		}
		next.ServeHTTP(w, r)
	})
}

// Register mounts the routes on r.
func (a *API) Register(r chi.Router) {
	r.Route("/v1/ledger", func(r chi.Router) {
		r.Use(a.periodGate)
		r.With(authz.RequireDuty(dutyLedgerWrite)).Post("/ledgers", a.createLedger)
		r.With(authz.RequireAnyDuty(dutyRead, dutyLedgerLookup)).Get("/ledgers/{id}", a.getLedger)
		r.With(authz.RequireDuty(dutyLedgerWrite)).Patch("/ledgers/{id}", a.patchLedger)
		r.With(authz.RequireDuty(dutyAccountWrite)).Put("/charts/{chart}/accounts/{number}", a.putAccount)
		r.With(authz.RequireAnyDuty(dutyRead, dutyAccountLookup)).Get("/charts/{chart}/accounts", a.listAccounts)
		r.With(authz.RequireAnyDuty(dutyPost, dutyEntryPost)).Post("/journal-entries", a.postEntry)
		r.With(authz.RequireDuty(dutyRead)).Get("/journal-entries/{id}", a.getEntry)
		r.With(authz.RequireDuty(dutyReverse)).Post("/journal-entries/{id}/reverse", a.reverseEntry)
		r.With(authz.RequireAnyDuty(dutyPost, dutyEntryValidate)).Post("/validate", a.validateEntry)
		r.With(authz.RequireAnyDuty(dutyRead, dutyEntryLookup)).Get("/journal-lines", a.listLines)
		r.With(authz.RequireDuty(dutyBalanceRead)).Get("/balances", a.trialBalance)
		r.With(authz.RequireDuty(dutyRebuild)).Post("/balances:rebuild", a.rebuildBalances)
		r.With(authz.RequireDuty(dutyRead)).Get("/reason-codes", a.listReasonCodes)
		r.With(authz.RequireDuty(dutyRead)).Get("/reason-codes/{code}", a.getReasonCode)
		r.With(authz.RequireDuty(dutyReasonWrite)).Put("/reason-codes/{code}", a.putReasonCode)
		r.With(authz.RequireDuty(dutyRead)).Get("/ledgers/{ledger}/account-structures", a.listStructures)
		r.With(authz.RequireDuty(dutyRead)).Get("/ledgers/{ledger}/account-structures/{id}", a.getStructure)
		r.With(authz.RequireDuty(dutyStructureWrite)).Put("/ledgers/{ledger}/account-structures/{id}", a.putStructure)
		r.With(authz.RequireDuty(dutyRead)).Get("/ledgers/{ledger}/dimension-values", a.listDimensionValues)
		r.With(authz.RequireDuty(dutyDimensionWrite)).Put("/ledgers/{ledger}/dimension-values/{attribute}/{value}", a.putDimensionValue)
		r.With(authz.RequireDuty(dutyRead)).Get("/description-templates", a.listDescriptionTemplates)
		r.With(authz.RequireDuty(dutyRead)).Get("/description-templates/{id}", a.getDescriptionTemplate)
		r.With(authz.RequireDuty(dutySetupWrite)).Put("/description-templates/{id}", a.putDescriptionTemplate)
		a.registerBatches(r)
		a.registerRecurring(r)
		a.registerLedgerSets(r)
		a.registerOpenItems(r)
		a.registerAutoClearing(r)
		a.registerDerivation(r)
		a.registerAdvancedRules(r)
		a.registerAttachments(r)
		a.registerValidationRules(r)
		a.registerSplitting(r)
		a.registerRealignment(r)
	})
}

// Admin serves the admin port: Prometheus metrics and the DB-backed readiness
// probe. The platform router already owns /readyz on the main port (static), so
// the ledger's readiness check (SELECT 1 through the pool) lives here.
func (a *API) Admin() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/metrics", a.Metrics())
	mux.HandleFunc("/readyz", a.ready)
	return mux
}

// Metrics serves Prometheus text counters for the admin port.
func (a *API) Metrics() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		tenant.WriteReplayMetrics(w)
		fmt.Fprintf(w, "# HELP ledger_entries_posted_total Journal entries posted (not counting replays).\n# TYPE ledger_entries_posted_total counter\nledger_entries_posted_total %d\n", a.posted.Load())
		fmt.Fprintf(w, "# HELP ledger_idempotent_replays_total Keyed requests answered from the stored response.\n# TYPE ledger_idempotent_replays_total counter\nledger_idempotent_replays_total %d\n", a.replayed.Load())
		fmt.Fprintf(w, "# HELP ledger_entries_reversed_total Reversing entries posted.\n# TYPE ledger_entries_reversed_total counter\nledger_entries_reversed_total %d\n", a.reversed.Load())
		fmt.Fprintf(w, "# HELP ledger_entries_rejected_total Entries refused by posting rules (422).\n# TYPE ledger_entries_rejected_total counter\nledger_entries_rejected_total %d\n", a.rejected.Load())
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

// --- shared helpers ---------------------------------------------------------

func principal(w http.ResponseWriter, r *http.Request) (tenant.Principal, bool) {
	p, ok := tenant.FromContext(r.Context())
	if !ok || !ids.Valid(p.TenantID) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusUnauthorized, "AUTH-0001", "missing or malformed tenant"))
		return p, false
	}
	return p, true
}

func validationFailed(w http.ResponseWriter, r *http.Request, fields map[string]string) {
	p := httpx.NewProblem(http.StatusBadRequest, "COM-0001", validationDetail)
	p.Errors = fields
	httpx.Fail(w, r, p)
}

// violationProblem is the 422 body: a problem with every rule violation.
type violationProblem struct {
	httpx.Problem
	Violations []domain.Violation `json:"violations"`
}

// fail renders err. Rule rejections become 422 with violations[]; everything
// else goes through httpx.Fail.
func (a *API) fail(w http.ResponseWriter, r *http.Request, err error) {
	var rej *domain.RejectedError
	if errors.As(err, &rej) && len(rej.Violations) > 0 {
		a.rejected.Add(1)
		first := rej.Violations[0]
		body := violationProblem{
			Problem: httpx.Problem{Type: "https://passage.dev/problems/" + first.Code, Title: http.StatusText(http.StatusUnprocessableEntity),
				Status: http.StatusUnprocessableEntity, Code: first.Code, Detail: first.Message, Instance: r.URL.Path,
				TraceID: middleware.GetReqID(r.Context())},
			Violations: rej.Violations,
		}
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(http.StatusUnprocessableEntity)
		_ = json.NewEncoder(w).Encode(body)
		return
	}
	var p *httpx.Problem
	if !errors.As(err, &p) {
		slog.Error("ledger request failed", "err", err, "correlationId", middleware.GetReqID(r.Context()))
	} else if p.Code == store.CodeIdempotencyInProgress {
		w.Header().Set("Retry-After", "1")
	}
	httpx.Fail(w, r, err)
}

// idempotencyKey reads the Idempotency-Key header, trimmed (BR-11.6), and
// records a field error when it is missing or longer than 128 characters.
func idempotencyKey(r *http.Request, fields map[string]string) string {
	key := strings.TrimSpace(r.Header.Get(idempotencyHeader))
	if n := utf8.RuneCountInString(key); n < 1 || n > maxIdempotencyKey {
		fields[idempotencyHeader] = "is required and must be 1 to 128 characters"
	}
	return key
}

func parseDate(value string) (time.Time, bool) {
	d, err := time.Parse(time.DateOnly, value)
	return d, err == nil
}

// today is the business date used for posting-date checks (UTC).
func (a *API) today() time.Time {
	n := a.now().UTC()
	return time.Date(n.Year(), n.Month(), n.Day(), 0, 0, 0, 0, time.UTC)
}

// traceID takes the W3C traceparent trace id, or mints a 32-char id.
func traceID(r *http.Request) string {
	if m := traceParentRe.FindStringSubmatch(r.Header.Get("traceparent")); m != nil {
		return m[1]
	}
	return hex.EncodeToString([]byte(ids.New()[:16]))
}

// --- ledgers and charts -----------------------------------------------------

type createLedgerRequest struct {
	LegalEntityID       string   `json:"legalEntityId"`
	Code                string   `json:"code"`
	Name                string   `json:"name"`
	ChartCode           string   `json:"chartCode"`
	Currency            string   `json:"currency"`
	DimensionAttributes []string `json:"dimensionAttributes"`
	FirstOpenDate       *string  `json:"firstOpenDate"`
	FuturePostingDays   *int     `json:"futurePostingDays"`
	// ReversalReasonRequired defaults to true (FR-11.15); NegativePostingEnabled to false (BR-11.16).
	ReversalReasonRequired *bool `json:"reversalReasonRequired"`
	NegativePostingEnabled *bool `json:"negativePostingEnabled"`
}

var (
	calendarCodeRe      = regexp.MustCompile(`^[A-Z0-9_-]{1,20}$`)
	accountingPrinciple = map[string]bool{"IFRS": true, "US_GAAP": true, "LOCAL_GAAP": true, "TAX": true, "MANAGEMENT": true}
)

type patchLedgerRequest struct {
	CalendarCode        *string `json:"calendarCode"`
	AccountingPrinciple *string `json:"accountingPrinciple"`
	ReportingCurrency   *string `json:"reportingCurrency"`
}

func (a *API) patchLedger(w http.ResponseWriter, r *http.Request) {
	if _, ok := principal(w, r); !ok {
		return
	}
	id := chi.URLParam(r, "id")
	if !ids.Valid(id) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, "LEDGER_NOT_FOUND", "ledger not found"))
		return
	}
	var req patchLedgerRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	if req.CalendarCode == nil && req.AccountingPrinciple == nil && req.ReportingCurrency == nil {
		fields["body"] = "send at least one of calendarCode, accountingPrinciple, reportingCurrency (an empty string clears it)"
	}
	if v := req.CalendarCode; v != nil && *v != "" && !calendarCodeRe.MatchString(*v) {
		fields["calendarCode"] = "must be 1 to 20 characters of A-Z, 0-9, '_' or '-'"
	}
	if v := req.AccountingPrinciple; v != nil && *v != "" && !accountingPrinciple[*v] {
		fields["accountingPrinciple"] = "must be one of IFRS, US_GAAP, LOCAL_GAAP, TAX, MANAGEMENT"
	}
	if v := req.ReportingCurrency; v != nil && *v != "" && !currencyRe.MatchString(*v) {
		fields["reportingCurrency"] = "must be an ISO 4217 alpha-3 code"
	}
	version, versionOK := parseIfMatch(r.Header.Get("If-Match"))
	if !versionOK {
		fields["If-Match"] = `must be the ledger version as an entity tag, for example "3"`
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	ledger, err := a.store.UpdateLedgerSettings(r.Context(), id, version, store.LedgerSettings(req))
	if err != nil {
		a.fail(w, r, err)
		return
	}
	w.Header().Set("ETag", fmt.Sprintf(`"%d"`, ledger.Version))
	httpx.JSON(w, http.StatusOK, ledger)
}

func (a *API) createLedger(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	var req createLedgerRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	if !codeRe.MatchString(req.Code) {
		fields["code"] = "must be 1 to 10 characters of A-Z or 0-9"
	}
	if !codeRe.MatchString(req.ChartCode) {
		fields["chartCode"] = "must be 1 to 10 characters of A-Z or 0-9"
	}
	if !currencyRe.MatchString(req.Currency) {
		fields["currency"] = "must be an ISO 4217 alpha-3 code"
	}
	if n := utf8.RuneCountInString(req.Name); n < 1 || n > 120 {
		fields["name"] = "must be 1 to 120 characters"
	}
	if !ids.Valid(req.LegalEntityID) {
		fields["legalEntityId"] = "must be a UUID"
	}
	if len(req.DimensionAttributes) > domain.MaxDimensionAttributes {
		fields["dimensionAttributes"] = fmt.Sprintf("at most %d attributes per ledger", domain.MaxDimensionAttributes)
	}
	seen := map[string]bool{}
	for _, attr := range req.DimensionAttributes {
		if !dimAttrRe.MatchString(attr) || seen[attr] {
			fields["dimensionAttributes"] = "each attribute must be unique, A-Z with digits or underscores, at most 20 characters"
		}
		seen[attr] = true
	}
	var firstOpen *string
	if req.FirstOpenDate != nil && *req.FirstOpenDate != "" {
		if _, ok := parseDate(*req.FirstOpenDate); !ok {
			fields["firstOpenDate"] = "must be a date as YYYY-MM-DD"
		}
		firstOpen = req.FirstOpenDate
	}
	future := defaultFutureDays
	if req.FuturePostingDays != nil {
		future = *req.FuturePostingDays
		if future < 0 || future > maxFutureDays {
			fields["futurePostingDays"] = fmt.Sprintf("must be 0 to %d", maxFutureDays)
		}
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	if !tenant.InLegalEntityScope(p, req.LegalEntityID) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusForbidden, "AUTH-0003", "the caller is scoped to another legal entity"))
		return
	}
	reasonRequired, negativeEnabled := true, false
	if req.ReversalReasonRequired != nil {
		reasonRequired = *req.ReversalReasonRequired
	}
	if req.NegativePostingEnabled != nil {
		negativeEnabled = *req.NegativePostingEnabled
	}
	created, err := a.store.CreateLedger(r.Context(), store.Ledger{LegalEntityID: req.LegalEntityID, Code: req.Code,
		Name: req.Name, ChartCode: req.ChartCode, Currency: req.Currency, DimensionAttributes: req.DimensionAttributes,
		FirstOpenDate: firstOpen, FuturePostingDays: future, ReversalReasonRequired: reasonRequired,
		NegativePostingEnabled: negativeEnabled})
	if err != nil {
		a.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, created)
}

func (a *API) getLedger(w http.ResponseWriter, r *http.Request) {
	if _, ok := principal(w, r); !ok {
		return
	}
	id := chi.URLParam(r, "id")
	if !ids.Valid(id) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, "LEDGER_NOT_FOUND", "ledger not found"))
		return
	}
	l, err := a.store.GetLedger(r.Context(), id)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, l)
}

type putAccountRequest struct {
	Name                      string  `json:"name"`
	AccountType               string  `json:"accountType"`
	IsPostable                *bool   `json:"isPostable"`
	SuspendedFrom             *string `json:"suspendedFrom"`
	ReconciliationAccountType string  `json:"reconciliationAccountType"`
}

// putAccount creates or replaces a main account (PUT is naturally idempotent).
// Responds 201 when the account was created and 200 when it was replaced.
func (a *API) putAccount(w http.ResponseWriter, r *http.Request) {
	if _, ok := principal(w, r); !ok {
		return
	}
	chart, number := chi.URLParam(r, "chart"), chi.URLParam(r, "number")
	var req putAccountRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	if !codeRe.MatchString(chart) {
		fields["chart"] = "must be 1 to 10 characters of A-Z or 0-9"
	}
	if !accountNumberRe.MatchString(number) {
		fields["number"] = "must be 1 to 20 characters of A-Z, a-z, 0-9, '.', '_' or '-'"
	}
	if n := utf8.RuneCountInString(req.Name); n < 1 || n > 120 {
		fields["name"] = "must be 1 to 120 characters"
	}
	if !accountTypes[req.AccountType] {
		fields["accountType"] = "must be one of asset, liability, equity, revenue, expense, profit_and_loss_total, balance_sheet_total, reporting"
	}
	reconType := req.ReconciliationAccountType
	if reconType == "" {
		reconType = store.ReconNone
	}
	if !reconTypes[reconType] {
		fields["reconciliationAccountType"] = "must be none, customer, vendor, bank, inventory or fixed_asset"
	}
	postable := true
	if req.IsPostable != nil {
		postable = *req.IsPostable
	}
	if (req.AccountType == "profit_and_loss_total" || req.AccountType == "balance_sheet_total" || req.AccountType == "reporting") && postable {
		fields["isPostable"] = "totals and reporting accounts are not postable"
	}
	if req.SuspendedFrom != nil {
		if _, ok := parseDate(*req.SuspendedFrom); !ok {
			fields["suspendedFrom"] = "must be a date as YYYY-MM-DD"
		}
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	acct, created, err := a.store.PutAccount(r.Context(), chart, number, store.Account{Name: req.Name, Type: req.AccountType,
		Postable: postable, SuspendedFrom: req.SuspendedFrom, ReconType: reconType})
	if err != nil {
		a.fail(w, r, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	httpx.JSON(w, status, acct)
}

func (a *API) listAccounts(w http.ResponseWriter, r *http.Request) {
	if _, ok := principal(w, r); !ok {
		return
	}
	q := r.URL.Query()
	limit := defaultAccountPage
	if raw := q.Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > maxAccountPage {
			validationFailed(w, r, map[string]string{"limit": fmt.Sprintf("must be 1 to %d", maxAccountPage)})
			return
		}
		limit = n
	}
	items, err := a.store.ListAccounts(r.Context(), chi.URLParam(r, "chart"), q.Get("after"), limit)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	next := ""
	if len(items) == limit {
		next = items[len(items)-1].Number
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items, "nextAfter": next})
}

// --- journal entries --------------------------------------------------------

type sourceDTO struct {
	System string `json:"system"`
	Type   string `json:"type"`
	ID     string `json:"id"`
	URI    string `json:"uri"`
}

type amountDTO struct {
	Currency string `json:"currency"`
	Value    string `json:"value"`
}

type lineDTO struct {
	Account       string            `json:"account"`
	Dimensions    map[string]string `json:"dimensions"`
	DebitCredit   string            `json:"debitCredit"`
	Amount        amountDTO         `json:"amount"`
	Text          string            `json:"text"`
	SourceLineRef string            `json:"sourceLineRef"`
}

type postEntryRequest struct {
	LedgerID     string    `json:"ledgerId"`
	DocumentID   string    `json:"documentId"`
	PostingDate  string    `json:"postingDate"`
	DocumentDate *string   `json:"documentDate"`
	Description  string    `json:"description"`
	Source       sourceDTO `json:"source"`
	Lines        []lineDTO `json:"lines"`
}

type singleEntryRequest struct {
	postEntryRequest
	LedgerLines []ledgerLinesDTO `json:"ledgerLines,omitempty"`
}

// toPostInput checks the request's syntax and builds the store input. Rule
// checks happen in the domain and are reported as violations, not here.
func (a *API) toPostInput(req postEntryRequest, r *http.Request) (store.PostInput, map[string]string) {
	fields := map[string]string{}
	in := store.PostInput{LedgerID: req.LedgerID, DocumentID: req.DocumentID, Description: req.Description, Today: a.today()}
	if !ids.Valid(req.LedgerID) {
		fields["ledgerId"] = "must be a UUID"
	}
	if !ids.Valid(req.DocumentID) {
		fields["documentId"] = "must be a UUID"
	}
	if d, ok := parseDate(req.PostingDate); ok {
		in.PostingDate = d
	} else {
		fields["postingDate"] = "must be a date as YYYY-MM-DD"
	}
	if req.DocumentDate != nil && *req.DocumentDate != "" {
		if d, ok := parseDate(*req.DocumentDate); ok {
			in.DocumentDate = &d
		} else {
			fields["documentDate"] = "must be a date as YYYY-MM-DD"
		}
	}
	if utf8.RuneCountInString(req.Description) > maxTextLength {
		fields["description"] = fmt.Sprintf("must be at most %d characters", maxTextLength)
	}
	for name, v := range map[string]string{"source.system": req.Source.System, "source.type": req.Source.Type, "source.id": req.Source.ID} {
		if v == "" || utf8.RuneCountInString(v) > 128 {
			fields[name] = "is required and must be at most 128 characters"
		}
	}
	in.Source = ledgerevents.Source{System: req.Source.System, Type: req.Source.Type, ID: req.Source.ID, URI: req.Source.URI}
	if len(req.Lines) == 0 {
		fields["lines"] = "at least one line is required"
	}
	if len(req.Lines) > domain.MaxLinesPerEntry {
		fields["lines"] = fmt.Sprintf("at most %d lines per entry; split the document with a shared documentId", domain.MaxLinesPerEntry)
	}
	for i, l := range req.Lines {
		in.Lines = append(in.Lines, lineInput(fmt.Sprintf("lines[%d]", i), l, fields))
	}
	in.TraceID = traceID(r)
	return in, fields
}

func lineInput(prefix string, l lineDTO, fields map[string]string) domain.LineInput {
	if l.Account == "" || len(l.Account) > 20 {
		fields[prefix+".account"] = "is required and must be at most 20 characters"
	}
	if l.DebitCredit != domain.SideDebit && l.DebitCredit != domain.SideCredit {
		fields[prefix+".debitCredit"] = "must be D or C"
	}
	amount, err := decimal.NewFromString(l.Amount.Value)
	switch {
	case err != nil:
		fields[prefix+".amount.value"] = "must be a decimal string"
	case amount.IsNegative():
		fields[prefix+".amount.value"] = "must not be negative; use the opposite side"
	}
	if utf8.RuneCountInString(l.Text) > maxTextLength {
		fields[prefix+".text"] = fmt.Sprintf("must be at most %d characters", maxTextLength)
	}
	if utf8.RuneCountInString(l.SourceLineRef) > maxSourceRefLength {
		fields[prefix+".sourceLineRef"] = fmt.Sprintf("must be at most %d characters", maxSourceRefLength)
	}
	return domain.LineInput{Account: l.Account, Dimensions: l.Dimensions, Side: l.DebitCredit,
		Amount: amount, Currency: l.Amount.Currency, Text: l.Text, SourceLineRef: l.SourceLineRef}
}

func (a *API) postEntry(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	var req singleEntryRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	in, fields := a.toPostInput(req.postEntryRequest, r)
	if len(req.LedgerLines) > 0 && len(req.Lines) == 0 {
		delete(fields, "lines")
	}
	in.LedgerLines = ledgerLinesInput(req.LedgerLines, fields)
	key := idempotencyKey(r, fields)
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	req.LedgerLines = canonicalGroups(req.LedgerLines)
	hash, err := domain.HashPayload(req)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	in.Key, in.Hash, in.Actor = key, hash, p.UserID
	in.CanPostLedgerSpecific = p.HasDuty(store.DutyPostLedgerSpecific)
	res, err := a.store.PostEntry(r.Context(), in)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	if res.Idempotency.Replayed {
		a.replayed.Add(1)
		httpx.JSON(w, http.StatusOK, res)
		return
	}
	a.posted.Add(1)
	httpx.JSON(w, http.StatusCreated, res)
}

func (a *API) getEntry(w http.ResponseWriter, r *http.Request) {
	if _, ok := principal(w, r); !ok {
		return
	}
	id := chi.URLParam(r, "id")
	if !ids.Valid(id) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, "LEDGER_ENTRY_NOT_FOUND", "journal entry not found"))
		return
	}
	e, err := a.store.GetEntry(r.Context(), id)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, e)
}

type reverseRequest struct {
	PostingDate   *string `json:"postingDate"`
	Description   string  `json:"description"`
	ReasonCode    string  `json:"reasonCode"`
	ReasonComment string  `json:"reasonComment"`
}

func (a *API) reverseEntry(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	id := chi.URLParam(r, "id")
	if !ids.Valid(id) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, "LEDGER_ENTRY_NOT_FOUND", "journal entry not found"))
		return
	}
	var req reverseRequest
	if r.Body != nil && r.ContentLength != 0 {
		if err := httpx.Decode(r, &req); err != nil {
			httpx.Fail(w, r, err)
			return
		}
	}
	fields := map[string]string{}
	in := store.ReverseInput{EntryID: id, Description: req.Description, Actor: p.UserID, TraceID: traceID(r), Today: a.today(),
		ReasonCode: req.ReasonCode, ReasonComment: req.ReasonComment, CanPostLedgerSpecific: p.HasDuty(store.DutyPostLedgerSpecific)}
	if req.PostingDate != nil {
		d, ok := parseDate(*req.PostingDate)
		if !ok {
			fields["postingDate"] = "must be a date as YYYY-MM-DD"
		}
		in.PostingDate = &d
	}
	if utf8.RuneCountInString(req.Description) > maxTextLength {
		fields["description"] = fmt.Sprintf("must be at most %d characters", maxTextLength)
	}
	if req.ReasonCode != "" && !domain.ValidReasonCode(req.ReasonCode) {
		fields["reasonCode"] = "must be 1 to 10 characters of A-Z, 0-9 or underscore"
	}
	if utf8.RuneCountInString(req.ReasonComment) > domain.MaxReasonCommentLength {
		fields["reasonComment"] = fmt.Sprintf("must be at most %d characters", domain.MaxReasonCommentLength)
	}
	key := idempotencyKey(r, fields)
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	hash, err := domain.HashPayload(struct {
		EntryID string         `json:"entryId"`
		Request reverseRequest `json:"request"`
	}{id, req})
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	in.Key, in.Hash = key, hash
	res, err := a.store.ReverseEntry(r.Context(), in)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	if res.Idempotency.Replayed {
		a.replayed.Add(1)
		httpx.JSON(w, http.StatusOK, res)
		return
	}
	a.reversed.Add(1)
	httpx.JSON(w, http.StatusCreated, res)
}

type validatedLine struct {
	Index                  int    `json:"lineIndex"`
	Account                string `json:"account"`
	DisplayName            string `json:"displayName"`
	DimensionCombinationID string `json:"dimensionCombinationId"`
	// Dimensions are the line's values after derivation (FR-11.22); Derivation is the trace of the steps that set them.
	Dimensions       map[string]string        `json:"dimensions"`
	DerivedByStepIDs []string                 `json:"derivedByStepIds"`
	Derivation       []domain.DerivationTrace `json:"derivation"`
}

type validateResponse struct {
	Valid      bool               `json:"valid"`
	Violations []domain.Violation `json:"violations"`
	// Warnings are failures of validation rules with severity warning (FR-11.21); they do not make the entry invalid.
	Warnings []domain.Violation `json:"warnings"`
	Lines    []validatedLine    `json:"lines"`
	Currency string             `json:"currency"`
	Debit    string             `json:"debit"`
	Credit   string             `json:"credit"`
	// Splitting lists the lines document splitting changed or created (FR-11.23).
	Splitting []domain.SplitTrace `json:"splitting"`
}

// validateEntry is the dry run: every violation in one response, no writes.
func (a *API) validateEntry(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	var req postEntryRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	in, fields := a.toPostInput(req, r)
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	in.CanPostLedgerSpecific = p.HasDuty(store.DutyPostLedgerSpecific)
	res, err := a.store.ValidateEntry(r.Context(), in)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	out := validateResponse{Valid: res.Valid(), Violations: res.Violations, Warnings: res.Warnings, Lines: []validatedLine{},
		Currency: res.Currency, Debit: res.Debit.StringFixed(money.MinorUnits(res.Currency)), Credit: res.Credit.StringFixed(money.MinorUnits(res.Currency))}
	if out.Violations == nil {
		out.Violations = []domain.Violation{}
	}
	if out.Warnings == nil {
		out.Warnings = []domain.Violation{}
	}
	out.Splitting = append([]domain.SplitTrace{}, res.Splitting...)
	for _, l := range res.Lines {
		out.Lines = append(out.Lines, validatedLine{Index: l.Index, Account: l.Account.Number, DisplayName: l.DisplayName,
			DimensionCombinationID: hex.EncodeToString(l.CombinationID), Dimensions: l.Dimensions,
			DerivedByStepIDs: append([]string{}, l.DerivedBy...), Derivation: append([]domain.DerivationTrace{}, res.Derivation[l.Index]...)})
	}
	httpx.JSON(w, http.StatusOK, out)
}

// --- balances ---------------------------------------------------------------

func (a *API) trialBalance(w http.ResponseWriter, r *http.Request) {
	if _, ok := principal(w, r); !ok {
		return
	}
	q := r.URL.Query()
	f := store.BalanceFilter{LedgerID: q.Get("ledger"), PeriodFrom: q.Get("period_from"), PeriodTo: q.Get("period_to"),
		AccountFrom: q.Get("account_from"), AccountTo: q.Get("account_to")}
	fields := map[string]string{}
	if !ids.Valid(f.LedgerID) {
		fields["ledger"] = "is required and must be a UUID"
	}
	for name, v := range map[string]string{"period_from": f.PeriodFrom, "period_to": f.PeriodTo} {
		if v != "" && !periodRe.MatchString(v) {
			fields[name] = "must be a period as YYYY-MM"
		}
	}
	f.Dimensions = map[string]string{}
	for key, values := range q {
		attr, ok := strings.CutPrefix(key, "dim.")
		if !ok {
			continue
		}
		switch {
		case !dimAttrRe.MatchString(attr):
			fields[key] = "the attribute must be A-Z, digits or underscore, starting with a letter"
		case len(values) != 1 || utf8.RuneCountInString(values[0]) > 20:
			fields[key] = "must be given once, at most 20 characters (empty: the attribute is not set)"
		default:
			f.Dimensions[attr] = values[0]
		}
	}
	if raw := q.Get("group_by"); raw != "" {
		seen := map[string]bool{}
		for _, attr := range strings.Split(raw, ",") {
			if !dimAttrRe.MatchString(attr) || seen[attr] {
				fields["group_by"] = "must list distinct dimension attributes (A-Z, digits, underscore) separated by commas"
				break
			}
			seen[attr] = true
			f.GroupBy = append(f.GroupBy, attr)
		}
		if len(f.GroupBy) > store.MaxBalanceGroupBy {
			fields["group_by"] = "must list at most 5 dimension attributes"
		}
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	tb, err := a.store.TrialBalance(r.Context(), f)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, tb)
}

type rebuildRequest struct {
	LedgerID string `json:"ledgerId"`
}

func (a *API) rebuildBalances(w http.ResponseWriter, r *http.Request) {
	if _, ok := principal(w, r); !ok {
		return
	}
	var req rebuildRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if !ids.Valid(req.LedgerID) {
		validationFailed(w, r, map[string]string{"ledgerId": "must be a UUID"})
		return
	}
	res, err := a.store.RebuildBalances(r.Context(), req.LedgerID)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, res)
}
