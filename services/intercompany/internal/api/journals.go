package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/money"
	"github.com/BenjaminSRussell/passage/services/intercompany/internal/domain"
	"github.com/BenjaminSRussell/passage/services/intercompany/internal/posting"
	"github.com/BenjaminSRussell/passage/services/intercompany/internal/store"
)

// JournalPoster posts a derived journal to the ledgers (internal/posting).
type JournalPoster interface {
	Post(ctx context.Context, in posting.Input) (domain.JournalRecord, bool, error)
}

// JournalStore reads the posted journals.
type JournalStore interface {
	GetJournal(ctx context.Context, id string) (domain.JournalRecord, error)
	ListJournals(ctx context.Context, f store.JournalFilter, after string, limit int) (store.JournalPage, error)
}

// ChargeStore persists IC charge requests (FR-40.13).
type ChargeStore interface {
	BeginCharge(ctx context.Context, c domain.ChargeRecord, hash string) (domain.ChargeRecord, bool, error)
	FinishCharge(ctx context.Context, id string, status domain.ChargeStatus, method string, p domain.ChargePricing, journalID, failureCode string) (domain.ChargeRecord, error)
	GetCharge(ctx context.Context, id string) (domain.ChargeRecord, error)
	ListCharges(ctx context.Context, f store.ChargeFilter, after string, limit int) (store.ChargePage, error)
}

// WithPosting gives the API the poster, the journal reader and the charge store.
// Without them the posting routes answer 503 COM-0503.
func (a *API) WithPosting(p JournalPoster, j JournalStore, c ChargeStore) *API {
	a.poster, a.journals, a.charges = p, j, c
	return a
}

const (
	defaultPostingPage = 50
	maxPostingPage     = 200
)

type entityTarget struct {
	Entity        string `json:"entity"`
	LegalEntityID string `json:"legalEntityId"`
	LedgerID      string `json:"ledgerId"`
}

type postJournalRequest struct {
	previewRequest
	PostingDate string         `json:"postingDate"`
	Entities    []entityTarget `json:"entities"`
}

type postedLegBody struct {
	Account string `json:"account"`
	Side    string `json:"side"`
	Amount  string `json:"amount"`
}

type postedEntryBody struct {
	Entity          string          `json:"entity"`
	LegalEntityID   string          `json:"legalEntityId"`
	LedgerID        string          `json:"ledgerId"`
	Currency        string          `json:"currency"`
	Legs            []postedLegBody `json:"legs"`
	LedgerEntryID   string          `json:"ledgerEntryId,omitempty"`
	ReversalEntryID string          `json:"reversalEntryId,omitempty"`
}

type journalResponse struct {
	ID            string            `json:"id"`
	PairID        string            `json:"pairId"`
	Source        string            `json:"source"`
	SourceRef     string            `json:"sourceRef"`
	PostingDate   string            `json:"postingDate"`
	Status        string            `json:"status"`
	FailureCode   string            `json:"failureCode,omitempty"`
	FailureDetail string            `json:"failureDetail,omitempty"`
	Entries       []postedEntryBody `json:"entries"`
	CreatedBy     string            `json:"createdBy"`
	CreatedAt     time.Time         `json:"createdAt"`
}

type journalListResponse struct {
	Items      []journalResponse `json:"items"`
	NextCursor string            `json:"nextCursor,omitempty"`
}

func toJournalResponse(r domain.JournalRecord) journalResponse {
	out := journalResponse{ID: r.ID, PairID: r.PairID, Source: r.Source, SourceRef: r.SourceRef, PostingDate: r.PostingDate.Format(dateLayout),
		Status: string(r.Status), FailureCode: r.FailureCode, FailureDetail: r.FailureDetail, Entries: make([]postedEntryBody, 0, len(r.Entries)),
		CreatedBy: r.CreatedBy, CreatedAt: r.CreatedAt.UTC()}
	for _, e := range r.Entries {
		eb := postedEntryBody{Entity: e.Entity, LegalEntityID: e.Target.LegalEntityID, LedgerID: e.Target.LedgerID, Currency: e.Currency,
			LedgerEntryID: e.LedgerEntryID, ReversalEntryID: e.ReversalEntryID, Legs: make([]postedLegBody, 0, len(e.Legs))}
		for _, l := range e.Legs {
			eb.Legs = append(eb.Legs, postedLegBody{Account: l.Account, Side: string(l.Side), Amount: l.Amount.Value.StringFixed(money.MinorUnits(e.Currency))})
		}
		out.Entries = append(out.Entries, eb)
	}
	return out
}

func (a *API) postingReady(w http.ResponseWriter, r *http.Request) bool {
	if !guard(w, r) {
		return false
	}
	if a.poster == nil || a.journals == nil || a.charges == nil {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusServiceUnavailable, "COM-0503", "posting is not configured: the ledger is not set for this service"))
		return false
	}
	return true
}

func idempotencyKey(w http.ResponseWriter, r *http.Request) (string, bool) {
	key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if key == "" || len(key) > maxIdempotencyKey {
		validation(w, r, map[string]string{"Idempotency-Key": "header is required, at most 255 characters"})
		return "", false
	}
	return key, true
}

