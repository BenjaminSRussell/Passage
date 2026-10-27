// Package api is the HTTP layer of the inventory service: journals, posting,
// the on-hand and transaction inquiries, reservations, blocking and cycle
// counting. Handlers decode, validate, read the tenant principal,
// call the store and answer with JSON or RFC 9457 problems.
package api

import (
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"slices"
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
	"github.com/BenjaminSRussell/passage/services/inventory/internal/domain"
	"github.com/BenjaminSRussell/passage/services/inventory/internal/store"
)

// Privilege codes (<entity>.<action>; see README).
const (
	dutyRead          = "inventory.read"
	dutyJournalWrite  = "inventory.journal.write"
	dutyJournalPost   = "inventory.journal.post"
	journalsPath      = "/v1/inventory/journals"
	onHandPath        = "/v1/inventory/onhand"
	idempotencyHeader = "Idempotency-Key"
	ifMatchHeader     = "If-Match"
	validationDetail  = "one or more fields are invalid"
	codeValidation    = "COM-0001"
	codePrecondition  = "COM-0428"
	maxKeyLength      = 255
	maxJournalPage    = 200
)

var (
	currencyCode = regexp.MustCompile(`^[A-Z]{3}$`)
	unitCode     = regexp.MustCompile(`^[A-Z0-9_-]{1,12}$`)
	accountCode  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
)

// API groups the handlers and their dependencies.
type API struct {
	store  store.Store
	post   store.PostParams
	posted atomic.Int64
	onhand atomic.Int64
	fail   atomic.Int64
}

// New builds the API with the posting parameters from configuration.
func New(s store.Store, post store.PostParams) *API {
	return &API{store: s, post: post}
}

// Register mounts the routes on r.
func (a *API) Register(r chi.Router) {
	r.With(authz.RequireDuty(dutyJournalWrite)).Post(journalsPath, a.createJournal)
	r.With(authz.RequireDuty(dutyRead)).Get(journalsPath, a.listJournals)
	r.With(authz.RequireDuty(dutyRead)).Get(journalsPath+"/{id}", journalRoute(a.getJournal))
	r.With(authz.RequireDuty(dutyJournalWrite)).Post(journalsPath+"/{id}/lines:bulk", journalRoute(a.addLines))
	r.With(authz.RequireDuty(dutyJournalPost)).Post(journalsPath+"/{id}:post", journalRoute(a.postJournal))
	r.With(authz.RequireDuty(dutyRead)).Get(onHandPath, a.onHand)
	a.registerReservations(r)
	a.registerBlocking(r)
	a.registerCount(r)
	a.registerTransfers(r)
}

// Metrics serves the counters for the admin port.
func (a *API) Metrics() http.Handler { return http.HandlerFunc(a.metrics) }

type createJournalRequest struct {
	JournalType     string `json:"journalType"`
	LegalEntityID   string `json:"legalEntityId"`
	PostingDate     string `json:"postingDate"`
	OffsetAccountID string `json:"offsetAccountId"`
	Currency        string `json:"currency"`
}

type lineRequest struct {
	ClientKey   string            `json:"clientKey"`
	ProductID   string            `json:"productId"`
	Dimensions  map[string]string `json:"dimensions"`
	Qty         string            `json:"qty"`
	EnteredQty  *string           `json:"enteredQty"`
	EnteredUnit string            `json:"enteredUnit"`
	CostPrice   string            `json:"costPrice"`
}

type addLinesRequest struct {
	Lines []lineRequest `json:"lines"`
}

type lineResponse struct {
	ID          string            `json:"id"`
	LineNo      int               `json:"lineNo"`
	ClientKey   string            `json:"clientKey,omitempty"`
	ProductID   string            `json:"productId"`
	Dimensions  map[string]string `json:"dimensions"`
	Qty         string            `json:"qty"`
	EnteredQty  string            `json:"enteredQty"`
	EnteredUnit string            `json:"enteredUnit"`
	CostPrice   string            `json:"costPrice"`
}

type glLineResponse struct {
	Account string `json:"account"`
	Debit   string `json:"debit"`
	Credit  string `json:"credit"`
}

type glEntryResponse struct {
	Currency      string           `json:"currency"`
	Lines         []glLineResponse `json:"lines"`
	LedgerEntryID *string          `json:"ledgerEntryId"`
}

type journalSummary struct {
	ID              string     `json:"id"`
	LegalEntityID   string     `json:"legalEntityId"`
	JournalType     string     `json:"journalType"`
	Status          string     `json:"status"`
	PostingDate     string     `json:"postingDate"`
	OffsetAccountID string     `json:"offsetAccountId,omitempty"`
	Currency        string     `json:"currency"`
	Version         int        `json:"version"`
	LineCount       int        `json:"lineCount"`
	CreatedAt       time.Time  `json:"createdAt"`
	PostedAt        *time.Time `json:"postedAt,omitempty"`
}

