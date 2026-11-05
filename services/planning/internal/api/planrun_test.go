package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/planning/internal/store"
)

// fakePlanRuns keeps asynchronous runs in memory. Its lock also orders the
// background run's writes to the fake before the test reads them.
type fakePlanRuns struct {
	mu     sync.Mutex
	runs   map[string]*store.PlanRun
	hashes map[string]string // input hash by run id
	keys   map[string]string // run id by Idempotency-Key
	beats  int
}

func (f *fakeStore) replayLocked(key, hash string) (store.PlanRun, bool, error) {
	id, ok := f.planRuns.keys[key]
	if !ok {
		return store.PlanRun{}, false, nil
	}
	if f.planRuns.hashes[id] != hash {
		return store.PlanRun{}, false, httpx.NewProblem(http.StatusUnprocessableEntity, store.CodeIdempotencyReuse, "reuse")
	}
	return *f.planRuns.runs[id], true, nil
}

func (f *fakeStore) BeginPlanRun(_ context.Context, req store.PlanRunStart) (store.PlanRun, bool, error) {
	f.planRuns.mu.Lock()
	defer f.planRuns.mu.Unlock()
	if run, ok, err := f.replayLocked(req.IdempotencyKey, req.InputHash); ok || err != nil {
		return run, ok, err
	}
	if f.planRuns.runs == nil {
		f.planRuns.runs, f.planRuns.hashes, f.planRuns.keys = map[string]*store.PlanRun{}, map[string]string{}, map[string]string{}
	}
	for _, r := range f.planRuns.runs {
		if r.PlanCode == req.PlanCode && r.Status == store.RunRunning {
			return store.PlanRun{}, false, httpx.NewProblem(http.StatusConflict, store.CodeAlreadyRunning, "a run of plan "+req.PlanCode+" is already in progress")
		}
	}
	id := fmt.Sprintf("00000000-0000-4000-8000-%012d", len(f.planRuns.runs)+1)
	run := &store.PlanRun{ID: id, PlanCode: req.PlanCode, Status: store.RunRunning, Phase: store.PhasePlanning, Items: req.Items, RequestedBy: req.RequestedBy, CreatedAt: time.Now(), HeartbeatAt: time.Now()}
	f.planRuns.runs[id], f.planRuns.hashes[id], f.planRuns.keys[req.IdempotencyKey] = run, req.InputHash, id
	return *run, false, nil
}

func (f *fakeStore) ReplayPlanRun(_ context.Context, key, hash string) (store.PlanRun, bool, error) {
	f.planRuns.mu.Lock()
	defer f.planRuns.mu.Unlock()
	return f.replayLocked(key, hash)
}

func (f *fakeStore) GetPlanRun(_ context.Context, id string) (store.PlanRun, error) {
	f.planRuns.mu.Lock()
	defer f.planRuns.mu.Unlock()
	run, ok := f.planRuns.runs[id]
	if !ok {
		return store.PlanRun{}, httpx.NewProblem(http.StatusNotFound, store.CodeRunNotFound, "plan run not found")
	}
	return *run, nil
}

func (f *fakeStore) HeartbeatPlanRun(_ context.Context, id string, p store.PlanRunProgress) (bool, error) {
	f.planRuns.mu.Lock()
	defer f.planRuns.mu.Unlock()
	f.planRuns.beats++
	run := f.planRuns.runs[id]
	if run.Status != store.RunRunning {
		return true, nil
	}
	run.Phase, run.RowsWritten, run.RowsTotal, run.HeartbeatAt = p.Phase, p.RowsWritten, p.RowsTotal, time.Now()
	return run.CancelRequested, nil
}

func (f *fakeStore) FinishPlanRun(_ context.Context, id string, fin store.PlanRunFinish) (store.PlanRun, error) {
	f.planRuns.mu.Lock()
	defer f.planRuns.mu.Unlock()
	run := f.planRuns.runs[id]
	if run.Status == store.RunRunning {
		now := time.Now()
		run.Status, run.Phase, run.Replayed, run.Problem, run.FinishedAt = fin.Status, store.PhaseDone, fin.Replayed, fin.Problem, &now
		run.RowsWritten = max(run.RowsWritten, fin.RowsWritten)
		for _, v := range f.runs {
			if v.ID == fin.VersionID && fin.VersionID != "" {
				v := v
				run.Version, run.VersionNumber = &v, &v.Number
			}
		}
	}
	return *run, nil
}

