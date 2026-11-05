package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/money"
	"github.com/BenjaminSRussell/passage/services/scheduling/internal/domain"
	schedevents "github.com/BenjaminSRussell/passage/services/scheduling/internal/events"
)

// Run comparison and rollback (PRD 4.23.14, slice 29).
const (
	// CodeRunNoSnapshot: the run kept no jobs (a move run, or a run from before
	// snapshots), so it cannot be compared or rolled back to.
	CodeRunNoSnapshot = "SCHED-0143"
	// CodeRollbackConflict: a job of the run overlaps a booking made since.
	CodeRollbackConflict = "SCHED-0144"
	// CodeRollbackNothing: every job of the run was skipped, so there is nothing to restore.
	CodeRollbackNothing = "SCHED-0145"
	// CodeRollbackBreaks: a restored job would break a scheduling rule as the
	// plan and the calendars are now (open capacity, a block, the order's
	// sequence and queue times, a peg or a changeover).
	CodeRollbackBreaks = "SCHED-0146"

	// MaxCompareItems bounds each list of a run comparison; the counts stay exact.
	MaxCompareItems = 500

	SkipOperationMissing = "operation_missing"
	SkipResourceMissing  = "resource_missing"
	SkipFirmed           = "firmed"
	// SkipOperationChanged: the operation was changed or re-imported after the
	// run (its version moved), so the run's placement of it is out of date.
	SkipOperationChanged = "operation_changed"

	// maxRollbackBreaks bounds the violations a SCHED-0146 answer lists.
	maxRollbackBreaks = 50
)

// CompareJob is a job that only one of the two runs placed.
type CompareJob struct {
	JobID       string    `json:"jobId"`
	OrderID     string    `json:"orderId"`
	OperNumber  int       `json:"operNumber"`
	ResourceID  string    `json:"resourceId"`
	Start       time.Time `json:"start"`
	End         time.Time `json:"end"`
	LateMinutes int       `json:"lateMinutes"`
}

// CompareChange is a job both runs placed, differently. From is the base run
// and To the other run; shifts are To minus From.
type CompareChange struct {
	JobID             string    `json:"jobId"`
	OrderID           string    `json:"orderId"`
	OperNumber        int       `json:"operNumber"`
	FromResourceID    string    `json:"fromResourceId"`
	ToResourceID      string    `json:"toResourceId"`
	FromStart         time.Time `json:"fromStart"`
	ToStart           time.Time `json:"toStart"`
	FromEnd           time.Time `json:"fromEnd"`
	ToEnd             time.Time `json:"toEnd"`
	StartShiftMinutes int       `json:"startShiftMinutes"`
	EndShiftMinutes   int       `json:"endShiftMinutes"`
	LateDeltaMinutes  int       `json:"lateDeltaMinutes"`
}

// RunMeasures are the figures of one run's snapshot.
type RunMeasures struct {
	Jobs              int `json:"jobs"`
	Orders            int `json:"orders"`
	LateJobs          int `json:"lateJobs"`
	TotalLateMinutes  int `json:"totalLateMinutes"`
	MaxLateMinutes    int `json:"maxLateMinutes"`
	ChangeoverMinutes int `json:"changeoverMinutes"`
	MakespanMinutes   int `json:"makespanMinutes"`
	// BookedMinutes and OpenMinutes are the minutes the run booked on its
	// resources (primary and secondary) and their open minutes between the
	// run's first start and last end, calendars as they are now; utilization
	// is one over the other (PRD 4.23.14 Schedule run comparison).
	BookedMinutes  int     `json:"bookedMinutes"`
	OpenMinutes    int     `json:"openMinutes"`
	UtilizationPct float64 `json:"utilizationPct"`
}

// RunSide is one run of a comparison with its snapshot figures.
type RunSide struct {
	Run      Run         `json:"run"`
	Measures RunMeasures `json:"measures"`
}

// RunComparison is the difference between two runs' snapshots: what the
// other run added, dropped and moved relative to the base run.
type RunComparison struct {
	Base           RunSide         `json:"base"`
	Other          RunSide         `json:"other"`
	AddedCount     int             `json:"addedCount"`
	RemovedCount   int             `json:"removedCount"`
	ChangedCount   int             `json:"changedCount"`
	UnchangedCount int             `json:"unchangedCount"`
	Added          []CompareJob    `json:"added"`
	Removed        []CompareJob    `json:"removed"`
	Changed        []CompareChange `json:"changed"`
	Truncated      bool            `json:"truncated"`
}

