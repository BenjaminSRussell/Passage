package store

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/shopfloor/internal/domain"
	sfevents "github.com/BenjaminSRussell/passage/services/shopfloor/internal/events"
)

// CodeRegistrationNotFound is a correction of a registration that does not exist.
const CodeRegistrationNotFound = "SF-0126"

// Correction is the audit record of one supervisor edit of a registration.
type Correction struct {
	ID             string           `json:"id"`
	RegistrationID string           `json:"registrationId"`
	WorkerID       string           `json:"workerId"`
	Reason         string           `json:"reason"`
	OldStartedAt   time.Time        `json:"oldStartedAt"`
	OldStoppedAt   *time.Time       `json:"oldStoppedAt"`
	OldHours       *decimal.Decimal `json:"oldHours"`
	NewStartedAt   time.Time        `json:"newStartedAt"`
	NewStoppedAt   *time.Time       `json:"newStoppedAt"`
	NewHours       *decimal.Decimal `json:"newHours"`
	CorrectedBy    string           `json:"correctedBy"`
	CorrectedAt    time.Time        `json:"correctedAt"`
}

func parseHours(s *string) (*decimal.Decimal, error) {
	if s == nil {
		return nil, nil
	}
	d, err := decimal.NewFromString(*s)
	if err != nil {
		return nil, err
	}
	return &d, nil
}

// CorrectRegistration changes the times of a registration with a reason, keeps the
// before and after in registration_correction and publishes
// dev.passage.shopfloor.registration.corrected.v1 in the same transaction (FR-25.8).
// A day calculated before the correction can no longer be approved until it is
// recalculated (SF-0104). The worker's own user cannot correct their time (SF-0106).
// The domain rules decide what may change (SF-0127, SF-0128); an auto clock-out not
// yet reviewed is confirmed by a correction that keeps its time (BR-25.3); a job-start already
// closed by a job-stop is SF-0127 too. An unknown id is SF-0126.
func (s *Postgres) CorrectRegistration(ctx context.Context, id string, req domain.CorrectionRequest, correctedBy string, idem Idem) (Correction, error) {
	p, _ := tenant.FromContext(ctx)
	var out Correction
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var workerID, kind string
		var jobID, bundleID *string
		var oldStart time.Time
		var oldStop *time.Time
		var oldHoursText *string
		err := tx.QueryRow(ctx, `select worker_id::text, kind, job_id::text, bundle_id::text, started_at, stopped_at, hours::text
			from time_registration where tenant_id = $1 and id = $2`, p.TenantID, id).
			Scan(&workerID, &kind, &jobID, &bundleID, &oldStart, &oldStop, &oldHoursText)
		if errors.Is(err, pgx.ErrNoRows) {
			return httpx.NewProblem(http.StatusNotFound, CodeRegistrationNotFound, "registration not found")
		}
		if err != nil {
			return err
		}
		// Serialize with calculation and approval of the worker; the worker row is
		// locked whether or not the worker is still active.
		var userID string
		if err := tx.QueryRow(ctx, `select coalesce(user_id, '') from worker where tenant_id = $1 and id = $2 for update`, p.TenantID, workerID).Scan(&userID); err != nil {
			return err
		}
		if userID != "" && userID == correctedBy {
			return httpx.NewProblem(http.StatusForbidden, CodeOwnApproval, "you cannot correct your own time registrations")
		}
		// Re-read under the worker lock so two corrections apply one after the other.
		// An auto clock-out no supervisor has reviewed yet may be confirmed as it is.
		var unconfirmed bool
		if err := tx.QueryRow(ctx, `select started_at, stopped_at, hours::text,
				$3 = any(flags) and not exists (select 1 from registration_correction c where c.tenant_id = $1 and c.registration_id = $2)
			from time_registration where tenant_id = $1 and id = $2 for update`,
			p.TenantID, id, domain.FlagAutoClockOut).Scan(&oldStart, &oldStop, &oldHoursText, &unconfirmed); err != nil {
			return err
		}
		// A job-start that a job-stop already closed holds no hours: the job-stop carries
		// the interval and the hours that w21 posted. Giving the start a stop time or
		// moving it would count the job twice or split it from its stop, so the
		// supervisor corrects the job-stop instead.
		if kind == KindJobStart && jobID != nil {
			var closed bool
			if err := tx.QueryRow(ctx, `select exists (select 1 from time_registration x
				where x.tenant_id = $1 and x.worker_id = $2 and x.kind = 'job-stop' and x.job_id = $3 and x.started_at >= $4)`,
				p.TenantID, workerID, *jobID, oldStart).Scan(&closed); err != nil {
				return err
			}
			if closed {
				return httpx.NewProblem(http.StatusUnprocessableEntity, domain.CodeRegistrationNotCorrectable,
					"this job-start was closed by a job-stop; correct the job-stop instead")
			}
		}
		plan, err := domain.PlanCorrection(domain.StoredRegistration{Kind: kind, Bundled: bundleID != nil, StartedAt: oldStart, StoppedAt: oldStop, Unconfirmed: unconfirmed}, req)
		if err != nil {
			var de *domain.Error
			if errors.As(err, &de) {
				return httpx.NewProblem(http.StatusUnprocessableEntity, de.Code, de.Detail)
			}
			return err
		}
		var newHoursText *string
		if plan.Hours != nil {
			h := plan.Hours.String()
			newHoursText = &h
		}
		if _, err := tx.Exec(ctx, `update time_registration set started_at = $3, stopped_at = $4, hours = $5 where tenant_id = $1 and id = $2`,
			p.TenantID, id, plan.StartedAt, plan.StoppedAt, newHoursText); err != nil {
			return err
		}
		out = Correction{ID: ids.New(), RegistrationID: id, WorkerID: workerID, Reason: req.Reason, OldStartedAt: oldStart, OldStoppedAt: oldStop,
			NewStartedAt: plan.StartedAt, NewStoppedAt: plan.StoppedAt, NewHours: plan.Hours, CorrectedBy: correctedBy}
		if out.OldHours, err = parseHours(oldHoursText); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `insert into registration_correction (tenant_id, id, registration_id, worker_id, reason,
				old_started_at, old_stopped_at, old_hours, new_started_at, new_stopped_at, new_hours, corrected_by)
			values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12) returning corrected_at`,
			p.TenantID, out.ID, id, workerID, req.Reason, oldStart, oldStop, oldHoursText, plan.StartedAt, plan.StoppedAt, newHoursText, correctedBy).
			Scan(&out.CorrectedAt); err != nil {
			return err
		}
		if err := enqueue(ctx, tx, "registration", id, sfevents.RegistrationCorrected, p.TenantID, sfevents.RegistrationCorrectedData{
			RegistrationID: id, WorkerID: workerID, Kind: kind, JobID: jobID, OldStartedAt: oldStart, OldStoppedAt: oldStop,
			NewStartedAt: plan.StartedAt, NewStoppedAt: plan.StoppedAt, Hours: plan.Hours, Reason: req.Reason, CorrectedBy: correctedBy,
		}); err != nil {
			return err
		}
		if idem.Key == "" {
			return nil
		}
		return remember(ctx, tx, idem, http.StatusOK, out)
	})
	if err != nil {
		return Correction{}, err
	}
	return out, nil
}

