package store

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	schedevents "github.com/BenjaminSRussell/passage/services/scheduling/internal/events"
)

// CodeCleanoutCountSeen is returned when the counter moved since the caller
// read it, so the reset would throw away counts the planner never saw.
const CodeCleanoutCountSeen = "SCHED-0148"

// CleanoutCounter is the batches and run minutes a resource has run since its
// last cleanout (FR-23.15). A resource that never ran a campaign reads zero.
type CleanoutCounter struct {
	ResourceID     string     `json:"resourceId"`
	ResourceCode   string     `json:"resourceCode"`
	ResourceName   string     `json:"resourceName"`
	BatchesSince   int        `json:"batchesSince"`
	MinutesSince   int        `json:"minutesSince"`
	LastCampaignID *string    `json:"lastCampaignId"`
	UpdatedAt      *time.Time `json:"updatedAt"`
	LastResetAt    *time.Time `json:"lastResetAt"`
}

// CleanoutReset is one manual reset of a counter, kept for the record.
type CleanoutReset struct {
	ID              string    `json:"id"`
	ResourceID      string    `json:"resourceId"`
	PreviousBatches int       `json:"previousBatches"`
	PreviousMinutes int       `json:"previousMinutes"`
	Reason          string    `json:"reason"`
	ResetBy         string    `json:"resetBy"`
	CreatedAt       time.Time `json:"createdAt"`
}

// CleanoutCounterDetail is a counter with its latest resets, newest first (at
// most 50).
type CleanoutCounterDetail struct {
	CleanoutCounter
	Resets []CleanoutReset `json:"resets"`
}

// CleanoutResetInput resets a counter. SeenBatches and SeenMinutes are the
// counts the planner saw; a counter that moved since is refused.
type CleanoutResetInput struct {
	ResourceID  string
	SeenBatches int
	SeenMinutes int
	Reason      string
}

// CleanoutResetResult is the stored reset, the counter after it and the planned
// campaigns it left stale.
type CleanoutResetResult struct {
	CleanoutReset
	Counter          CleanoutCounterDetail `json:"counter"`
	StaleCampaignIDs []string              `json:"staleCampaignIds"`
}

// CleanoutStore is the cleanout counter part of the Store.
type CleanoutStore interface {
	ListCleanoutCounters(ctx context.Context) ([]CleanoutCounter, error)
	GetCleanoutCounter(ctx context.Context, resourceID string) (CleanoutCounterDetail, error)
	ResetCleanoutCounter(ctx context.Context, in CleanoutResetInput, idem Idem) (CleanoutResetResult, bool, error)
}

const counterSelect = `select r.id::text, r.code, r.name, coalesce(c.batches_since, 0), coalesce(c.minutes_since, 0), c.last_campaign_id::text, c.updated_at,
	(select max(x.created_at) from scheduling_cleanout_reset x where x.resource_id = r.id)
	from scheduling_resource r left join scheduling_cleanout_counter c on c.tenant_id = r.tenant_id and c.resource_id = r.id`

func scanCounter(row pgx.Row) (CleanoutCounter, error) {
	var c CleanoutCounter
	err := row.Scan(&c.ResourceID, &c.ResourceCode, &c.ResourceName, &c.BatchesSince, &c.MinutesSince, &c.LastCampaignID, &c.UpdatedAt, &c.LastResetAt)
	return c, err
}

// ListCleanoutCounters returns the counter of every resource, the ones with
// the most batches since their last cleanout first.
func (s *Postgres) ListCleanoutCounters(ctx context.Context) ([]CleanoutCounter, error) {
	out := []CleanoutCounter{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, counterSelect+` order by coalesce(c.batches_since, 0) desc, coalesce(c.minutes_since, 0) desc, r.code limit 1000`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			c, err := scanCounter(rows)
			if err != nil {
				return err
			}
			out = append(out, c)
		}
		return rows.Err()
	})
	return out, err
}

const resetCols = "id::text, resource_id::text, previous_batches, previous_minutes, reason, reset_by, created_at"

func scanReset(row pgx.Row) (CleanoutReset, error) {
	var r CleanoutReset
	err := row.Scan(&r.ID, &r.ResourceID, &r.PreviousBatches, &r.PreviousMinutes, &r.Reason, &r.ResetBy, &r.CreatedAt)
	return r, err
}

func counterDetail(ctx context.Context, tx pgx.Tx, resourceID string) (CleanoutCounterDetail, error) {
	c, err := scanCounter(tx.QueryRow(ctx, counterSelect+` where r.id = $1::uuid`, resourceID))
	if errors.Is(err, pgx.ErrNoRows) {
		return CleanoutCounterDetail{}, httpx.NewProblem(http.StatusNotFound, CodeResourceNotFound, "resource not found")
	}
	if err != nil {
		return CleanoutCounterDetail{}, err
	}
	out := CleanoutCounterDetail{CleanoutCounter: c, Resets: []CleanoutReset{}}
	rows, err := tx.Query(ctx, `select `+resetCols+` from scheduling_cleanout_reset where resource_id = $1::uuid order by created_at desc, id limit 50`, resourceID)
	if err != nil {
		return CleanoutCounterDetail{}, err
	}
	defer rows.Close()
	for rows.Next() {
		r, err := scanReset(rows)
		if err != nil {
			return CleanoutCounterDetail{}, err
		}
		out.Resets = append(out.Resets, r)
	}
	return out, rows.Err()
}