type journalResponse struct {
	journalSummary
	Lines   []lineResponse   `json:"lines"`
	GLEntry *glEntryResponse `json:"glEntry,omitempty"`
}

type onHandItem struct {
	Group            map[string]string `json:"group"`
	PhysicalInvent   string            `json:"physicalInvent"`
	AvailPhysical    string            `json:"availPhysical"`
	AvailOrdered     string            `json:"availOrdered"`
	PostedValue      string            `json:"postedValue"`
	ReservedPhysical string            `json:"reservedPhysical"`
	Blocked          string            `json:"blocked"`
	Currency         string            `json:"currency"`
	Negative         bool              `json:"negative"`
}

func fieldProblem(w http.ResponseWriter, r *http.Request, fields map[string]string) {
	p := httpx.NewProblem(http.StatusBadRequest, codeValidation, validationDetail)
	p.Errors = fields
	httpx.Fail(w, r, p)
}

func validTenant(w http.ResponseWriter, r *http.Request) bool {
	p, ok := tenant.FromContext(r.Context())
	if !ok || !ids.Valid(p.TenantID) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusUnauthorized, "AUTH-0001", "missing or malformed tenant"))
		return false
	}
	return true
}

// journalRoute checks the tenant and the journal id before the handler runs.
func journalRoute(next func(http.ResponseWriter, *http.Request, string)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !validTenant(w, r) {
			return
		}
		id := chi.URLParam(r, "id")
		if !ids.Valid(id) {
			httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, store.CodeJournalNotFound, "inventory journal not found"))
			return
		}
		next(w, r, id)
	}
}

func (a *API) createJournal(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var req createJournalRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	if !ids.Valid(req.LegalEntityID) {
		fields["legalEntityId"] = "must be a UUID"
	}
	if !slices.Contains(domain.KeyedJournalTypes, domain.JournalType(req.JournalType)) {
		fields["journalType"] = "must be MOVEMENT, ADJUSTMENT or OPENING_BALANCE"
	}
	date, err := time.Parse("2006-01-02", req.PostingDate)
	if err != nil {
		fields["postingDate"] = "must be a date (YYYY-MM-DD)"
	}
	if !currencyCode.MatchString(req.Currency) {
		fields["currency"] = "must be an ISO 4217 alpha-3 code"
	}
	req.OffsetAccountID = strings.TrimSpace(req.OffsetAccountID)
	switch {
	case req.JournalType == string(domain.JournalMovement) && req.OffsetAccountID == "":
		fields["offsetAccountId"] = "is required for a MOVEMENT journal"
	case req.JournalType != string(domain.JournalMovement) && req.OffsetAccountID != "":
		fields["offsetAccountId"] = "only MOVEMENT journals take an offset account; the posting profile supplies the others"
	case req.OffsetAccountID != "" && !accountCode.MatchString(req.OffsetAccountID):
		fields["offsetAccountId"] = "must be an account id of 1 to 64 letters, digits, '.', '_' or '-'"
	}
	key := strings.TrimSpace(r.Header.Get(idempotencyHeader))
	if key == "" || len(key) > maxKeyLength {
		fields[idempotencyHeader] = fmt.Sprintf("is required (1 to %d characters)", maxKeyLength)
	}
	if len(fields) > 0 {
		fieldProblem(w, r, fields)
		return
	}
	in := domain.Journal{LegalEntityID: req.LegalEntityID, Type: domain.JournalType(req.JournalType), PostingDate: date,
		OffsetAccountID: req.OffsetAccountID, Currency: req.Currency}
	j, inserted, err := a.store.CreateJournal(r.Context(), in, key)
	if err != nil {
		a.failed(w, r, "create journal", err)
		return
	}
	status := http.StatusCreated
	if !inserted {
		status = http.StatusOK
		w.Header().Set("Idempotent-Replayed", "true")
	}
	writeJournal(w, status, j)
}

func (a *API) getJournal(w http.ResponseWriter, r *http.Request, id string) {
	if !validTenant(w, r) {
		return
	}
	j, err := a.store.GetJournal(r.Context(), id)
	if err != nil {
		a.failed(w, r, "get journal", err)
		return
	}
	writeJournal(w, http.StatusOK, j)
}

