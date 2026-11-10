package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/fieldservice/internal/optimizer"
	"github.com/BenjaminSRussell/passage/services/fieldservice/internal/store"
)

const optimizationsPath = "/v1/service/dispatch/optimizations"

type fakeOptimizer struct {
	mu        sync.Mutex
	submitErr error
	getErr    error
	status    string
	failure   *optimizer.RunError
	sent      []optimizer.Request
	keys      []string
	gets      int
}

func (f *fakeOptimizer) Configured() bool { return true }

func (f *fakeOptimizer) Submit(_ context.Context, _, key string, req optimizer.Request) (optimizer.Run, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.keys = append(f.keys, key)
	if f.submitErr != nil {
		return optimizer.Run{}, f.submitErr
	}
	f.sent = append(f.sent, req)
	return optimizer.Run{ID: "run-" + key, Status: optimizer.Queued}, nil
}

func (f *fakeOptimizer) Get(_ context.Context, _, id string) (optimizer.Run, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.gets++
	if f.getErr != nil {
		return optimizer.Run{}, f.getErr
	}
	run := optimizer.Run{ID: id, Status: f.status, Error: f.failure}
	if f.status == optimizer.Succeeded {
		req := f.sent[len(f.sent)-1]
		held := map[string]bool{}
		for _, p := range req.Pinned {
			held[p.WorkOrderID] = true
		}
		result := optimizer.Result{Assignments: append([]optimizer.Assignment{}, req.Pinned...), Unassigned: []string{}, Solver: "cpsat", CPSatStatus: "OPTIMAL"}
		start := 540
		for _, w := range req.WorkOrders {
			if held[w.ID] {
				continue
			}
			result.Assignments = append(result.Assignments, optimizer.Assignment{WorkOrderID: w.ID, TechnicianID: req.Technicians[0].ID, Start: start, End: start + w.Duration, Sequence: 1})
			start += w.Duration + 30
		}
		run.Result = &result
	}
	return run, nil
}

type optimizationDay struct {
	h             http.Handler
	ctx, unscoped context.Context
	le, territory string
	technicianID  string
	workOrderIDs  []string
	optimizeBody  string
}

func newOptimizationDay(t *testing.T, h http.Handler, ctx context.Context, orders int) *optimizationDay {
	t.Helper()
	d := &optimizationDay{h: h, ctx: ctx, unscoped: ctx, le: ids.New(), territory: ids.New()}
	tech := call(t, h, ctx, http.MethodPost, "/v1/service/technicians", `{"code":"ANA","name":"Ana Ruiz","legalEntityId":"`+d.le+`","territoryIds":["`+d.territory+
		`"],"skills":["hvac"],"timeZone":"America/Chicago","homeLatitude":41.88,"homeLongitude":-87.63}`, map[string]string{"Idempotency-Key": "tech"})
	if tech.Code != http.StatusCreated {
		t.Fatalf("create technician: %d %s", tech.Code, tech.Body.String())
	}
	d.technicianID = decodeInto[store.Technician](t, tech.Body.String()).ID
	for i := 0; i < orders; i++ {
		rec := call(t, h, ctx, http.MethodPost, "/v1/service/work-orders", `{"customerPartyId":"`+ids.New()+`","legalEntityId":"`+d.le+`","territoryId":"`+d.territory+
			`","estDurationMinutes":60,"currency":"USD","requiredSkills":["HVAC"],"latitude":41.9,"longitude":-87.65}`, map[string]string{"Idempotency-Key": "wo-" + string(rune('a'+i))})
		if rec.Code != http.StatusCreated {
			t.Fatalf("create work order: %d %s", rec.Code, rec.Body.String())
		}
		d.workOrderIDs = append(d.workOrderIDs, decodeInto[store.WorkOrder](t, rec.Body.String()).ID)
	}
	p, _ := tenant.FromContext(ctx)
	p.LegalEntityID = d.le
	d.ctx = tenant.With(ctx, p)
	d.optimizeBody = `{"legalEntityId":"` + d.le + `","territoryId":"` + d.territory + `","date":"2026-10-26","timeZone":"America/Chicago"}`
	return d
}