// ListCorrections returns the corrections of a worker whose correction time is in
// [from, to), newest first, at most limit rows.
func (s *Postgres) ListCorrections(ctx context.Context, workerID string, from, to time.Time, limit int) ([]Correction, error) {
	p, _ := tenant.FromContext(ctx)
	out := []Correction{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select id::text, registration_id::text, worker_id::text, reason, old_started_at, old_stopped_at, old_hours::text,
				new_started_at, new_stopped_at, new_hours::text, corrected_by, corrected_at
			from registration_correction
			where tenant_id = $1 and ($2 = '' or worker_id::text = $2) and corrected_at >= $3 and corrected_at < $4
			order by corrected_at desc, id limit $5`, p.TenantID, workerID, from, to, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var c Correction
			var oh, nh *string
			if err := rows.Scan(&c.ID, &c.RegistrationID, &c.WorkerID, &c.Reason, &c.OldStartedAt, &c.OldStoppedAt, &oh,
				&c.NewStartedAt, &c.NewStoppedAt, &nh, &c.CorrectedBy, &c.CorrectedAt); err != nil {
				return err
			}
			if c.OldHours, err = parseHours(oh); err != nil {
				return err
			}
			if c.NewHours, err = parseHours(nh); err != nil {
				return err
			}
			out = append(out, c)
		}
		return rows.Err()
	})
	return out, err
}
