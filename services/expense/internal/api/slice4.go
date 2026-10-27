package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/expense/internal/domain"
	"github.com/BenjaminSRussell/passage/services/expense/internal/store"
)

const (
	headerIdempotency = "Idempotency-Key"
	maxKeyLength      = 200
	defaultPageSize   = 50
	maxPageSize       = 200
)

// reportStore is the persistence the report routes need (satisfied by *store.Store).
type reportStore interface {
	CreateReport(ctx context.Context, key, hash string, in store.NewReport) (store.Report, bool, error)
	GetReport(ctx context.Context, id string) (store.Report, error)
	ListReports(ctx context.Context, f store.ReportFilter, after string, limit int) ([]store.Report, string, error)
	AddLine(ctx context.Context, reportID, key, hash string, l domain.Line, commitDate time.Time) (store.Report, bool, error)
	DeleteLine(ctx context.Context, reportID, lineID string, expectedVersion int, commitDate time.Time) (store.Report, error)
	SubmitReport(ctx context.Context, reportID, key, hash string, expectedVersion int, submitDate time.Time) (store.Report, bool, error)
}

// WithReports attaches report persistence to the /v1/expense/reports routes.
// Without it those routes answer 503 and the stateless previews still run.
func (a *API) WithReports(s reportStore) *API {
	a.reports = s
	return a
}

// WithClock replaces the clock that dates line commits and submits (tests).
func (a *API) WithClock(now func() time.Time) *API {
	a.now = now
	return a
}

func (a *API) registerReports(r chi.Router, duty func(http.Handler) http.Handler) {
	r.With(duty).Get("/v1/expense/reports", a.listReports)
	r.With(duty).Post("/v1/expense/reports", a.createReport)
	r.With(duty).Get("/v1/expense/reports/{id}", a.getReport)
	r.With(duty).Post("/v1/expense/reports/{id}/lines", a.addLine)
	r.With(duty).Delete("/v1/expense/reports/{id}/lines/{lineId}", a.deleteLine)
	r.With(duty).Post("/v1/expense/reports/{id}/submit", a.submitReport)
}

type createReportRequest struct {
	LegalEntityID string `json:"legalEntityId"`
	WorkerID      string `json:"workerId"`
	Purpose       string `json:"purpose"`
	Currency      string `json:"currency"`
}

// reportDTO is the wire form of a report. List pages leave lines empty.
type reportDTO struct {
	ID            string     `json:"id"`
	LegalEntityID string     `json:"legalEntityId"`
	WorkerID      string     `json:"workerId"`
	Purpose       string     `json:"purpose"`
	Currency      string     `json:"currency"`
	Status        string     `json:"status"`
	Total         string     `json:"total"`
	Reimbursable  string     `json:"reimbursable"`
	CompanyPaid   string     `json:"companyPaid"`
	Submittable   bool       `json:"submittable"`
	Version       int        `json:"version"`
	CreatedAt     string     `json:"createdAt"`
	SubmittedAt   *string    `json:"submittedAt"`
	Lines         []lineView `json:"lines"`
}

type lineView struct {
	lineDTO
	LineNo        int             `json:"lineNo"`
	PolicyResults []domain.Result `json:"policyResults"`
}

type reportPage struct {
	Items      []reportDTO `json:"items"`
	NextCursor string      `json:"nextCursor"`
}

var reportStatuses = map[string]bool{"DRAFT": true, "SUBMITTED": true, "APPROVED": true, "REJECTED": true, "RETURNED": true, "POSTED": true, "PAID": true}

func (a *API) createReport(w http.ResponseWriter, r *http.Request) {
	if !a.authorize(w, r) || !a.storeReady(w, r) {
		return
	}
	key, ok := idempotencyKey(w, r)
	if !ok {
		return
	}
	var req createReportRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	req.Purpose = strings.TrimSpace(req.Purpose)
	fields := map[string]string{}
	requireID(fields, "legalEntityId", req.LegalEntityID)
	requireID(fields, "workerId", req.WorkerID)
	if n := len(req.Purpose); n == 0 || n > 500 {
		fields["purpose"] = "is required and at most 500 characters"
	}
	if !validCurrency(req.Currency) {
		fields["currency"] = "must be a three-letter upper-case ISO 4217 code"
	}
	if len(fields) > 0 {
		refuse(w, r, fields)
		return
	}
	rep, replayed, err := a.reports.CreateReport(r.Context(), key, hashOf(req), store.NewReport{
		LegalEntityID: req.LegalEntityID, WorkerID: req.WorkerID, Purpose: req.Purpose, Currency: req.Currency,
	})
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if !replayed {
		a.reportsCreated.Add(1)
	}
	writeReport(w, rep, replayed, http.StatusCreated)
}