func (a *API) addLines(w http.ResponseWriter, r *http.Request, id string) {
	if !validTenant(w, r) {
		return
	}
	var req addLinesRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if len(req.Lines) == 0 || len(req.Lines) > domain.DefaultJournalRules.MaxLines {
		fieldProblem(w, r, map[string]string{"lines": fmt.Sprintf("must hold 1 to %d lines", domain.DefaultJournalRules.MaxLines)})
		return
	}
	lines, fields := parseLines(req.Lines)
	if len(fields) > 0 {
		fieldProblem(w, r, fields)
		return
	}
	j, err := a.store.AddJournalLines(r.Context(), id, lines)
	if err != nil {
		a.failed(w, r, "add journal lines", err)
		return
	}
	writeJournal(w, http.StatusCreated, j)
}

// listJournals is the journal list inquiry, newest first, filtered by status
// and type, with cursor paging.
func (a *API) listJournals(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	q := r.URL.Query()
	f := store.JournalFilter{Status: domain.JournalStatus(q.Get("status")), Type: domain.JournalType(q.Get("type")), Cursor: q.Get("cursor"), Limit: 50}
	fields := map[string]string{}
	if f.Status != "" && !slices.Contains(domain.JournalStatuses, f.Status) {
		fields["status"] = "unknown journal status"
	}
	if f.Type != "" && !slices.Contains(domain.JournalTypes, f.Type) {
		fields["type"] = "must be MOVEMENT, ADJUSTMENT, OPENING_BALANCE or COUNT"
	}
	if raw := q.Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > maxJournalPage {
			fields["limit"] = fmt.Sprintf("must be an integer from 1 to %d", maxJournalPage)
		}
		f.Limit = n
	}
	if len(fields) > 0 {
		fieldProblem(w, r, fields)
		return
	}
	page, err := a.store.ListJournals(r.Context(), f)
	if err != nil {
		a.failed(w, r, "list journals", err)
		return
	}
	items := make([]journalSummary, 0, len(page.Items))
	for _, j := range page.Items {
		items = append(items, summaryOf(j))
	}
	body := map[string]any{"items": items}
	if page.NextCursor != "" {
		body["nextCursor"] = page.NextCursor
	}
	httpx.JSON(w, http.StatusOK, body)
}

func parseLines(in []lineRequest) ([]domain.JournalLine, map[string]string) {
	fields := map[string]string{}
	out := make([]domain.JournalLine, 0, len(in))
	for i, l := range in {
		prefix := fmt.Sprintf("lines[%d].", i)
		l.ClientKey = strings.TrimSpace(l.ClientKey)
		if len(l.ClientKey) > maxKeyLength {
			fields[prefix+"clientKey"] = fmt.Sprintf("must be at most %d characters", maxKeyLength)
		}
		if !ids.Valid(l.ProductID) {
			fields[prefix+"productId"] = "must be a UUID"
		}
		qty, err := decimal.NewFromString(l.Qty)
		if err != nil {
			fields[prefix+"qty"] = "must be a decimal number"
		}
		entered := qty
		if l.EnteredQty != nil {
			if entered, err = decimal.NewFromString(*l.EnteredQty); err != nil {
				fields[prefix+"enteredQty"] = "must be a decimal number"
			}
		}
		cost := decimal.Zero
		if l.CostPrice != "" {
			if cost, err = decimal.NewFromString(l.CostPrice); err != nil || cost.Sign() < 0 {
				fields[prefix+"costPrice"] = "must be a non-negative decimal number"
			}
		}
		if !unitCode.MatchString(l.EnteredUnit) {
			fields[prefix+"enteredUnit"] = "is required (1 to 12 characters of A-Z, 0-9, _ or -)"
		}
		out = append(out, domain.JournalLine{ClientKey: l.ClientKey, ProductID: l.ProductID, Dims: domain.Dimensions(l.Dimensions), Qty: qty,
			EnteredQty: entered, EnteredUnit: l.EnteredUnit, CostPrice: cost, LineNo: i + 1})
	}
	return out, fields
}

func (a *API) postJournal(w http.ResponseWriter, r *http.Request, id string) {
	if !validTenant(w, r) {
		return
	}
	version, err := ifMatchVersion(r)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	j, err := a.store.PostJournal(r.Context(), id, version, a.post)
	if err != nil {
		a.failed(w, r, "post journal", err)
		return
	}
	a.posted.Add(1)
	writeJournal(w, http.StatusOK, j)
}

