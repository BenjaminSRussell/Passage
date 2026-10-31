package store

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/shopfloor/internal/domain"
	sfevents "github.com/BenjaminSRussell/passage/services/shopfloor/internal/events"
)

// AutoClosed is one clock-in the auto clock-out sweep closed.
type AutoClosed struct {
	RegistrationID string    `json:"registrationId"`
	WorkerID       string    `json:"workerId"`
	ClockInID      string    `json:"clockInId"`
	ClockedInAt    time.Time `json:"clockedInAt"`
	ClockedOutAt   time.Time `json:"clockedOutAt"`
}

// AutoClockOut closes the clock-ins that are still open afterHours after they were
// punched (D2h). For each such worker it writes a clock-out at clock-in plus
// afterHours, flagged auto-clock-out for supervisor review, and publishes
// dev.passage.shopfloor.registration.posted.v1 in the same transaction. The worker
// rows are locked with skip locked, so concurrent sweeps close each shift once, and
// the insert re-checks that no later clock punch exists, so a clock-out that arrives
// meanwhile wins. Workers who were deactivated are closed too: an open shift must not
// stay open forever. At most limit shifts are closed per call, oldest first.
func (s *Postgres) AutoClockOut(ctx context.Context, now time.Time, afterHours, limit int) ([]AutoClosed, error) {
	p, _ := tenant.FromContext(ctx)
	out := []AutoClosed{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select w.id::text, lp.id::text, lp.started_at
			from worker w
			cross join lateral (select t.id, t.kind, t.started_at from time_registration t
				where t.tenant_id = w.tenant_id and t.worker_id = w.id and t.kind in ('clock-in', 'clock-out')
				order by t.started_at desc, t.id desc limit 1) lp
			where w.tenant_id = $1 and lp.kind = 'clock-in' and lp.started_at <= $2
			order by lp.started_at, w.id
			limit $3
			for update of w skip locked`, p.TenantID, now.Add(-time.Duration(afterHours)*time.Hour), limit)
		if err != nil {
			return err
		}
		var open []domain.OpenShift
		for rows.Next() {
			var o domain.OpenShift
			if err := rows.Scan(&o.WorkerID, &o.RegistrationID, &o.ClockedInAt); err != nil {
				rows.Close()
				return err
			}
			open = append(open, o)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		for _, c := range domain.PlanAutoClockOut(open, now, afterHours) {
			id := ids.New()
			tag, err := tx.Exec(ctx, `insert into time_registration (tenant_id, id, worker_id, kind, started_at, flags, note)
				select $1, $2, $3, 'clock-out', $4, array[$5]::text[], 'closed by the auto clock-out sweep'
				where not exists (select 1 from time_registration t where t.tenant_id = $1 and t.worker_id = $3
					and t.kind in ('clock-in', 'clock-out') and (t.started_at, t.id) > ($6, $7::uuid))`,
				p.TenantID, id, c.WorkerID, c.ClockOutAt, domain.FlagAutoClockOut, c.ClockedInAt, c.ClockInID)
			if err != nil {
				return err
			}
			if tag.RowsAffected() == 0 {
				continue
			}
			if err := enqueue(ctx, tx, "registration", id, sfevents.RegistrationPosted, p.TenantID, sfevents.RegistrationPostedData{
				RegistrationID: id, WorkerID: c.WorkerID, Kind: domain.KindClockOut, Flags: []string{domain.FlagAutoClockOut},
			}); err != nil {
				return err
			}
			out = append(out, AutoClosed{RegistrationID: id, WorkerID: c.WorkerID, ClockInID: c.ClockInID, ClockedInAt: c.ClockedInAt, ClockedOutAt: c.ClockOutAt})
		}
		return nil
	})
	return out, err
}

// AutoClockOutReview is an auto clock-out for the supervisor's review list.
type AutoClockOutReview struct {
	RegistrationID string     `json:"registrationId"`
	WorkerID       string     `json:"workerId"`
	WorkerName     string     `json:"workerName"`
	ClockedInAt    *time.Time `json:"clockedInAt"`
	ClockedOutAt   time.Time  `json:"clockedOutAt"`
	ClosedAt       time.Time  `json:"closedAt"`
	// Reviewed tells that a supervisor confirmed or corrected the clock-out; until
	// then its day carries missing-clock-out and cannot be approved (BR-25.3).
	Reviewed bool `json:"reviewed"`
}

// ListAutoClockOuts lists the clock-outs the sweep wrote that were received in
// [start, end), newest first, optionally for one worker.
func (s *Postgres) ListAutoClockOuts(ctx context.Context, workerID string, start, end time.Time, limit int) ([]AutoClockOutReview, error) {
	p, _ := tenant.FromContext(ctx)
	out := []AutoClockOutReview{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select r.id::text, r.worker_id::text, w.name, ci.started_at, r.started_at, r.server_received_at,
				exists (select 1 from registration_correction c where c.tenant_id = r.tenant_id and c.registration_id = r.id)
			from time_registration r
			join worker w on w.tenant_id = r.tenant_id and w.id = r.worker_id
			left join lateral (select t.started_at from time_registration t
				where t.tenant_id = r.tenant_id and t.worker_id = r.worker_id and t.kind = 'clock-in' and t.started_at <= r.started_at
				order by t.started_at desc, t.id desc limit 1) ci on true
			where r.tenant_id = $1 and r.kind = 'clock-out' and $2 = any(r.flags)
				and r.server_received_at >= $3 and r.server_received_at < $4 and ($5 = '' or r.worker_id::text = $5)
			order by r.server_received_at desc, r.id limit $6`,
			p.TenantID, domain.FlagAutoClockOut, start, end, workerID, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var a AutoClockOutReview
			if err := rows.Scan(&a.RegistrationID, &a.WorkerID, &a.WorkerName, &a.ClockedInAt, &a.ClockedOutAt, &a.ClosedAt, &a.Reviewed); err != nil {
				return err
			}
			out = append(out, a)
		}
		return rows.Err()
	})
	return out, err
}