// SkippedJob is a job of the restored run that rollback left alone.
type SkippedJob struct {
	JobID  string `json:"jobId"`
	Reason string `json:"reason"`
}

// RollbackOutcome is the result of re-pointing the plan to a previous run.
type RollbackOutcome struct {
	Run      Run          `json:"run"`
	Restored int          `json:"restored"`
	Skipped  []SkippedJob `json:"skipped"`
}

// RunSnapshotStore compares runs and rolls the plan back to one.
type RunSnapshotStore interface {
	CompareRuns(ctx context.Context, baseID, otherID string) (RunComparison, error)
	RollbackRun(ctx context.Context, runID string, idem Idem) (RollbackOutcome, bool, error)
}

// writeSnapshot keeps what a run placed, one row per segment, with the
// version of the operation it placed so a rollback can tell a placement that
// is out of date. The changeover goes on the first segment, like scheduled_job.
// The operations are locked by the caller, so each row finds its operation.
func writeSnapshot(ctx context.Context, tx pgx.Tx, runID string, ps []domain.Placement) error {
	tenantID := tenantOf(ctx)
	batch := &pgx.Batch{}
	for _, pl := range ps {
		secondaries := pl.Secondaries
		if secondaries == nil {
			secondaries = []string{}
		}
		for i, seg := range pl.Segments {
			changeover := 0
			var cost, currency, cleaning *string
			if i == 0 {
				changeover = pl.ChangeoverMinutes
				if pl.ChangeoverCost != nil {
					v := pl.ChangeoverCost.Value.String()
					cost, currency = &v, &pl.ChangeoverCost.Currency
				}
				cleaning = nullable(pl.CleaningSetID)
			}
			batch.Queue(`insert into schedule_run_job (tenant_id, run_id, job_id, segment_no, order_id, oper_number, resource_id, start_at, end_at,
				late_minutes, changeover_minutes, changeover_cost, changeover_currency, cleaning_operation_set_id, secondary_resource_ids, operation_version)
				select $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12::numeric, $13, $14, $15::uuid[], o.version
				from scheduling_operation o where o.id = $3`,
				tenantID, runID, pl.OperationID, i+1, pl.OrderID, pl.OperNumber, pl.ResourceID, seg.Start, seg.End,
				pl.LateMinutes, changeover, cost, currency, cleaning, secondaries)
		}
	}
	return tx.SendBatch(ctx, batch).Close()
}

// snapshotPlacements reads a run's snapshot back as placements, one per job,
// in order id, operation number and job id order, with the version each
// operation had when the run placed it.
func snapshotPlacements(ctx context.Context, tx pgx.Tx, runID string) ([]domain.Placement, map[string]int, error) {
	rows, err := tx.Query(ctx, `select job_id::text, order_id::text, oper_number, resource_id::text, start_at, end_at, late_minutes,
			changeover_minutes, changeover_cost::text, changeover_currency, coalesce(cleaning_operation_set_id, ''), secondary_resource_ids::text[],
			operation_version
		from schedule_run_job where run_id = $1 order by order_id, oper_number, job_id, segment_no`, runID)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	var out []domain.Placement
	idx := map[string]int{}
	versions := map[string]int{}
	for rows.Next() {
		var jobID, orderID, resID, cleaning string
		var oper, late, changeover, version int
		var start, end time.Time
		var cost, currency *string
		var secondaries []string
		if err := rows.Scan(&jobID, &orderID, &oper, &resID, &start, &end, &late, &changeover, &cost, &currency, &cleaning, &secondaries, &version); err != nil {
			return nil, nil, err
		}
		versions[jobID] = version
		i, ok := idx[jobID]
		if !ok {
			pl := domain.Placement{OperationID: jobID, OrderID: orderID, OperNumber: oper, ResourceID: resID, Start: start, LateMinutes: late,
				ChangeoverMinutes: changeover, CleaningSetID: cleaning}
			if len(secondaries) > 0 {
				pl.Secondaries = secondaries
			}
			if cost != nil && currency != nil {
				pl.ChangeoverCost = &money.Amount{Value: dec(*cost), Currency: *currency}
			}
			out = append(out, pl)
			i = len(out) - 1
			idx[jobID] = i
		}
		out[i].Segments = append(out[i].Segments, domain.Segment{Start: start, End: end})
		out[i].End = end
	}
	return out, versions, rows.Err()
}

