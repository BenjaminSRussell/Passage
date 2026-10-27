package api

import (
	"fmt"
	"net/http"
	"strconv"
	"time"

	"log/slog"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/pricing/internal/domain"
	"github.com/BenjaminSRussell/passage/services/pricing/internal/store"
)

const (
	journalsPath    = "/v1/pricing/journals"
	dutyJournalPost = "pricing.journal.post"
	// maxJournalLinesShown caps the lines a journal read returns; errors are always complete.
	maxJournalLinesShown = 1000
)

// journalLineDTO is one operation. RESTORE is internal to reversal and is refused here.
type journalLineDTO struct {
	Op              string     `json:"op"`
	RecordID        string     `json:"recordId"`
	ExpectedVersion int        `json:"expectedVersion"`
	AdjustPct       string     `json:"adjustPct"`
	Record          *recordDTO `json:"record"`
}

type journalRequest struct {
	LegalEntityID string           `json:"legalEntityId"`
	Name          string           `json:"name"`
	SmartRound    bool             `json:"smartRound"`
	Lines         []journalLineDTO `json:"lines"`
}

// lineErrorView names one failing line of a journal.
type lineErrorView struct {
	LineNo int    `json:"lineNo"`
	Code   string `json:"code"`
	Detail string `json:"detail"`
}

type journalView struct {
	ID                  string          `json:"id"`
	LegalEntityID       string          `json:"legalEntityId"`
	Name                string          `json:"name"`
	Status              string          `json:"status"`
	SmartRound          bool            `json:"smartRound"`
	LineCount           int             `json:"lineCount"`
	ErrorCount          int             `json:"errorCount"`
	ReversesJournalID   string          `json:"reversesJournalId,omitempty"`
	ReversedByJournalID string          `json:"reversedByJournalId,omitempty"`
	PostedAt            *time.Time      `json:"postedAt,omitempty"`
	CreatedAt           time.Time       `json:"createdAt"`
	Errors              []lineErrorView `json:"errors"`
	// Lines is set on a single-journal read (at most maxJournalLinesShown; LinesTruncated says more exist).
	Lines          []journalLineView `json:"lines,omitempty"`
	LinesTruncated bool              `json:"linesTruncated,omitempty"`
}

// journalLineView is one line of a journal with its before and after snapshots.
type journalLineView struct {
	LineNo          int             `json:"lineNo"`
	Op              string          `json:"op"`
	RecordID        string          `json:"recordId"`
	ExpectedVersion int             `json:"expectedVersion"`
	AdjustPct       string          `json:"adjustPct"`
	Before          *recordView     `json:"before,omitempty"`
	After           *recordView     `json:"after,omitempty"`
	Errors          []lineErrorView `json:"errors"`
}

// toJournalLine validates one operation. Field errors are keyed lines[i].<field>.
func toJournalLine(dto journalLineDTO, idx int) (domain.JournalLine, map[string]string, *httpx.Problem) {
	fields := map[string]string{}
	field := func(name string) string { return fmt.Sprintf("lines[%d].%s", idx, name) }
	line := domain.JournalLine{No: idx + 1, RecordID: dto.RecordID, ExpectedVersion: dto.ExpectedVersion}
	adjust := decimal.Zero
	if dto.AdjustPct != "" {
		v, err := decimal.NewFromString(dto.AdjustPct)
		if err != nil || v.LessThanOrEqual(decimal.NewFromInt(-100)) || v.GreaterThanOrEqual(decimal.NewFromInt(100000)) || !v.Equal(v.Truncate(4)) {
			fields[field("adjustPct")] = "must be a percentage above -100 and below 100000 with at most 4 decimals"
		} else {
			adjust = v
		}
	}
	line.AdjustPct = adjust
	switch domain.JournalOp(dto.Op) {
	case domain.OpCreate:
		line.Op = domain.OpCreate
		line.RecordID = ""
		if dto.Record == nil {
			fields[field("record")] = "is required for CREATE"
		} else if rec, p := toRecord(*dto.Record); p != nil {
			return line, nil, prefixProblem(p, field("record"))
		} else {
			line.Record = rec
		}
		if !adjust.IsZero() {
			fields[field("adjustPct")] = "is not allowed for CREATE"
		}
	case domain.OpUpdate:
		line.Op = domain.OpUpdate
		if !ids.Valid(dto.RecordID) {
			fields[field("recordId")] = "must be a UUID"
		}
		if dto.ExpectedVersion < 1 {
			fields[field("expectedVersion")] = "must be the version read from the record"
		}
		if adjust.IsZero() {
			if dto.Record == nil {
				fields[field("record")] = "is required when adjustPct is zero"
			} else if rec, p := toRecord(*dto.Record); p != nil {
				return line, nil, prefixProblem(p, field("record"))
			} else {
				line.Record = rec
			}
		} else if dto.Record != nil {
			fields[field("record")] = "must be omitted when adjustPct is set"
		}
	case domain.OpDelete:
		line.Op = domain.OpDelete
		if !ids.Valid(dto.RecordID) {
			fields[field("recordId")] = "must be a UUID"
		}
		if dto.ExpectedVersion < 1 {
			fields[field("expectedVersion")] = "must be the version read from the record"
		}
		if dto.Record != nil || !adjust.IsZero() {
			fields[field("op")] = "DELETE takes only recordId and expectedVersion"
		}
	default:
		fields[field("op")] = "must be CREATE, UPDATE or DELETE"
	}
	return line, fields, nil
}