func (f *fakeStore) CancelPlanRun(_ context.Context, id string) (store.PlanRun, error) {
	f.planRuns.mu.Lock()
	defer f.planRuns.mu.Unlock()
	run, ok := f.planRuns.runs[id]
	if !ok {
		return store.PlanRun{}, httpx.NewProblem(http.StatusNotFound, store.CodeRunNotFound, "plan run not found")
	}
	if run.Status != store.RunRunning {
		return store.PlanRun{}, httpx.NewProblem(http.StatusConflict, store.CodeRunFinished, "finished")
	}
	run.CancelRequested = true
	return *run, nil
}

// asyncServer is one API on the production router, kept across requests so
// its runner sees every call.
type asyncServer struct {
	t      *testing.T
	api    *API
	router http.Handler
}

func newAsyncServer(t *testing.T, st store.Store) *asyncServer {
	a := New(st)
	r := httpx.NewRouter()
	a.Register(r)
	t.Cleanup(func() { a.StopRuns(5 * time.Second) })
	return &asyncServer{t: t, api: a, router: r}
}

func (s *asyncServer) do(method, path, body, duties, key string, async bool) *httptest.ResponseRecorder {
	s.t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Passage-Tenant", testkit.TenantID(1))
	req.Header.Set("X-Passage-Duties", duties)
	req.Header.Set("X-Passage-User", "planner-1")
	if key != "" {
		req.Header.Set(idempotencyKey, key)
	}
	if async {
		req.Header.Set("Prefer", "respond-async, wait=0")
	}
	rec := httptest.NewRecorder()
	s.router.ServeHTTP(rec, req)
	return rec
}

func (s *asyncServer) start(plan, key string) store.PlanRun {
	s.t.Helper()
	rec := s.do(http.MethodPost, "/v1/planning/plans/"+plan+":run", runBody, dutyRun, key, true)
	if rec.Code != http.StatusAccepted {
		s.t.Fatalf("async run = %d %s; want 202", rec.Code, rec.Body)
	}
	run := decodeRun(s.t, rec)
	if loc := rec.Header().Get("Location"); loc != planRunsPath+"/"+run.ID {
		s.t.Fatalf("Location = %q; want %s/%s", loc, planRunsPath, run.ID)
	}
	return run
}

func (s *asyncServer) get(id string) store.PlanRun {
	s.t.Helper()
	rec := s.do(http.MethodGet, planRunsPath+"/"+id, "", dutyRead, "", false)
	if rec.Code != http.StatusOK {
		s.t.Fatalf("GET run = %d %s", rec.Code, rec.Body)
	}
	return decodeRun(s.t, rec)
}