func snapJobs(ps []domain.Placement) []domain.SnapJob {
	out := make([]domain.SnapJob, 0, len(ps))
	for _, p := range ps {
		out = append(out, domain.SnapJob{JobID: p.OperationID, OrderID: p.OrderID, OperNumber: p.OperNumber, ResourceID: p.ResourceID,
			Start: p.Start, End: p.End, LateMinutes: p.LateMinutes, ChangeoverMinutes: p.ChangeoverMinutes})
	}
	return out
}

func measuresOf(m domain.SnapMeasures) RunMeasures {
	return RunMeasures{Jobs: m.Jobs, Orders: m.Orders, LateJobs: m.LateJobs, TotalLateMinutes: m.TotalLateMinutes,
		MaxLateMinutes: m.MaxLateMinutes, ChangeoverMinutes: m.ChangeoverMinutes, MakespanMinutes: m.MakespanMinutes}
}

func compareJob(j domain.SnapJob) CompareJob {
	return CompareJob{JobID: j.JobID, OrderID: j.OrderID, OperNumber: j.OperNumber, ResourceID: j.ResourceID, Start: j.Start, End: j.End, LateMinutes: j.LateMinutes}
}

func loadSnapshotRun(ctx context.Context, tx pgx.Tx, id string, lock bool) (Run, error) {
	q := `select ` + runCols + ` from schedule_run where id = $1`
	if lock {
		q += ` for share`
	}
	r, err := scanRun(tx.QueryRow(ctx, q, id), nil)
	if errors.Is(err, pgx.ErrNoRows) {
		return Run{}, httpx.NewProblem(http.StatusNotFound, CodeRunNotFound, "schedule run not found")
	}
	if err != nil {
		return Run{}, err
	}
	if !r.Snapshot {
		return Run{}, httpx.NewProblem(http.StatusUnprocessableEntity, CodeRunNoSnapshot, "that run kept no job snapshot (a move run, or a run from before snapshots)")
	}
	return r, nil
}

// snapshotUtilization is the use of the resources a snapshot booked (primary
// and secondary) over the snapshot's own span, calendars as they are now
// (domain.RunUtilization per resource, summed). A resource deleted since
// counts in neither figure.
func snapshotUtilization(ps []domain.Placement, resources []domain.Resource) (booked, open int, pct float64) {
	segs := map[string][]domain.Busy{}
	var first, last time.Time
	for _, p := range ps {
		for _, sg := range p.Segments {
			for _, rid := range append([]string{p.ResourceID}, p.Secondaries...) {
				segs[rid] = append(segs[rid], domain.Busy{ResourceID: rid, JobID: p.OperationID, Start: sg.Start, End: sg.End})
			}
			if first.IsZero() || sg.Start.Before(first) {
				first = sg.Start
			}
			if sg.End.After(last) {
				last = sg.End
			}
		}
	}
	for _, r := range resources {
		if len(segs[r.ID]) == 0 {
			continue
		}
		k := domain.RunUtilization(r, segs[r.ID], first, last)
		booked += k.BookedMinutes
		open += k.OpenMinutes
	}
	if open > 0 {
		pct = math.Round(float64(booked)*1000/float64(open)) / 10
	}
	return booked, open, pct
}

