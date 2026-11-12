package store

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	sfevents "github.com/BenjaminSRussell/passage/services/shopfloor/internal/events"
)

// Worker maintenance codes (slice 22).
const (
	// CodeWorkerClockedIn: a worker who is clocked in or has an open job start cannot be
	// deactivated; the open time must be closed first (409).
	CodeWorkerClockedIn = "SF-0166"
	// CodeWorkerInactive: the worker is already deactivated (409).
	CodeWorkerInactive = "SF-0167"
	// CodeWorkerActive: the worker is already active (409).
	CodeWorkerActive = "SF-0168"
	// CodeCurrencyLocked: the worker's currency cannot change once a day of the worker is
	// approved, as a revision of that day would mix currencies in it (422).
	CodeCurrencyLocked = "SF-0169"
)

// WorkerInput is the editable part of a worker: everything but the party link, which never changes.
type WorkerInput struct {
	Name     string
	BadgeID  *string
	UserID   *string
	CostRate decimal.Decimal
	Currency string
	// TimeZone replaces the worker's work-day zone when not empty; empty keeps it.
	TimeZone string
}

// lockWorkerRow locks one worker row and returns it, or SF-0101 (404).
func lockWorkerRow(ctx context.Context, tx pgx.Tx, tenantID, workerID string) (Worker, error) {
	w, err := scanWorker(tx.QueryRow(ctx, `select `+workerColumns+` from worker where tenant_id = $1 and id = $2 for update`, tenantID, workerID))
	if errors.Is(err, pgx.ErrNoRows) {
		return Worker{}, httpx.NewProblem(http.StatusNotFound, CodeWorkerNotFound, "worker not found")
	}
	return w, err
}

func publishWorkerSaved(ctx context.Context, tx pgx.Tx, tenantID string, w Worker, change string, reason *string, savedBy string) error {
	return enqueue(ctx, tx, workerSource, w.ID, sfevents.WorkerSaved, tenantID, sfevents.WorkerSavedData{
		WorkerID: w.ID, Name: w.Name, UserID: w.UserID, CostRate: w.CostRate.String(), Currency: w.Currency,
		Active: w.Active, Version: w.Version, Change: change, Reason: reason, SavedBy: savedBy})
}

// UpdateWorker replaces a worker's name, badge, sign-in user, cost rate and currency when version
// is the stored version (SF-0143, 412) and bumps the version. A badge held by another worker is
// SF-0105. A deactivated worker can be edited (clear the badge before reissuing it). The worker's
// own user cannot edit the worker (SF-0106, 403): unlinking the sign-in user would get round the
// own-approval rule and the cost rate is the worker's pay. The currency cannot change once a day
// of the worker is approved (SF-0169, 422). The edit rewrites no pay line: days already
// calculated keep the rate they were calculated with until a supervisor recalculates them, and
// approved days keep theirs (a later pay:revise of an approved day prices it at the rate then
// current).
func (s *Postgres) UpdateWorker(ctx context.Context, workerID string, version int, in WorkerInput, savedBy string, idem Idem) (Worker, error) {
	p, _ := tenant.FromContext(ctx)
	var out Worker
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		cur, err := lockWorkerRow(ctx, tx, p.TenantID, workerID)
		if err != nil {
			return err
		}
		if cur.UserID != nil && *cur.UserID != "" && *cur.UserID == savedBy {
			return httpx.NewProblem(http.StatusForbidden, CodeOwnApproval, "you cannot edit your own worker record")
		}
		if cur.Version == version && !strings.EqualFold(cur.Currency, in.Currency) {
			var approved bool
			if err := tx.QueryRow(ctx, `select exists (select 1 from worker_day where tenant_id = $1 and worker_id = $2 and status = 'approved')`,
				p.TenantID, workerID).Scan(&approved); err != nil {
				return err
			}
			if approved {
				return httpx.NewProblem(http.StatusUnprocessableEntity, CodeCurrencyLocked, "the currency cannot change once pay of the worker is approved")
			}
		}
		if in.TimeZone != "" {
			if err := checkZone(ctx, tx, in.TimeZone); err != nil {
				return err
			}
		}
		out, err = scanWorker(tx.QueryRow(ctx, `update worker set name = $3, badge_id = $4, user_id = $5, cost_rate = $6, currency = $7,
				time_zone = coalesce(nullif($9, ''), time_zone), version = version + 1, updated_at = now()
			where tenant_id = $1 and id = $2 and version = $8 returning `+workerColumns,
			p.TenantID, workerID, in.Name, in.BadgeID, in.UserID, in.CostRate.String(), in.Currency, version, in.TimeZone))
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation {
			return httpx.NewProblem(http.StatusConflict, CodeBadgeTaken, "badge is already assigned to another worker")
		}
		if errors.Is(err, pgx.ErrNoRows) {
			return versionMismatch("worker")
		}
		if err != nil {
			return err
		}
		if err := publishWorkerSaved(ctx, tx, p.TenantID, out, "updated", nil, savedBy); err != nil {
			return err
		}
		return remember(ctx, tx, idem, http.StatusOK, out)
	})
	return out, err
}

