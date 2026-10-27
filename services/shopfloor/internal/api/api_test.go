package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/shopfloor/internal/domain"
	"github.com/BenjaminSRussell/passage/services/shopfloor/internal/store"
)

const (
	workerID = "0195f0a0-0000-7000-8000-000000000001"
	jobID    = "0195f0a0-0000-7000-8000-000000000002"
	regID    = "0195f0a0-0000-7000-8000-000000000003"
	allDuty  = "shopfloor.time.register,shopfloor.pay.calculate,shopfloor.pay.approve,shopfloor.view,shopfloor.setup.maintain"
)

// fakeStore records calls and returns canned data; no database is needed.
type fakeStore struct {
	registered []store.Registration
	saved      *store.PayCalculation
	savedResp  any
	punches    []domain.Punch
	loadStart  time.Time
	loadEnd    time.Time
	approved   []string
	punchErr   error
	replays    map[string]*store.Replay
	workers    []store.Worker
	days       []store.WorkerDay
	dayFilter  store.DayFilter
}

func (f *fakeStore) Ping(context.Context) error { return nil }

func (f *fakeStore) LookupReplay(_ context.Context, idem store.Idem) (*store.Replay, error) {
	return f.replays[idem.Key], nil
}

func (f *fakeStore) CreateWorker(_ context.Context, in store.Worker, _ store.Idem) (store.Worker, error) {
	in.ID, in.Active, in.Version = workerID, true, 1
	f.workers = append(f.workers, in)
	return in, nil
}

func (f *fakeStore) GetWorker(_ context.Context, id string) (store.Worker, error) {
	for _, w := range f.workers {
		if w.ID == id {
			return w, nil
		}
	}
	return store.Worker{}, httpx.NewProblem(http.StatusNotFound, store.CodeWorkerNotFound, "worker not found")
}

func (f *fakeStore) ListWorkers(context.Context, bool, int) ([]store.Worker, error) {
	return f.workers, nil
}

func (f *fakeStore) CreateRegistrations(_ context.Context, items []store.Registration) ([]store.RegistrationResult, error) {
	f.registered = append(f.registered, items...)
	out := make([]store.RegistrationResult, 0, len(items))
	for _, it := range items {
		out = append(out, store.RegistrationResult{ID: it.ID, Status: store.StatusAccepted})
	}
	return out, nil
}

func (f *fakeStore) ListRegistrations(context.Context, string, time.Time, time.Time, int) ([]store.RegistrationView, error) {
	return []store.RegistrationView{}, nil
}

func (f *fakeStore) LoadPunches(_ context.Context, _ string, start, end time.Time) (store.PunchInput, error) {
	f.loadStart, f.loadEnd = start, end
	if f.punchErr != nil {
		return store.PunchInput{}, f.punchErr
	}
	return store.PunchInput{Punches: f.punches, Rate: decimal.NewFromInt(20), Currency: "USD"}, nil
}

func (f *fakeStore) SavePay(_ context.Context, calc store.PayCalculation, _ store.Idem, resp any) error {
	f.saved, f.savedResp = &calc, resp
	return nil
}

func (f *fakeStore) ApprovePay(_ context.Context, workerID, from, to, _ string, _ store.Idem) (store.ApproveResult, error) {
	f.approved = append(f.approved, workerID)
	return store.ApproveResult{WorkerID: workerID, From: from, To: to,
		Days: []store.ApprovedDay{{WorkDate: "2026-10-20", Status: "approved", NewlyApproved: true, PayLines: []store.PayLine{}}}}, nil
}

func (f *fakeStore) ListWorkerDays(_ context.Context, filter store.DayFilter) ([]store.WorkerDay, error) {
	f.dayFilter = filter
	return f.days, nil
}

func newRouter(s Store) chi.Router {
	r := httpx.NewRouter()
	New(s).Register(r)
	return r
}

