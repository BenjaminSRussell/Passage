package api

import (
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"

	"github.com/BenjaminSRussell/passage/services/finreport/internal/domain"
	"github.com/BenjaminSRussell/passage/services/finreport/internal/store"
)

func badQuery(w http.ResponseWriter, r *http.Request, detail string) {
	httpx.Fail(w, r, httpx.NewProblem(http.StatusBadRequest, "COM-0002", detail))
}

// libraryQuery reads the library filters: definition, ledger, schedule, format
// and period (YYYY-PP).
func libraryQuery(q url.Values) (store.LibraryQuery, error) {
	f := store.LibraryQuery{Definition: q.Get("definition"), LedgerID: q.Get("ledger"), ScheduleCode: q.Get("schedule"), Format: q.Get("format")}
	if f.LedgerID != "" {
		id, ok := ids.Canonical(f.LedgerID)
		if !ok {
			return f, fmt.Errorf("ledger must be a UUID")
		}
		f.LedgerID = id
	}
	if f.Format != "" && f.Format != "pdf" {
		if _, ok := formatNames[f.Format]; !ok {
			return f, fmt.Errorf("format must be csv, html, xlsx or pdf")
		}
	}
	if v := q.Get("period"); v != "" {
		y, n, _ := strings.Cut(v, "-")
		year, _ := strconv.Atoi(y)
		no, _ := strconv.Atoi(n)
		if !periodQuery.MatchString(v) || year < 1 {
			return f, fmt.Errorf("period must be YYYY-PP, such as 2026-03")
		}
		f.PeriodYear, f.PeriodNo = year, no
	}
	return f, nil
}

// periodQuery is the period filter of the library and run lists, as the
// OpenAPI pattern states it.
var periodQuery = regexp.MustCompile(`^[0-9]{4}-[0-9]{2}$`)

var formatNames = map[string]struct{}{"csv": {}, "html": {}, "xlsx": {}}

// listLibrary is the report library (FR-15.8, PRD §2.15.6): the stored output
// files of scheduled runs, newest first, filtered by definition, entity
// (ledger), period, schedule and format and keyset paged. The files are not in
// the list.
func (a *API) listLibrary(w http.ResponseWriter, r *http.Request) {
	if !principal(r) {
		httpx.Fail(w, r, unauthenticated())
		return
	}
	q := r.URL.Query()
	f, err := libraryQuery(q)
	if err != nil {
		badQuery(w, r, err.Error())
		return
	}
	limit := store.LibraryDefaultPage
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > store.LibraryMaxPage {
			badQuery(w, r, fmt.Sprintf("limit must be 1 to %d", store.LibraryMaxPage))
			return
		}
		limit = n
	}
	var after *domain.AuditCursor
	if v := q.Get("cursor"); v != "" {
		c, err := domain.DecodeAuditCursor(v)
		if err != nil {
			badQuery(w, r, "cursor is not one this endpoint returned")
			return
		}
		after = &c
	}
	page, err := a.store.ListLibrary(r.Context(), f, after, limit)
	if err != nil {
		fail(w, r, err)
		return
	}
	var next any
	if page.Next != nil {
		next = page.Next.Encode()
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": page.Items, "limit": limit, "nextCursor": next})
}

func libraryID(w http.ResponseWriter, r *http.Request) (string, bool) {
	id := chi.URLParam(r, "id")
	if !ids.Valid(id) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, store.CodeLibraryNotFound, "report library entry "+id+" does not exist"))
		return "", false
	}
	return id, true
}

func (a *API) getLibraryEntry(w http.ResponseWriter, r *http.Request) {
	id, ok := libraryID(w, r)
	if !ok {
		return
	}
	e, err := a.store.GetLibraryEntry(r.Context(), id)
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, e)
}

// libraryContent serves the stored file, exactly the bytes that were
// distributed. The ETag and X-Passage-Content-Sha256 carry the SHA-256 listed
// on the entry, so a download can be verified.
func (a *API) libraryContent(w http.ResponseWriter, r *http.Request) {
	id, ok := libraryID(w, r)
	if !ok {
		return
	}
	e, body, err := a.store.LibraryContent(r.Context(), id)
	if err != nil {
		fail(w, r, err)
		return
	}
	w.Header().Set("Content-Type", e.ContentType)
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, safeFileName(e.FileName)))
	w.Header().Set("ETag", `"`+e.SHA256+`"`)
	w.Header().Set("X-Passage-Content-Sha256", e.SHA256)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

// safeFileName keeps a stored file name to characters that cannot break out of
// the Content-Disposition header.
func safeFileName(name string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			return r
		}
		return '_'
	}, name)
}

// listFirings is the history of schedule firings, newest first: all schedules,
// or one with ?schedule=CODE. ?latest=true keeps the newest firing of each
// schedule and ledger (the Schedules screen's "last run").
func (a *API) listFirings(w http.ResponseWriter, r *http.Request) {
	if !principal(r) {
		httpx.Fail(w, r, unauthenticated())
		return
	}
	q := r.URL.Query()
	limit := store.MaxFiringPage
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > store.MaxFiringPage {
			badQuery(w, r, fmt.Sprintf("limit must be 1 to %d", store.MaxFiringPage))
			return
		}
		limit = n
	}
	latest := false
	if v := q.Get("latest"); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			badQuery(w, r, "latest must be true or false")
			return
		}
		latest = b
	}
	items, err := a.store.ListFirings(r.Context(), q.Get("schedule"), latest, limit)
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
}