// wait blocks until the run is no longer running.
func (s *asyncServer) wait(id string) store.PlanRun {
	s.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		run := s.get(id)
		if run.Status != store.RunRunning {
			return run
		}
		if time.Now().After(deadline) {
			s.t.Fatalf("run %s still running", id)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func decodeRun(t *testing.T, rec *httptest.ResponseRecorder) store.PlanRun {
	t.Helper()
	var run store.PlanRun
	if err := json.Unmarshal(rec.Body.Bytes(), &run); err != nil {
		t.Fatalf("%v: %s", err, rec.Body)
	}
	return run
}

func TestRunAsync_AcceptsThenStoresTheVersion(t *testing.T) {
	st := &fakeStore{}
	s := newAsyncServer(t, st)
	run := s.start("MAIN", "a1")
	if run.Status != store.RunRunning || run.PlanCode != "MAIN" || run.Items != 1 || run.RequestedBy != "planner-1" {
		t.Fatalf("accepted run = %+v", run)
	}
	done := s.wait(run.ID)
	if done.Status != store.RunSucceeded || done.Version == nil || done.Version.PlannedOrders == 0 || done.Replayed || done.Problem != nil {
		t.Fatalf("finished run = %+v", done)
	}
	if st.last.RunID != run.ID || st.last.Progress == nil || st.last.IdempotencyKey != "a1" {
		t.Fatalf("store run = RunID %q, progress set %v, key %q; want the run's id, a progress callback and the request key", st.last.RunID, st.last.Progress != nil, st.last.IdempotencyKey)
	}
	// The same key and body replay the run; another body under the key is a reuse.
	rec := s.do(http.MethodPost, "/v1/planning/plans/MAIN:run", runBody, dutyRun, "a1", true)
	if rec.Code != http.StatusOK || rec.Header().Get("Idempotent-Replayed") != "true" || decodeRun(t, rec).ID != run.ID {
		t.Fatalf("replay = %d %s", rec.Code, rec.Body)
	}
	other := strings.Replace(runBody, `"qty":"120"`, `"qty":"121"`, 1)
	if rec := s.do(http.MethodPost, "/v1/planning/plans/MAIN:run", other, dutyRun, "a1", true); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("key reuse = %d %s; want 422", rec.Code, rec.Body)
	}
	// The synchronous contract is unchanged without the preference.
	if rec := s.do(http.MethodPost, "/v1/planning/plans/MAIN:run", runBody, dutyRun, "s1", false); rec.Code != http.StatusCreated {
		t.Fatalf("synchronous run = %d %s; want 201", rec.Code, rec.Body)
	}
	if st.last.RunID != "" || st.last.Progress != nil {
		t.Fatal("a synchronous run must not name an asynchronous run")
	}
}

func TestRunAsync_ValidationAnswersInTheRequest(t *testing.T) {
	st := &fakeStore{}
	s := newAsyncServer(t, st)
	if rec := s.do(http.MethodPost, "/v1/planning/plans/MAIN:run", `{"asOf":"2026-11-02"}`, dutyRun, "v1", true); rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid async run = %d %s; want 400", rec.Code, rec.Body)
	}
	if rec := s.do(http.MethodPost, "/v1/planning/plans/MAIN:run", runBody, dutyRun, "", true); rec.Code != http.StatusBadRequest {
		t.Fatalf("async run without Idempotency-Key = %d; want 400", rec.Code)
	}
	if len(st.planRuns.runs) != 0 {
		t.Fatalf("refused requests recorded %d runs", len(st.planRuns.runs))
	}
}

func TestRunAsync_StoreFailureIsRecordedOnTheRun(t *testing.T) {
	st := &fakeStore{runErr: httpx.NewProblem(http.StatusConflict, store.CodeAlreadyRunning, "a run of plan MAIN is already in progress")}
	s := newAsyncServer(t, st)
	done := s.wait(s.start("MAIN", "f1").ID)
	if done.Status != store.RunFailed || done.Problem == nil || done.Problem.Code != store.CodeAlreadyRunning {
		t.Fatalf("run = %+v; want failed with PLAN-0001", done)
	}
	st.runErr = fmt.Errorf("connection reset")
	done = s.wait(s.start("MAIN", "f2").ID)
	if done.Status != store.RunFailed || done.Problem == nil || done.Problem.Code != "COM-0500" || strings.Contains(done.Problem.Detail, "reset") {
		t.Fatalf("run = %+v; want failed with COM-0500 and the cause hidden", done)
	}
}

func TestRunAsync_CancelStopsTheRunBeforeItsVersion(t *testing.T) {
	st := &fakeStore{runGate: make(chan struct{})}
	s := newAsyncServer(t, st)
	s.api.WithRunLimits(2, DefaultRunTimeout) // room for a second run, so the plan refuses it
	run := s.start("MAIN", "c1")
	if rec := s.do(http.MethodPost, planRunsPath+"/"+run.ID+":cancel", "", dutyRead, "", false); rec.Code != http.StatusForbidden {
		t.Fatalf("cancel without planning.run = %d; want 403", rec.Code)
	}
	// A second run of the plan is refused while this one runs.
	if rec := s.do(http.MethodPost, "/v1/planning/plans/MAIN:run", runBody, dutyRun, "c2", true); rec.Code != http.StatusConflict {
		t.Fatalf("second run = %d %s; want 409", rec.Code, rec.Body)
	}
	rec := s.do(http.MethodPost, planRunsPath+"/"+run.ID+":cancel", "", dutyRun, "", false)
	if rec.Code != http.StatusAccepted || !decodeRun(t, rec).CancelRequested {
		t.Fatalf("cancel = %d %s; want 202 with cancelRequested", rec.Code, rec.Body)
	}
	done := s.wait(run.ID)
	if done.Status != store.RunCancelled || done.Problem == nil || done.Problem.Code != codeRunCancelled || done.Version != nil {
		t.Fatalf("cancelled run = %+v", done)
	}
	if len(st.runs) != 0 {
		t.Fatal("a cancelled run stored a version")
	}
	if rec := s.do(http.MethodPost, planRunsPath+"/"+run.ID+":cancel", "", dutyRun, "", false); rec.Code != http.StatusConflict {
		t.Fatalf("cancel of a finished run = %d; want 409", rec.Code)
	}
	if rec := s.do(http.MethodGet, planRunsPath+"/00000000-0000-4000-8000-999999999999", "", dutyRun, "", false); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown run = %d; want 404", rec.Code)
	}
}

