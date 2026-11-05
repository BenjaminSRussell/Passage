package api

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/planning/internal/store"
)

// Asynchronous plan runs (#479). POST /plans/{planCode}:run with
// "Prefer: respond-async" validates the request and builds the snapshot in the
// request as a synchronous run does, then answers 202 with the run and its
// Location and plans and stores the version in the background, outside the
// router's request timeout (httpx.RequestTimeout). GET /plan-runs/{id} reads
// the run's status, phase and row progress, and its version once it
// succeeded; POST /plan-runs/{id}:cancel stops it before its version commits.
const (
	planRunsPath = "/v1/planning/plan-runs"

	// DefaultMaxConcurrentRuns is how many asynchronous runs one process
	// executes at once (PLANNING_MAX_CONCURRENT_RUNS); a 50k-item run holds
	// several GB, so the default is one. A run beyond it is 503 with Retry-After.
	DefaultMaxConcurrentRuns = 1
	MaxMaxConcurrentRuns     = 16
	// DefaultRunTimeout bounds an asynchronous run (PLANNING_RUN_TIMEOUT).
	DefaultRunTimeout = time.Hour

	defaultHeartbeat = 5 * time.Second
	finishTimeout    = 30 * time.Second
	retryAfterSecs   = "30"

	codeRunCapacity    = "PLAN-RUN-CAPACITY"
	codeRunTimeout     = "PLAN-RUN-TIMEOUT"
	codeRunInterrupted = "PLAN-RUN-INTERRUPTED"
	codeRunCancelled   = "PLAN-RUN-CANCELLED"
)

var (
	errRunCancelled = errors.New("plan run cancelled")
	errRunTimeout   = errors.New("plan run timed out")
	errShutdown     = errors.New("planning service shutting down")
)

// runner executes the asynchronous runs of one process.
type runner struct {
	slots     chan struct{}
	timeout   time.Duration
	heartbeat time.Duration

	base context.Context
	stop context.CancelFunc
	wg   sync.WaitGroup

	mu     sync.Mutex
	active map[string]context.CancelCauseFunc

	started, succeeded, failed, cancelled atomic.Int64
}

func newRunner(concurrent int, timeout time.Duration) *runner {
	base, stop := context.WithCancel(context.Background())
	return &runner{slots: make(chan struct{}, concurrent), timeout: timeout, heartbeat: defaultHeartbeat, base: base, stop: stop, active: map[string]context.CancelCauseFunc{}}
}

// WithRunLimits sets how many asynchronous runs the process executes at once
// (1 to MaxMaxConcurrentRuns) and how long one may take. Call it before
// Register, while no run is executing.
func (a *API) WithRunLimits(concurrent int, timeout time.Duration) *API {
	a.runs = newRunner(min(max(concurrent, 1), MaxMaxConcurrentRuns), timeout)
	return a
}

// StopRuns cancels the asynchronous runs still executing (at shutdown) and
// waits up to wait for them to record that they were interrupted. A run whose
// process dies without that is reported abandoned once its heartbeat is stale.
func (a *API) StopRuns(wait time.Duration) {
	a.runs.stop()
	done := make(chan struct{})
	go func() { a.runs.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(wait):
		slog.Warn("plan runs still executing at shutdown")
	}
}

func (a *API) registerPlanRuns(r chi.Router) {
	// A run is read by whoever may start one or read plans.
	r.With(authz.RequireAnyDuty(dutyRun, dutyRead)).Get(planRunsPath+"/{id}", a.getPlanRun)
	r.With(authz.RequireDuty(dutyRun)).Post(planRunsPath+"/{id}:cancel", a.cancelPlanRun)
}

// prefersAsync reports whether the request asked for an asynchronous answer
// (RFC 7240 "Prefer: respond-async").
func prefersAsync(r *http.Request) bool {
	for _, v := range r.Header.Values("Prefer") {
		for _, pref := range strings.Split(v, ",") {
			token, _, _ := strings.Cut(pref, ";") // parameters follow ";"
			name, _, _ := strings.Cut(token, "=")
			if strings.EqualFold(strings.TrimSpace(name), "respond-async") {
				return true
			}
		}
	}
	return false
}