// GetCleanoutCounter returns a resource's counter with its latest resets.
func (s *Postgres) GetCleanoutCounter(ctx context.Context, resourceID string) (CleanoutCounterDetail, error) {
	var out CleanoutCounterDetail
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		out, err = counterDetail(ctx, tx, resourceID)
		return err
	})
	return out, err
}

// ResetCleanoutCounter sets a resource's counter to zero after a cleanout done
// outside the plan (FR-23.15). The counter has no version, so the caller's
// seen counts stand in for one: the counter row is locked first (the same lock
// a campaign start and cancel take), then compared, so a reset and a start are
// never interleaved and a counter that moved since the planner read it is
// refused (SCHED-0148). Taking the lock before the idempotency lookup also
// serialises two requests with one key, so the second replays the first.
// Campaigns are not changed: the planned ones whose plan carries in a
// non-zero counter can no longer start (SCHED-0123, their carry-in no longer
// matches) and are listed and kept on the reset; a planned campaign sequenced
// from a zero counter is still valid and is not listed. A replay of the same
// key returns the stored reset and its list with created=false.
func (s *Postgres) ResetCleanoutCounter(ctx context.Context, in CleanoutResetInput, idem Idem) (CleanoutResetResult, bool, error) {
	var out CleanoutResetResult
	created := false
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var n int
		if err := tx.QueryRow(ctx, `select count(*) from scheduling_resource where id = $1::uuid`, in.ResourceID).Scan(&n); err != nil {
			return err
		}
		if n == 0 {
			return httpx.NewProblem(http.StatusNotFound, CodeResourceNotFound, "resource not found")
		}
		cur, err := cleanoutCounter(ctx, tx, in.ResourceID, true)
		if err != nil {
			return err
		}
		id, found, err := replayRow(ctx, tx, "scheduling_cleanout_reset", idem)
		if err != nil {
			return err
		}
		var reset CleanoutReset
		stale := []string{}
		if found {
			if err := tx.QueryRow(ctx, `select `+resetCols+`, stale_campaign_ids::text[] from scheduling_cleanout_reset where id = $1::uuid`, id).Scan(
				&reset.ID, &reset.ResourceID, &reset.PreviousBatches, &reset.PreviousMinutes, &reset.Reason, &reset.ResetBy, &reset.CreatedAt, &stale); err != nil {
				return err
			}
		} else {
			if cur.Batches != in.SeenBatches || cur.Minutes != in.SeenMinutes {
				return httpx.NewProblem(http.StatusConflict, CodeCleanoutCountSeen, fmt.Sprintf(
					"the resource has run %d batches and %d minutes since its last cleanout, not the %d and %d you saw; reload and reset again",
					cur.Batches, cur.Minutes, in.SeenBatches, in.SeenMinutes))
			}
			rows, err := tx.Query(ctx, `select id::text from scheduling_campaign where resource_id = $1::uuid and status = 'planned'
				and (coalesce((plan->'carryIn'->>'batches')::int, 0) <> 0 or coalesce((plan->'carryIn'->>'minutes')::int, 0) <> 0) order by id`, in.ResourceID)
			if err != nil {
				return err
			}
			for rows.Next() {
				var cid string
				if err := rows.Scan(&cid); err != nil {
					rows.Close()
					return err
				}
				stale = append(stale, cid)
			}
			rows.Close()
			if err := rows.Err(); err != nil {
				return err
			}
			actor, _ := tenant.FromContext(ctx)
			reset, err = scanReset(tx.QueryRow(ctx, `insert into scheduling_cleanout_reset (tenant_id, id, resource_id, previous_batches, previous_minutes, reason,
				reset_by, stale_campaign_ids, idempotency_key, idempotency_fingerprint) values ($1, $2, $3, $4, $5, $6, $7, $8::uuid[], $9, $10) returning `+resetCols,
				tenantOf(ctx), ids.New(), in.ResourceID, cur.Batches, cur.Minutes, in.Reason, actor.UserID, stale, nullable(idem.Key), nullable(idem.Fingerprint)))
			if err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `update scheduling_cleanout_counter set batches_since = 0, minutes_since = 0, last_campaign_id = null, updated_at = now()
				where resource_id = $1::uuid`, in.ResourceID); err != nil {
				return err
			}
			created = true
			if err := enqueue(ctx, tx, "cleanoutcounter", in.ResourceID, schedevents.CleanoutCounterReset, schedevents.CleanoutCounterResetData{
				ResetID: reset.ID, ResourceID: in.ResourceID, PreviousBatches: reset.PreviousBatches, PreviousMinutes: reset.PreviousMinutes,
				Reason: reset.Reason, StaleCampaignIDs: stale}); err != nil {
				return err
			}
		}
		detail, err := counterDetail(ctx, tx, in.ResourceID)
		if err != nil {
			return err
		}
		out = CleanoutResetResult{CleanoutReset: reset, Counter: detail, StaleCampaignIDs: stale}
		return nil
	})
	if err != nil {
		return CleanoutResetResult{}, false, mapWriteErr(err, CodeIdempotencyReuse, "a concurrent request used the same Idempotency-Key; retry")
	}
	return out, created, nil
}