func call(t *testing.T, h http.Handler, method, path, body, duties string) *httptest.ResponseRecorder {
	t.Helper()
	return callWith(t, h, method, path, body, duties, map[string]string{"Idempotency-Key": "key-1"})
}

func callWith(t *testing.T, h http.Handler, method, path, body, duties string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Passage-Tenant", testkit.TenantID(1))
	req.Header.Set("X-Passage-User", "supervisor-1")
	if duties != "" {
		req.Header.Set("X-Passage-Duties", duties)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func problemCode(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var p httpx.Problem
	if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
		t.Fatalf("decode problem: %v (%s)", err, rec.Body.String())
	}
	return p.Code
}

func registrationBody(items ...string) string {
	return `{"items":[` + strings.Join(items, ",") + `]}`
}

func clock(id, kind, at string) string {
	return fmt.Sprintf(`{"id":%q,"workerId":%q,"kind":%q,"startedAt":%q}`, id, workerID, kind, at)
}

func TestRegistrations_BatchAcceptedAndIdempotencyKeysKept(t *testing.T) {
	fs := &fakeStore{}
	rec := call(t, newRouter(fs), http.MethodPost, "/api/v1/shopfloor/registrations",
		registrationBody(clock(regID, "clock-in", "2026-10-20T09:00:00Z")), allDuty)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if len(fs.registered) != 1 || fs.registered[0].ID != regID || fs.registered[0].Kind != "clock-in" {
		t.Fatalf("registered = %+v", fs.registered)
	}
	var out registrationResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Items[0].Status != store.StatusAccepted {
		t.Fatalf("status = %s", out.Items[0].Status)
	}
}

func TestRegistrations_ValidationNamesEveryField(t *testing.T) {
	bad := fmt.Sprintf(`{"id":"nope","workerId":%q,"kind":"job-start","startedAt":"2026-10-20T09:00:00Z","qtyGood":"-1"}`, workerID)
	rec := call(t, newRouter(&fakeStore{}), http.MethodPost, "/api/v1/shopfloor/registrations", registrationBody(bad), allDuty)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d", rec.Code)
	}
	var p httpx.Problem
	_ = json.Unmarshal(rec.Body.Bytes(), &p)
	for _, field := range []string{"items[0].id", "items[0].jobId", "items[0].qtyGood"} {
		if _, ok := p.Errors[field]; !ok {
			t.Errorf("missing field error %s in %v", field, p.Errors)
		}
	}
}

func TestRegistrations_BatchLimitAndEmpty(t *testing.T) {
	h := newRouter(&fakeStore{})
	if rec := call(t, h, http.MethodPost, "/api/v1/shopfloor/registrations", `{"items":[]}`, allDuty); rec.Code != http.StatusBadRequest {
		t.Fatalf("empty batch status %d", rec.Code)
	}
	items := make([]string, maxBatch+1)
	for i := range items {
		items[i] = clock(regID, "clock-in", "2026-10-20T09:00:00Z")
	}
	if rec := call(t, h, http.MethodPost, "/api/v1/shopfloor/registrations", registrationBody(items...), allDuty); rec.Code != http.StatusBadRequest {
		t.Fatalf("501-item batch status %d", rec.Code)
	}
}

func TestRegistrations_DutyRequired(t *testing.T) {
	rec := call(t, newRouter(&fakeStore{}), http.MethodPost, "/api/v1/shopfloor/registrations",
		registrationBody(clock(regID, "clock-in", "2026-10-20T09:00:00Z")), "shopfloor.pay.approve")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status %d, want 403", rec.Code)
	}
}

func TestMissingTenantIsUnauthorized(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/api/v1/shopfloor/pay:approve", strings.NewReader(`{}`))
	rec := httptest.NewRecorder()
	newRouter(&fakeStore{}).ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status %d", rec.Code)
	}
}

