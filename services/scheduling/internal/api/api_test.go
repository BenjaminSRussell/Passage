package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/scheduling/internal/domain"
	"github.com/BenjaminSRussell/passage/services/scheduling/internal/store"
)

const (
	tenantID = "00000000-0000-0000-0000-000000000001"
	jobID    = "00000000-0000-0000-0000-0000000000a1"
	resID    = "00000000-0000-0000-0000-0000000000b1"
)

var (
	allDuties = "scheduling.read,scheduling.run,scheduling.schedule.write,scheduling.capacity.write,scheduling.setup.write,scheduling.campaign.write"
	at0       = time.Date(2026, 1, 5, 8, 0, 0, 0, time.UTC)
)

// fakeStore answers the Store interface from canned values and records the
// last move and firm requests so tests can assert what reached the store.
type fakeStore struct {
	conflict *domain.Conflict
	replay   bool
	err      error
	lastMove store.MoveRequest
	lastIdem store.Idem
	lastLoad store.LoadFilter
	lastOps  store.OperationFilter
	lastOp   store.Operation

	// slice 2
	campaign     store.Campaign
	lastCmd      store.CampaignCommand
	lastCampaign store.Campaign
	lastNetwork  store.Network
	lastMatrix   store.SetupMatrix
	lastRule     store.CleanoutRule
	lastBlock    store.CapacityBlock
	lastFilter   store.CampaignFilter

	// slice 4
	lastPeg store.Peg

	// slice 5
	lastUseID              string
	lastUseFrom, lastUseTo time.Time
}

func (f *fakeStore) CreateResource(_ context.Context, in store.Resource, _ store.Idem) (store.Resource, bool, error) {
	if f.err != nil {
		return store.Resource{}, false, f.err
	}
	in.ID = ids.New()
	return in, !f.replay, nil
}

func (f *fakeStore) ListResources(context.Context) ([]store.Resource, error) {
	return []store.Resource{{ID: resID, Code: "MIX-1", Name: "Mixer", Finite: true, TimeZone: "UTC"}}, f.err
}

func (f *fakeStore) GetResource(_ context.Context, id string) (store.Resource, error) {
	return store.Resource{ID: id, Code: "MIX-1"}, f.err
}

func (f *fakeStore) ResourceCapacity(_ context.Context, _ string, from, _ time.Time) ([]domain.Interval, error) {
	return []domain.Interval{{Start: from, End: from.Add(8 * time.Hour)}}, f.err
}

func (f *fakeStore) CreateOperation(_ context.Context, in store.Operation, _ store.Idem) (store.Operation, bool, error) {
	if f.err != nil {
		return store.Operation{}, false, f.err
	}
	f.lastOp = in
	in.ID, in.Status = ids.New(), "open"
	return in, true, nil
}

func (f *fakeStore) ListOperations(_ context.Context, filter store.OperationFilter) ([]store.Operation, error) {
	f.lastOps = filter
	return []store.Operation{}, f.err
}

func (f *fakeStore) RunSchedule(_ context.Context, req store.RunRequest, _ store.Idem) (store.Run, bool, error) {
	if f.err != nil {
		return store.Run{}, false, f.err
	}
	return store.Run{ID: ids.New(), Scope: req.Scope, Mode: req.Mode, Finite: req.Finite, Status: "completed", Messages: []string{}}, true, nil
}

func (f *fakeStore) GetRun(_ context.Context, id string) (store.Run, error) {
	return store.Run{ID: id, Status: "completed"}, f.err
}

func (f *fakeStore) ListRuns(context.Context, int) ([]store.Run, error) {
	return []store.Run{}, f.err
}

func (f *fakeStore) Gantt(context.Context, time.Time, time.Time, []string) ([]store.Bar, error) {
	return []store.Bar{}, f.err
}

