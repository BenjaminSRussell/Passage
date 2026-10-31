package store

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/shopfloor/internal/domain"
	sfevents "github.com/BenjaminSRussell/passage/services/shopfloor/internal/events"
)

// Registration kinds that drive job dispatch (FR-25.3).
const (
	KindJobStart = domain.KindJobStart
	KindJobStop  = "job-stop"

	autoStopNote   = "Stopped automatically when the worker started another job."
	autoStopCapped = "Stopped automatically at 24 hours after its start; a supervisor reviews it."
	// autoStopSupersededBy is correctedBy on the correction that cancels a server
	// stop once the kiosk's own stop for the same start arrives (BR-25.7 replay).
	autoStopSupersededBy = "system:offline-replay"
)

// AutoStop is one job the server stopped because the worker started another one
// (FR-25.3, warning SF-0002). RegistrationID is the server-written job-stop.
type AutoStop struct {
	RegistrationID string          `json:"registrationId"`
	JobID          string          `json:"jobId"`
	BundleID       *string         `json:"bundleId"`
	StartedAt      time.Time       `json:"startedAt"`
	StoppedAt      time.Time       `json:"stoppedAt"`
	Hours          decimal.Decimal `json:"hours"`
	Flags          []string        `json:"flags"`
}

// Warning is a catalog warning on an accepted registration (SF-0002, SF-0004).
type Warning struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// lockJobWorkers takes the worker row lock for every worker with an open
// job-start in the batch, in one order, so two batches of one worker cannot both
// see no open job and leave two open (FR-25.3). Unknown workers are left to the
// insert, which reports SF-0101.
func lockJobWorkers(ctx context.Context, tx pgx.Tx, tenantID string, items []Registration) error {
	seen := map[string]bool{}
	var workers []string
	for _, in := range items {
		if in.Kind == KindJobStart && in.StoppedAt == nil && !seen[in.WorkerID] {
			seen[in.WorkerID] = true
			workers = append(workers, in.WorkerID)
		}
	}
	sort.Strings(workers)
	for _, w := range workers {
		if _, err := tx.Exec(ctx, `select 1 from worker where tenant_id = $1 and id = $2 for update`, tenantID, w); err != nil {
			return err
		}
	}
	return nil
}

// jobStartFlags reads the job a job-start names and returns the flags the start
// carries: job-finished-conflict when the server already finished the job
// (BR-25.7). A job the dispatch projection does not hold yet is accepted without
// flags, so an offline replay never loses a start (FR-25.12). The row is not
// locked: the status move in applyJobStart is guarded by status = 'released', and
// a finish that commits after this read finished the job after the start.
func jobStartFlags(ctx context.Context, tx pgx.Tx, tenantID string, in Registration) (status string, flags []string, err error) {
	flags = []string{}
	if in.Kind != KindJobStart || in.JobID == nil {
		return "", flags, nil
	}
	err = tx.QueryRow(ctx, `select status from shop_job where tenant_id = $1 and id = $2`, tenantID, *in.JobID).Scan(&status)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", flags, nil
	}
	if err != nil {
		return "", nil, err
	}
	conflicts := domain.FinishedConflicts([]domain.ReplayAction{{ID: in.ID, Kind: in.Kind, JobID: *in.JobID}},
		map[string]bool{*in.JobID: status == JobFinished})
	if f, ok := conflicts[in.ID]; ok {
		flags = append(flags, f)
	}
	return status, flags, nil
}

