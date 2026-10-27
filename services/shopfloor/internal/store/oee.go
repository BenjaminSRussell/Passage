package store

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/shopfloor/internal/domain"
	sfevents "github.com/BenjaminSRussell/passage/services/shopfloor/internal/events"
)

const oeeBucketSource = "oee-bucket"

// OEERow is one computed bucket of a resource, ready to store.
type OEERow struct {
	ResourceID        string
	IdealCycleSeconds decimal.Decimal
	Bucket            domain.OEEBucket
}

// OEERollup is one rollup window: every bucket computed for the resources over
// [From, To). Stored buckets of those resources in the window that were not
// computed again (their planned time is gone) are removed.
type OEERollup struct {
	ResourceIDs   []string
	BucketMinutes int
	From, To      time.Time
	Rows          []OEERow
}

// OEERollupResult counts what a rollup wrote.
type OEERollupResult struct {
	BucketMinutes int `json:"bucketMinutes"`
	Computed      int `json:"computed"`
	Changed       int `json:"changed"`
	Removed       int `json:"removed"`
}

func bucketKey(resourceID string, start time.Time) string {
	return fmt.Sprintf("%s|%d", resourceID, start.UnixMicro())
}

// SaveOEEBuckets upserts the rollup's buckets by (resource, width, start)
// (FR-25.10, PRD job "OEE rollup"). A bucket whose stored values are unchanged
// is not rewritten and publishes nothing, so running the same window again is a
// no-op; a new or changed bucket publishes oeebucket.updated, and a removed one
// oeebucket.removed, in the same transaction.
func (s *Postgres) SaveOEEBuckets(ctx context.Context, r OEERollup) (OEERollupResult, error) {
	out := OEERollupResult{BucketMinutes: r.BucketMinutes, Computed: len(r.Rows)}
	if len(r.ResourceIDs) == 0 {
		return out, nil
	}
	p, _ := tenant.FromContext(ctx)
	n := len(r.Rows)
	var (
		resources = make([]string, 0, n)
		starts    = make([]time.Time, 0, n)
		planned   = make([]string, 0, n)
		run       = make([]string, 0, n)
		down      = make([]string, 0, n)
		ideal     = make([]string, 0, n)
		total     = make([]int64, 0, n)
		good      = make([]int64, 0, n)
		avail     = make([]string, 0, n)
		perf      = make([]string, 0, n)
		quality   = make([]string, 0, n)
		oee       = make([]string, 0, n)
		over      = make([]bool, 0, n)
		sources   = make([]int32, 0, n)
		payloads  = make(map[string]sfevents.OEEBucketUpdatedData, n)
	)
	for _, row := range r.Rows {
		b := row.Bucket
		pm, rm := b.PlannedMinutes.Round(4), b.RunMinutes.Round(4)
		dm := pm.Sub(rm)
		resources = append(resources, row.ResourceID)
		starts = append(starts, b.Start.UTC())
		planned, run, down = append(planned, pm.String()), append(run, rm.String()), append(down, dm.String())
		ideal = append(ideal, row.IdealCycleSeconds.String())
		total, good = append(total, b.TotalCount), append(good, b.GoodCount)
		avail, perf = append(avail, b.Availability.String()), append(perf, b.Performance.String())
		quality, oee = append(quality, b.Quality.String()), append(oee, b.OEE.OEE.String())
		over, sources = append(over, b.OverRate), append(sources, int32(b.SourceEvents))
		payloads[bucketKey(row.ResourceID, b.Start)] = sfevents.OEEBucketUpdatedData{
			ResourceID: row.ResourceID, BucketMinutes: r.BucketMinutes, BucketStart: b.Start.UTC(),
			PlannedMinutes: pm, RunMinutes: rm, DownMinutes: dm, TotalCount: b.TotalCount, GoodCount: b.GoodCount,
			Availability: b.Availability, Performance: b.Performance, Quality: b.Quality, OEE: b.OEE.OEE, OverRate: b.OverRate,
		}
	}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `insert into oee_bucket (tenant_id, resource_id, bucket_minutes, bucket_start, planned_minutes, run_minutes,
				down_minutes, ideal_cycle_s, total_count, good_count, availability, performance, quality, oee, over_rate, source_events)
			select $1, u.r, $2, u.b, u.pm, u.rm, u.dm, u.ic, u.tc, u.gc, u.a, u.p, u.q, u.o, u.ov, u.se
			from unnest($3::uuid[], $4::timestamptz[], $5::numeric[], $6::numeric[], $7::numeric[], $8::numeric[], $9::bigint[],
				$10::bigint[], $11::numeric[], $12::numeric[], $13::numeric[], $14::numeric[], $15::boolean[], $16::int[])
				as u(r, b, pm, rm, dm, ic, tc, gc, a, p, q, o, ov, se)
			on conflict (tenant_id, resource_id, bucket_minutes, bucket_start) do update set
				planned_minutes = excluded.planned_minutes, run_minutes = excluded.run_minutes, down_minutes = excluded.down_minutes,
				ideal_cycle_s = excluded.ideal_cycle_s, total_count = excluded.total_count, good_count = excluded.good_count,
				availability = excluded.availability, performance = excluded.performance, quality = excluded.quality,
				oee = excluded.oee, over_rate = excluded.over_rate, source_events = excluded.source_events, computed_at = now()
			where (oee_bucket.planned_minutes, oee_bucket.run_minutes, oee_bucket.ideal_cycle_s, oee_bucket.total_count,
					oee_bucket.good_count, oee_bucket.availability, oee_bucket.performance, oee_bucket.quality, oee_bucket.oee,
					oee_bucket.over_rate, oee_bucket.source_events)
				is distinct from (excluded.planned_minutes, excluded.run_minutes, excluded.ideal_cycle_s, excluded.total_count,
					excluded.good_count, excluded.availability, excluded.performance, excluded.quality, excluded.oee,
					excluded.over_rate, excluded.source_events)
			returning resource_id::text, bucket_start`,
			p.TenantID, r.BucketMinutes, resources, starts, planned, run, down, ideal, total, good, avail, perf, quality, oee, over, sources)
		if err != nil {
			return err
		}
		changed, err := scanBucketKeys(rows)
		if err != nil {
			return err
		}
		for _, k := range changed {
			data := payloads[bucketKey(k.resourceID, k.start)]
			if err := enqueue(ctx, tx, oeeBucketSource, k.resourceID, sfevents.OEEBucketUpdated, p.TenantID, data); err != nil {
				return err
			}
		}
		out.Changed = len(changed)

		rows, err = tx.Query(ctx, `delete from oee_bucket o
			where o.resource_id = any($1::uuid[]) and o.bucket_minutes = $2 and o.bucket_start >= $3 and o.bucket_start < $4
				and not exists (select 1 from unnest($5::uuid[], $6::timestamptz[]) as k(r, b) where k.r = o.resource_id and k.b = o.bucket_start)
			returning o.resource_id::text, o.bucket_start`,
			r.ResourceIDs, r.BucketMinutes, r.From, r.To, resources, starts)
		if err != nil {
			return err
		}
		removed, err := scanBucketKeys(rows)
		if err != nil {
			return err
		}
		for _, k := range removed {
			if err := enqueue(ctx, tx, oeeBucketSource, k.resourceID, sfevents.OEEBucketRemoved, p.TenantID, sfevents.OEEBucketRemovedData{
				ResourceID: k.resourceID, BucketMinutes: r.BucketMinutes, BucketStart: k.start.UTC(),
			}); err != nil {
				return err
			}
		}
		out.Removed = len(removed)
		return nil
	})
	if err != nil {
		return OEERollupResult{}, err
	}
	return out, nil
}

type storedBucketKey struct {
	resourceID string
	start      time.Time
}

func scanBucketKeys(rows pgx.Rows) ([]storedBucketKey, error) {
	defer rows.Close()
	var out []storedBucketKey
	for rows.Next() {
		var k storedBucketKey
		if err := rows.Scan(&k.resourceID, &k.start); err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, rows.Err()
}