func (f *fakeStore) MoveJob(_ context.Context, req store.MoveRequest, idem store.Idem) (store.MoveOutcome, bool, error) {
	f.lastMove, f.lastIdem = req, idem
	if f.err != nil {
		return store.MoveOutcome{}, false, f.err
	}
	if f.conflict != nil {
		return store.MoveOutcome{Conflict: f.conflict}, false, nil
	}
	return store.MoveOutcome{OrderID: ids.New(), Changes: []store.Change{{OperationID: req.JobID, ResourceID: req.ResourceID,
		NewStart: req.Start, NewEnd: req.Start.Add(time.Hour)}}}, f.replay, nil
}

func (f *fakeStore) FirmJob(_ context.Context, id string, firmed bool, idem store.Idem) (store.Firm, bool, error) {
	f.lastIdem = idem
	if f.err != nil {
		return store.Firm{}, false, f.err
	}
	return store.Firm{JobID: id, OrderID: ids.New(), ResourceID: resID, Start: at0, End: at0.Add(time.Hour), Firmed: firmed}, f.replay, nil
}

func (f *fakeStore) CapacityLoad(_ context.Context, f2 store.LoadFilter) ([]domain.WeekLoad, error) {
	f.lastLoad = f2
	return []domain.WeekLoad{{ResourceID: resID, WeekStart: f2.From, OpenMinutes: 100, BookedMinutes: 95, LoadPct: 95}}, f.err
}

func (f *fakeStore) Ping(context.Context) error { return nil }

func newRouter(s store.Store) http.Handler {
	r := httpx.NewRouter()
	New(s, DefaultConfig).Register(r)
	return r
}

func do(h http.Handler, method, path, body, duties, key string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("X-Passage-Tenant", tenantID)
	req.Header.Set("X-Passage-Duties", duties)
	req.Header.Set("Content-Type", "application/json")
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestCreateResource_RequiresIdempotencyKeyAndValidTimeZone(t *testing.T) {
	h := newRouter(&fakeStore{})
	body := `{"code":"V1","name":"Vessel 1","timeZone":"Europe/Berlin","shifts":[{"weekday":1,"startMinute":480,"endMinute":960}]}`
	if rec := do(h, http.MethodPost, "/v1/scheduling/resources", body, allDuties, ""); rec.Code != http.StatusBadRequest {
		t.Fatalf("no key: status %d", rec.Code)
	}
	bad := strings.Replace(body, "Europe/Berlin", "Mars/Olympus", 1)
	rec := do(h, http.MethodPost, "/v1/scheduling/resources", bad, allDuties, "k1")
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), `"timeZone"`) {
		t.Fatalf("bad tz: status %d body %s", rec.Code, rec.Body)
	}
}

func TestCreateResource_CreatedThenReplayed(t *testing.T) {
	body := `{"code":"V1","name":"Vessel 1","timeZone":"Europe/Berlin","shifts":[{"weekday":1,"startMinute":480,"endMinute":960}]}`
	if rec := do(newRouter(&fakeStore{}), http.MethodPost, "/v1/scheduling/resources", body, allDuties, "k1"); rec.Code != http.StatusCreated {
		t.Fatalf("create status %d", rec.Code)
	}
	rec := do(newRouter(&fakeStore{replay: true}), http.MethodPost, "/v1/scheduling/resources", body, allDuties, "k1")
	if rec.Code != http.StatusOK || rec.Header().Get("Idempotent-Replayed") != "true" {
		t.Fatalf("replay status %d headers %v", rec.Code, rec.Header())
	}
}

func TestDutiesGateWrites(t *testing.T) {
	h := newRouter(&fakeStore{})
	rec := do(h, http.MethodPost, "/v1/scheduling/runs", `{"scope":"site"}`, "scheduling.read", "k2")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status %d, want 403 without scheduling.run", rec.Code)
	}
}