// applyJobStart runs the dispatch rules for a job-start that was just stored in
// tx: a released job becomes started, and an open start (no stoppedAt) closes the
// worker's other open unit of work (FR-25.3, BR-25.1).
//
// A start that its own job-stop already closes (see ownStopExists; stored or in
// this batch: the kiosk replays a start and its stop together) still stops the
// worker's older units, but is never closed by the server at a newer unit's
// start: its own stop carries its time, and a server stop as well would count the
// job's hours twice (BR-25.7 replay). Only the members of that unit that have
// their own stop are left out; the others are still closed (trimStaleUnit). When
// the stop comes in a later batch, supersedeAutoStop cancels the server stop.
func applyJobStart(ctx context.Context, tx pgx.Tx, tenantID string, in Registration, status string, batch []Registration, res *RegistrationResult) error {
	if status == JobReleased {
		if _, err := tx.Exec(ctx, `update shop_job set status = 'started', version = version + 1, updated_at = now()
			where tenant_id = $1 and id = $2 and status = 'released'`, tenantID, *in.JobID); err != nil {
			return err
		}
	}
	if in.StoppedAt != nil {
		return nil
	}
	open, err := openStarts(ctx, tx, tenantID, in.WorkerID)
	if err != nil {
		return err
	}
	incoming := domain.OpenStart{RegistrationID: in.ID, JobID: *in.JobID, StartedAt: in.StartedAt, QtyGood: in.QtyGood}
	if in.BundleID != nil {
		incoming.BundleID = *in.BundleID
	}
	for _, unit := range domain.PlanAutoStop(open, incoming) {
		if unitHolds(unit, in.ID) {
			var keep bool
			if unit, keep, err = trimStaleUnit(ctx, tx, tenantID, in, unit, batch); err != nil {
				return err
			}
			if !keep {
				continue
			}
		}
		hours, err := unit.Hours()
		if err != nil {
			return err
		}
		for i, m := range unit.Members {
			stop, err := insertAutoStop(ctx, tx, tenantID, in.WorkerID, m, unit, hours[i])
			if err != nil {
				return err
			}
			res.AutoStopped = append(res.AutoStopped, stop)
			res.Warnings = append(res.Warnings, Warning{Code: domain.CodeJobAutoStopped,
				Message: fmt.Sprintf("Previous job %s was stopped automatically.", m.JobID)})
		}
	}
	return nil
}

// trimStaleUnit adjusts the unit that closes a stale incoming start (one older
// than the worker's open unit). Members with their own job-stop are left out
// (keep is false when none is left). The rest are closed at the worker's next
// job-start after them, stored or in the batch and outside their bundle, when
// that comes before the open unit's start, so a stale start never overlaps work
// the worker registered in between (such as a later session of the same job).
func trimStaleUnit(ctx context.Context, tx pgx.Tx, tenantID string, in Registration, unit domain.StopUnit, batch []Registration) (domain.StopUnit, bool, error) {
	kept := unit.Members[:0:0]
	var last time.Time
	for _, m := range unit.Members {
		closed, err := ownStopExists(ctx, tx, tenantID, in.WorkerID, m.JobID, m.StartedAt, batch)
		if err != nil {
			return unit, false, err
		}
		if !closed {
			kept = append(kept, m)
			if m.StartedAt.After(last) {
				last = m.StartedAt
			}
		}
	}
	if len(kept) == 0 {
		return unit, false, nil
	}
	unit.Members = kept
	var next *time.Time
	if err := tx.QueryRow(ctx, `select min(started_at) from time_registration
		where tenant_id = $1 and worker_id = $2 and kind = 'job-start' and started_at > $3 and id <> $4
		  and ($5::uuid is null or bundle_id is distinct from $5::uuid)`,
		tenantID, in.WorkerID, last, in.ID, in.BundleID).Scan(&next); err != nil {
		return unit, false, err
	}
	for _, b := range batch {
		if b.Kind == KindJobStart && b.WorkerID == in.WorkerID && b.ID != in.ID && b.StartedAt.After(last) &&
			(in.BundleID == nil || b.BundleID == nil || *b.BundleID != *in.BundleID) && (next == nil || b.StartedAt.Before(*next)) {
			at := b.StartedAt
			next = &at
		}
	}
	if next != nil && next.Before(unit.StoppedAt) {
		unit.StoppedAt, unit.Capped = *next, false
	}
	return unit, true, nil
}