func (d *optimizationDay) request(t *testing.T, key string) *httptest.ResponseRecorder {
	t.Helper()
	return call(t, d.h, d.ctx, http.MethodPost, optimizationsPath, d.optimizeBody, map[string]string{"Idempotency-Key": key})
}

func (d *optimizationDay) step(t *testing.T, id, verb string) store.Optimization {
	t.Helper()
	rec := call(t, d.h, d.ctx, http.MethodPost, optimizationsPath+"/"+id+":"+verb, "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("%s: %d %s", verb, rec.Code, rec.Body.String())
	}
	return decodeInto[store.Optimization](t, rec.Body.String())
}

func (d *optimizationDay) read(t *testing.T, id string) store.Optimization {
	t.Helper()
	rec := call(t, d.h, d.ctx, http.MethodGet, optimizationsPath+"/"+id, "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("get: %d %s", rec.Code, rec.Body.String())
	}
	return decodeInto[store.Optimization](t, rec.Body.String())
}

func TestAPI_OptimizationFlow(t *testing.T) {
	fake := &fakeOptimizer{status: optimizer.Queued}
	a, h, ctx := dbAPI(t, fake)
	d := newOptimizationDay(t, h, ctx, 2)

	created := d.request(t, "opt-1")
	o := decodeInto[store.Optimization](t, created.Body.String())
	if created.Code != http.StatusCreated || o.Status != store.OptRunning || o.RemoteRunID == nil || o.Technicians != 1 || o.WorkOrders != 2 || o.AttemptCount != 0 {
		t.Fatalf("request: %d %s", created.Code, created.Body.String())
	}
	if len(fake.sent) != 1 || fake.sent[0].TimeLimitSeconds != store.DefaultOptimizerSeconds || fake.keys[0] != o.ID+":0:0" || len(fake.sent[0].Travel) != 3 {
		t.Fatalf("sent %+v keys %v", fake.sent, fake.keys)
	}
	replay := d.request(t, "opt-1")
	if replay.Code != http.StatusOK || replay.Header().Get("Idempotent-Replayed") != "true" || len(fake.keys) != 1 {
		t.Fatalf("replay: %d, %d submits", replay.Code, len(fake.keys))
	}

	if got := d.step(t, o.ID, "sync"); got.Status != store.OptRunning || got.Result != nil || fake.gets != 1 {
		t.Fatalf("queued run: %+v", got)
	}
	if rec := call(t, h, ctx, http.MethodPost, optimizationsPath+"/"+o.ID+":accept", "", nil); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("accept before the plan: %d %s", rec.Code, rec.Body.String())
	} else if code, _ := problemOf(t, rec); code != store.CodeOptimizationState {
		t.Fatalf("accept before the plan: %s", code)
	}

	fake.status = optimizer.Succeeded
	if got := d.step(t, o.ID, "sync"); got.Status != store.OptSucceeded || got.Result == nil || len(got.Result.Assignments) != 2 || got.Result.Solver != "cpsat" {
		t.Fatalf("finished run: %+v", got)
	}
	accepted := d.step(t, o.ID, "accept")
	if accepted.Status != store.OptAccepted || accepted.Outcome == nil || len(accepted.Outcome.Applied) != 2 || len(accepted.Outcome.Rejected) != 0 || accepted.AcceptedAt == nil {
		t.Fatalf("accept: %+v", accepted)
	}
	for _, id := range d.workOrderIDs {
		wo := decodeInto[store.WorkOrder](t, call(t, h, ctx, http.MethodGet, "/v1/service/work-orders/"+id, "", nil).Body.String())
		if wo.Status != "SCHEDULED" || wo.TechnicianID == nil || *wo.TechnicianID != d.technicianID || wo.AssignedStart == nil {
			t.Fatalf("work order after accept: %+v", wo)
		}
	}
	again := call(t, h, ctx, http.MethodPost, optimizationsPath+"/"+o.ID+":accept", "", nil)
	if again.Code != http.StatusOK || again.Header().Get("Idempotent-Replayed") != "true" {
		t.Fatalf("second accept: %d %s", again.Code, again.Body.String())
	}

	page := call(t, h, ctx, http.MethodGet, optimizationsPath+"?status=ACCEPTED&territoryId="+d.territory, "", nil)
	if items := decodeInto[store.Page[store.Optimization]](t, page.Body.String()).Items; page.Code != http.StatusOK || len(items) != 1 || items[0].ID != o.ID {
		t.Fatalf("list: %d %s", page.Code, page.Body.String())
	}
	if bad := call(t, h, ctx, http.MethodGet, optimizationsPath+"?status=DONE", "", nil); bad.Code != http.StatusBadRequest {
		t.Fatalf("unknown status filter: %d", bad.Code)
	}
	rec := httptest.NewRecorder()
	a.Metrics().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	for _, line := range []string{`outcome="requested"} 1`, `outcome="accepted"} 1`, `outcome="parked"} 0`} {
		if !strings.Contains(rec.Body.String(), "service_dispatch_optimizations_total{"+line) {
			t.Fatalf("metrics lack %s:\n%s", line, rec.Body.String())
		}
	}
}