func TestCreateRun_ValidatesScopeAndSelection(t *testing.T) {
	h := newRouter(&fakeStore{})
	cases := []struct{ name, body string }{
		{"unknown scope", `{"scope":"plant"}`},
		{"selection without operations", `{"scope":"selection","operationIds":[]}`},
	}
	for _, c := range cases {
		if rec := do(h, http.MethodPost, "/v1/scheduling/runs", c.body, allDuties, "k3"); rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: status %d", c.name, rec.Code)
		}
	}
	rec := do(h, http.MethodPost, "/v1/scheduling/runs", `{"scope":"site"}`, allDuties, "k4")
	if rec.Code != http.StatusCreated {
		t.Fatalf("site run status %d body %s", rec.Code, rec.Body)
	}
}

func TestMove_DryRunReportsViolationWithoutFailing(t *testing.T) {
	h := newRouter(&fakeStore{conflict: &domain.Conflict{Code: domain.CodeOverlap, Detail: "overlaps"}})
	body := `{"resourceId":"` + resID + `","start":"2026-01-05T09:30:00Z","dryRun":true}`
	rec := do(h, http.MethodPost, "/v1/scheduling/jobs/"+jobID+":move", body, allDuties, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("dry run status %d body %s", rec.Code, rec.Body)
	}
	var out moveResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Feasible || len(out.Violations) != 1 || out.Violations[0].Code != domain.CodeOverlap {
		t.Fatalf("response = %+v", out)
	}
}

func TestMove_CommitWithConflictIsUnprocessable(t *testing.T) {
	h := newRouter(&fakeStore{conflict: &domain.Conflict{Code: domain.CodeOutsideCapacity, Detail: "outside"}})
	body := `{"resourceId":"` + resID + `","start":"2026-01-05T18:00:00Z"}`
	rec := do(h, http.MethodPost, "/v1/scheduling/jobs/"+jobID+":move", body, allDuties, "m1")
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "SCHED-0002") {
		t.Fatalf("status %d body %s", rec.Code, rec.Body)
	}
}