func TestRunAsync_CancelFromAnotherInstanceArrivesOnTheHeartbeat(t *testing.T) {
	st := &fakeStore{runGate: make(chan struct{})}
	s := newAsyncServer(t, st)
	s.api.runs.heartbeat = 5 * time.Millisecond
	run := s.start("MAIN", "h1")
	// Another replica marks the row; this process only learns of it from its heartbeat.
	st.planRuns.mu.Lock()
	st.planRuns.runs[run.ID].CancelRequested = true
	st.planRuns.mu.Unlock()
	if done := s.wait(run.ID); done.Status != store.RunCancelled {
		t.Fatalf("run = %+v; want cancelled", done)
	}
	st.planRuns.mu.Lock()
	beats := st.planRuns.beats
	st.planRuns.mu.Unlock()
	if beats == 0 {
		t.Fatal("no heartbeat was recorded")
	}
}

func TestRunAsync_CapacityAndShutdown(t *testing.T) {
	st := &fakeStore{runGate: make(chan struct{})}
	s := newAsyncServer(t, st)
	run := s.start("MAIN", "k1")
	rec := s.do(http.MethodPost, "/v1/planning/plans/OTHER:run", runBody, dutyRun, "k2", true)
	if rec.Code != http.StatusServiceUnavailable || rec.Header().Get("Retry-After") == "" {
		t.Fatalf("run beyond capacity = %d %s; want 503 with Retry-After", rec.Code, rec.Body)
	}
	// A retry of the executing run's request still finds it.
	if rec := s.do(http.MethodPost, "/v1/planning/plans/MAIN:run", runBody, dutyRun, "k1", true); rec.Code != http.StatusOK || decodeRun(t, rec).ID != run.ID {
		t.Fatalf("retry at capacity = %d %s; want 200 with the run", rec.Code, rec.Body)
	}
	s.api.StopRuns(5 * time.Second)
	done := s.get(run.ID)
	if done.Status != store.RunFailed || done.Problem == nil || done.Problem.Code != codeRunInterrupted {
		t.Fatalf("run after shutdown = %+v; want failed with %s", done, codeRunInterrupted)
	}
}

func TestRunAsync_TimeoutFailsTheRun(t *testing.T) {
	st := &fakeStore{runGate: make(chan struct{})}
	s := newAsyncServer(t, st)
	s.api.WithRunLimits(1, 20*time.Millisecond)
	done := s.wait(s.start("MAIN", "t1").ID)
	if done.Status != store.RunFailed || done.Problem == nil || done.Problem.Code != codeRunTimeout {
		t.Fatalf("run = %+v; want failed with %s", done, codeRunTimeout)
	}
}

func TestPrefersAsync(t *testing.T) {
	for header, want := range map[string]bool{"": false, "respond-async": true, "Respond-Async; wait=5": true, "return=minimal, respond-async": true, "wait=10,respond-async": true, "return=representation": false} {
		r := httptest.NewRequest(http.MethodPost, "/", nil)
		if header != "" {
			r.Header.Set("Prefer", header)
		}
		if got := prefersAsync(r); got != want {
			t.Errorf("Prefer %q: async = %v; want %v", header, got, want)
		}
	}
}