// CompareRuns diffs the snapshots of two runs: what the other run placed
// that the base run did not (added), the reverse (removed), and the jobs both
// placed at a different resource or time (changed). Lists are cut at
// MaxCompareItems; the counts are exact. Two runs of a different scope or
// horizon are compared as they are: a job only one of them selected shows as
// added or removed, and each side carries its run so the reader can tell.
func (s *Postgres) CompareRuns(ctx context.Context, baseID, otherID string) (RunComparison, error) {
	var out RunComparison
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		base, err := loadSnapshotRun(ctx, tx, baseID, false)
		if err != nil {
			return err
		}
		other, err := loadSnapshotRun(ctx, tx, otherID, false)
		if err != nil {
			return err
		}
		bp, _, err := snapshotPlacements(ctx, tx, baseID)
		if err != nil {
			return err
		}
		op, _, err := snapshotPlacements(ctx, tx, otherID)
		if err != nil {
			return err
		}
		resources, _, err := loadResources(ctx, tx)
		if err != nil {
			return err
		}
		bj, oj := snapJobs(bp), snapJobs(op)
		d := domain.CompareRuns(bj, oj)
		bm, om := measuresOf(domain.MeasureSnapshot(bj)), measuresOf(domain.MeasureSnapshot(oj))
		bm.BookedMinutes, bm.OpenMinutes, bm.UtilizationPct = snapshotUtilization(bp, resources)
		om.BookedMinutes, om.OpenMinutes, om.UtilizationPct = snapshotUtilization(op, resources)
		out = RunComparison{
			Base:       RunSide{Run: base, Measures: bm},
			Other:      RunSide{Run: other, Measures: om},
			AddedCount: len(d.Added), RemovedCount: len(d.Removed), ChangedCount: len(d.Changed), UnchangedCount: d.Unchanged,
			Added: []CompareJob{}, Removed: []CompareJob{}, Changed: []CompareChange{},
		}
		for _, j := range d.Added[:min(len(d.Added), MaxCompareItems)] {
			out.Added = append(out.Added, compareJob(j))
		}
		for _, j := range d.Removed[:min(len(d.Removed), MaxCompareItems)] {
			out.Removed = append(out.Removed, compareJob(j))
		}
		for _, c := range d.Changed[:min(len(d.Changed), MaxCompareItems)] {
			out.Changed = append(out.Changed, CompareChange{JobID: c.Base.JobID, OrderID: c.Base.OrderID, OperNumber: c.Base.OperNumber,
				FromResourceID: c.Base.ResourceID, ToResourceID: c.Other.ResourceID, FromStart: c.Base.Start, ToStart: c.Other.Start,
				FromEnd: c.Base.End, ToEnd: c.Other.End, StartShiftMinutes: c.StartShiftMinutes, EndShiftMinutes: c.EndShiftMinutes,
				LateDeltaMinutes: c.LateDeltaMinutes})
		}
		out.Truncated = len(d.Added) > MaxCompareItems || len(d.Removed) > MaxCompareItems || len(d.Changed) > MaxCompareItems
		return nil
	})
	return out, err
}

