package api

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"

	"github.com/BenjaminSRussell/passage/services/finreport/internal/domain"
	"github.com/BenjaminSRussell/passage/services/finreport/internal/store"
)

// scheduleStore records saves in memory. It answers like the Postgres store:
// REPORT-0001 for a definition other than BS-STD, and REPORT-0019 for an
// unknown code.
type scheduleStore struct {
	Store
	saved   *domain.Schedule
	ifMatch *int
	items   []domain.Schedule
}

func (s *scheduleStore) SaveSchedule(_ context.Context, sc domain.Schedule, ifMatch *int) (domain.Schedule, int, error) {
	if sc.Definition != "BS-STD" {
		return sc, 0, httpx.NewProblem(http.StatusUnprocessableEntity, store.CodeNotPublished, "Definition "+sc.Definition+" has no published version.")
	}
	s.saved, s.ifMatch = &sc, ifMatch
	return sc, 3, nil
}

func (s *scheduleStore) ListSchedules(context.Context) ([]domain.Schedule, error) {
	return s.items, nil
}

func (s *scheduleStore) GetSchedule(_ context.Context, code string) (domain.Schedule, int, error) {
	for _, sc := range s.items {
		if sc.Code == code {
			return sc, 2, nil
		}
	}
	return domain.Schedule{}, 0, httpx.NewProblem(http.StatusNotFound, store.CodeScheduleNotFound, "schedule "+code+" does not exist")
}

const scheduleBody = `{"code":"NIGHTLY","name":"Nightly pack","definition":"BS-STD","cron":"0 23 * * *",
	"timezone":"UTC","ledgerIds":["0196a0c0-7b1e-7000-8000-000000000001"],"formats":["pdf","csv"],
	"recipients":["Controller@Example.com"],"enabled":true}`

func putSchedule(st Store, path, body string, headers map[string]string, duties ...string) (*scheduleDTO, int, string) {
	w := call(scheduleRouter(st), http.MethodPut, path, body, headers, duties...)
	if w.Code != http.StatusOK {
		return nil, w.Code, w.Body.String()
	}
	var out scheduleDTO
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return &out, w.Code, w.Header().Get("ETag")
}

func scheduleRouter(st Store) http.Handler {
	r := chi.NewRouter()
	New(st).Register(r)
	return r
}

func TestPutSchedule_AppliesDefaultsAndRecordsTheEditor(t *testing.T) {
	st := &scheduleStore{}
	out, status, tag := putSchedule(st, "/v1/reporting/schedules/NIGHTLY", scheduleBody, nil, DutySchedule)
	if status != http.StatusOK {
		t.Fatalf("status %d: %s", status, tag)
	}
	if tag != `"3"` {
		t.Fatalf("ETag = %q, want the stored revision \"3\"", tag)
	}
	got := st.saved
	if got.AttachmentLimitMB != domain.DefaultAttachMB || got.CurrencyType != "acct" || got.UpdatedBy != "u1" {
		t.Fatalf("saved %+v: want the 20 MB limit, currency type acct and updatedBy u1", got)
	}
	if got.Recipients[0] != "controller@example.com" {
		t.Fatalf("recipient %q should be lower-cased", got.Recipients[0])
	}
	if st.ifMatch != nil {
		t.Fatalf("a create sends no If-Match, got %d", *st.ifMatch)
	}
	if out.NextRunAt == nil || out.NextRunAt.Hour() != 23 || out.NextRunAt.Minute() != 0 {
		t.Fatalf("nextRunAt = %v, want the next 23:00", out.NextRunAt)
	}
}

func TestPutSchedule_PassesIfMatchAndIgnoresReadOnlyFields(t *testing.T) {
	st := &scheduleStore{}
	body := strings.Replace(scheduleBody, `"enabled":true`, `"enabled":true,"nextRunAt":"2026-01-01T23:00:00Z","updatedBy":"someone-else"`, 1)
	_, status, msg := putSchedule(st, "/v1/reporting/schedules/NIGHTLY", body, map[string]string{"If-Match": `"2"`}, DutySchedule)
	if status != http.StatusOK {
		t.Fatalf("status %d: %s", status, msg)
	}
	if st.ifMatch == nil || *st.ifMatch != 2 {
		t.Fatalf("If-Match passed = %v, want 2", st.ifMatch)
	}
	if st.saved.UpdatedBy != "u1" {
		t.Fatalf("updatedBy = %q, want the caller u1, not the body", st.saved.UpdatedBy)
	}
}