func TestMove_CommitReturnsChanges(t *testing.T) {
	h := newRouter(&fakeStore{})
	body := `{"resourceId":"` + resID + `","start":"` + at0.Format(time.RFC3339) + `"}`
	rec := do(h, http.MethodPost, "/v1/scheduling/jobs/"+jobID+":move", body, allDuties, "m2")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"feasible":true`) || !strings.Contains(rec.Body.String(), `"changes":[{`) {
		t.Fatalf("status %d body %s", rec.Code, rec.Body)
	}
}

func TestGantt_RequiresOrderedWindow(t *testing.T) {
	h := newRouter(&fakeStore{})
	rec := do(h, http.MethodGet, "/v1/scheduling/gantt?from=2026-01-06T00:00:00Z&to=2026-01-05T00:00:00Z", "", allDuties, "")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d", rec.Code)
	}
	rec = do(h, http.MethodGet, "/v1/scheduling/gantt?from=2026-01-05T00:00:00Z&to=2026-01-06T00:00:00Z", "", allDuties, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d body %s", rec.Code, rec.Body)
	}
}

func TestFirm_SetsFlagAndRequiresKey(t *testing.T) {
	h := newRouter(&fakeStore{})
	if rec := do(h, http.MethodPost, "/v1/scheduling/jobs/"+jobID+":firm", `{"firmed":true}`, allDuties, ""); rec.Code != http.StatusBadRequest {
		t.Fatalf("no key: status %d", rec.Code)
	}
	rec := do(h, http.MethodPost, "/v1/scheduling/jobs/"+jobID+":firm", `{"firmed":true}`, allDuties, "k5")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"firmed":true`) {
		t.Fatalf("status %d body %s", rec.Code, rec.Body)
	}
	if rec := do(h, http.MethodPost, "/v1/scheduling/jobs/"+jobID+":firm", `{"firmed":true}`, "scheduling.read", "k6"); rec.Code != http.StatusForbidden {
		t.Fatalf("read-only duty status %d, want 403", rec.Code)
	}
}

func TestCapacityLoad_ReturnsWeeks(t *testing.T) {
	h := newRouter(&fakeStore{})
	rec := do(h, http.MethodGet, "/v1/scheduling/capacity-load?from=2026-01-05&weeks=2", "", allDuties, "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"loadPct":95`) {
		t.Fatalf("status %d body %s", rec.Code, rec.Body)
	}
	if rec := do(h, http.MethodGet, "/v1/scheduling/capacity-load?weeks=0", "", allDuties, ""); rec.Code != http.StatusBadRequest {
		t.Fatalf("weeks=0 status %d", rec.Code)
	}
}

func TestMove_CommitRequiresKeyButPreviewDoesNot(t *testing.T) {
	f := &fakeStore{}
	h := newRouter(f)
	body := `{"resourceId":"` + resID + `","start":"2026-01-05T09:30:00Z"}`
	if rec := do(h, http.MethodPost, "/v1/scheduling/jobs/"+jobID+":move", body, allDuties, ""); rec.Code != http.StatusBadRequest {
		t.Fatalf("commit without key: status %d", rec.Code)
	}
	preview := `{"start":"2026-01-05T09:30:00Z","dryRun":true}`
	if rec := do(h, http.MethodPost, "/v1/scheduling/jobs/"+jobID+":move", preview, allDuties, ""); rec.Code != http.StatusOK || !f.lastMove.DryRun {
		t.Fatalf("preview without key: status %d, dryRun reached store = %v", rec.Code, f.lastMove.DryRun)
	}
}

func TestMove_FingerprintBindsKeyToTheJob(t *testing.T) {
	f := &fakeStore{}
	h := newRouter(f)
	body := `{"start":"2026-01-05T09:30:00Z"}`
	do(h, http.MethodPost, "/v1/scheduling/jobs/"+jobID+":move", body, allDuties, "same")
	first := f.lastIdem
	other := "00000000-0000-0000-0000-0000000000a2"
	do(h, http.MethodPost, "/v1/scheduling/jobs/"+other+":move", body, allDuties, "same")
	if first.Key != "same" || first.Fingerprint == f.lastIdem.Fingerprint {
		t.Fatalf("same body on two jobs must fingerprint differently: %+v vs %+v", first, f.lastIdem)
	}
}

func TestMove_ReplayIsFlagged(t *testing.T) {
	h := newRouter(&fakeStore{replay: true})
	body := `{"start":"2026-01-05T09:30:00Z"}`
	rec := do(h, http.MethodPost, "/v1/scheduling/jobs/"+jobID+":move", body, allDuties, "m3")
	if rec.Code != http.StatusOK || rec.Header().Get("Idempotent-Replayed") != "true" {
		t.Fatalf("status %d headers %v", rec.Code, rec.Header())
	}
}

func TestMove_StoreProblemPassesThrough(t *testing.T) {
	h := newRouter(&fakeStore{err: httpx.NewProblem(http.StatusNotFound, store.CodeJobNotFound, "scheduled job not found")})
	rec := do(h, http.MethodPost, "/v1/scheduling/jobs/"+jobID+":move", `{"start":"2026-01-05T09:30:00Z","dryRun":true}`, allDuties, "")
	if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), store.CodeJobNotFound) {
		t.Fatalf("status %d body %s", rec.Code, rec.Body)
	}
}

func TestFirm_RequiresExplicitFlag(t *testing.T) {
	h := newRouter(&fakeStore{})
	rec := do(h, http.MethodPost, "/v1/scheduling/jobs/"+jobID+":firm", `{}`, allDuties, "k7")
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), `"firmed"`) {
		t.Fatalf("status %d body %s", rec.Code, rec.Body)
	}
}