func TestAPI_OptimizationBacksOffAndParks(t *testing.T) {
	fake := &fakeOptimizer{submitErr: errors.New("connection refused")}
	a, h, ctx := dbAPI(t, fake)
	clock := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	a.now = func() time.Time { return clock }
	d := newOptimizationDay(t, h, ctx, 1)

	created := d.request(t, "opt-1")
	o := decodeInto[store.Optimization](t, created.Body.String())
	if created.Code != http.StatusCreated || o.Status != store.OptPending || o.AttemptCount != 1 || o.LastError == nil || !o.NextAttemptAt.Equal(clock.Add(time.Minute)) {
		t.Fatalf("first failure: %d %s", created.Code, created.Body.String())
	}
	if got := d.step(t, o.ID, "sync"); got.AttemptCount != 1 || len(fake.keys) != 1 {
		t.Fatalf("a sync inside the wait called the service again: %+v, %d calls", got, len(fake.keys))
	}
	for attempt, wait := range []time.Duration{2 * time.Minute, 4 * time.Minute, 8 * time.Minute} {
		clock = o.NextAttemptAt.Add(time.Second)
		o = d.step(t, o.ID, "sync")
		if o.Status != store.OptPending || o.AttemptCount != attempt+2 || !o.NextAttemptAt.Equal(clock.Add(wait)) {
			t.Fatalf("failure %d: %+v, want next attempt in %s", attempt+2, o, wait)
		}
	}
	clock = o.NextAttemptAt.Add(time.Second)
	if o = d.step(t, o.ID, "sync"); o.Status != store.OptParked || o.AttemptCount != store.MaxOptimizationAttempts {
		t.Fatalf("fifth failure: %+v", o)
	}
	if got := d.step(t, o.ID, "sync"); got.Status != store.OptParked || len(fake.keys) != 5 {
		t.Fatalf("a parked optimization was tried again: %+v, %d calls", got, len(fake.keys))
	}

	fake.submitErr = nil
	retried := d.step(t, o.ID, "retry")
	if retried.Status != store.OptRunning || retried.AttemptCount != 0 || fake.keys[len(fake.keys)-1] != o.ID+":1:0" {
		t.Fatalf("retry: %+v keys %v", retried, fake.keys)
	}
	rec := httptest.NewRecorder()
	a.Metrics().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if !strings.Contains(rec.Body.String(), `service_dispatch_optimizations_total{outcome="parked"} 1`) {
		t.Fatalf("parked counter:\n%s", rec.Body.String())
	}
	if rec := call(t, h, ctx, http.MethodPost, optimizationsPath+"/"+o.ID+":retry", "", nil); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("retry of a running optimization: %d", rec.Code)
	}
}