func respondRun(w http.ResponseWriter, status int, run store.PlanRun) {
	w.Header().Set("Location", planRunsPath+"/"+run.ID)
	httpx.JSON(w, status, run)
}

// startRun records the run and starts it in the background: 202 with the
// run, or 200 with the run an earlier request with the same Idempotency-Key
// started (Idempotent-Replayed: true).
func (a *API) startRun(w http.ResponseWriter, r *http.Request, job runJob) {
	select {
	case a.runs.slots <- struct{}{}:
	default:
		// A retry of a request whose run is executing still finds it.
		run, found, err := a.store.ReplayPlanRun(r.Context(), job.key, job.inputHash)
		switch {
		case err != nil:
			httpx.Fail(w, r, err)
		case found:
			w.Header().Set("Idempotent-Replayed", "true")
			respondRun(w, http.StatusOK, run)
		default:
			w.Header().Set("Retry-After", retryAfterSecs)
			httpx.Fail(w, r, httpx.NewProblem(http.StatusServiceUnavailable, codeRunCapacity, "this instance is executing as many plan runs as it may; retry later"))
		}
		return
	}
	release := func() { <-a.runs.slots }
	p, _ := tenant.FromContext(r.Context())
	run, replayed, err := a.store.BeginPlanRun(r.Context(), store.PlanRunStart{
		PlanCode: job.code, LegalEntityID: job.req.LegalEntityID, Items: len(job.req.Items),
		IdempotencyKey: job.key, InputHash: job.inputHash, RequestedBy: p.UserID,
	})
	if err != nil || replayed {
		release()
		if err != nil {
			var pr *httpx.Problem
			if errors.As(err, &pr) && pr.Code == store.CodeAlreadyRunning {
				a.runsRefused.Add(1)
			}
			httpx.Fail(w, r, err)
			return
		}
		w.Header().Set("Idempotent-Replayed", "true")
		respondRun(w, http.StatusOK, run)
		return
	}
	// The run keeps the request's principal and correlation id but not its
	// cancellation or deadline; it ends on its own timeout, a cancel or the
	// process stopping.
	ctx, cancel := context.WithCancelCause(context.WithoutCancel(r.Context()))
	stopOnShutdown := context.AfterFunc(a.runs.base, func() { cancel(errShutdown) })
	ctx, cancelTimeout := context.WithTimeoutCause(ctx, a.runs.timeout, errRunTimeout)
	a.runs.mu.Lock()
	a.runs.active[run.ID] = cancel
	a.runs.mu.Unlock()
	a.runs.wg.Add(1)
	a.runs.started.Add(1)
	go func() {
		defer a.runs.wg.Done()
		defer release()
		defer stopOnShutdown()
		defer cancelTimeout()
		defer cancel(nil)
		defer func() {
			a.runs.mu.Lock()
			delete(a.runs.active, run.ID)
			a.runs.mu.Unlock()
		}()
		a.work(ctx, cancel, run.ID, job)
	}()
	slog.InfoContext(ctx, "plan run started", "runId", run.ID, "planCode", job.code, "items", len(job.req.Items), "correlationId", middleware.GetReqID(ctx))
	respondRun(w, http.StatusAccepted, run)
}

// runProgress is the phase and row count a run reports on its heartbeat.
type runProgress struct {
	id      string
	phase   atomic.Value // string
	written atomic.Int64
	total   atomic.Int64
}

func (p *runProgress) persisting(total int64) {
	p.total.Store(total)
	p.phase.Store(store.PhasePersisting)
}

func (p *runProgress) wrote(rows int64) { p.written.Store(min(rows, p.total.Load())) }

func (p *runProgress) snapshot() store.PlanRunProgress {
	return store.PlanRunProgress{Phase: p.phase.Load().(string), RowsWritten: p.written.Load(), RowsTotal: p.total.Load()}
}

