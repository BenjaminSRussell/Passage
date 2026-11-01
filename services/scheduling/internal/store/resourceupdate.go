package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/scheduling/internal/domain"
	schedevents "github.com/BenjaminSRussell/passage/services/scheduling/internal/events"
)

// CodeResourceVersion is returned when a resource was changed since the
// caller read it.
const CodeResourceVersion = "SCHED-0124"

// ResourcePatch changes the descriptive and calendar fields of a resource;
// nil fields stay as they are. The code and the finite flag never change.
type ResourcePatch struct {
	Name     *string
	TimeZone *string
	Shifts   []domain.Shift
}

// ResourceUpdate is the updated resource with the booked jobs its new
// calendar no longer covers (AC-23.7).
type ResourceUpdate struct {
	Resource
	AffectedJobs []AffectedJob `json:"affectedJobs"`
}

// ResourceUpdateStore is the resource update part of the Store.
type ResourceUpdateStore interface {
	UpdateResource(ctx context.Context, id string, version int, patch ResourcePatch, from time.Time, idem Idem) (ResourceUpdate, bool, error)
}

// UpdateResource changes a resource's name, time zone or weekly calendar under
// optimistic versioning. Jobs whose booking after from now lies outside the
// resource's open capacity, as primary or as secondary, are returned in
// affectedJobs and listed in the event; they stay where they are until the
// planner moves them or runs the schedule again (AC-23.7). A replay of the
// same key returns the stored answer with replayed=true, even after the
// version has moved on.
func (s *Postgres) UpdateResource(ctx context.Context, id string, version int, patch ResourcePatch, from time.Time, idem Idem) (ResourceUpdate, bool, error) {
	var out ResourceUpdate
	replayed := false
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		// Lock the resource before looking up the key, so a retry that raced
		// the first request waits for it and then replays its answer instead
		// of failing on the version the first request bumped.
		cur, err := scanResource(tx.QueryRow(ctx, `select `+resourceCols+` from scheduling_resource where id = $1::uuid for update`, id), nil)
		if errors.Is(err, pgx.ErrNoRows) {
			return httpx.NewProblem(http.StatusNotFound, CodeResourceNotFound, "resource not found")
		}
		if err != nil {
			return err
		}
		if idem.Key != "" {
			found, err := replay(ctx, tx, idem, &out)
			if err != nil || found {
				replayed = found
				return err
			}
		}
		if cur.Version != version {
			return httpx.NewProblem(http.StatusPreconditionFailed, CodeResourceVersion,
				fmt.Sprintf("resource was changed by another user; current version is %d", cur.Version))
		}
		if patch.Name != nil {
			cur.Name = *patch.Name
		}
		if patch.TimeZone != nil {
			cur.TimeZone = *patch.TimeZone
		}
		if patch.Shifts != nil {
			cur.Shifts = patch.Shifts
		}
		shifts, err := json.Marshal(cur.Shifts)
		if err != nil {
			return err
		}
		upd, err := scanResource(tx.QueryRow(ctx, `update scheduling_resource set name = $2, time_zone = $3, shifts = $4, version = version + 1
			where id = $1::uuid returning `+resourceCols, id, cur.Name, cur.TimeZone, shifts), nil)
		if err != nil {
			return err
		}
		jobs, err := uncoveredJobs(ctx, tx, upd, from)
		if err != nil {
			return err
		}
		out = ResourceUpdate{Resource: upd, AffectedJobs: jobs}
		refs := make([]string, 0, len(jobs))
		for _, j := range jobs {
			refs = append(refs, j.JobID)
		}
		if err := enqueue(ctx, tx, "resource", upd.ID, schedevents.ResourceUpdated, schedevents.ResourceUpdatedData{
			ResourceID: upd.ID, Code: upd.Code, Name: upd.Name, TimeZone: upd.TimeZone, Shifts: upd.Shifts, Version: upd.Version, AffectedJobs: refs}); err != nil {
			return err
		}
		if idem.Key != "" {
			return remember(ctx, tx, idem, http.StatusOK, out)
		}
		return nil
	})
	if err != nil {
		return ResourceUpdate{}, false, mapWriteErr(err, CodeIdempotencyReuse, "a concurrent request used the same Idempotency-Key; retry")
	}
	return out, replayed, nil
}

// uncoveredJobs lists the booked jobs that hold the resource after from, as
// primary or as secondary, where the part of the booking after from lies
// partly outside the resource's open capacity, calendar and overrides
// applied; hours a running job has already used do not count. Start and end
// span every segment of the job, as for a downtime (at most 500, by start).
func uncoveredJobs(ctx context.Context, tx pgx.Tx, res Resource, from time.Time) ([]AffectedJob, error) {
	out := []AffectedJob{}
	loc, err := time.LoadLocation(res.TimeZone)
	if err != nil {
		return nil, err
	}
	type span struct {
		job        string
		start, end time.Time
	}
	var spans []span
	rows, err := tx.Query(ctx, `select job_id::text, start_at, end_at from scheduled_job where resource_id = $1::uuid and end_at > $2
		union all
		select job_id::text, start_at, end_at from scheduled_job_secondary where resource_id = $1::uuid and end_at > $2`, res.ID, from)
	if err != nil {
		return nil, err
	}
	hi := from
	for rows.Next() {
		var sp span
		if err := rows.Scan(&sp.job, &sp.start, &sp.end); err != nil {
			rows.Close()
			return nil, err
		}
		if sp.start.Before(from) {
			sp.start = from
		}
		if sp.end.After(hi) {
			hi = sp.end
		}
		spans = append(spans, sp)
	}
	rows.Close()
	if err := rows.Err(); err != nil || len(spans) == 0 {
		return out, err
	}
	ovs, err := loadOverrides(ctx, tx, res.ID)
	if err != nil {
		return nil, err
	}
	// A day either side so the local days straddling the spans are whole.
	open := domain.OpenWithOverrides(loc, res.Shifts, ovs[res.ID], from.Add(-24*time.Hour), int(hi.Sub(from)/(24*time.Hour))+3)
	seen := map[string]bool{}
	jobIDs := []string{}
	for _, sp := range spans {
		if !seen[sp.job] && domain.OutsideOpen(open, sp.start, sp.end) {
			seen[sp.job] = true
			jobIDs = append(jobIDs, sp.job)
		}
	}
	return affectedByID(ctx, tx, jobIDs)
}

// affectedByID describes the listed jobs with start and end spanning every
// segment, by start (at most 500).
func affectedByID(ctx context.Context, tx pgx.Tx, jobIDs []string) ([]AffectedJob, error) {
	out := []AffectedJob{}
	if len(jobIDs) == 0 {
		return out, nil
	}
	rows, err := tx.Query(ctx, `select job_id::text, order_id::text, oper_number, min(start_at), max(end_at), bool_or(firmed)
		from scheduled_job where job_id = any($1::uuid[])
		group by job_id, order_id, oper_number order by min(start_at), oper_number limit $2`, jobIDs, maxAffectedJobs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var a AffectedJob
		if err := rows.Scan(&a.JobID, &a.OrderID, &a.OperNumber, &a.Start, &a.End, &a.Firmed); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}