func (a *API) listReports(w http.ResponseWriter, r *http.Request) {
	if !a.authorize(w, r) || !a.storeReady(w, r) {
		return
	}
	q := r.URL.Query()
	fields := map[string]string{}
	limit := defaultPageSize
	if raw := q.Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > maxPageSize {
			fields["limit"] = fmt.Sprintf("must be 1 to %d", maxPageSize)
		} else {
			limit = n
		}
	}
	after := q.Get("cursor")
	if after != "" && !ids.Valid(after) {
		fields["cursor"] = "must be a nextCursor value from an earlier page"
	}
	f := store.ReportFilter{WorkerID: q.Get("workerId"), Status: q.Get("status")}
	if f.WorkerID != "" {
		requireID(fields, "workerId", f.WorkerID)
	}
	if f.Status != "" && !reportStatuses[f.Status] {
		fields["status"] = "must be DRAFT, SUBMITTED, APPROVED, REJECTED, RETURNED, POSTED or PAID"
	}
	if len(fields) > 0 {
		refuse(w, r, fields)
		return
	}
	reps, next, err := a.reports.ListReports(r.Context(), f, after, limit)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	items := make([]reportDTO, 0, len(reps))
	for _, rep := range reps {
		items = append(items, toReportDTO(rep))
	}
	httpx.JSON(w, http.StatusOK, reportPage{Items: items, NextCursor: next})
}

func (a *API) getReport(w http.ResponseWriter, r *http.Request) {
	if !a.authorize(w, r) || !a.storeReady(w, r) {
		return
	}
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	rep, err := a.reports.GetReport(r.Context(), id)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	writeReport(w, rep, false, http.StatusOK)
}

func (a *API) addLine(w http.ResponseWriter, r *http.Request) {
	if !a.authorize(w, r) || !a.storeReady(w, r) {
		return
	}
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	key, ok := idempotencyKey(w, r)
	if !ok {
		return
	}
	var req lineDTO
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	// The hash covers the body as sent, before a server id is assigned, so a retry
	// with the same body matches even when the client left the line id out.
	hash := hashOf(req)
	fields := map[string]string{}
	if req.ID == "" {
		req.ID = ids.New()
	} else if !ids.Valid(req.ID) {
		fields["id"] = "must be a UUID when given"
	}
	line, _ := toLine(fields, "", req)
	if len(fields) > 0 {
		refuse(w, r, fields)
		return
	}
	rep, replayed, err := a.reports.AddLine(r.Context(), id, key, hash, line, a.today())
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if !replayed {
		a.linesAdded.Add(1)
	}
	writeReport(w, rep, replayed, http.StatusCreated)
}

func (a *API) deleteLine(w http.ResponseWriter, r *http.Request) {
	if !a.authorize(w, r) || !a.storeReady(w, r) {
		return
	}
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	lineID, ok := pathID(w, r, "lineId")
	if !ok {
		return
	}
	expected, ok := ifMatch(w, r)
	if !ok {
		return
	}
	rep, err := a.reports.DeleteLine(r.Context(), id, lineID, expected, a.today())
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	writeReport(w, rep, false, http.StatusOK)
}

func (a *API) submitReport(w http.ResponseWriter, r *http.Request) {
	if !a.authorize(w, r) || !a.storeReady(w, r) {
		return
	}
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	key, ok := idempotencyKey(w, r)
	if !ok {
		return
	}
	expected, ok := ifMatch(w, r)
	if !ok {
		return
	}
	hash := hashOf(map[string]any{"op": "submit", "reportId": id, "ifMatch": expected})
	rep, replayed, err := a.reports.SubmitReport(r.Context(), id, key, hash, expected, a.today())
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if !replayed {
		a.reportsSubmitted.Add(1)
	}
	writeReport(w, rep, replayed, http.StatusOK)
}