// work executes one run and records how it ended.
func (a *API) work(ctx context.Context, cancel context.CancelCauseFunc, id string, job runJob) {
	prog := &runProgress{id: id}
	prog.phase.Store(store.PhasePlanning)
	beat := make(chan struct{})
	beating := make(chan struct{})
	go func() {
		defer close(beating)
		a.beat(ctx, cancel, prog, beat)
	}()
	began := time.Now()
	v, replayed, err := a.executeSafely(ctx, job, prog)
	close(beat)
	<-beating

	finish := store.PlanRunFinish{Status: store.RunSucceeded, VersionID: v.ID, Replayed: replayed, RowsWritten: prog.written.Load()}
	if err != nil {
		finish = store.PlanRunFinish{Status: store.RunFailed, Problem: runFailure(ctx, err), RowsWritten: prog.written.Load()}
		if errors.Is(context.Cause(ctx), errRunCancelled) {
			finish.Status = store.RunCancelled
		}
	}
	fctx, done := context.WithTimeout(context.WithoutCancel(ctx), finishTimeout)
	defer done()
	if _, ferr := a.store.FinishPlanRun(fctx, id, finish); ferr != nil {
		slog.ErrorContext(ctx, "recording the end of a plan run failed", "err", ferr, "runId", id, "status", finish.Status)
	}
	switch finish.Status {
	case store.RunSucceeded:
		a.runs.succeeded.Add(1)
	case store.RunCancelled:
		a.runs.cancelled.Add(1)
	default:
		a.runs.failed.Add(1)
	}
	slog.InfoContext(ctx, "plan run finished", "runId", id, "planCode", job.code, "status", finish.Status, "version", v.Number,
		"durationMs", time.Since(began).Milliseconds(), "correlationId", middleware.GetReqID(ctx))
}

// executeSafely is executeRun with a panic turned into an error: the run is
// outside the router's Recoverer.
func (a *API) executeSafely(ctx context.Context, job runJob, prog *runProgress) (v store.Version, replayed bool, err error) {
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("plan run panicked: %v", p)
		}
	}()
	return a.executeRun(ctx, job, prog)
}

// runFailure is the problem a failed or cancelled run records.
func runFailure(ctx context.Context, err error) *httpx.Problem {
	switch cause := context.Cause(ctx); {
	case errors.Is(cause, errRunCancelled):
		return httpx.NewProblem(http.StatusConflict, codeRunCancelled, "the run was cancelled before its version was stored")
	case errors.Is(cause, errRunTimeout):
		return httpx.NewProblem(http.StatusGatewayTimeout, codeRunTimeout, "the run did not finish within its time limit; no version was stored")
	case errors.Is(cause, errShutdown):
		return httpx.NewProblem(http.StatusServiceUnavailable, codeRunInterrupted, "the planning service stopped during the run; no version was stored, start the run again")
	}
	var p *httpx.Problem
	if errors.As(err, &p) {
		return p
	}
	slog.ErrorContext(ctx, "plan run failed", "err", err)
	return httpx.NewProblem(http.StatusInternalServerError, "COM-0500", "internal error")
}

// beat renews the run's heartbeat with its progress until done is closed,
// and cancels the run when a cancel was requested from any instance.
func (a *API) beat(ctx context.Context, cancel context.CancelCauseFunc, prog *runProgress, done <-chan struct{}) {
	t := time.NewTicker(a.runs.heartbeat)
	defer t.Stop()
	for {
		select {
		case <-done:
			return
		case <-ctx.Done():
			return
		case <-t.C:
		}
		hctx, stop := context.WithTimeout(ctx, a.runs.heartbeat)
		stopRun, err := a.store.HeartbeatPlanRun(hctx, prog.id, prog.snapshot())
		stop()
		switch {
		case err != nil:
			slog.WarnContext(ctx, "plan run heartbeat failed", "err", err, "runId", prog.id)
		case stopRun:
			cancel(errRunCancelled)
			return
		}
	}
}

func (a *API) getPlanRun(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	run, err := a.store.GetPlanRun(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, run)
}

// cancelPlanRun asks a running run to stop: 202 with the run. The instance
// executing it stops at once when it is this one, else on its next heartbeat;
// a version already committed stays, and the run then reads succeeded.
func (a *API) cancelPlanRun(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	run, err := a.store.CancelPlanRun(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	a.runs.mu.Lock()
	cancel := a.runs.active[run.ID]
	a.runs.mu.Unlock()
	if cancel != nil {
		cancel(errRunCancelled)
	}
	respondRun(w, http.StatusAccepted, run)
}