func TestPutSchedule_Errors(t *testing.T) {
	tests := []struct {
		name, path, body string
		duty             string
		status           int
		code             string
	}{
		{"needs the schedule duty", "/v1/reporting/schedules/NIGHTLY", scheduleBody, DutyView, http.StatusForbidden, ""},
		{"cron that never fires", "/v1/reporting/schedules/NIGHTLY", strings.Replace(scheduleBody, "0 23 * * *", "0 0 30 2 *", 1),
			DutySchedule, http.StatusUnprocessableEntity, domain.CodeScheduleInvalid},
		{"unknown time zone", "/v1/reporting/schedules/NIGHTLY", strings.Replace(scheduleBody, `"UTC"`, `"Mars/Olympus"`, 1),
			DutySchedule, http.StatusUnprocessableEntity, domain.CodeScheduleInvalid},
		{"definition not published", "/v1/reporting/schedules/NIGHTLY", strings.Replace(scheduleBody, `"BS-STD"`, `"DRAFT-ONLY"`, 1),
			DutySchedule, http.StatusUnprocessableEntity, store.CodeNotPublished},
		{"body code differs from path", "/v1/reporting/schedules/OTHER", scheduleBody, DutySchedule, http.StatusUnprocessableEntity, domain.CodeScheduleInvalid},
		{"unknown field", "/v1/reporting/schedules/NIGHTLY", strings.Replace(scheduleBody, `"enabled"`, `"enabeld"`, 1),
			DutySchedule, http.StatusBadRequest, "COM-0002"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			st := &scheduleStore{}
			w := call(scheduleRouter(st), http.MethodPut, tc.path, tc.body, nil, tc.duty)
			if w.Code != tc.status {
				t.Fatalf("status %d, want %d: %s", w.Code, tc.status, w.Body.String())
			}
			if tc.code != "" && problemCode(t, w) != tc.code {
				t.Fatalf("code %s, want %s", problemCode(t, w), tc.code)
			}
			if tc.status != http.StatusOK && tc.code != store.CodeNotPublished && st.saved != nil {
				t.Fatal("an invalid schedule must not reach the store")
			}
		})
	}
}

