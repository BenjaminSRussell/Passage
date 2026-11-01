package store

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/scheduling/internal/domain"
)

// MaxLoadDays bounds a daily load window and a load detail range.
const MaxLoadDays = 93

// MaxLoadJobs bounds the jobs one drill-down returns; the rest is flagged truncated.
const MaxLoadJobs = 1000

// DailyLoadFilter selects resources and the days of the daily view: Days
// local days of each resource's calendar from the date of From.
type DailyLoadFilter struct {
	ResourceIDs []string
	From        time.Time
	Days        int
}

// LoadJob is one booked segment on a resource inside a load period (PRD
// 4.23.14 Capacity load detail). Role is "primary" or "secondary" (BR-23.4);
// MinutesInRange is the part of the segment inside the asked range.
type LoadJob struct {
	JobID          string    `json:"jobId"`
	OrderID        string    `json:"orderId"`
	OperNumber     int       `json:"operNumber"`
	Segment        int       `json:"segment"`
	Role           string    `json:"role"`
	Start          time.Time `json:"start"`
	End            time.Time `json:"end"`
	MinutesInRange int       `json:"minutesInRange"`
	Finite         bool      `json:"finite"`
	Firmed         bool      `json:"firmed"`
	LateMinutes    int       `json:"lateMinutes"`
}

// LoadStore is the daily load view and its drill-down to booked jobs.
type LoadStore interface {
	DailyCapacityLoad(ctx context.Context, f DailyLoadFilter) ([]domain.DayLoad, error)
	LoadJobs(ctx context.Context, resourceID string, from, to time.Time) ([]LoadJob, bool, error)
}

// DailyCapacityLoad returns booked and open minutes per local day of each
// resource's calendar time zone (FR-23.9, BR-23.7), so a night shift is not
// split at 00:00 UTC.
func (s *Postgres) DailyCapacityLoad(ctx context.Context, f DailyLoadFilter) ([]domain.DayLoad, error) {
	out := []domain.DayLoad{}
	y, m, d := f.From.Date()
	date := time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
	// Local days of any zone (UTC-12 to UTC+14) lie within a day either side
	// of the same UTC dates; DailyLoad clips the bookings to each local day.
	from, to := date.AddDate(0, 0, -1), date.AddDate(0, 0, f.Days+1)
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		resources, byResource, err := loadResourcesBooked(ctx, tx, f.ResourceIDs, from, to)
		if err != nil {
			return err
		}
		for _, r := range resources {
			out = append(out, domain.DailyLoad(r, domain.DayIntervals(r, date, f.Days), byResource[r.ID], date, f.Days)...)
		}
		return nil
	})
	return out, err
}

// LoadJobs lists the primary and secondary bookings of a resource that overlap
// [from, to), earliest first. It is the drill-down behind a load cell; the
// sum of MinutesInRange equals the cell's
// booked minutes when the segments of a resource do not overlap.
func (s *Postgres) LoadJobs(ctx context.Context, resourceID string, from, to time.Time) ([]LoadJob, bool, error) {
	out := []LoadJob{}
	truncated := false
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var one int
		if err := tx.QueryRow(ctx, `select 1 from scheduling_resource where id = $1`, resourceID).Scan(&one); errors.Is(err, pgx.ErrNoRows) {
			return httpx.NewProblem(http.StatusNotFound, CodeResourceNotFound, "resource not found")
		} else if err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `select j.job_id::text, j.order_id::text, j.oper_number, j.segment_no, 'primary', j.start_at, j.end_at,
				j.finite, j.firmed, j.late_minutes
			from scheduled_job j where j.resource_id = $1 and j.end_at > $2 and j.start_at < $3
			union all
			select s.job_id::text, o.order_id::text, o.oper_number, s.segment_no, 'secondary', s.start_at, s.end_at,
				s.finite, coalesce(p.firmed, false), coalesce(p.late_minutes, 0)
			from scheduled_job_secondary s join scheduling_operation o on o.tenant_id = s.tenant_id and o.id = s.job_id
			left join scheduled_job p on p.tenant_id = s.tenant_id and p.job_id = s.job_id and p.segment_no = s.segment_no
			where s.resource_id = $1 and s.end_at > $2 and s.start_at < $3
			order by 6, 1, 4 limit 1001`, resourceID, from, to)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var j LoadJob
			if err := rows.Scan(&j.JobID, &j.OrderID, &j.OperNumber, &j.Segment, &j.Role, &j.Start, &j.End, &j.Finite, &j.Firmed, &j.LateMinutes); err != nil {
				return err
			}
			j.MinutesInRange = domain.ClippedMinutes(j.Start, j.End, from, to)
			out = append(out, j)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		if len(out) > MaxLoadJobs {
			out, truncated = out[:MaxLoadJobs], true
		}
		return nil
	})
	return out, truncated, err
}
