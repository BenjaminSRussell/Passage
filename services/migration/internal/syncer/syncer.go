// Package syncer is the in-service scheduler of delta sync (integration track S4, decision B5): it finds bindings
// whose schedule is due, claims them, runs a delta through the same pipeline a manual run uses and records the
// outcome. Temporal can replace it later: everything it needs is behind Store and Runner, and Scheduler.Run is the
// only entry point cmd/migration calls.
package syncer

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/migration/internal/domain"
	"github.com/BenjaminSRussell/passage/services/migration/internal/store"
)

const (
	DefaultInterval    = 30 * time.Second
	DefaultBatch       = 100
	DefaultConcurrency = 4
	// RunTimeout is the deadline of one delta run, the same as a manual run's.
	RunTimeout  = 35 * time.Minute
	maxErrorLen = 500
	// Actor is the user id the scheduler's system context carries.
	Actor = "service:migration-scheduler"
)

// Store is what the scheduler needs from the store.
type Store interface {
	store.SyncStore
	BeginProjectRun(ctx context.Context, projectID, runID string) (store.Project, error)
	EndProjectRun(ctx context.Context, projectID, runID string) error
}

// Result is what a Runner reports for one delta run.
type Result struct {
	RunID  string
	Report domain.Report
	// BindingVersion is the entity binding version the run read; it is stored with the watermark.
	BindingVersion int
	// FullRead is set when the run read the whole source (a binding without a cursor, a first or reset run, or a
	// periodic key check); Absent is then the loaded keys the source no longer has.
	FullRead bool
}

// Runner executes one delta run of a claimed binding: extract since st.Watermark, map, load, save the run row under
// runID. A returned error is a failure that produced no report (the source or a target could not be reached).
type Runner interface {
	Run(ctx context.Context, st store.SyncState, runID string) (Result, error)
}

type Scheduler struct {
	Store       Store
	Runner      Runner
	Interval    time.Duration
	Batch       int
	Concurrency int
	// Now is the clock used to compute the next cron slot (the database clock decides what is due). Defaults to time.Now.
	Now func() time.Time

	claimed, succeeded, failed, lost atomic.Int64
	scanErrs                         atomic.Int64

	mu       sync.Mutex
	slots    chan struct{}
	inflight map[string]bool // tenant/binding pairs this replica is handling
	wg       sync.WaitGroup
}

// Stats are the scheduler's counters, for the metrics endpoint.
func (s *Scheduler) Stats() (claimed, succeeded, failed, scanErrors int64) {
	return s.claimed.Load(), s.succeeded.Load(), s.failed.Load(), s.scanErrs.Load()
}

