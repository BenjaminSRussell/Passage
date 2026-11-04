package store

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/scheduling/internal/domain"
)

// MaxDispatchRows bounds the lines of one dispatch list; the rest is flagged truncated.
const MaxDispatchRows = 1000

// DispatchStore reads the bookings behind a dispatch list (PRD 4.23.15).
type DispatchStore interface {
	DispatchRows(ctx context.Context, resourceID string, from, to time.Time) ([]domain.DispatchRow, bool, error)
}

// DispatchRows lists the primary and secondary bookings of a resource that
// overlap [from, to), with the setup group and changeover of the job. It is
// read only; the numbering and clipping are domain.DispatchLines.
func (s *Postgres) DispatchRows(ctx context.Context, resourceID string, from, to time.Time) ([]domain.DispatchRow, bool, error) {
	out := []domain.DispatchRow{}
	truncated := false
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var one int
		if err := tx.QueryRow(ctx, `select 1 from scheduling_resource where id = $1`, resourceID).Scan(&one); errors.Is(err, pgx.ErrNoRows) {
			return httpx.NewProblem(http.StatusNotFound, CodeResourceNotFound, "resource not found")
		} else if err != nil {
			return err
		}
		// The window rows are cut to the limit before the joins, so the
		// operation and primary-booking lookups stay index probes however many
		// bookings the resource holds outside the window.
		rows, err := tx.Query(ctx, `select w.job_id::text, o.order_id::text, o.oper_number, w.segment_no, w.role, w.start_at, w.end_at,
				case when w.role = 'primary' then coalesce(o.setup_group, '') else '' end,
				case when w.role = 'primary' then coalesce(p.changeover_minutes, 0) else 0 end,
				w.finite, coalesce(p.firmed, false), coalesce(p.late_minutes, 0)
			from (
				select tenant_id, job_id, segment_no, 'primary' as role, start_at, end_at, finite
				from scheduled_job where resource_id = $1 and end_at > $2 and start_at < $3
				union all
				select tenant_id, job_id, segment_no, 'secondary', start_at, end_at, finite
				from scheduled_job_secondary where resource_id = $1 and end_at > $2 and start_at < $3
				order by start_at, end_at, job_id, segment_no, role
				limit `+strconv.Itoa(MaxDispatchRows+1)+`
			) w
			join scheduling_operation o on o.tenant_id = w.tenant_id and o.id = w.job_id
			left join scheduled_job p on p.tenant_id = w.tenant_id and p.job_id = w.job_id and p.segment_no = w.segment_no
			order by w.start_at, w.end_at, w.job_id, w.segment_no, w.role`, resourceID, from, to)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var r domain.DispatchRow
			if err := rows.Scan(&r.JobID, &r.OrderID, &r.OperNumber, &r.Segment, &r.Role, &r.Start, &r.End,
				&r.SetupGroup, &r.ChangeoverMins, &r.Finite, &r.Firmed, &r.LateMinutes); err != nil {
				return err
			}
			out = append(out, r)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		if len(out) > MaxDispatchRows {
			out, truncated = out[:MaxDispatchRows], true
		}
		return nil
	})
	return out, truncated, err
}