// ownStopExists reports whether the job-start of jobID at startedAt has its own
// job-stop, in the batch or stored: a job-stop of the same worker and job whose
// start is at or after startedAt and before the worker's next start of that job.
// A stop that belongs to a later re-start of the job does not close this start,
// so the server still closes it and the time up to the newer unit is kept.
func ownStopExists(ctx context.Context, tx pgx.Tx, tenantID, workerID, jobID string, startedAt time.Time, batch []Registration) (bool, error) {
	same := func(b Registration, kind string) bool {
		return b.Kind == kind && b.WorkerID == workerID && b.JobID != nil && *b.JobID == jobID
	}
	var next *time.Time
	if err := tx.QueryRow(ctx, `select min(started_at) from time_registration
		where tenant_id = $1 and worker_id = $2 and kind = 'job-start' and job_id = $3 and started_at > $4`,
		tenantID, workerID, jobID, startedAt).Scan(&next); err != nil {
		return false, err
	}
	for _, b := range batch {
		if same(b, KindJobStart) && b.StartedAt.After(startedAt) && (next == nil || b.StartedAt.Before(*next)) {
			at := b.StartedAt
			next = &at
		}
	}
	for _, b := range batch {
		if same(b, KindJobStop) && !b.StartedAt.Before(startedAt) && (next == nil || b.StartedAt.Before(*next)) {
			return true, nil
		}
	}
	var stored bool
	err := tx.QueryRow(ctx, `select exists (select 1 from time_registration
		where tenant_id = $1 and worker_id = $2 and kind = 'job-stop' and job_id = $3 and started_at >= $4
		  and ($5::timestamptz is null or started_at < $5))`,
		tenantID, workerID, jobID, startedAt, next).Scan(&stored)
	return stored, err
}