// storeReady refuses report routes when no store is attached; the previews still run.
func (a *API) storeReady(w http.ResponseWriter, r *http.Request) bool {
	if a.reports == nil {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusServiceUnavailable, "COM-0503", "report persistence is not configured"))
		return false
	}
	return true
}

// today is the line-commit and submit date: the UTC calendar day of the clock.
func (a *API) today() time.Time {
	t := a.now().UTC()
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}

func writeReport(w http.ResponseWriter, rep store.Report, replayed bool, status int) {
	w.Header().Set("ETag", strconv.Quote(strconv.Itoa(rep.Version)))
	if replayed {
		w.Header().Set("Idempotent-Replayed", "true")
		status = http.StatusOK
	}
	httpx.JSON(w, status, toReportDTO(rep))
}

func toReportDTO(rep store.Report) reportDTO {
	out := reportDTO{
		ID: rep.ID, LegalEntityID: rep.LegalEntityID, WorkerID: rep.WorkerID, Purpose: rep.Purpose,
		Currency: rep.Currency, Status: rep.Status, Total: rep.Total.StringFixed(2),
		Reimbursable: rep.Reimbursable.StringFixed(2), CompanyPaid: rep.CompanyPaid.StringFixed(2),
		Submittable: rep.Status == string(domain.ReportDraft) && len(rep.Lines) > 0,
		Version:     rep.Version, CreatedAt: rep.CreatedAt.UTC().Format(time.RFC3339), Lines: make([]lineView, 0, len(rep.Lines)),
	}
	if rep.SubmittedAt != nil {
		s := rep.SubmittedAt.UTC().Format(time.RFC3339)
		out.SubmittedAt = &s
	}
	for _, l := range rep.Lines {
		if len(domain.Blockers(l.Results)) > 0 {
			out.Submittable = false
		}
		out.Lines = append(out.Lines, lineView{
			lineDTO: lineDTO{
				ID: l.ID, Category: l.Category, TxnDate: l.TxnDate.Format(dateLayout), Amount: l.Amount.StringFixed(2),
				Merchant: l.Merchant, Attendees: l.Attendees, HasReceipt: l.HasReceipt,
				Justification: l.Justification, PaidBy: l.PaidBy,
			},
			LineNo:        l.LineNo,
			PolicyResults: nonNil(l.Results),
		})
	}
	return out
}

// idempotencyKey reads the required Idempotency-Key header (CONVENTIONS: every
// creating POST and custom method carries one).
func idempotencyKey(w http.ResponseWriter, r *http.Request) (string, bool) {
	key := strings.TrimSpace(r.Header.Get(headerIdempotency))
	if key == "" || len(key) > maxKeyLength {
		refuse(w, r, map[string]string{headerIdempotency: fmt.Sprintf("is required and at most %d characters", maxKeyLength)})
		return "", false
	}
	return key, true
}

// ifMatch reads the optional If-Match header as a report version; 0 means absent.
func ifMatch(w http.ResponseWriter, r *http.Request) (int, bool) {
	raw := strings.TrimSpace(r.Header.Get("If-Match"))
	if raw == "" {
		return 0, true
	}
	v, err := strconv.Atoi(strings.Trim(strings.TrimPrefix(raw, "W/"), `"`))
	if err != nil || v < 1 {
		refuse(w, r, map[string]string{"If-Match": `must be the report version as an entity tag, for example "3"`})
		return 0, false
	}
	return v, true
}

func pathID(w http.ResponseWriter, r *http.Request, name string) (string, bool) {
	id := chi.URLParam(r, name)
	if !ids.Valid(id) {
		refuse(w, r, map[string]string{name: "must be a UUID"})
		return "", false
	}
	return id, true
}

func requireID(fields map[string]string, name, v string) {
	if !ids.Valid(v) {
		fields[name] = "must be a UUID"
	}
}

func validCurrency(s string) bool {
	if len(s) != 3 {
		return false
	}
	for _, c := range s {
		if c < 'A' || c > 'Z' {
			return false
		}
	}
	return true
}

// hashOf is the request hash that detects an Idempotency-Key reused with another request.
func hashOf(v any) string {
	b, _ := json.Marshal(v)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
