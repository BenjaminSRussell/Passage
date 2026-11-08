package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/httpx"

	"github.com/BenjaminSRussell/passage/services/finreport/internal/domain"
	"github.com/BenjaminSRussell/passage/services/finreport/internal/store"
)

const libUUID = "33333333-3333-3333-3333-333333333333"

type libraryStore struct {
	Store
	query   store.LibraryQuery
	after   *domain.AuditCursor
	limit   int
	page    store.LibraryPage
	content []byte
	missing bool
	firings []store.Firing
	fSched  string
	fLimit  int
	fLatest bool
}

func (l *libraryStore) ListLibrary(_ context.Context, q store.LibraryQuery, after *domain.AuditCursor, limit int) (store.LibraryPage, error) {
	l.query, l.after, l.limit = q, after, limit
	return l.page, nil
}

func (l *libraryStore) entry() store.LibraryEntry {
	return store.LibraryEntry{ID: libUUID, RunID: runUUID, ScheduleCode: "NIGHTLY", DefinitionCode: "BS-STD", DefinitionVersion: 2, LedgerID: ledgerUUID,
		PeriodYear: 2026, PeriodNo: 3, CurrencyType: "acct", Format: "csv", FileName: "BS-STD-22222222-2026-03.csv",
		ContentType: "text/csv; charset=utf-8", SizeBytes: int64(len(l.content)), SHA256: strings.Repeat("ab", 32),
		RetainUntil: time.Date(2033, 4, 1, 0, 0, 0, 0, time.UTC), CreatedAt: time.Date(2026, 4, 1, 2, 0, 0, 0, time.UTC)}
}

func (l *libraryStore) GetLibraryEntry(context.Context, string) (store.LibraryEntry, error) {
	if l.missing {
		return store.LibraryEntry{}, httpx.NewProblem(404, store.CodeLibraryNotFound, "gone")
	}
	return l.entry(), nil
}

func (l *libraryStore) LibraryContent(context.Context, string) (store.LibraryEntry, []byte, error) {
	if l.missing {
		return store.LibraryEntry{}, nil, httpx.NewProblem(404, store.CodeLibraryNotFound, "gone")
	}
	return l.entry(), l.content, nil
}

func (l *libraryStore) ListFirings(_ context.Context, schedule string, latest bool, limit int) ([]store.Firing, error) {
	l.fSched, l.fLatest, l.fLimit = schedule, latest, limit
	return l.firings, nil
}