// supersedeAutoStop runs for a newly stored job-stop the kiosk wrote. When the
// start it closes (the worker's latest start of the job at or before the stop's
// start) was already closed by the server, because the start was replayed in an
// earlier batch than its stop and was older than the worker's newer unit, the
// server's stop is cancelled: it is corrected to zero length (stoppedAt =
// startedAt, 0.00 h) with a registration_correction row and
// registration.corrected, so the job's hours are the kiosk stop's only. A server
// stop a supervisor already corrected is left alone, and bundled stops are not
// handled (a bundle's closed jobs come in one batch, SF-0113).
func supersedeAutoStop(ctx context.Context, tx pgx.Tx, tenantID string, in Registration, batch []Registration) error {
	if in.Kind != KindJobStop || in.JobID == nil || in.BundleID != nil || in.StoppedAt == nil {
		return nil
	}
	// The start this stop closes; a start of the same batch not stored yet counts,
	// so an older session of the same job is never mistaken for it.
	var owner *time.Time
	if err := tx.QueryRow(ctx, `select max(started_at) from time_registration
		where tenant_id = $1 and worker_id = $2 and kind = 'job-start' and job_id = $3 and started_at <= $4`,
		tenantID, in.WorkerID, *in.JobID, in.StartedAt).Scan(&owner); err != nil {
		return err
	}
	for _, b := range batch {
		if b.Kind == KindJobStart && b.WorkerID == in.WorkerID && b.JobID != nil && *b.JobID == *in.JobID &&
			!b.StartedAt.After(in.StartedAt) && (owner == nil || b.StartedAt.After(*owner)) {
			at := b.StartedAt
			owner = &at
		}
	}
	if owner == nil {
		return nil
	}
	type autoStop struct {
		id       string
		start    time.Time
		stop     *time.Time
		oldHours *string
	}
	rows, err := tx.Query(ctx, `select a.id::text, a.started_at, a.stopped_at, a.hours::text from time_registration a
		where a.tenant_id = $1 and a.worker_id = $2 and a.kind = 'job-stop' and a.job_id = $3 and a.bundle_id is null
		  and a.started_at = $4 and a.id <> $5 and $6 = any(a.flags)
		  and not exists (select 1 from registration_correction c where c.tenant_id = a.tenant_id and c.registration_id = a.id)
		order by a.id for update`,
		tenantID, in.WorkerID, *in.JobID, *owner, in.ID, domain.FlagAutoStopped)
	if err != nil {
		return err
	}
	var found []autoStop
	for rows.Next() {
		var a autoStop
		if err := rows.Scan(&a.id, &a.start, &a.stop, &a.oldHours); err != nil {
			rows.Close()
			return err
		}
		found = append(found, a)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	zero := decimal.Zero
	reason := fmt.Sprintf("Superseded by the worker's own job-stop %s, replayed after the automatic stop.", in.ID)
	for _, a := range found {
		if _, err := tx.Exec(ctx, `update time_registration set stopped_at = started_at, hours = 0 where tenant_id = $1 and id = $2`,
			tenantID, a.id); err != nil {
			return err
		}
		newStop := a.start
		if _, err := tx.Exec(ctx, `insert into registration_correction (tenant_id, id, registration_id, worker_id, reason,
				old_started_at, old_stopped_at, old_hours, new_started_at, new_stopped_at, new_hours, corrected_by)
			values ($1, $2, $3, $4, $5, $6, $7, $8, $6, $6, 0, $9)`,
			tenantID, ids.New(), a.id, in.WorkerID, reason, a.start, a.stop, a.oldHours, autoStopSupersededBy); err != nil {
			return err
		}
		if err := enqueue(ctx, tx, "registration", a.id, sfevents.RegistrationCorrected, tenantID, sfevents.RegistrationCorrectedData{
			RegistrationID: a.id, WorkerID: in.WorkerID, Kind: KindJobStop, JobID: in.JobID, OldStartedAt: a.start, OldStoppedAt: a.stop,
			NewStartedAt: a.start, NewStoppedAt: &newStop, Hours: &zero, Reason: reason, CorrectedBy: autoStopSupersededBy,
		}); err != nil {
			return err
		}
	}
	return nil
}

func unitHolds(u domain.StopUnit, registrationID string) bool {
	for _, m := range u.Members {
		if m.RegistrationID == registrationID {
			return true
		}
	}
	return false
}

// openStarts loads a worker's open job-starts in start order: job-starts with no
// stoppedAt and no job-stop of the same job at or after their start. This is the
// rule of the "Who is clocked in" inquiry.
func openStarts(ctx context.Context, tx pgx.Tx, tenantID, workerID string) ([]domain.OpenStart, error) {
	rows, err := tx.Query(ctx, `select s.id::text, s.job_id::text, coalesce(s.bundle_id::text, ''), s.started_at, s.qty_good::text
		from time_registration s
		where s.tenant_id = $1 and s.worker_id = $2 and s.kind = 'job-start' and s.job_id is not null and s.stopped_at is null
		  and not exists (select 1 from time_registration x
		        where x.tenant_id = s.tenant_id and x.worker_id = s.worker_id and x.kind = 'job-stop'
		          and x.job_id = s.job_id and x.started_at >= s.started_at)
		order by s.started_at, s.id`, tenantID, workerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.OpenStart
	for rows.Next() {
		var o domain.OpenStart
		var good string
		if err := rows.Scan(&o.RegistrationID, &o.JobID, &o.BundleID, &o.StartedAt, &good); err != nil {
			return nil, err
		}
		if o.QtyGood, err = decimal.NewFromString(good); err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

// insertAutoStop writes the closed job-stop that ends one open start, from its
// own start to the unit's stop, with its hours (its share for a bundle), and
// enqueues its registration.posted event in the same transaction.
func insertAutoStop(ctx context.Context, tx pgx.Tx, tenantID, workerID string, m domain.OpenStart, unit domain.StopUnit, hours decimal.Decimal) (AutoStop, error) {
	stop := AutoStop{RegistrationID: ids.New(), JobID: m.JobID, StartedAt: m.StartedAt, StoppedAt: unit.StoppedAt, Hours: hours,
		Flags: []string{domain.FlagAutoStopped}}
	note := autoStopNote
	if unit.Capped {
		stop.Flags = append(stop.Flags, domain.FlagAutoStopCapped)
		note = autoStopCapped
	}
	if m.BundleID != "" {
		b := m.BundleID
		stop.BundleID = &b
	}
	if _, err := tx.Exec(ctx, `insert into time_registration (tenant_id, id, worker_id, kind, job_id, bundle_id, started_at, stopped_at, hours, note, flags)
		values ($1, $2, $3, 'job-stop', $4, $5, $6, $7, $8, $9, $10)`,
		tenantID, stop.RegistrationID, workerID, m.JobID, stop.BundleID, stop.StartedAt, stop.StoppedAt, hours.String(), note, stop.Flags); err != nil {
		return AutoStop{}, err
	}
	jobID := m.JobID
	return stop, enqueue(ctx, tx, "registration", stop.RegistrationID, sfevents.RegistrationPosted, tenantID, sfevents.RegistrationPostedData{
		RegistrationID: stop.RegistrationID, WorkerID: workerID, Kind: KindJobStop, JobID: &jobID, BundleID: stop.BundleID,
		Hours: &hours, QtyGood: decimal.Zero, QtyScrap: decimal.Zero, Flags: stop.Flags,
	})
}