// prefixProblem keys a record's field errors under its journal line (lines[i].record.field)
// and names the line in the detail, so a client can point at the failing line.
func prefixProblem(p *httpx.Problem, prefix string) *httpx.Problem {
	out := httpx.NewProblem(p.Status, p.Code, prefix+": "+p.Detail)
	if p.Errors != nil {
		out.Errors = map[string]string{}
		for k, v := range p.Errors {
			out.Errors[prefix+"."+k] = v
		}
	}
	return out
}

func (a *API) createJournal(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var req journalRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	a.submitJournal(w, r, req)
}

// submitJournal validates a journal and stores it. The JSON route and the CSV import share it,
// so both reject the same faults with the same field keys.
func (a *API) submitJournal(w http.ResponseWriter, r *http.Request, req journalRequest) {
	fields := map[string]string{}
	if !ids.Valid(req.LegalEntityID) {
		fields["legalEntityId"] = "must be a UUID"
	}
	if req.Name == "" || len(req.Name) > 120 {
		fields["name"] = "must be 1 to 120 characters"
	}
	switch {
	case len(req.Lines) == 0:
		fields["lines"] = "at least one line is required"
	case len(req.Lines) > domain.MaxJournalLines:
		fields["lines"] = fmt.Sprintf("a journal holds at most %d lines", domain.MaxJournalLines)
	}
	lines := make([]domain.JournalLine, 0, len(req.Lines))
	for i, dto := range req.Lines {
		line, lf, p := toJournalLine(dto, i)
		if p != nil {
			httpx.Fail(w, r, p)
			return
		}
		for k, v := range lf {
			fields[k] = v
		}
		lines = append(lines, line)
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	j, err := a.store.CreateJournal(r.Context(), store.JournalInput{LegalEntityID: req.LegalEntityID, Name: req.Name,
		SmartRound: req.SmartRound, Lines: lines}, r.Header.Get(idempotencyKey))
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, journalResponse(j))
}

// listJournals is the Price journal log (PRD 5.26.14): headers, newest first.
func (a *API) listJournals(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	q := r.URL.Query()
	f := store.JournalFilter{LegalEntityID: q.Get("legalEntityId"), Status: q.Get("status")}
	fields := map[string]string{}
	if f.LegalEntityID != "" && !ids.Valid(f.LegalEntityID) {
		fields["legalEntityId"] = "must be a UUID"
	}
	switch f.Status {
	case "", domain.JournalOpen, domain.JournalValidated, domain.JournalPosted, domain.JournalReversed:
	default:
		fields["status"] = "must be OPEN, VALIDATED, POSTED or REVERSED"
	}
	if raw := q.Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > store.MaxListLimit {
			fields["limit"] = fmt.Sprintf("must be an integer from 1 to %d", store.MaxListLimit)
		}
		f.Limit = n
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	rows, err := a.store.ListJournals(r.Context(), f)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	items := make([]journalView, len(rows))
	for i, j := range rows {
		items[i] = journalResponse(j)
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
}

func (a *API) getJournal(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id, ok := journalID(w, r)
	if !ok {
		return
	}
	j, err := a.store.GetJournal(r.Context(), id)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	v := journalResponse(j)
	v.Lines = make([]journalLineView, 0, min(len(j.Lines), maxJournalLinesShown))
	for i, l := range j.Lines {
		if i == maxJournalLinesShown {
			v.LinesTruncated = true
			break
		}
		v.Lines = append(v.Lines, journalLineOf(j.LegalEntityID, l))
	}
	httpx.JSON(w, http.StatusOK, v)
}

func (a *API) validateJournal(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id, ok := journalID(w, r)
	if !ok {
		return
	}
	j, err := a.store.ValidateJournal(r.Context(), id)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, journalResponse(j))
}

