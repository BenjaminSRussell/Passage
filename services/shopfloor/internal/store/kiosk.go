package store

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
)

// CodeBadgeUnknown is PRD catalog SF-0001: the badge matches no active worker.
const CodeBadgeUnknown = "SF-0001"

// KioskSession is the worker a badge resolves to on a shared kiosk (FR-25.1).
// The session itself (cookie, idle timeout) is held by the BFF; this service
// only says who the badge belongs to and whether they are clocked in.
type KioskSession struct {
	WorkerID    string     `json:"workerId"`
	PartyID     string     `json:"partyId"`
	Name        string     `json:"name"`
	UserID      *string    `json:"userId"`
	ClockedInAt *time.Time `json:"clockedInAt"`
	// DefaultResourceID is the worker's home resource, whose jobs the kiosk lists (GET
	// jobs?workerId=); nil means the list holds every job, which the kiosk says.
	DefaultResourceID *string `json:"defaultResourceId"`
	// OpenJobs are the worker's open job-starts (one job, or the jobs of one
	// bundle), so the kiosk can offer to stop them (FR-25.3).
	OpenJobs []CurrentJob `json:"openJobs"`
}

// CurrentJob is an open job registration of a clocked-in worker.
type CurrentJob struct {
	JobID     string    `json:"jobId"`
	BundleID  *string   `json:"bundleId"`
	StartedAt time.Time `json:"startedAt"`
}

// ClockedWorker is one row of the "Who is clocked in" inquiry (PRD 4.25.14,
// FR-25.14): a worker whose latest clock punch is a clock-in, with the jobs
// they started since then and have not stopped.
type ClockedWorker struct {
	WorkerID    string       `json:"workerId"`
	Name        string       `json:"name"`
	BadgeID     *string      `json:"badgeId"`
	Active      bool         `json:"active"`
	ClockedInAt time.Time    `json:"clockedInAt"`
	Jobs        []CurrentJob `json:"jobs"`
}

// lastPunch is the latest clock-in or clock-out of worker w.
const lastPunch = `select t.kind, t.started_at from time_registration t
	where t.tenant_id = w.tenant_id and t.worker_id = w.id and t.kind in ('clock-in', 'clock-out')
	order by t.started_at desc, t.id desc limit 1`

// BadgeLogin resolves a badge to an active worker. An unknown badge and a badge
// of a deactivated worker are both SF-0001, so the kiosk learns nothing more.
func (s *Postgres) BadgeLogin(ctx context.Context, badge string) (KioskSession, error) {
	var out KioskSession
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var kind *string
		var at *time.Time
		err := tx.QueryRow(ctx, `select w.id::text, w.party_id::text, w.name, w.user_id, w.default_resource_id::text, lp.kind, lp.started_at
			from worker w left join lateral (`+lastPunch+`) lp on true
			where w.badge_id = $1 and w.active`, badge).Scan(&out.WorkerID, &out.PartyID, &out.Name, &out.UserID, &out.DefaultResourceID, &kind, &at)
		if errors.Is(err, pgx.ErrNoRows) {
			return httpx.NewProblem(http.StatusUnprocessableEntity, CodeBadgeUnknown, "badge is not recognized")
		}
		if err != nil {
			return err
		}
		if kind != nil && *kind == "clock-in" {
			out.ClockedInAt = at
		}
		p, _ := tenant.FromContext(ctx)
		open, err := openStarts(ctx, tx, p.TenantID, out.WorkerID)
		if err != nil {
			return err
		}
		out.OpenJobs = make([]CurrentJob, 0, len(open))
		for _, o := range open {
			job := CurrentJob{JobID: o.JobID, StartedAt: o.StartedAt}
			if o.BundleID != "" {
				b := o.BundleID
				job.BundleID = &b
			}
			out.OpenJobs = append(out.OpenJobs, job)
		}
		return nil
	})
	return out, err
}

// ClockedIn lists the workers whose latest clock punch is a clock-in, longest
// present first, at most limit rows. Jobs are the job-start registrations since
// that clock-in that are still open (no stoppedAt and no later job-stop for the
// same job), so a bundle shows every job in it.
func (s *Postgres) ClockedIn(ctx context.Context, limit int) ([]ClockedWorker, error) {
	out := []ClockedWorker{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select w.id::text, w.name, w.badge_id, w.active, lp.started_at, j.jobs
			from worker w
			cross join lateral (`+lastPunch+`) lp
			cross join lateral (
				select coalesce(json_agg(json_build_object('jobId', s.job_id, 'bundleId', s.bundle_id, 'startedAt', s.started_at)
					order by s.started_at, s.job_id), '[]'::json) as jobs
				from time_registration s
				where s.tenant_id = w.tenant_id and s.worker_id = w.id and s.kind = 'job-start' and s.stopped_at is null
					and s.started_at >= lp.started_at
					and not exists (select 1 from time_registration x
						where x.tenant_id = s.tenant_id and x.worker_id = s.worker_id and x.kind = 'job-stop'
							and x.job_id = s.job_id and x.started_at >= s.started_at)) j
			where lp.kind = 'clock-in'
			order by lp.started_at, w.name, w.id
			limit $1`, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var c ClockedWorker
			var jobs []byte
			if err := rows.Scan(&c.WorkerID, &c.Name, &c.BadgeID, &c.Active, &c.ClockedInAt, &jobs); err != nil {
				return err
			}
			if err := json.Unmarshal(jobs, &c.Jobs); err != nil {
				return err
			}
			out = append(out, c)
		}
		return rows.Err()
	})
	return out, err
}