// AC-25.2 through the API: 09:00 to 19:30 with no break is 10.50 h, so 8.00 base and 2.50 overtime-1.5.
func TestPayCalculate_ComputesAndPersistsLines(t *testing.T) {
	fs := &fakeStore{punches: []domain.Punch{
		{ID: "in", Kind: domain.KindClockIn, At: time.Date(2026, 10, 20, 9, 0, 0, 0, time.UTC)},
		{ID: "out", Kind: domain.KindClockOut, At: time.Date(2026, 10, 20, 19, 30, 0, 0, time.UTC)},
	}}
	body := fmt.Sprintf(`{"workerId":%q,"from":"2026-10-20","to":"2026-10-20"}`, workerID)
	rec := call(t, newRouter(fs), http.MethodPost, "/api/v1/shopfloor/pay:calculate", body, allDuty)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var out payCalculateResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Days) != 1 || len(out.Days[0].PayLines) != 2 {
		t.Fatalf("days = %+v", out.Days)
	}
	if out.Days[0].PayLines[0].Type != domain.TypeBase || out.Days[0].PayLines[0].Hours.StringFixed(2) != "8.00" {
		t.Fatalf("base line = %+v", out.Days[0].PayLines[0])
	}
	if ot := out.Days[0].PayLines[1]; ot.Type != domain.TypeOvertime15 || ot.Hours.StringFixed(2) != "2.50" || ot.Amount.String() != "75" {
		t.Fatalf("overtime line = %+v", ot)
	}
	if fs.saved == nil || fs.saved.FromDate != "2026-10-20" || len(fs.saved.Days) != 1 || fs.saved.TimeZone != "UTC" {
		t.Fatalf("store saved %+v", fs.saved)
	}
}

func TestPayCalculate_FlagsAndRules(t *testing.T) {
	cases := []struct {
		name string
		body string
		code string
		fs   *fakeStore
		want int
	}{
		{"range end before start", fmt.Sprintf(`{"workerId":%q,"from":"2026-10-21","to":"2026-10-20"}`, workerID), "COM-0001", &fakeStore{}, http.StatusBadRequest},
		{"range over 28 days", fmt.Sprintf(`{"workerId":%q,"from":"2026-10-01","to":"2026-11-15"}`, workerID), "COM-0001", &fakeStore{}, http.StatusBadRequest},
		{"unknown zone", fmt.Sprintf(`{"workerId":%q,"from":"2026-10-20","to":"2026-10-20","timeZone":"Mars/Olympus"}`, workerID), "COM-0001", &fakeStore{}, http.StatusBadRequest},
		{"worker unknown", fmt.Sprintf(`{"workerId":%q,"from":"2026-10-20","to":"2026-10-20"}`, workerID), store.CodeWorkerNotFound, &fakeStore{punchErr: httpx.NewProblem(http.StatusUnprocessableEntity, store.CodeWorkerNotFound, "worker not found")}, http.StatusUnprocessableEntity},
		{"more than 24 hours", fmt.Sprintf(`{"workerId":%q,"from":"2026-10-20","to":"2026-10-20"}`, workerID), domain.CodeHoursExceed, &fakeStore{punches: []domain.Punch{
			{ID: "a", Kind: domain.KindClockIn, At: time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)},
			{ID: "b", Kind: domain.KindClockOut, At: time.Date(2026, 10, 21, 1, 0, 0, 0, time.UTC)}}}, http.StatusUnprocessableEntity},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := call(t, newRouter(c.fs), http.MethodPost, "/api/v1/shopfloor/pay:calculate", c.body, allDuty)
			if rec.Code != c.want {
				t.Fatalf("status %d want %d: %s", rec.Code, c.want, rec.Body.String())
			}
			if got := problemCode(t, rec); got != c.code {
				t.Fatalf("code %s want %s", got, c.code)
			}
		})
	}
}