func (a *API) postJournal(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id, ok := journalID(w, r)
	if !ok {
		return
	}
	j, err := a.store.PostJournal(r.Context(), id, r.Header.Get(idempotencyKey))
	if err != nil {
		slog.Error("post price journal failed", "err", err, "journalId", id, "correlationId", middleware.GetReqID(r.Context()))
		httpx.Fail(w, r, err)
		return
	}
	a.journalsPosted.Add(1)
	httpx.JSON(w, http.StatusOK, journalResponse(j))
}

func (a *API) reverseJournal(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id, ok := journalID(w, r)
	if !ok {
		return
	}
	j, err := a.store.ReverseJournal(r.Context(), id, r.Header.Get(idempotencyKey))
	if err != nil {
		slog.Error("reverse price journal failed", "err", err, "journalId", id, "correlationId", middleware.GetReqID(r.Context()))
		httpx.Fail(w, r, err)
		return
	}
	a.journalsPosted.Add(1)
	httpx.JSON(w, http.StatusCreated, journalResponse(j))
}

func journalLineOf(legalEntityID string, l domain.JournalLine) journalLineView {
	v := journalLineView{LineNo: l.No, Op: string(l.Op), RecordID: l.RecordID, ExpectedVersion: l.ExpectedVersion,
		AdjustPct: l.AdjustPct.String(), Errors: []lineErrorView{}}
	snap := func(rec *domain.Record) *recordView {
		if rec == nil {
			return nil
		}
		rv := viewOf(store.Entry{Record: *rec, LegalEntityID: legalEntityID})
		return &rv
	}
	v.Before, v.After = snap(l.Before), snap(l.After)
	for _, e := range l.Errors {
		v.Errors = append(v.Errors, lineErrorView{LineNo: l.No, Code: e.Code, Detail: e.Detail})
	}
	return v
}

// journalID reads the path id; anything that is not a UUID is the same 404 as an unknown journal.
func journalID(w http.ResponseWriter, r *http.Request) (string, bool) {
	id := chi.URLParam(r, "id")
	if !ids.Valid(id) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, store.CodeJournalNotFound, "no price journal "+id))
		return "", false
	}
	return id, true
}

func journalResponse(j store.Journal) journalView {
	v := journalView{ID: j.ID, LegalEntityID: j.LegalEntityID, Name: j.Name, Status: j.Status, SmartRound: j.SmartRound,
		LineCount: j.LineCount, ErrorCount: j.ErrorCount, ReversesJournalID: j.ReversesID, ReversedByJournalID: j.ReversedByID,
		PostedAt: j.PostedAt, CreatedAt: j.CreatedAt, Errors: []lineErrorView{}}
	for _, l := range j.Lines {
		for _, e := range l.Errors {
			v.Errors = append(v.Errors, lineErrorView{LineNo: l.No, Code: e.Code, Detail: e.Detail})
		}
	}
	return v
}

// registerJournalRoutes mounts the price journal endpoints (FR-26.9).
func (a *API) registerJournalRoutes(r chi.Router) {
	r.With(authz.RequireDuty(dutyJournalPost)).Post(journalsPath, a.createJournal)
	r.With(authz.RequireDuty(dutyView)).Get(journalsPath, a.listJournals)
	r.With(authz.RequireDuty(dutyView)).Get(journalsPath+"/{id}", a.getJournal)
	r.With(authz.RequireDuty(dutyJournalPost)).Post(journalsPath+"/{id}:validate", a.validateJournal)
	r.With(authz.RequireDuty(dutyJournalPost)).Post(journalsPath+"/{id}:post", a.postJournal)
	r.With(authz.RequireDuty(dutyJournalPost)).Post(journalsPath+"/{id}:reverse", a.reverseJournal)
}