func TestListLibrary_PassesFiltersAndPagesByKeyset(t *testing.T) {
	st := &libraryStore{}
	e := st.entry()
	st.page = store.LibraryPage{Items: []store.LibraryEntry{e}, Next: &domain.AuditCursor{OccurredAt: e.CreatedAt, ID: e.ID}}
	w := call(scheduleRouter(st), http.MethodGet,
		"/v1/reporting/library?definition=BS-STD&ledger="+strings.ToUpper(ledgerUUID)+"&schedule=NIGHTLY&format=csv&period=2026-03&limit=10", "", nil, DutyView)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	want := store.LibraryQuery{Definition: "BS-STD", LedgerID: ledgerUUID, ScheduleCode: "NIGHTLY", Format: "csv", PeriodYear: 2026, PeriodNo: 3}
	if st.query != want || st.limit != 10 || st.after != nil {
		t.Fatalf("store got %+v limit %d after %v, want %+v", st.query, st.limit, st.after, want)
	}
	var body struct {
		Items      []store.LibraryEntry `json:"items"`
		Limit      int                  `json:"limit"`
		NextCursor string               `json:"nextCursor"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || len(body.Items) != 1 || body.Items[0].SHA256 != e.SHA256 || body.NextCursor == "" || body.Limit != 10 {
		t.Fatalf("body %s (%v)", w.Body.String(), err)
	}
	// The cursor the page returned continues the list.
	call(scheduleRouter(st), http.MethodGet, "/v1/reporting/library?cursor="+body.NextCursor, "", nil, DutyView)
	if st.after == nil || st.after.ID != e.ID || !st.after.OccurredAt.Equal(e.CreatedAt) {
		t.Fatalf("cursor not passed on: %+v", st.after)
	}
}

func TestListLibrary_EmptyListIsAnArrayAndNullCursor(t *testing.T) {
	st := &libraryStore{page: store.LibraryPage{Items: []store.LibraryEntry{}}}
	w := call(scheduleRouter(st), http.MethodGet, "/v1/reporting/library", "", nil, DutyView)
	if !strings.Contains(w.Body.String(), `"items":[]`) || !strings.Contains(w.Body.String(), `"nextCursor":null`) || st.limit != store.LibraryDefaultPage {
		t.Fatalf("body %s limit %d", w.Body.String(), st.limit)
	}
}

func TestListLibrary_RejectsBadQueries(t *testing.T) {
	for _, q := range []string{"ledger=nope", "format=docx", "period=2026-3x", "period=26-03", "limit=0", "limit=201", "limit=x", "cursor=%25%25"} {
		w := call(scheduleRouter(&libraryStore{}), http.MethodGet, "/v1/reporting/library?"+q, "", nil, DutyView)
		if w.Code != http.StatusBadRequest || problemCode(t, w) != "COM-0002" {
			t.Errorf("%s: status %d: %s", q, w.Code, w.Body.String())
		}
	}
}

func TestLibrary_RequiresTheViewDuty(t *testing.T) {
	for _, path := range []string{"/v1/reporting/library", "/v1/reporting/library/" + libUUID, "/v1/reporting/library/" + libUUID + "/content"} {
		if w := call(scheduleRouter(&libraryStore{}), http.MethodGet, path, "", nil, DutyMaintain); w.Code != http.StatusForbidden {
			t.Errorf("%s without the view duty: status %d", path, w.Code)
		}
	}
	if w := call(scheduleRouter(&libraryStore{}), http.MethodGet, "/v1/reporting/schedule-firings", "", nil, DutyView); w.Code != http.StatusForbidden {
		t.Errorf("firings need the schedule duty, got %d", w.Code)
	}
}

func TestLibraryContent_ServesTheStoredBytesWithTheirDigest(t *testing.T) {
	st := &libraryStore{content: []byte("row,CUR\nCASH,12.500000\n")}
	w := call(scheduleRouter(st), http.MethodGet, "/v1/reporting/library/"+libUUID+"/content", "", nil, DutyView)
	if w.Code != http.StatusOK || w.Body.String() != string(st.content) {
		t.Fatalf("status %d body %q", w.Code, w.Body.String())
	}
	h := w.Header()
	if h.Get("Content-Type") != "text/csv; charset=utf-8" || h.Get("ETag") != `"`+strings.Repeat("ab", 32)+`"` ||
		h.Get("X-Passage-Content-Sha256") != strings.Repeat("ab", 32) ||
		h.Get("Content-Disposition") != `attachment; filename="BS-STD-22222222-2026-03.csv"` {
		t.Fatalf("headers %v", h)
	}
}

func TestLibraryEntry_NotFoundAndMalformedIDAnswer404(t *testing.T) {
	for _, path := range []string{"/v1/reporting/library/not-a-uuid", "/v1/reporting/library/not-a-uuid/content"} {
		w := call(scheduleRouter(&libraryStore{}), http.MethodGet, path, "", nil, DutyView)
		if w.Code != http.StatusNotFound || problemCode(t, w) != store.CodeLibraryNotFound {
			t.Errorf("%s: status %d: %s", path, w.Code, w.Body.String())
		}
	}
	w := call(scheduleRouter(&libraryStore{missing: true}), http.MethodGet, "/v1/reporting/library/"+libUUID, "", nil, DutyView)
	if w.Code != http.StatusNotFound || problemCode(t, w) != store.CodeLibraryNotFound {
		t.Errorf("missing entry: status %d", w.Code)
	}
}

func TestSafeFileName_CannotBreakTheHeader(t *testing.T) {
	if got := safeFileName("a\"b\r\nc d/é.csv"); got != "a_b__c_d__.csv" {
		t.Fatalf("got %q", got)
	}
}

func TestListFirings(t *testing.T) {
	st := &libraryStore{firings: []store.Firing{{ID: libUUID, ScheduleCode: "NIGHTLY", Status: store.FiringFailed, ErrorCode: "REPORT-0004", Attempts: 2}}}
	w := call(scheduleRouter(st), http.MethodGet, "/v1/reporting/schedule-firings?schedule=NIGHTLY&limit=5", "", nil, DutySchedule)
	if w.Code != http.StatusOK || st.fSched != "NIGHTLY" || st.fLimit != 5 || !strings.Contains(w.Body.String(), `"errorCode":"REPORT-0004"`) {
		t.Fatalf("status %d sched %q limit %d body %s", w.Code, st.fSched, st.fLimit, w.Body.String())
	}
	if w := call(scheduleRouter(st), http.MethodGet, "/v1/reporting/schedule-firings?limit=999", "", nil, DutySchedule); w.Code != http.StatusBadRequest {
		t.Fatalf("limit 999: status %d", w.Code)
	}
	if st.fLatest {
		t.Fatal("latest must default to false")
	}
	if w := call(scheduleRouter(st), http.MethodGet, "/v1/reporting/schedule-firings?latest=true", "", nil, DutySchedule); w.Code != http.StatusOK || !st.fLatest {
		t.Fatalf("latest=true: status %d latest %v", w.Code, st.fLatest)
	}
	if w := call(scheduleRouter(st), http.MethodGet, "/v1/reporting/schedule-firings?latest=maybe", "", nil, DutySchedule); w.Code != http.StatusBadRequest {
		t.Fatalf("latest=maybe: status %d", w.Code)
	}
}
