package store

import (
	"context"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/scheduling/internal/domain"
	schedevents "github.com/BenjaminSRussell/passage/services/scheduling/internal/events"
)

// Error codes of capacity overrides.
const (
	CodeOverrideNotFound = "SCHED-0130"
	maxAffectedJobs      = 500
)

// CapacityOverride closes (downtime) or opens (extra) a span of a resource's
// calendar (FR-23.1).
type CapacityOverride struct {
	ID         string    `json:"id"`
	ResourceID string    `json:"resourceId"`
	Kind       string    `json:"kind"`
	StartAt    time.Time `json:"start"`
	EndAt      time.Time `json:"end"`
	Reason     string    `json:"reason"`
	CreatedAt  time.Time `json:"createdAt"`
}

// AffectedJob is a booked job that overlaps a downtime (AC-23.7).
type AffectedJob struct {
	JobID      string    `json:"jobId"`
	OrderID    string    `json:"orderId"`
	OperNumber int       `json:"operNumber"`
	Start      time.Time `json:"start"`
	End        time.Time `json:"end"`
	Firmed     bool      `json:"firmed"`
}

// OverrideResult is a stored override with the jobs a downtime overlaps.
type OverrideResult struct {
	CapacityOverride
	AffectedJobs []AffectedJob `json:"affectedJobs"`
}

// OverrideStore is the capacity override part of the Store.
type OverrideStore interface {
	CreateCapacityOverride(ctx context.Context, in CapacityOverride, idem Idem) (OverrideResult, bool, error)
	ListCapacityOverrides(ctx context.Context, resourceID string, from, to time.Time) ([]CapacityOverride, error)
	DeleteCapacityOverride(ctx context.Context, resourceID, id string) error
}

const overrideCols = "id::text, resource_id::text, kind, start_at, end_at, reason, created_at"

func scanOverride(row pgx.Row) (CapacityOverride, error) {
	var o CapacityOverride
	err := row.Scan(&o.ID, &o.ResourceID, &o.Kind, &o.StartAt, &o.EndAt, &o.Reason, &o.CreatedAt)
	return o, err
}

// loadOverrides returns the overrides of one resource, or of every resource
// when resourceID is empty, keyed by resource id.
func loadOverrides(ctx context.Context, tx pgx.Tx, resourceID string) (map[string][]domain.CapacityOverride, error) {
	// A placed campaign's cleanout window closes the resource like a
	// downtime (BR-23.13): nothing else is planned into it.
	rows, err := tx.Query(ctx, `select resource_id::text, kind, start_at, end_at from scheduling_capacity_override
		where ($1 = '' or resource_id::text = $1)
		union all
		select resource_id::text, 'downtime', start_at, end_at from scheduling_campaign_cleanout
		where ($1 = '' or resource_id::text = $1) order by start_at`, resourceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]domain.CapacityOverride{}
	for rows.Next() {
		var rid string
		var o domain.CapacityOverride
		if err := rows.Scan(&rid, &o.Kind, &o.Start, &o.End); err != nil {
			return nil, err
		}
		out[rid] = append(out[rid], o)
	}
	return out, rows.Err()
}

func (s *Postgres) resourceOverrides(ctx context.Context, resourceID string) ([]domain.CapacityOverride, error) {
	var out []domain.CapacityOverride
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		m, err := loadOverrides(ctx, tx, resourceID)
		out = m[resourceID]
		return err
	})
	return out, err
}