func TestAPI_OptimizationFailureKinds(t *testing.T) {
	fake := &fakeOptimizer{submitErr: &optimizer.Error{Status: http.StatusUnprocessableEntity, Detail: "SVC-DSP-001 pinned assignments are infeasible"}}
	a, h, ctx := dbAPI(t, fake)
	clock := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	a.now = func() time.Time { return clock }
	d := newOptimizationDay(t, h, ctx, 1)

	refused := decodeInto[store.Optimization](t, d.request(t, "opt-1").Body.String())
	if refused.Status != store.OptParked || refused.AttemptCount != 1 || refused.LastError == nil || !strings.Contains(*refused.LastError, "infeasible") {
		t.Fatalf("refusal: %+v", refused)
	}

	fake.submitErr = nil
	o := decodeInto[store.Optimization](t, d.request(t, "opt-2").Body.String())
	if o.Status != store.OptRunning {
		t.Fatalf("second optimization: %+v", o)
	}
	message := "solver_error RuntimeError"
	fake.status, fake.failure = optimizer.Failed, &optimizer.RunError{Code: "solver_error", Detail: "RuntimeError"}
	if o = d.step(t, o.ID, "sync"); o.Status != store.OptPending || o.AttemptCount != 1 || o.LastError == nil || *o.LastError != message || o.RemoteRunID != nil {
		t.Fatalf("failed run: %+v", o)
	}
	clock = o.NextAttemptAt.Add(time.Second)
	if o = d.step(t, o.ID, "sync"); o.Status != store.OptRunning || fake.keys[len(fake.keys)-1] != o.ID+":0:1" {
		t.Fatalf("resubmitted: %+v keys %v", o, fake.keys)
	}
	fake.getErr = optimizer.ErrRunLost
	if o = d.step(t, o.ID, "sync"); o.Status != store.OptPending || o.AttemptCount != 2 {
		t.Fatalf("lost run: %+v", o)
	}
}

func TestAPI_OptimizationRefusals(t *testing.T) {
	h, ctx := dbRouter(t)
	d := newOptimizationDay(t, h, ctx, 1)

	bad := call(t, h, ctx, http.MethodPost, optimizationsPath, `{"legalEntityId":"x","territoryId":"`+d.territory+`","date":"26/10","timeLimitSeconds":500}`, map[string]string{"Idempotency-Key": "k"})
	if code, fields := problemOf(t, bad); bad.Code != http.StatusBadRequest || code != "COM-0001" || fields["legalEntityId"] == "" || fields["date"] == "" || fields["timeLimitSeconds"] == "" {
		t.Fatalf("validation: %d %s", bad.Code, bad.Body.String())
	}
	if noKey := call(t, h, ctx, http.MethodPost, optimizationsPath, d.optimizeBody, nil); noKey.Code != http.StatusServiceUnavailable {
		t.Fatalf("without a configured dispatch service: %d %s", noKey.Code, noKey.Body.String())
	} else if code, _ := problemOf(t, noKey); code != codeOptimizerUnavailable {
		t.Fatalf("unconfigured code %s", code)
	}
	for _, path := range []string{"/" + ids.New(), "/not-a-uuid"} {
		if rec := call(t, h, ctx, http.MethodGet, optimizationsPath+path, "", nil); rec.Code != http.StatusNotFound {
			t.Fatalf("GET %s: %d", path, rec.Code)
		} else if code, _ := problemOf(t, rec); code != store.CodeOptimizationUnknown {
			t.Fatalf("unknown code %s", code)
		}
	}
	if rec := call(t, h, ctx, http.MethodPost, optimizationsPath+"/"+ids.New()+":accept", "", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("accept of an unknown optimization: %d", rec.Code)
	}
}