func TestGetSchedule_ETagAndNotFound(t *testing.T) {
	sc := domain.Schedule{Code: "NIGHTLY", Name: "Nightly", Definition: "BS-STD", Cron: "0 23 * * *", Timezone: "Europe/Berlin", Enabled: false}
	st := &scheduleStore{items: []domain.Schedule{sc}}
	w := call(scheduleRouter(st), http.MethodGet, "/v1/reporting/schedules/NIGHTLY", "", nil, DutySchedule)
	if w.Code != http.StatusOK || w.Header().Get("ETag") != `"2"` {
		t.Fatalf("status %d ETag %q", w.Code, w.Header().Get("ETag"))
	}
	if strings.Contains(w.Body.String(), "nextRunAt") {
		t.Fatalf("a paused schedule has no next run: %s", w.Body.String())
	}
	w = call(scheduleRouter(st), http.MethodGet, "/v1/reporting/schedules/MISSING", "", nil, DutySchedule)
	if w.Code != http.StatusNotFound || problemCode(t, w) != store.CodeScheduleNotFound {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
}

func TestListSchedules_NextRunInTheScheduleZone(t *testing.T) {
	defer func(f func() time.Time) { now = f }(now)
	now = func() time.Time { return time.Date(2026, 3, 31, 21, 30, 0, 0, time.UTC) }
	st := &scheduleStore{items: []domain.Schedule{
		{Code: "BERLIN", Cron: "0 23 * * *", Timezone: "Europe/Berlin", Enabled: true},
	}}
	w := call(scheduleRouter(st), http.MethodGet, "/v1/reporting/schedules", "", nil, DutySchedule)
	var body struct {
		Items []scheduleDTO `json:"items"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || len(body.Items) != 1 {
		t.Fatalf("status %d body %s", w.Code, w.Body.String())
	}
	// 21:30 UTC is 23:30 in Berlin (CEST), so the next 23:00 Berlin is tomorrow, 21:00 UTC.
	want := time.Date(2026, 4, 1, 21, 0, 0, 0, time.UTC)
	if got := body.Items[0].NextRunAt; got == nil || !got.Equal(want) {
		t.Fatalf("nextRunAt = %v, want %s", got, want)
	}
}

// outputStore serves one stored run with a header, an account row and a
// missing prior-year cell.
type outputStore struct{ Store }

func (outputStore) RunOutput(_ context.Context, id string) (store.Run, domain.Definition, []domain.Cell, error) {
	run := store.Run{ID: id, DefinitionCode: "TB-STD", DefinitionVersion: 1, LedgerID: ledgerUUID, CurrencyType: "acct",
		PeriodYear: 2026, PeriodNo: 3, ResultHash: "h1", HWMByLedger: map[string]int64{ledgerUUID: 42},
		StartedAt: time.Date(2026, 4, 1, 6, 0, 0, 0, time.UTC), RequestedBy: "u7"}
	def := domain.Definition{Code: "TB-STD", Name: "Trial balance",
		Rows:    []domain.Row{{Code: "CASH", Kind: domain.RowAccountRange, Label: "=HYPERLINK(1)", NormalBalance: "debit"}},
		Columns: []domain.Column{{Code: "CUR", Label: "Closing", PeriodExpr: "P"}, {Code: "PY", Label: "Prior year", PeriodExpr: "PY"}}}
	cells := []domain.Cell{
		{Row: "CASH", Column: "CUR", Value: decimal.RequireFromString("12.5")},
		{Row: "CASH", Column: "PY", Blank: true, Note: domain.CodePYMissing},
	}
	return run, def, cells, nil
}

func TestRunOutput_CSV(t *testing.T) {
	w := call(scheduleRouter(outputStore{}), http.MethodGet, "/v1/reporting/runs/"+runUUID+"/outputs/csv", "", nil, DutyView)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/csv") {
		t.Fatalf("Content-Type = %q", ct)
	}
	if cd := w.Header().Get("Content-Disposition"); cd != `attachment; filename="TB-STD-`+runUUID+`.csv"` {
		t.Fatalf("Content-Disposition = %q", cd)
	}
	reader := csv.NewReader(strings.NewReader(w.Body.String()))
	reader.FieldsPerRecord = -1
	records, err := reader.ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	find := func(first string) []string {
		for _, r := range records {
			if len(r) > 0 && r[0] == first {
				return r
			}
		}
		t.Fatalf("no %q line in\n%s", first, w.Body.String())
		return nil
	}
	if got := find("CASH"); strings.Join(got, "|") != "CASH|'=HYPERLINK(1)|12.500000|n/a" {
		t.Fatalf("CASH line = %q", got)
	}
	if got := find("period"); got[1] != "2026-03" {
		t.Fatalf("period line = %q", got)
	}
	if got := find("as of sequence"); got[1] != ledgerUUID || got[2] != "42" {
		t.Fatalf("as of line = %q", got)
	}
	if got := find("generated by"); got[1] != "u7" {
		t.Fatalf("generated by line = %q", got)
	}
}

func TestRunOutput_OtherFormatsAndBadIDs(t *testing.T) {
	w := call(scheduleRouter(outputStore{}), http.MethodGet, "/v1/reporting/runs/"+runUUID+"/outputs/pdf", "", nil, DutyView)
	if w.Code != http.StatusNotFound || problemCode(t, w) != "COM-0404" {
		t.Fatalf("pdf: status %d %s", w.Code, w.Body.String())
	}
	w = call(scheduleRouter(outputStore{}), http.MethodGet, "/v1/reporting/runs/not-a-uuid/outputs/csv", "", nil, DutyView)
	if w.Code != http.StatusNotFound || problemCode(t, w) != store.CodeRunNotFound {
		t.Fatalf("bad id: status %d %s", w.Code, w.Body.String())
	}
	w = call(scheduleRouter(outputStore{}), http.MethodGet, "/v1/reporting/runs/"+runUUID+"/outputs/csv", "", nil, DutySchedule)
	if w.Code != http.StatusForbidden {
		t.Fatalf("without the view duty: status %d", w.Code)
	}
}
