package api

import (
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/receivables/internal/store"
)

var glAccountPattern = regexp.MustCompile(`^[0-9A-Za-z._-]{1,20}$`)

var glJournalStatuses = map[string]bool{"PENDING": true, "POSTED": true}

// glJournalRoutes mounts the ledger journal operations (30-2): the owed journals, the sweep, the retry of one and the
// reconciliation of the control accounts.
func (a *API) glJournalRoutes(r chi.Router) {
	r.With(authz.RequireDuty(dutyView)).Get("/gl-journals", a.listGLJournals)
	r.With(authz.RequireDuty(dutyPayment)).Post("/gl-journals:sweep", a.sweepGLJournals)
	r.With(authz.RequireDuty(dutyPayment)).Post("/gl-journals:backfill", a.backfillGLJournals)
	r.Post("/gl-journals/{ref}", a.glJournalAction)
	r.With(authz.RequireDuty(dutyView)).Get("/gl-reconciliation", a.glReconciliation)
}

func (a *API) listGLJournals(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	q := r.URL.Query()
	f := store.GLJournalFilter{LegalEntityID: q.Get("legalEntityId"), Status: q.Get("status"), SourceType: q.Get("sourceType")}
	if f.LegalEntityID != "" && !ids.Valid(f.LegalEntityID) {
		badQuery(w, r, "legalEntityId", "must be a UUID")
		return
	}
	if f.Status != "" && !glJournalStatuses[f.Status] {
		badQuery(w, r, "status", "must be PENDING or POSTED")
		return
	}
	switch q.Get("parked") {
	case "", "false":
	case "true":
		f.ParkedOnly = true
	default:
		badQuery(w, r, "parked", "must be true or false")
		return
	}
	limit, ok := pageLimit(w, r)
	if !ok {
		return
	}
	f.Limit = limit
	if f.LegalEntityID != "" && !scopeAllows(w, r, f.LegalEntityID) {
		return
	}
	items, err := a.store.ListGLJournals(r.Context(), f)
	if err != nil {
		a.fail("list gl journals", w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
}

// sweepGLJournals posts the caller's due journals: the scheduler's entry point, and the same store call the in-process
// sweeper runs.
func (a *API) sweepGLJournals(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) || !legalEntityScoped(w, r) {
		return
	}
	limit, ok := pageLimit(w, r)
	if !ok {
		return
	}
	res, err := a.store.PostDueJournals(r.Context(), time.Now().UTC(), limit)
	if err != nil {
		a.fail("post gl journals", w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, res)
}

// glJournalAction dispatches :retry on one journal (chi does not match ':' after a parameter).
func (a *API) glJournalAction(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id, action, found := strings.Cut(chi.URLParam(r, "ref"), ":")
	if !found || !ids.Valid(id) || action != "retry" {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, store.CodeJournalNotFound, "journal not found"))
		return
	}
	if !require(w, r, dutyPayment) || !legalEntityScoped(w, r) {
		return
	}
	var req rmaRetryRequest
	if r.ContentLength != 0 {
		if err := httpx.Decode(r, &req); err != nil {
			httpx.Fail(w, r, err)
			return
		}
	}
	if req.PostingDate != "" {
		if _, err := store.ParseDay(req.PostingDate); err != nil {
			badQuery(w, r, "postingDate", "must be YYYY-MM-DD")
			return
		}
	}
	out, err := a.store.RetryGLJournal(r.Context(), id, req.PostingDate)
	if err != nil {
		a.fail("retry gl journal", w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}

func (a *API) glReconciliation(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	q := r.URL.Query()
	le, currency, asOf := q.Get("legalEntityId"), q.Get("currency"), q.Get("asOf")
	if !ids.Valid(le) {
		badQuery(w, r, "legalEntityId", "must be a UUID")
		return
	}
	if len(currency) != 3 || strings.ToUpper(currency) != currency {
		badQuery(w, r, "currency", "must be a 3-letter ISO code")
		return
	}
	if asOf == "" {
		asOf = time.Now().UTC().Format("2006-01-02")
	} else if _, err := store.ParseDay(asOf); err != nil {
		badQuery(w, r, "asOf", "must be YYYY-MM-DD")
		return
	}
	if !scopeAllows(w, r, le) {
		return
	}
	out, err := a.store.GLReconcile(r.Context(), le, currency, asOf)
	if err != nil {
		a.fail("reconcile gl", w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}

type backfillRequest struct {
	LegalEntityID string `json:"legalEntityId"`
	// OpeningAccount and OpeningDate switch on the opening-item journals (both or neither).
	OpeningAccount string `json:"openingAccount"`
	OpeningDate    string `json:"openingDate"`
}

// backfillGLJournals queues the journals owed for payments recorded before 30-2 and, when asked, for opening-item imports.
func (a *API) backfillGLJournals(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var req backfillRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	if !ids.Valid(req.LegalEntityID) {
		fields["legalEntityId"] = "must be a UUID"
	}
	if (req.OpeningAccount == "") != (req.OpeningDate == "") {
		fields["openingAccount"] = "give the opening account and the opening date together, or neither"
	}
	if req.OpeningAccount != "" && !glAccountPattern.MatchString(req.OpeningAccount) {
		fields["openingAccount"] = "must be a ledger account number of at most 20 letters, digits, dots, dashes or underscores"
	}
	if req.OpeningDate != "" {
		if _, err := store.ParseDay(req.OpeningDate); err != nil {
			fields["openingDate"] = "must be YYYY-MM-DD"
		}
	}
	if len(fields) > 0 {
		writeFields(w, r, fields)
		return
	}
	if !scopeAllows(w, r, req.LegalEntityID) {
		return
	}
	out, err := a.store.BackfillGLJournals(r.Context(), store.BackfillRequest{LegalEntityID: req.LegalEntityID, OpeningAccount: req.OpeningAccount, OpeningDate: req.OpeningDate})
	if err != nil {
		a.fail("backfill gl journals", w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}