func affectedJobs(ctx context.Context, tx pgx.Tx, o CapacityOverride) ([]AffectedJob, error) {
	out := []AffectedJob{}
	if o.Kind != domain.OverrideDowntime {
		return out, nil
	}
	// Start and end span every segment of the job, not only those inside the
	// downtime; a job's segments all lie on one primary resource, and a downtime on a
	// secondary resource affects the jobs that book it.
	rows, err := tx.Query(ctx, `select job_id::text, order_id::text, oper_number, min(start_at), max(end_at), bool_or(firmed)
		from scheduled_job where job_id in (
			select job_id from scheduled_job where resource_id = $1::uuid and start_at < $3 and end_at > $2
			union
			select job_id from scheduled_job_secondary where resource_id = $1::uuid and start_at < $3 and end_at > $2)
		group by job_id, order_id, oper_number order by min(start_at), oper_number limit $4`, o.ResourceID, o.StartAt, o.EndAt, maxAffectedJobs)
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

// CreateCapacityOverride stores a downtime or extra shift and, for a
// downtime, lists the booked jobs it overlaps (AC-23.7); the jobs stay where
// they are until the planner moves them or runs the schedule again. A replay
// returns the stored override with created=false.
func (s *Postgres) CreateCapacityOverride(ctx context.Context, in CapacityOverride, idem Idem) (OverrideResult, bool, error) {
	var out OverrideResult
	created := false
	err := s.loadTx(&ctx, func(tx pgx.Tx) error {
		id, found, err := replayRow(ctx, tx, "scheduling_capacity_override", idem)
		if err != nil {
			return err
		}
		if !found {
			var n int
			if err := tx.QueryRow(ctx, `select count(*) from scheduling_resource where id = $1::uuid`, in.ResourceID).Scan(&n); err != nil {
				return err
			}
			if n == 0 {
				return httpx.NewProblem(http.StatusNotFound, CodeResourceNotFound, "resource not found")
			}
			id = ids.New()
			trackResources(ctx, in.ResourceID)
			if _, err := tx.Exec(ctx, `insert into scheduling_capacity_override (tenant_id, id, resource_id, kind, start_at, end_at, reason, idempotency_key, idempotency_fingerprint)
				values ($1, $2, $3, $4, $5, $6, $7, $8, $9)`, tenantOf(ctx), id, in.ResourceID, in.Kind, in.StartAt, in.EndAt, in.Reason,
				nullable(idem.Key), nullable(idem.Fingerprint)); err != nil {
				return err
			}
			created = true
		}
		o, err := scanOverride(tx.QueryRow(ctx, `select `+overrideCols+` from scheduling_capacity_override where id = $1::uuid`, id))
		if err != nil {
			return err
		}
		jobs, err := affectedJobs(ctx, tx, o)
		if err != nil {
			return err
		}
		out = OverrideResult{CapacityOverride: o, AffectedJobs: jobs}
		if !created {
			return nil
		}
		refs := make([]string, 0, len(jobs))
		for _, j := range jobs {
			refs = append(refs, j.JobID)
		}
		return enqueue(ctx, tx, "capacityoverride", o.ID, schedevents.CapacityOverrideCreated, schedevents.CapacityOverrideData{
			OverrideID: o.ID, ResourceID: o.ResourceID, Kind: o.Kind, Start: rfc(o.StartAt), End: rfc(o.EndAt), Reason: o.Reason, AffectedJobs: refs})
	})
	if err != nil {
		return OverrideResult{}, false, mapWriteErr(err, CodeIdempotencyReuse, "a concurrent request used the same Idempotency-Key; retry")
	}
	return out, created, nil
}

// ListCapacityOverrides returns the overrides of a resource that overlap
// [from, to) (all of them when both are zero), by start.
func (s *Postgres) ListCapacityOverrides(ctx context.Context, resourceID string, from, to time.Time) ([]CapacityOverride, error) {
	out := []CapacityOverride{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var n int
		if err := tx.QueryRow(ctx, `select count(*) from scheduling_resource where id = $1::uuid`, resourceID).Scan(&n); err != nil {
			return err
		}
		if n == 0 {
			return httpx.NewProblem(http.StatusNotFound, CodeResourceNotFound, "resource not found")
		}
		rows, err := tx.Query(ctx, `select `+overrideCols+` from scheduling_capacity_override
			where resource_id = $1::uuid and ($2::timestamptz is null or end_at > $2) and ($3::timestamptz is null or start_at < $3)
			order by start_at, id limit 1000`, resourceID, nullTime(from), nullTime(to))
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			o, err := scanOverride(rows)
			if err != nil {
				return err
			}
			out = append(out, o)
		}
		return rows.Err()
	})
	return out, err
}

func nullTime(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}

// DeleteCapacityOverride removes an override; the calendar of its resource
// returns to what it was for later runs and moves.
func (s *Postgres) DeleteCapacityOverride(ctx context.Context, resourceID, id string) error {
	return s.loadTx(&ctx, func(tx pgx.Tx) error {
		o, err := scanOverride(tx.QueryRow(ctx, `delete from scheduling_capacity_override where id = $1::uuid and resource_id = $2::uuid returning `+overrideCols, id, resourceID))
		if err != nil {
			if err == pgx.ErrNoRows {
				return httpx.NewProblem(http.StatusNotFound, CodeOverrideNotFound, "capacity override not found")
			}
			return err
		}
		trackResources(ctx, o.ResourceID)
		return enqueue(ctx, tx, "capacityoverride", o.ID, schedevents.CapacityOverrideDeleted, schedevents.CapacityOverrideData{
			OverrideID: o.ID, ResourceID: o.ResourceID, Kind: o.Kind, Start: rfc(o.StartAt), End: rfc(o.EndAt), Reason: o.Reason, AffectedJobs: []string{}})
	})
}
