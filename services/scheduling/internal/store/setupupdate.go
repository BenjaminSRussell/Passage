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

const (
	// CodeBlockNotFound is returned for an unknown capacity block.
	CodeBlockNotFound = "SCHED-0126"
	// CodeBlockVersion is returned when a block was changed since the caller read it.
	CodeBlockVersion = "SCHED-0127"
	// CodeNetworkVersion is returned when a network was changed since the caller read it.
	CodeNetworkVersion = "SCHED-0128"
	// CodeNetworkEnd is returned when the end date lies before the network's start.
	CodeNetworkEnd = "SCHED-0129"
)

// BlockPatch changes what a block admits and whether it applies; nil fields
// stay as they are. The resource, code and window never change.
type BlockPatch struct {
	Priority  *int
	Active    *bool
	Condition *[]domain.Predicate
}

// NetworkPatch renames a network or ends it on a date (inclusive); nil fields
// stay as they are. Its code, start date and edges never change.
type NetworkPatch struct {
	Name     *string
	ActiveTo *string
}

// CapacityBlockUpdate is the updated block with the booked jobs the
// resource's active blocks now keep out of the time they hold (BR-23.15).
type CapacityBlockUpdate struct {
	CapacityBlock
	AffectedJobs []AffectedJob `json:"affectedJobs"`
}

// SetupUpdateStore is the block and network maintenance part of the Store.
type SetupUpdateStore interface {
	GetCapacityBlock(ctx context.Context, id string) (CapacityBlock, error)
	UpdateCapacityBlock(ctx context.Context, id string, version int, patch BlockPatch, from time.Time, idem Idem) (CapacityBlockUpdate, bool, error)
	UpdateNetwork(ctx context.Context, id string, version int, patch NetworkPatch, idem Idem) (Network, bool, error)
}

// GetCapacityBlock returns one capacity block.
func (s *Postgres) GetCapacityBlock(ctx context.Context, id string) (CapacityBlock, error) {
	var out CapacityBlock
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		out, err = scanBlock(tx.QueryRow(ctx, `select `+blockCols+` from scheduling_capacity_block where id = $1::uuid`, id))
		if errors.Is(err, pgx.ErrNoRows) {
			return httpx.NewProblem(http.StatusNotFound, CodeBlockNotFound, "capacity block not found")
		}
		return err
	})
	return out, err
}

// UpdateCapacityBlock changes a block's priority, condition or active flag
// under optimistic versioning. Runs and moves read active blocks when they
// start, so the change applies to the next run or move. Jobs already placed
// stay where they are; those booked after from on the block's resource that
// the active blocks now keep out (a job inside a block it no longer matches,
// or inside a block a higher priority now gives to another) are returned in
// affectedJobs and listed in the event, as a calendar change lists the jobs
// it no longer covers (AC-23.7). A replay of the same key returns the stored
// answer with replayed=true, even after the version has moved on.
func (s *Postgres) UpdateCapacityBlock(ctx context.Context, id string, version int, patch BlockPatch, from time.Time, idem Idem) (CapacityBlockUpdate, bool, error) {
	var out CapacityBlockUpdate
	replayed := false
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		// Lock first so a retry that raced the first request waits and replays.
		cur, err := scanBlock(tx.QueryRow(ctx, `select `+blockCols+` from scheduling_capacity_block where id = $1::uuid for update`, id))
		if errors.Is(err, pgx.ErrNoRows) {
			return httpx.NewProblem(http.StatusNotFound, CodeBlockNotFound, "capacity block not found")
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
			return httpx.NewProblem(http.StatusPreconditionFailed, CodeBlockVersion,
				fmt.Sprintf("capacity block was changed by another user; current version is %d", cur.Version))
		}
		if patch.Priority != nil {
			cur.Priority = *patch.Priority
		}
		if patch.Active != nil {
			cur.Active = *patch.Active
		}
		if patch.Condition != nil {
			cur.Condition = *patch.Condition
		}
		condition, err := json.Marshal(cur.Condition)
		if err != nil {
			return err
		}
		upd, err := scanBlock(tx.QueryRow(ctx, `update scheduling_capacity_block set priority = $2, active = $3, condition = $4, version = version + 1
			where id = $1::uuid returning `+blockCols, id, cur.Priority, cur.Active, condition))
		if err != nil {
			return err
		}
		jobs, err := blockedOutJobs(ctx, tx, upd.ResourceID, from)
		if err != nil {
			return err
		}
		out = CapacityBlockUpdate{CapacityBlock: upd, AffectedJobs: jobs}
		refs := make([]string, 0, len(jobs))
		for _, j := range jobs {
			refs = append(refs, j.JobID)
		}
		if err := enqueue(ctx, tx, "capacityblock", upd.ID, schedevents.CapacityBlockUpdated, schedevents.CapacityBlockUpdatedData{
			BlockID: upd.ID, ResourceID: upd.ResourceID, Code: upd.Code, Priority: upd.Priority, Active: upd.Active, Condition: upd.Condition, Version: upd.Version,
			AffectedJobs: refs}); err != nil {
			return err
		}
		if idem.Key != "" {
			return remember(ctx, tx, idem, http.StatusOK, out)
		}
		return nil
	})
	if err != nil {
		return CapacityBlockUpdate{}, false, mapWriteErr(err, CodeIdempotencyReuse, "a concurrent request used the same Idempotency-Key; retry")
	}
	return out, replayed, nil
}