func TestPayApprove_RequiresAuthenticatedUser(t *testing.T) {
	fs := &fakeStore{}
	body := fmt.Sprintf(`{"workerId":%q,"from":"2026-10-20","to":"2026-10-20"}`, workerID)
	h := newRouter(fs)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/shopfloor/pay:approve", strings.NewReader(body))
	req.Header.Set("X-Passage-Tenant", testkit.TenantID(1))
	req.Header.Set("X-Passage-Duties", allDuty)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status %d", rec.Code)
	}
	if len(fs.approved) != 0 {
		t.Fatal("approval reached the store without a user")
	}
}

func TestPayApprove_ReturnsDays(t *testing.T) {
	fs := &fakeStore{}
	body := fmt.Sprintf(`{"workerId":%q,"from":"2026-10-20","to":"2026-10-20"}`, workerID)
	rec := call(t, newRouter(fs), http.MethodPost, "/api/v1/shopfloor/pay:approve", body, allDuty)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if len(fs.approved) != 1 || fs.approved[0] != workerID {
		t.Fatalf("approved = %v", fs.approved)
	}
}

func TestReadyz_UsesStore(t *testing.T) {
	rec := call(t, newRouter(&fakeStore{}), http.MethodGet, "/readyz", "", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
}

func TestCommands_RequireIdempotencyKey(t *testing.T) {
	rangeBody := fmt.Sprintf(`{"workerId":%q,"from":"2026-10-20","to":"2026-10-20"}`, workerID)
	cases := map[string]string{
		"/api/v1/shopfloor/pay:calculate": rangeBody,
		"/api/v1/shopfloor/pay:approve":   rangeBody,
		"/api/v1/shopfloor/workers":       `{"partyId":"` + jobID + `","name":"Ana","costRate":"20","currency":"USD"}`,
	}
	for path, body := range cases {
		fs := &fakeStore{}
		rec := callWith(t, newRouter(fs), http.MethodPost, path, body, allDuty, nil)
		if rec.Code != http.StatusBadRequest || problemCode(t, rec) != "COM-0001" {
			t.Fatalf("%s without key: status %d %s", path, rec.Code, rec.Body.String())
		}
		if fs.saved != nil || len(fs.approved) != 0 || len(fs.workers) != 0 {
			t.Fatalf("%s reached the store without a key", path)
		}
	}
}

// A repeated Idempotency-Key returns the stored outcome and does not recompute.
func TestPayCalculate_ReplayReturnsStoredOutcome(t *testing.T) {
	fs := &fakeStore{replays: map[string]*store.Replay{"key-1": {Status: http.StatusOK, Body: []byte(`{"workerId":"stored"}`)}}}
	body := fmt.Sprintf(`{"workerId":%q,"from":"2026-10-20","to":"2026-10-20"}`, workerID)
	rec := call(t, newRouter(fs), http.MethodPost, "/api/v1/shopfloor/pay:calculate", body, allDuty)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"stored"`) || rec.Header().Get("Idempotent-Replayed") != "true" {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	if fs.saved != nil || !fs.loadStart.IsZero() {
		t.Fatal("a replay recomputed the pay")
	}
}

// BR-25.8: a calculation from Wednesday reads the pay week from Monday (and the
// day before, for a clock-out pairing) to the day after `to`, and saves only
// the requested days.
func TestPayCalculate_ReadsWholePayWeekButSavesRange(t *testing.T) {
	at := func(d, h int) time.Time { return time.Date(2026, 10, 19+d, h, 0, 0, 0, time.UTC) }
	var punches []domain.Punch
	for d := 0; d < 3; d++ { // Monday to Wednesday, 15 h each
		punches = append(punches,
			domain.Punch{ID: fmt.Sprintf("in-%d", d), Kind: domain.KindClockIn, At: at(d, 6)},
			domain.Punch{ID: fmt.Sprintf("out-%d", d), Kind: domain.KindClockOut, At: at(d, 21)})
	}
	fs := &fakeStore{punches: punches}
	body := fmt.Sprintf(`{"workerId":%q,"from":"2026-10-21","to":"2026-10-21"}`, workerID)
	rec := call(t, newRouter(fs), http.MethodPost, "/api/v1/shopfloor/pay:calculate", body, allDuty)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if want := time.Date(2026, 10, 18, 0, 0, 0, 0, time.UTC); !fs.loadStart.Equal(want) {
		t.Fatalf("load start %s, want %s", fs.loadStart, want)
	}
	if want := time.Date(2026, 10, 23, 0, 0, 0, 0, time.UTC); !fs.loadEnd.Equal(want) {
		t.Fatalf("load end %s, want %s", fs.loadEnd, want)
	}
	if len(fs.saved.Days) != 1 || fs.saved.Days[0].Date.Day() != 21 {
		t.Fatalf("saved days %+v", fs.saved.Days)
	}
	// Wednesday: 8 h base (16 h of the 40 used Mon-Tue) and 7 h daily overtime.
	lines := fs.saved.Days[0].Lines
	if len(lines) != 2 || lines[0].Hours.StringFixed(2) != "8.00" || lines[1].Hours.StringFixed(2) != "7.00" {
		t.Fatalf("wednesday lines %+v", lines)
	}
}

func TestPayApprove_ResponseIsTheApproveResult(t *testing.T) {
	fs := &fakeStore{}
	body := fmt.Sprintf(`{"workerId":%q,"from":"2026-10-20","to":"2026-10-20"}`, workerID)
	rec := call(t, newRouter(fs), http.MethodPost, "/api/v1/shopfloor/pay:approve", body, allDuty)
	var out store.ApproveResult
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.WorkerID != workerID || out.From != "2026-10-20" || len(out.Days) != 1 || !out.Days[0].NewlyApproved {
		t.Fatalf("approve response %+v", out)
	}
}

func TestRegistrations_ScrapNeedsReasonAndDuplicateIDsRefused(t *testing.T) {
	scrap := fmt.Sprintf(`{"id":%q,"workerId":%q,"kind":"job-stop","jobId":%q,"startedAt":"2026-10-20T09:00:00Z","stoppedAt":"2026-10-20T10:00:00Z","qtyScrap":"2"}`, regID, workerID, jobID)
	again := clock(regID, "clock-in", "2026-10-20T09:00:00Z")
	rec := call(t, newRouter(&fakeStore{}), http.MethodPost, "/api/v1/shopfloor/registrations", registrationBody(scrap, again), allDuty)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d", rec.Code)
	}
	var p httpx.Problem
	_ = json.Unmarshal(rec.Body.Bytes(), &p)
	for _, field := range []string{"items[0].scrapReasonId", "items[1].id"} {
		if _, ok := p.Errors[field]; !ok {
			t.Errorf("missing field error %s in %v", field, p.Errors)
		}
	}
}

func TestRegistrations_ClosedJobCarriesElapsedHours(t *testing.T) {
	fs := &fakeStore{}
	item := fmt.Sprintf(`{"id":%q,"workerId":%q,"kind":"job-stop","jobId":%q,"startedAt":"2026-10-20T09:00:00Z","stoppedAt":"2026-10-20T10:30:00Z","qtyGood":"12"}`, regID, workerID, jobID)
	rec := call(t, newRouter(fs), http.MethodPost, "/api/v1/shopfloor/registrations", registrationBody(item), allDuty)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if h := fs.registered[0].Hours; h == nil || h.StringFixed(4) != "1.5000" {
		t.Fatalf("hours = %v, want 1.5000", h)
	}
}

func TestRegistrations_LongerThan24HoursIsRefused(t *testing.T) {
	item := fmt.Sprintf(`{"id":%q,"workerId":%q,"kind":"indirect","startedAt":"2026-10-20T09:00:00Z","stoppedAt":"2026-10-21T09:00:01Z"}`, regID, workerID)
	rec := call(t, newRouter(&fakeStore{}), http.MethodPost, "/api/v1/shopfloor/registrations", registrationBody(item), allDuty)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d", rec.Code)
	}
}

func TestWorkers_CreateValidatesAndReturns201(t *testing.T) {
	h := newRouter(&fakeStore{})
	bad := call(t, h, http.MethodPost, "/api/v1/shopfloor/workers", `{"partyId":"x","name":" ","costRate":"-1","currency":"usd"}`, allDuty)
	if bad.Code != http.StatusBadRequest {
		t.Fatalf("invalid worker status %d", bad.Code)
	}
	var p httpx.Problem
	_ = json.Unmarshal(bad.Body.Bytes(), &p)
	for _, f := range []string{"partyId", "name", "costRate", "currency"} {
		if _, ok := p.Errors[f]; !ok {
			t.Errorf("missing field error %s in %v", f, p.Errors)
		}
	}
	fs := &fakeStore{}
	rec := call(t, newRouter(fs), http.MethodPost, "/api/v1/shopfloor/workers", `{"partyId":"`+jobID+`","name":" Ana Silva ","badgeId":"B-17","costRate":"21.50","currency":"USD"}`, allDuty)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if len(fs.workers) != 1 || fs.workers[0].Name != "Ana Silva" || fs.workers[0].CostRate.String() != "21.5" {
		t.Fatalf("stored %+v", fs.workers)
	}
}

func TestWorkers_DutiesAndNotFound(t *testing.T) {
	h := newRouter(&fakeStore{})
	if rec := call(t, h, http.MethodPost, "/api/v1/shopfloor/workers", `{}`, "shopfloor.view"); rec.Code != http.StatusForbidden {
		t.Fatalf("create without setup duty: %d", rec.Code)
	}
	if rec := call(t, h, http.MethodGet, "/api/v1/shopfloor/workers", "", "shopfloor.time.register"); rec.Code != http.StatusForbidden {
		t.Fatalf("list without view duty: %d", rec.Code)
	}
	rec := call(t, h, http.MethodGet, "/api/v1/shopfloor/workers/not-a-uuid", "", allDuty)
	if rec.Code != http.StatusNotFound || problemCode(t, rec) != store.CodeWorkerNotFound {
		t.Fatalf("bad id: %d %s", rec.Code, rec.Body.String())
	}
}

func TestWorkerDays_FiltersAndValidation(t *testing.T) {
	fs := &fakeStore{days: []store.WorkerDay{}}
	h := newRouter(fs)
	rec := call(t, h, http.MethodGet, "/api/v1/shopfloor/worker-days?from=2026-10-19&to=2026-10-25&status=calculated", "", allDuty)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"items":[]`) {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	if fs.dayFilter.FromDate != "2026-10-19" || fs.dayFilter.ToDate != "2026-10-25" || fs.dayFilter.Status != "calculated" || fs.dayFilter.WorkerID != "" {
		t.Fatalf("filter %+v", fs.dayFilter)
	}
	for _, q := range []string{"from=2026-10-19&to=2026-12-25", "from=2026-10-19&to=2026-10-20&status=exported", "from=x&to=2026-10-20", "from=2026-10-19&to=2026-10-20&workerId=nope"} {
		if rec := call(t, h, http.MethodGet, "/api/v1/shopfloor/worker-days?"+q, "", allDuty); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status %d", q, rec.Code)
		}
	}
}

func TestRegistrationsList_NeedsWorker(t *testing.T) {
	h := newRouter(&fakeStore{})
	if rec := call(t, h, http.MethodGet, "/api/v1/shopfloor/registrations?from=2026-10-19&to=2026-10-20", "", allDuty); rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d", rec.Code)
	}
	rec := call(t, h, http.MethodGet, "/api/v1/shopfloor/registrations?workerId="+workerID+"&from=2026-10-19&to=2026-10-20", "", allDuty)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
}