func TestCreateRun_RejectsPastStartAndSiteSelection(t *testing.T) {
	h := newRouter(&fakeStore{})
	cases := []struct{ name, body, field string }{
		{"start in the past", `{"scope":"site","start":"2020-01-01T00:00:00Z"}`, `"start"`},
		{"site with operations", `{"scope":"site","operationIds":["` + jobID + `"]}`, `"operationIds"`},
		{"horizon out of range", `{"scope":"site","horizonDays":5}`, `"horizonDays"`},
	}
	for _, c := range cases {
		rec := do(h, http.MethodPost, "/v1/scheduling/runs", c.body, allDuties, "r-"+c.name)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), c.field) {
			t.Fatalf("%s: status %d body %s", c.name, rec.Code, rec.Body)
		}
	}
	rec := do(h, http.MethodPost, "/v1/scheduling/runs", `{"scope":"site","finite":false}`, allDuties, "r-inf")
	if rec.Code != http.StatusCreated || !strings.Contains(rec.Body.String(), `"finite":false`) {
		t.Fatalf("infinite run: status %d body %s", rec.Code, rec.Body)
	}
}

func TestCapacityLoad_ValidatesFromAndResources(t *testing.T) {
	f := &fakeStore{}
	h := newRouter(f)
	if rec := do(h, http.MethodGet, "/v1/scheduling/capacity-load?from=2026-01-05T00:00:00Z", "", allDuties, ""); rec.Code != http.StatusBadRequest {
		t.Fatalf("timestamp from: status %d", rec.Code)
	}
	if rec := do(h, http.MethodGet, "/v1/scheduling/capacity-load?resourceIds=nope", "", allDuties, ""); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad resource id: status %d", rec.Code)
	}
	rec := do(h, http.MethodGet, "/v1/scheduling/capacity-load?from=2026-01-07&resourceIds="+strings.ToUpper(resID), "", allDuties, "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"weekStart":"2026-01-07"`) {
		t.Fatalf("status %d body %s", rec.Code, rec.Body)
	}
	if len(f.lastLoad.ResourceIDs) != 1 || f.lastLoad.ResourceIDs[0] != resID || f.lastLoad.Weeks != 4 {
		t.Fatalf("filter = %+v, want lower-cased resource and 4 weeks default", f.lastLoad)
	}
}

func TestReadEndpoints(t *testing.T) {
	f := &fakeStore{}
	h := newRouter(f)
	cases := []struct {
		name, path string
		status     int
		want       string
	}{
		{"resources", "/v1/scheduling/resources", http.StatusOK, `"code":"MIX-1"`},
		{"resource", "/v1/scheduling/resources/" + resID, http.StatusOK, `"id":"` + resID + `"`},
		{"resource bad id", "/v1/scheduling/resources/nope", http.StatusNotFound, store.CodeResourceNotFound},
		{"capacity", "/v1/scheduling/resources/" + resID + "/capacity?from=2026-01-05T00:00:00Z&to=2026-01-06T00:00:00Z", http.StatusOK, `"minutes":480`},
		{"capacity window too long", "/v1/scheduling/resources/" + resID + "/capacity?from=2026-01-05T00:00:00Z&to=2026-06-06T00:00:00Z", http.StatusBadRequest, `"from"`},
		{"operations bad status", "/v1/scheduling/operations?status=done", http.StatusBadRequest, `"status"`},
		{"operations", "/v1/scheduling/operations?status=unplaced&limit=10", http.StatusOK, `"items":[]`},
		{"runs bad limit", "/v1/scheduling/runs?limit=0", http.StatusBadRequest, `"limit"`},
		{"runs", "/v1/scheduling/runs", http.StatusOK, `"items":[]`},
	}
	for _, c := range cases {
		rec := do(h, http.MethodGet, c.path, "", "scheduling.read", "")
		if rec.Code != c.status || !strings.Contains(rec.Body.String(), c.want) {
			t.Fatalf("%s: status %d body %s", c.name, rec.Code, rec.Body)
		}
	}
	if f.lastOps.Status != "unplaced" || f.lastOps.Limit != 10 {
		t.Fatalf("operation filter = %+v", f.lastOps)
	}
	if rec := do(h, http.MethodGet, "/v1/scheduling/resources", "", "", ""); rec.Code != http.StatusForbidden {
		t.Fatalf("no duty: status %d", rec.Code)
	}
}