// blockedOutJobs lists the finite jobs booked on the resource after from
// whose booking lies in open capacity but outside the capacity the active
// blocks leave them (BR-23.15, domain.Placeable): the jobs a block change
// put on the wrong side of a block. Jobs a calendar or downtime no longer
// covers are left to those changes. The part of a booking before from does
// not count (at most 500, by start).
func blockedOutJobs(ctx context.Context, tx pgx.Tx, resourceID string, from time.Time) ([]AffectedJob, error) {
	res, err := scanResource(tx.QueryRow(ctx, `select `+resourceCols+` from scheduling_resource where id = $1::uuid`, resourceID), nil)
	if err != nil {
		return nil, err
	}
	loc, err := time.LoadLocation(res.TimeZone)
	if err != nil {
		return nil, err
	}
	type segment struct {
		job        string
		start, end time.Time
		attrs      string
	}
	var segs []segment
	rows, err := tx.Query(ctx, `select j.job_id::text, j.start_at, j.end_at, o.attributes::text
		from scheduled_job j join scheduling_operation o on o.tenant_id = j.tenant_id and o.id = j.job_id
		where j.resource_id = $1::uuid and j.finite and j.end_at > $2`, resourceID, from)
	if err != nil {
		return nil, err
	}
	hi := from
	for rows.Next() {
		var sg segment
		if err := rows.Scan(&sg.job, &sg.start, &sg.end, &sg.attrs); err != nil {
			rows.Close()
			return nil, err
		}
		if sg.start.Before(from) {
			sg.start = from
		}
		if sg.end.After(hi) {
			hi = sg.end
		}
		segs = append(segs, sg)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(segs) == 0 {
		return []AffectedJob{}, nil
	}
	// A day either side so the local days straddling the bookings are whole.
	start := from.Add(-24 * time.Hour)
	days := int(hi.Sub(from)/(24*time.Hour)) + 3
	rows, err = tx.Query(ctx, `select `+blockCols+` from scheduling_capacity_block where active and resource_id = $1::uuid`, resourceID)
	if err != nil {
		return nil, err
	}
	var blocks []domain.CapacityBlock
	for rows.Next() {
		b, err := scanBlock(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		db, err := b.expand(loc, start, days)
		if err != nil {
			rows.Close()
			return nil, err
		}
		blocks = append(blocks, db)
	}
	rows.Close()
	if err := rows.Err(); err != nil || len(blocks) == 0 {
		return []AffectedJob{}, err
	}
	ovs, err := loadOverrides(ctx, tx, resourceID)
	if err != nil {
		return nil, err
	}
	open := domain.OpenWithOverrides(loc, res.Shifts, ovs[resourceID], start, days)
	placeable := map[string][]domain.Interval{}
	seen := map[string]bool{}
	jobIDs := []string{}
	for _, sg := range segs {
		if seen[sg.job] || domain.OutsideOpen(open, sg.start, sg.end) {
			continue
		}
		iv, ok := placeable[sg.attrs]
		if !ok {
			var attrs map[string]string
			if err := json.Unmarshal([]byte(sg.attrs), &attrs); err != nil {
				return nil, err
			}
			iv = domain.Placeable(open, blocks, attrs)
			placeable[sg.attrs] = iv
		}
		if domain.OutsideOpen(iv, sg.start, sg.end) {
			seen[sg.job] = true
			jobIDs = append(jobIDs, sg.job)
		}
	}
	return affectedByID(ctx, tx, jobIDs)
}

// UpdateNetwork renames a network or ends it on a date, under optimistic
// versioning. Runs and moves read the networks active on the run date, so an
// ended network stops constraining the next run; jobs already placed stay
// where they are.
func (s *Postgres) UpdateNetwork(ctx context.Context, id string, version int, patch NetworkPatch, idem Idem) (Network, bool, error) {
	var out Network
	replayed := false
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var activeFrom string
		var curVersion int
		err := tx.QueryRow(ctx, `select active_from::text, version from scheduling_resource_network where id = $1::uuid for update`, id).Scan(&activeFrom, &curVersion)
		if errors.Is(err, pgx.ErrNoRows) {
			return httpx.NewProblem(http.StatusNotFound, CodeNetworkNotFound, "resource network not found")
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
		if curVersion != version {
			return httpx.NewProblem(http.StatusPreconditionFailed, CodeNetworkVersion,
				fmt.Sprintf("resource network was changed by another user; current version is %d", curVersion))
		}
		if patch.ActiveTo != nil && *patch.ActiveTo < activeFrom {
			return httpx.NewProblem(http.StatusUnprocessableEntity, CodeNetworkEnd,
				fmt.Sprintf("activeTo must be on or after the network's start date %s", activeFrom))
		}
		if _, err := tx.Exec(ctx, `update scheduling_resource_network set name = coalesce($2, name), active_to = coalesce($3::date, active_to), version = version + 1
			where id = $1::uuid`, id, patch.Name, patch.ActiveTo); err != nil {
			return err
		}
		nets, err := loadNetworks(ctx, tx, id)
		if err != nil {
			return err
		}
		out = nets[0]
		if err := enqueue(ctx, tx, "network", out.ID, schedevents.NetworkUpdated, schedevents.NetworkUpdatedData{
			NetworkID: out.ID, Code: out.Code, Name: out.Name, ActiveFrom: out.ActiveFrom, ActiveTo: out.ActiveTo, Version: out.Version}); err != nil {
			return err
		}
		if idem.Key != "" {
			return remember(ctx, tx, idem, http.StatusOK, out)
		}
		return nil
	})
	if err != nil {
		return Network{}, false, mapWriteErr(err, CodeIdempotencyReuse, "a concurrent request used the same Idempotency-Key; retry")
	}
	return out, replayed, nil
}