// parseTargets validates the entity-to-ledger map that goes with a posting.
func parseTargets(rows []entityTarget, f fieldParser) map[string]domain.Target {
	if len(rows) == 0 || len(rows) > MaxPlanningMembers {
		f["entities"] = fmt.Sprintf("must list between 1 and %d entities with their legal entity and ledger", MaxPlanningMembers)
		return nil
	}
	out := make(map[string]domain.Target, len(rows))
	for i, row := range rows {
		at := func(name string) string { return fmt.Sprintf("entities[%d].%s", i, name) }
		entity := f.text(at("entity"), row.Entity)
		if !ids.Valid(row.LegalEntityID) {
			f[at("legalEntityId")] = "must be a legal entity id"
		}
		ledger := f.text(at("ledgerId"), row.LedgerID)
		if _, dup := out[entity]; dup {
			f[at("entity")] = "is listed twice"
		}
		out[entity] = domain.Target{LegalEntityID: row.LegalEntityID, LedgerID: ledger}
	}
	return out
}

// postProblem turns the poster's outcome into a response. A journal whose
// second side was refused answers 422 IC-0845 and the ledger being unreachable
// 503 IC-0846; both leave the stored journal readable by its id in the detail.
func postProblem(rec domain.JournalRecord, err error) error {
	var de *domain.Error
	if !errors.As(err, &de) {
		return fmt.Errorf("intercompany: %w", err)
	}
	status := http.StatusUnprocessableEntity
	switch de.Code {
	case domain.CodeInvalid:
		status = http.StatusBadRequest
	case domain.CodeLedgerUnavailable:
		status = http.StatusServiceUnavailable
	}
	detail := de.Detail
	if rec.ID != "" {
		detail += " (journal " + rec.ID + ")"
	}
	return httpx.NewProblem(status, de.Code, detail)
}

// postJournal derives the IC journal of one line and posts both sides to their
// ledgers, atomically as far as the ledgers allow: when a later side is
// refused the earlier ones are reversed (FR-40.5).
func (a *API) postJournal(w http.ResponseWriter, r *http.Request) {
	if !a.postingReady(w, r) {
		return
	}
	key, ok := idempotencyKey(w, r)
	if !ok {
		return
	}
	var req postJournalRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	line, matrix, counter, rate, fields := parsePreview(req.previewRequest)
	f := fieldParser(fields)
	if f == nil {
		f = fieldParser{}
	}
	date := f.date("postingDate", req.PostingDate)
	targets := parseTargets(req.Entities, f)
	if !validation(w, r, f) {
		return
	}
	pairID := ids.New()
	j, err := domain.DeriveJournal(matrix, pairID, line, req.OffsetEntity, req.OffsetAccount, counter, rate)
	if err != nil {
		httpx.Fail(w, r, problemFrom(err))
		return
	}
	hash, err := store.HashOf("journal", req)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	rec, replayed, err := a.poster.Post(r.Context(), posting.Input{Source: domain.SourceJournal, SourceRef: pairID, Key: key, Hash: hash, Journal: j, Targets: targets, PostingDate: date})
	if err != nil {
		httpx.Fail(w, r, postProblem(rec, err))
		return
	}
	a.respondPosted(w, r, rec, replayed)
}

func (a *API) respondPosted(w http.ResponseWriter, r *http.Request, rec domain.JournalRecord, replayed bool) {
	if rec.Status == domain.PostingCompensated {
		httpx.Fail(w, r, postProblem(rec, &domain.Error{Code: domain.CodeSecondSideFailed, Detail: rec.FailureDetail + "; the entries already posted were reversed"}))
		return
	}
	if replayed {
		w.Header().Set("Idempotent-Replayed", "true")
	} else {
		a.journalsPosted.Add(1)
	}
	w.Header().Set("Location", "/v1/interco/journals/"+rec.ID)
	httpx.JSON(w, http.StatusCreated, toJournalResponse(rec))
}

func pageParams(w http.ResponseWriter, r *http.Request, fields map[string]string) (after string, limit int) {
	q := r.URL.Query()
	after = q.Get("after")
	if after != "" && !ids.Valid(after) {
		fields["after"] = "must be the nextCursor of the previous page"
	}
	limit = defaultPostingPage
	if raw := q.Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > maxPostingPage {
			fields["limit"] = fmt.Sprintf("must be a whole number from 1 to %d", maxPostingPage)
		}
		limit = n
	}
	return after, limit
}

func (a *API) listJournals(w http.ResponseWriter, r *http.Request) {
	if !a.postingReady(w, r) {
		return
	}
	q := r.URL.Query()
	fields := map[string]string{}
	filter := store.JournalFilter{Status: q.Get("status"), Source: q.Get("source")}
	switch filter.Status {
	case "", "pending", "posted", "compensated":
	default:
		fields["status"] = "must be pending, posted or compensated"
	}
	switch filter.Source {
	case "", domain.SourceJournal, domain.SourceCharge:
	default:
		fields["source"] = "must be journal or charge"
	}
	after, limit := pageParams(w, r, fields)
	if !validation(w, r, fields) {
		return
	}
	page, err := a.journals.ListJournals(r.Context(), filter, after, limit)
	if err != nil {
		httpx.Fail(w, r, problemFrom(err))
		return
	}
	out := journalListResponse{Items: make([]journalResponse, 0, len(page.Items)), NextCursor: page.Next}
	for _, rec := range page.Items {
		out.Items = append(out.Items, toJournalResponse(rec))
	}
	httpx.JSON(w, http.StatusOK, out)
}

func (a *API) getJournal(w http.ResponseWriter, r *http.Request) {
	if !a.postingReady(w, r) {
		return
	}
	id := chi.URLParam(r, "id")
	if !ids.Valid(id) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, domain.CodePostingNotFound, "no intercompany journal with id "+id))
		return
	}
	rec, err := a.journals.GetJournal(r.Context(), id)
	if err != nil {
		httpx.Fail(w, r, problemFrom(err))
		return
	}
	httpx.JSON(w, http.StatusOK, toJournalResponse(rec))
}