// RollbackRun re-points the plan to a previous run: the jobs that run placed
// replace the current non-firmed jobs of the same operations, under a new run
// of scope rollback that keeps its own snapshot. Operations the run did not
// place keep their jobs (a later order is not taken off the plan).
//
// A job is skipped and listed when its operation no longer exists, was changed
// or re-imported since the run (its placement is out of date), is held by a
// firmed job, or a resource it used no longer exists. The plan the rest would
// leave is then checked as it is now, and any break refuses the whole rollback
// with nothing changed: a restored job that overlaps a booking made since
// (SCHED-0144, the exclusion constraints, every segment), and SCHED-0146 for
// a restored segment outside open capacity or in a block the job does not
// match, a restored job out of sequence with the job of a neighbouring
// operation of its order (queue times and overlap included), a peg to or from
// a restored order whose dependent would start before its parent ends (or
// behind an unplaced parent), or a changeover that no longer covers the step
// into or out of a restored job.
//
// Locks: the operations of every order the run placed, in order id and
// operation number order like POST /runs and :schedule, so a rollback and a
// run over the same operations serialize instead of deadlocking. The
// Idempotency-Key is looked up after the lock, so a racing retry replays.
func (s *Postgres) RollbackRun(ctx context.Context, runID string, idem Idem) (RollbackOutcome, bool, error) {
	var out RollbackOutcome
	replayed := false
	err := s.loadTx(&ctx, func(tx pgx.Tx) error {
		src, err := loadSnapshotRun(ctx, tx, runID, true)
		if err != nil {
			return err
		}
		snap, versions, err := snapshotPlacements(ctx, tx, runID)
		if err != nil {
			return err
		}
		snapOrders := []string{}
		seenOrder := map[string]bool{}
		for _, p := range snap {
			if !seenOrder[p.OrderID] {
				seenOrder[p.OrderID] = true
				snapOrders = append(snapOrders, p.OrderID)
			}
		}
		ops, err := loadOperations(ctx, tx, "o.order_id = any($1::uuid[])", true, snapOrders)
		if err != nil {
			return err
		}
		// The replay check comes after the lock: a retry racing the first
		// request waits for it and then answers with its stored result.
		if idem.Key != "" {
			found, err := replay(ctx, tx, idem, &out)
			if err != nil || found {
				replayed = found
				return err
			}
		}
		current := map[string]int{}
		vrows, err := tx.Query(ctx, `select id::text, version from scheduling_operation where order_id = any($1::uuid[])`, snapOrders)
		if err != nil {
			return err
		}
		for vrows.Next() {
			var id string
			var v int
			if err := vrows.Scan(&id, &v); err != nil {
				vrows.Close()
				return err
			}
			current[id] = v
		}
		vrows.Close()
		if err := vrows.Err(); err != nil {
			return err
		}
		snapIDs := make([]string, 0, len(snap))
		for _, p := range snap {
			snapIDs = append(snapIDs, p.OperationID)
		}
		firmed := map[string]bool{}
		frows, err := tx.Query(ctx, `select distinct job_id::text from scheduled_job where job_id = any($1::uuid[]) and firmed`, snapIDs)
		if err != nil {
			return err
		}
		for frows.Next() {
			var id string
			if err := frows.Scan(&id); err != nil {
				frows.Close()
				return err
			}
			firmed[id] = true
		}
		frows.Close()
		if err := frows.Err(); err != nil {
			return err
		}
		resources, finite, err := loadResources(ctx, tx)
		if err != nil {
			return err
		}
		known := map[string]bool{}
		for _, r := range resources {
			known[r.ID] = true
		}
		out.Skipped = []SkippedJob{}
		restore := make([]domain.Placement, 0, len(snap))
		for _, p := range snap {
			reason := ""
			v, exists := current[p.OperationID]
			switch {
			case !exists:
				reason = SkipOperationMissing
			case firmed[p.OperationID]:
				reason = SkipFirmed
			case v != versions[p.OperationID]:
				reason = SkipOperationChanged
			case !known[p.ResourceID]:
				reason = SkipResourceMissing
			default:
				for _, sid := range p.Secondaries {
					if !known[sid] {
						reason = SkipResourceMissing
					}
				}
			}
			if reason != "" {
				out.Skipped = append(out.Skipped, SkippedJob{JobID: p.OperationID, Reason: reason})
				continue
			}
			restore = append(restore, p)
		}
		if len(restore) == 0 {
			return httpx.NewProblem(http.StatusUnprocessableEntity, CodeRollbackNothing,
				"no job of that run can be restored: every one is firmed, or its operation was changed or deleted, or its resource no longer exists")
		}
		opIDs, orderIDs := make([]string, 0, len(restore)), []string{}
		seen := map[string]bool{}
		for _, p := range restore {
			opIDs = append(opIDs, p.OperationID)
			if !seen[p.OrderID] {
				seen[p.OrderID] = true
				orderIDs = append(orderIDs, p.OrderID)
			}
		}
		prior, err := orderDates(ctx, tx, orderIDs)
		if err != nil {
			return err
		}
		if err := trackOps(ctx, tx, opIDs); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `delete from scheduled_job where job_id = any($1::uuid[]) and not firmed`, opIDs); err != nil {
			return err
		}
		if err := dropOrphanSecondaries(ctx, tx, opIDs); err != nil {
			return err
		}
		run := Run{ID: ids.New(), Scope: "rollback", Mode: src.Mode, Direction: src.Direction, Finite: src.Finite, Algorithm: src.Algorithm,
			Status: "completed", OperationsCount: len(restore), MakespanMinutes: domain.Makespan(restore),
			TotalLatenessMinutes: domain.TotalLateness(restore), Messages: []string{}, Snapshot: true, RollbackOf: &runID}
		if err := insertRun(ctx, tx, &run, Idem{}); err != nil {
			return err
		}
		rowFinite := finite
		if !src.Finite {
			rowFinite = map[string]bool{}
		}
		if err := writePlacements(ctx, tx, run.ID, restore, rowFinite); err != nil {
			return err
		}
		// Restored operations count as scheduled before the check, so a
		// restored parent is not taken for an unplaced one.
		if err := setStatus(ctx, tx, opIDs, "scheduled"); err != nil {
			return err
		}
		breaks, err := rollbackBreaks(ctx, tx, ops, restore, orderIDs, rowFinite, src.Finite)
		if err != nil {
			return err
		}
		if len(breaks) > 0 {
			return rollbackBreaksProblem(breaks)
		}
		if err := writeSnapshot(ctx, tx, run.ID, restore); err != nil {
			return err
		}
		if err := enqueuePlacements(ctx, tx, run.ID, restore, false); err != nil {
			return err
		}
		if err := enqueueOrderDates(ctx, tx, orderIDs, prior); err != nil {
			return err
		}
		if err := enqueue(ctx, tx, "run", run.ID, schedevents.RunCompleted, schedevents.RunCompletedData{RunID: run.ID, Scope: run.Scope,
			Operations: run.OperationsCount, MakespanMinutes: run.MakespanMinutes, TotalLatenessMinutes: run.TotalLatenessMinutes,
			RolledBackToRunID: runID}); err != nil {
			return err
		}
		out.Run, out.Restored = run, len(restore)
		if idem.Key != "" {
			return remember(ctx, tx, idem, http.StatusCreated, out)
		}
		return nil
	})
	if err != nil {
		var pg *pgconn.PgError
		if errors.As(err, &pg) && pg.Code == exclusionViolation {
			return RollbackOutcome{}, false, httpx.NewProblem(http.StatusConflict, CodeRollbackConflict,
				"a job of that run overlaps a booking made since; nothing was changed")
		}
		return RollbackOutcome{}, false, mapWriteErr(err, CodeConcurrentBooking, "a concurrent request used the same Idempotency-Key; try again")
	}
	return out, replayed, nil
}