func TestAPI_OptimizationDuties(t *testing.T) {
	fake := &fakeOptimizer{}
	_, h, _ := dbAPI(t, fake)
	reader := tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(1), UserID: "u", Duties: map[string]struct{}{dutyInquire: {}, tenant.DutyAllScope: {}}})
	for _, tc := range []struct{ method, path string }{
		{http.MethodPost, optimizationsPath}, {http.MethodPost, optimizationsPath + "/" + ids.New() + ":sync"},
		{http.MethodPost, optimizationsPath + "/" + ids.New() + ":retry"}, {http.MethodPost, optimizationsPath + "/" + ids.New() + ":accept"},
	} {
		if rec := call(t, h, reader, tc.method, tc.path, `{}`, map[string]string{"Idempotency-Key": "k"}); rec.Code != http.StatusForbidden {
			t.Fatalf("%s %s without the dispatch duty: %d", tc.method, tc.path, rec.Code)
		}
	}
	if rec := call(t, h, reader, http.MethodGet, optimizationsPath, "", nil); rec.Code != http.StatusOK {
		t.Fatalf("list with the inquiry duty: %d %s", rec.Code, rec.Body.String())
	}
	if len(fake.keys) != 0 {
		t.Fatal("the dispatch service was called by a caller without the duty")
	}
}

func TestAPI_OptimizationRunPastItsDeadlineIsResubmitted(t *testing.T) {
	fake := &fakeOptimizer{status: optimizer.Queued}
	a, h, ctx := dbAPI(t, fake)
	clock := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	a.now = func() time.Time { return clock }
	d := newOptimizationDay(t, h, ctx, 1)
	o := decodeInto[store.Optimization](t, d.request(t, "opt-1").Body.String())

	clock = clock.Add(optimizerRunDeadline - time.Minute)
	if o = d.step(t, o.ID, "sync"); o.Status != store.OptRunning || o.AttemptCount != 0 {
		t.Fatalf("a run inside its deadline: %+v", o)
	}
	clock = clock.Add(2 * time.Minute)
	if o = d.step(t, o.ID, "sync"); o.Status != store.OptPending || o.AttemptCount != 1 || o.RemoteRunID != nil || o.LastError == nil || !strings.Contains(*o.LastError, "still QUEUED") {
		t.Fatalf("a run past its deadline: %+v", o)
	}
	clock = o.NextAttemptAt.Add(time.Second)
	if o = d.step(t, o.ID, "sync"); o.Status != store.OptRunning || fake.keys[len(fake.keys)-1] != o.ID+":0:1" || len(fake.sent) != 2 {
		t.Fatalf("resubmitted: %+v keys %v", o, fake.keys)
	}
}

func TestAPI_OptimizationNeedsACallerScopedToTheLegalEntity(t *testing.T) {
	fake := &fakeOptimizer{}
	_, h, ctx := dbAPI(t, fake)
	d := newOptimizationDay(t, h, ctx, 1)
	rec := call(t, h, d.unscoped, http.MethodPost, optimizationsPath, d.optimizeBody, map[string]string{"Idempotency-Key": "k"})
	if code, _ := problemOf(t, rec); rec.Code != http.StatusForbidden || code != "AUTH-0003" || len(fake.keys) != 0 {
		t.Fatalf("all-scope caller: %d %s", rec.Code, rec.Body.String())
	}
	other := tenant.Principal{TenantID: testkit.TenantID(1), UserID: "u", LegalEntityID: ids.New(), Duties: map[string]struct{}{dutyDispatch: {}}}
	if rec := call(t, h, tenant.With(context.Background(), other), http.MethodPost, optimizationsPath, d.optimizeBody, map[string]string{"Idempotency-Key": "k"}); rec.Code != http.StatusForbidden {
		t.Fatalf("caller scoped to another entity: %d", rec.Code)
	}
}

func TestAPI_OptimizationSigningRefusalParksAtOnce(t *testing.T) {
	fake := &fakeOptimizer{submitErr: &optimizer.Error{Status: http.StatusForbidden, Detail: "no service token for this legal entity"}}
	_, h, ctx := dbAPI(t, fake)
	d := newOptimizationDay(t, h, ctx, 1)
	if o := decodeInto[store.Optimization](t, d.request(t, "opt-1").Body.String()); o.Status != store.OptParked || o.AttemptCount != 1 {
		t.Fatalf("refusal that never changes: %+v", o)
	}
}