func (s *Scheduler) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// Run ticks until ctx ends. Any number of replicas may run it: a due binding is taken by one conditional update.
// A tick does not wait for the runs it starts, so one long run holds one of the Concurrency slots and never the
// whole loop; a binding found while every slot is busy is left for a later tick (it stays due).
func (s *Scheduler) Run(ctx context.Context) {
	interval := s.Interval
	if interval <= 0 {
		interval = DefaultInterval
	}
	if c, ok := s.Store.(interface {
		SyncCanScan(context.Context) (bool, error)
	}); ok {
		if can, err := c.SyncCanScan(ctx); err != nil {
			slog.ErrorContext(ctx, "sync scheduler self-check failed", "err", err)
		} else if !can {
			slog.ErrorContext(ctx, "sync scheduler is blind: the owner of migration.sync_candidates cannot bypass row-level security, so it will find nothing; migrate as a superuser or BYPASSRLS role")
		}
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		s.dispatch(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// Tick makes one pass and waits until the runs it started (and any still running from earlier passes) have ended.
func (s *Scheduler) Tick(ctx context.Context) {
	s.dispatch(ctx)
	s.Wait()
}

// Wait blocks until every run this scheduler started has ended.
func (s *Scheduler) Wait() { s.wg.Wait() }

// dispatch looks for candidates and starts each one this replica is not already handling, while a slot is free.
func (s *Scheduler) dispatch(ctx context.Context) {
	batch := s.Batch
	if batch < 1 {
		batch = DefaultBatch
	}
	cands, err := s.Store.SyncCandidates(ctx, batch)
	if err != nil {
		if ctx.Err() == nil {
			s.scanErrs.Add(1)
			slog.ErrorContext(ctx, "sync scheduler scan failed", "err", err)
		}
		return
	}
	s.mu.Lock()
	if s.slots == nil {
		conc := s.Concurrency
		if conc < 1 {
			conc = DefaultConcurrency
		}
		s.slots = make(chan struct{}, conc)
		s.inflight = map[string]bool{}
	}
	s.mu.Unlock()
	for _, c := range cands {
		if ctx.Err() != nil {
			return
		}
		key := c.TenantID + "/" + c.BindingID
		s.mu.Lock()
		busy := s.inflight[key]
		s.mu.Unlock()
		if busy {
			continue
		}
		select {
		case s.slots <- struct{}{}:
		default:
			return // every slot is busy: the rest stay due for a later tick
		}
		s.mu.Lock()
		s.inflight[key] = true
		s.mu.Unlock()
		s.wg.Add(1)
		go func() {
			defer func() {
				s.mu.Lock()
				delete(s.inflight, key)
				s.mu.Unlock()
				<-s.slots
				s.wg.Done()
			}()
			s.handle(ctx, c)
		}()
	}
}

// SystemContext is the tenant's system context the scheduler works in: one tenant, no legal entity, the scope duty
// the migration tables' policies need. It carries no administrator duty and never spans tenants.
func SystemContext(ctx context.Context, tenantID string) context.Context {
	return tenant.With(ctx, tenant.Principal{TenantID: tenantID, UserID: Actor, Service: "migration", Duties: map[string]struct{}{tenant.DutyAllScope: {}}})
}

// nextSlot is the first firing of cron after now, read in the schedule's zone (UTC when the zone is unknown, which the
// API refuses to store).
func nextSlot(cron, zone string, now time.Time) (time.Time, bool) {
	loc, ok := domain.ScheduleLocation(zone)
	if !ok {
		loc = time.UTC
	}
	return domain.CronNextIn(cron, now, loc)
}

func (s *Scheduler) handle(ctx context.Context, c store.SyncCandidate) {
	tctx := SystemContext(ctx, c.TenantID)
	log := slog.With("tenantId", c.TenantID, "bindingId", c.BindingID)
	next := func(cron, zone string) (time.Time, bool) { return nextSlot(cron, zone, s.now()) }
	if err := s.Store.SeedSync(tctx, c.BindingID, next); err != nil {
		log.ErrorContext(ctx, "seeding sync state failed", "err", err)
		return
	}
	claimID := ids.New()
	st, ok, err := s.Store.ClaimSync(tctx, c.BindingID, claimID)
	if err != nil {
		log.ErrorContext(ctx, "claiming sync failed", "err", err)
		return
	}
	if !ok {
		return // not due yet, paused, or another replica won
	}
	s.claimed.Add(1)
	s.execute(tctx, st, claimID, log)
}

func (s *Scheduler) execute(ctx context.Context, st store.SyncState, claimID string, log *slog.Logger) {
	// The run outlives a cancelled tick (shutdown) so a half-loaded run is not abandoned mid-batch; its deadline is the manual run's.
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), RunTimeout)
	defer cancel()
	runID := ids.New()
	if _, err := s.Store.BeginProjectRun(rctx, st.ProjectID, runID); err != nil {
		// One active run per project (BR-10.2): wait for the next tick. Nothing ran, so nothing is recorded.
		if relErr := s.Store.ReleaseSync(rctx, st.BindingID, claimID); relErr != nil {
			log.ErrorContext(ctx, "releasing sync claim failed", "err", relErr)
		}
		s.lost.Add(1)
		log.InfoContext(ctx, "sync waits for the project's active run", "projectId", st.ProjectID, "reason", err.Error())
		return
	}
	defer func() {
		if err := s.Store.EndProjectRun(context.WithoutCancel(rctx), st.ProjectID, runID); err != nil {
			log.ErrorContext(ctx, "releasing project claim failed", "err", err)
		}
	}()
	started := time.Now()
	res, runErr := s.Runner.Run(rctx, st, runID)
	fin := s.finish(st, claimID, runID, res, runErr)
	if err := s.Store.FinishSync(context.WithoutCancel(rctx), fin); err != nil {
		if store.IsSyncClaimLost(err) {
			log.WarnContext(ctx, "sync claim was reclaimed while the run was executing; its outcome is discarded", "runId", runID)
		} else {
			log.ErrorContext(ctx, "recording sync outcome failed", "err", err, "runId", runID)
		}
		return
	}
	if fin.Status == domain.SyncFailed {
		s.failed.Add(1)
	} else {
		s.succeeded.Add(1)
	}
	log.InfoContext(ctx, "sync run finished", "runId", runID, "entity", st.Entity, "status", fin.Status, "extracted", fin.Extracted, "loaded", fin.Loaded, "rejected", fin.Rejected,
		"conflicted", len(fin.Conflicts), "watermarkAdvanced", fin.Advance, "consecutiveFailures", fin.Failures, "durationMs", time.Since(started).Milliseconds())
}

// finish turns a run's outcome into the bookkeeping that ends the claim. The watermark moves only for a run that did
// not fail: a failed run (the source could not be read, a target batch failed) leaves it so the next run re-reads.
func (s *Scheduler) finish(st store.SyncState, claimID, runID string, res Result, runErr error) store.SyncFinish {
	rep := res.Report
	f := store.SyncFinish{BindingID: st.BindingID, ClaimID: claimID, Entity: st.Entity, RunID: res.RunID, Status: rep.Status, BindingVersion: res.BindingVersion,
		Extracted: rep.Extracted, Loaded: rep.Created + rep.Updated, Rejected: len(rep.Rejects), Conflicts: rep.Conflicts}
	f.Absent = rep.Absent
	switch {
	case runErr != nil:
		f.Status, f.Error, f.RunID = domain.SyncFailed, truncate(runErr.Error()), res.RunID
	case rep.Status == domain.StatusFailed:
		f.Error = "the run failed"
		if len(rep.Rejects) > 0 && len(rep.Rejects[0].Rejects) > 0 {
			r := rep.Rejects[0].Rejects[0]
			f.Error = truncate("the run failed: " + r.Code + " " + r.Detail)
		}
	case rep.Transient > 0:
		// Rows a target did not take because it was down are not loaded: leave the watermark so the next run reads them again.
		f.Status = domain.SyncFailed
		f.Error = fmt.Sprintf("%d rows were not loaded because a target service did not answer; they are read again next run", rep.Transient)
	default:
		f.Advance = rep.Watermark != "" && !rep.DryRun
		f.Watermark = rep.Watermark
	}
	if f.Status == domain.SyncFailed {
		f.Failures = st.ConsecutiveFailures + 1
		f.BackoffSecs = domain.SyncBackoff(f.Failures).Seconds()
	}
	next, ok := nextSlot(st.ScheduleCron, st.ScheduleTimezone, s.now())
	if !ok {
		next = s.now().Add(time.Hour)
	}
	f.NextCron, f.Cron, f.Timezone = next, st.ScheduleCron, st.ScheduleTimezone
	// Only a complete read that did not fail says which keys are gone: a failed run may not have seen every row. A
	// full read that failed still counts as the period's attempt, so the runs after it are deltas again.
	f.FullRead = res.FullRead && runErr == nil && f.Status != domain.SyncFailed
	f.KeyCheckAttempted = res.FullRead
	// Only the operator requests the claim saw are spent: one sent while this run read the source waits for the next.
	f.KeyCheckForced, f.KeyCheckAcceptLarge = st.KeyCheckForce, st.KeyCheckAcceptLarge
	if f.RunID == "" && runErr == nil {
		f.RunID = runID
	}
	return f
}

func truncate(s string) string {
	if len(s) > maxErrorLen {
		return s[:maxErrorLen]
	}
	return s
}

// ErrNotConfigured is what a Runner answers when the deployment has no extraction or target wiring.
var ErrNotConfigured = errors.New("sync needs the connection extraction environment and MIG_TARGET_*_URL")