// rollbackBreaks checks the plan a rollback has written (restored jobs in
// place, inside its transaction) against the rules runs and moves keep. ops
// are the locked operations of the orders the run placed; orderIDs the orders
// with a restored job. Only breaks that involve a restored job are listed.
func rollbackBreaks(ctx context.Context, tx pgx.Tx, ops []domain.Operation, restore []domain.Placement, orderIDs []string,
	rowFinite map[string]bool, finiteRun bool) ([]domain.Conflict, error) {
	restored := make(map[string]bool, len(restore))
	attrs := map[string]string{}
	for _, op := range ops {
		raw, err := json.Marshal(op.Attributes)
		if err != nil {
			return nil, err
		}
		attrs[op.ID] = string(raw)
	}
	var out []domain.Conflict

	// Open capacity and blocks, calendars and downtimes as they are now: the
	// Gantt's own outsideCapacity and blockBreach test on the restored bars.
	var bars []Bar
	for _, p := range restore {
		restored[p.OperationID] = true
		for i, sg := range p.Segments {
			bars = append(bars, Bar{JobID: p.OperationID, OrderID: p.OrderID, OperNumber: p.OperNumber, ResourceID: p.ResourceID,
				Segment: i + 1, Start: sg.Start, End: sg.End, SecondaryResourceIDs: p.Secondaries,
				attributes: attrs[p.OperationID], finite: rowFinite[p.ResourceID]})
		}
	}
	if err := flagOutsideCapacity(ctx, tx, bars); err != nil {
		return nil, err
	}
	for _, b := range bars {
		switch {
		case b.OutsideCapacity:
			out = append(out, domain.Conflict{Code: domain.CodeOutsideCapacity, JobID: b.JobID,
				Detail: fmt.Sprintf("segment %d of operation %d of order %s lies outside the open capacity of its resources as they are now", b.Segment, b.OperNumber, b.OrderID)})
		case b.BlockBreach:
			out = append(out, domain.Conflict{Code: domain.CodeOutsideCapacity, JobID: b.JobID,
				Detail: fmt.Sprintf("segment %d of operation %d of order %s lies in a capacity block the job does not match (BR-23.15)", b.Segment, b.OperNumber, b.OrderID)})
		}
	}

	// The order's sequence: queue times and overlap against the jobs of the
	// operations that were not restored (firmed, changed, or not in the run).
	plan, err := placementsOfOrders(ctx, tx, orderIDs)
	if err != nil {
		return nil, err
	}
	out = append(out, domain.ChainBreaks(ops, plan, restored)...)

	// Pegs between orders: a dependent starts no earlier than its parents'
	// last end, and not at all behind a parent with an unplaced operation.
	prows, err := tx.Query(ctx, `select p.parent_order_id::text, p.dependent_order_id::text, pe.e, ds.s, pe.unplaced
		from scheduling_order_peg p
		cross join lateral (select max(j.end_at) e, bool_or(o.status = 'unplaced') unplaced from scheduling_operation o
			left join scheduled_job j on j.tenant_id = o.tenant_id and j.job_id = o.id where o.order_id = p.parent_order_id) pe
		cross join lateral (select min(j.start_at) s from scheduled_job j where j.order_id = p.dependent_order_id) ds
		where (p.parent_order_id = any($1::uuid[]) or p.dependent_order_id = any($1::uuid[])) and ds.s is not null
			and (pe.unplaced or ds.s < pe.e)
		order by 1, 2`, orderIDs)
	if err != nil {
		return nil, err
	}
	inRestore := map[string]bool{}
	for _, id := range orderIDs {
		inRestore[id] = true
	}
	firstOf := map[string]string{}
	for _, p := range restore {
		if _, ok := firstOf[p.OrderID]; !ok {
			firstOf[p.OrderID] = p.OperationID
		}
	}
	for prows.Next() {
		var parent, dependent string
		var parentEnd *time.Time
		var depStart time.Time
		var unplaced *bool
		if err := prows.Scan(&parent, &dependent, &parentEnd, &depStart, &unplaced); err != nil {
			prows.Close()
			return nil, err
		}
		job := firstOf[dependent]
		if job == "" {
			job = firstOf[parent]
		}
		detail := fmt.Sprintf("order %s would start at %s, before its parent order %s ends", dependent, depStart.UTC().Format("2006-01-02T15:04Z"), parent)
		if unplaced != nil && *unplaced {
			detail = fmt.Sprintf("order %s would be scheduled although its parent order %s has an unplaced operation", dependent, parent)
		}
		out = append(out, domain.Conflict{Code: domain.CodeOverlap, JobID: job, Detail: detail})
	}
	prows.Close()
	if err := prows.Err(); err != nil {
		return nil, err
	}

	// Changeovers on finite resources with a setup matrix, from the group the
	// resource is in before the first restored job.
	if finiteRun {
		from := restore[0].Start
		for _, p := range restore {
			if p.Start.Before(from) {
				from = p.Start
			}
		}
		booked, err := loadBooked(ctx, tx, from)
		if err != nil {
			return nil, err
		}
		resources, _, err := loadResources(ctx, tx)
		if err != nil {
			return nil, err
		}
		if _, err := loadConstraints(ctx, tx, resources, from, 1, from); err != nil {
			return nil, err
		}
		byRes := map[string][]domain.Busy{}
		for _, b := range booked {
			byRes[b.ResourceID] = append(byRes[b.ResourceID], b)
		}
		for _, r := range resources {
			bs := byRes[r.ID]
			if len(bs) == 0 {
				continue
			}
			sort.SliceStable(bs, func(i, j int) bool { return bs[i].Start.Before(bs[j].Start) })
			out = append(out, domain.ChangeoverShortfalls(r, bs, restored)...)
		}
	}
	return out, nil
}

// rollbackBreaksProblem is the SCHED-0146 answer: the first break in the
// detail and up to maxRollbackBreaks of them in errors, keyed by job id (one
// line per job, the first break found for it).
func rollbackBreaksProblem(breaks []domain.Conflict) error {
	p := httpx.NewProblem(http.StatusConflict, CodeRollbackBreaks, fmt.Sprintf(
		"restoring that run would break %d scheduling rule(s) as the plan is now; nothing was changed. First: %s", len(breaks), breaks[0].Detail))
	p.Errors = map[string]string{}
	for _, b := range breaks {
		if len(p.Errors) >= maxRollbackBreaks {
			break
		}
		if _, ok := p.Errors[b.JobID]; !ok {
			p.Errors[b.JobID] = b.Code + ": " + b.Detail
		}
	}
	return p
}
