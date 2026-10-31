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
// A start that its own job-stop already closes (a job-stop of the same worker
// and job starting at or after it, stored or in this batch: the kiosk replays a
// start and its stop together) still stops the worker's older units, but is never
// closed by the server at a newer unit's start: its own stop carries its time,
// and a server stop as well would count the job's hours twice (BR-25.7 replay).
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
	closed, err := startClosedByOwnStop(ctx, tx, tenantID, in, batch)
	if err != nil {
		return err
	}
	for _, unit := range domain.PlanAutoStop(open, incoming) {
		if closed && unitHolds(unit, in.ID) {
			continue
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

// startClosedByOwnStop reports whether a job-stop of the same worker and job that
// starts at or after the start is in the batch or already stored (the openStarts
// rule applied to the incoming start).
func startClosedByOwnStop(ctx context.Context, tx pgx.Tx, tenantID string, in Registration, batch []Registration) (bool, error) {
	for _, b := range batch {
		if b.Kind == KindJobStop && b.WorkerID == in.WorkerID && b.JobID != nil && *b.JobID == *in.JobID && !b.StartedAt.Before(in.StartedAt) {
			return true, nil
		}
	}
	var stored bool
	err := tx.QueryRow(ctx, `select exists (select 1 from time_registration
		where tenant_id = $1 and worker_id = $2 and kind = 'job-stop' and job_id = $3 and started_at >= $4)`,
		tenantID, in.WorkerID, *in.JobID, in.StartedAt).Scan(&stored)
	return stored, err
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