// ifMatchVersion reads the resource version (journal, blocking, count sheet)
// from If-Match, required on the state-changing custom methods.
func ifMatchVersion(r *http.Request) (int, error) {
	raw := strings.Trim(strings.TrimSpace(r.Header.Get(ifMatchHeader)), `"`)
	if raw == "" {
		return 0, httpx.NewProblem(http.StatusPreconditionRequired, codePrecondition, "If-Match with the version from the last read is required")
	}
	v, err := strconv.Atoi(raw)
	if err != nil || v < 1 {
		return 0, httpx.NewProblem(http.StatusBadRequest, codeValidation, "If-Match must be the version from the last read (the ETag)")
	}
	return v, nil
}

func (a *API) onHand(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	q := r.URL.Query()
	f := store.OnHandFilter{ProductID: q.Get("product"), SiteID: q.Get("site"), WarehouseID: q.Get("warehouse"), BatchID: q.Get("batch")}
	fields := map[string]string{}
	if f.ProductID != "" && !ids.Valid(f.ProductID) {
		fields["product"] = "must be a UUID"
	}
	for _, g := range splitList(q.Get("groupBy")) {
		if !slices.Contains(store.GroupNames(), g) {
			fields["groupBy"] = "unknown grouping " + g
		}
		f.GroupBy = append(f.GroupBy, g)
	}
	if len(fields) > 0 {
		fieldProblem(w, r, fields)
		return
	}
	res, err := a.store.ListOnHand(r.Context(), f)
	if err != nil {
		a.failed(w, r, "on-hand inquiry", err)
		return
	}
	a.onhand.Add(1)
	items := make([]onHandItem, 0, len(res.Rows))
	for _, row := range res.Rows {
		items = append(items, onHandItem{Group: row.Group, PhysicalInvent: row.PhysicalInvent.String(), AvailPhysical: row.AvailPhysical.String(),
			AvailOrdered: row.AvailOrdered.String(), PostedValue: row.PostedValue.String(), ReservedPhysical: row.ReservedPhysical.String(),
			Blocked: row.Blocked.String(), Currency: row.Currency, Negative: row.Negative})
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items, "truncated": res.Truncated})
}

func splitList(raw string) []string {
	var out []string
	for _, part := range strings.Split(raw, ",") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func summaryOf(j domain.Journal) journalSummary {
	return journalSummary{ID: j.ID, LegalEntityID: j.LegalEntityID, JournalType: string(j.Type), Status: string(j.Status),
		PostingDate: j.PostingDate.Format("2006-01-02"), OffsetAccountID: j.OffsetAccountID, Currency: j.Currency, Version: j.Version,
		LineCount: j.LineCount, CreatedAt: j.CreatedAt, PostedAt: j.PostedAt}
}

func writeJournal(w http.ResponseWriter, status int, j domain.Journal) {
	resp := journalResponse{journalSummary: summaryOf(j), Lines: []lineResponse{}}
	for _, l := range j.Lines {
		resp.Lines = append(resp.Lines, lineResponse{ID: l.ID, LineNo: l.LineNo, ClientKey: l.ClientKey, ProductID: l.ProductID, Dimensions: l.Dims,
			Qty: l.Qty.String(), EnteredQty: l.EnteredQty.String(), EnteredUnit: l.EnteredUnit, CostPrice: l.CostPrice.String()})
	}
	resp.LineCount = len(j.Lines)
	if j.GLEntry != nil {
		gl := &glEntryResponse{Currency: j.GLEntry.Currency, Lines: []glLineResponse{}}
		for _, l := range j.GLEntry.Lines {
			gl.Lines = append(gl.Lines, glLineResponse{Account: l.Account, Debit: l.Debit.String(), Credit: l.Credit.String()})
		}
		resp.GLEntry = gl
	}
	w.Header().Set("ETag", strconv.Quote(strconv.Itoa(j.Version)))
	httpx.JSON(w, status, resp)
}

func (a *API) failed(w http.ResponseWriter, r *http.Request, op string, err error) {
	a.fail.Add(1)
	slog.Error(op+" failed", "err", err, "correlationId", middleware.GetReqID(r.Context()))
	httpx.Fail(w, r, err)
}

// metrics serves the counters on the admin port.
func (a *API) metrics(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4")
	fmt.Fprintf(w, "# HELP inventory_journals_posted_total Journals posted.\n# TYPE inventory_journals_posted_total counter\ninventory_journals_posted_total %d\n", a.posted.Load())
	fmt.Fprintf(w, "# HELP inventory_onhand_queries_total On-hand inquiries answered.\n# TYPE inventory_onhand_queries_total counter\ninventory_onhand_queries_total %d\n", a.onhand.Load())
	fmt.Fprintf(w, "# HELP inventory_requests_failed_total Requests answered with a failure.\n# TYPE inventory_requests_failed_total counter\ninventory_requests_failed_total %d\n", a.fail.Load())
}