// DeactivateWorker marks a worker inactive with a reason. The worker can no longer sign in at a
// kiosk or register time (SF-0001, SF-0101), while the pay of the days already worked is still
// calculated, approved, revised and exported. A worker who is clocked in or has an open job start
// is SF-0166: close the open time first. An inactive worker is SF-0167.
func (s *Postgres) DeactivateWorker(ctx context.Context, workerID string, version int, reason, savedBy string, idem Idem) (Worker, error) {
	p, _ := tenant.FromContext(ctx)
	var out Worker
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		cur, err := lockWorkerRow(ctx, tx, p.TenantID, workerID)
		if err != nil {
			return err
		}
		if cur.Version != version {
			return versionMismatch("worker")
		}
		if !cur.Active {
			return httpx.NewProblem(http.StatusConflict, CodeWorkerInactive, "worker is already inactive")
		}
		var clockedIn bool
		if err := tx.QueryRow(ctx, `select coalesce((select t.kind = 'clock-in' from time_registration t
				where t.tenant_id = $1 and t.worker_id = $2 and t.kind in ('clock-in', 'clock-out')
				order by t.started_at desc, t.id desc limit 1), false)`, p.TenantID, workerID).Scan(&clockedIn); err != nil {
			return err
		}
		if !clockedIn {
			open, err := openStarts(ctx, tx, p.TenantID, workerID)
			if err != nil {
				return err
			}
			clockedIn = len(open) > 0
		}
		if clockedIn {
			return httpx.NewProblem(http.StatusConflict, CodeWorkerClockedIn, "worker is clocked in or has an open job; close the open time before deactivating")
		}
		out, err = scanWorker(tx.QueryRow(ctx, `update worker set active = false, deactivated_at = now(), deactivated_by = $3, deactivation_reason = $4,
				version = version + 1, updated_at = now()
			where tenant_id = $1 and id = $2 returning `+workerColumns, p.TenantID, workerID, savedBy, reason))
		if err != nil {
			return err
		}
		if err := publishWorkerSaved(ctx, tx, p.TenantID, out, "deactivated", &reason, savedBy); err != nil {
			return err
		}
		return remember(ctx, tx, idem, http.StatusOK, out)
	})
	return out, err
}

// ReactivateWorker makes a deactivated worker active again and clears the deactivation record.
// An active worker is SF-0168.
func (s *Postgres) ReactivateWorker(ctx context.Context, workerID string, version int, savedBy string, idem Idem) (Worker, error) {
	p, _ := tenant.FromContext(ctx)
	var out Worker
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		cur, err := lockWorkerRow(ctx, tx, p.TenantID, workerID)
		if err != nil {
			return err
		}
		if cur.Version != version {
			return versionMismatch("worker")
		}
		if cur.Active {
			return httpx.NewProblem(http.StatusConflict, CodeWorkerActive, "worker is already active")
		}
		out, err = scanWorker(tx.QueryRow(ctx, `update worker set active = true, deactivated_at = null, deactivated_by = null, deactivation_reason = null,
				version = version + 1, updated_at = now()
			where tenant_id = $1 and id = $2 returning `+workerColumns, p.TenantID, workerID))
		if err != nil {
			return err
		}
		if err := publishWorkerSaved(ctx, tx, p.TenantID, out, "reactivated", nil, savedBy); err != nil {
			return err
		}
		return remember(ctx, tx, idem, http.StatusOK, out)
	})
	return out, err
}
