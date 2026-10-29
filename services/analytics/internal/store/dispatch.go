package store

import (
	"context"
	"errors"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/events"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/outbox"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/analytics/internal/domain"
	analyticsevents "github.com/BenjaminSRussell/passage/services/analytics/internal/events"
)

// maxErrorText is the stored length of a delivery error (report_delivery.error_text).
const maxErrorText = 500

const deliveryColumns = `delivery_id::text, schedule_id::text, run_id::text, attempt, status, format, destination_type,
	row_count, bytes, error_text, run_at, retry_at`

func scanDelivery(row pgx.Row) (domain.Delivery, error) {
	var d domain.Delivery
	err := row.Scan(&d.ID, &d.ScheduleID, &d.RunID, &d.Attempt, &d.Status, &d.Format, &d.DestinationType,
		&d.RowCount, &d.Bytes, &d.Error, &d.RunAt, &d.RetryAt)
	d.RunAt, d.RetryAt = d.RunAt.UTC(), utc(d.RetryAt)
	return d, err
}

// Delivered is what the destination adapter reports for a successful attempt.
type Delivered struct {
	RowCount int64
	Bytes    int64
}

// Deliverer renders one due schedule and sends it to its destination. A
// non-nil error is a failed attempt; the dispatcher decides whether to retry.
type Deliverer func(ctx context.Context, s domain.Schedule) (Delivered, error)

// DispatchDue runs up to limit schedules of the tenant in ctx whose next run is
// at or before now (FR-46.10, the every-minute dispatcher of §9.46.16). Each
// schedule is claimed in its own transaction with FOR UPDATE SKIP LOCKED, so
// two dispatchers never run the same schedule and one failure does not undo
// the others. Each attempt writes one report_delivery row. A delivered
// attempt, or a run whose retries are spent (FAILED), moves next_run_at to the
// next cron slot after now; a retryable failure keeps the schedule due again
// after domain.RetryDelay. A delivered attempt enqueues report.delivered in
// the same transaction. It returns the attempts made.
func (s *Postgres) DispatchDue(ctx context.Context, now time.Time, limit int, deliver Deliverer) (int, error) {
	p, _ := tenant.FromContext(ctx)
	now = now.UTC().Truncate(time.Microsecond)
	attempts := 0
	for attempts < limit {
		idle := false
		err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
			sc, err := scanSchedule(tx.QueryRow(ctx, `select `+scheduleColumns+` from report_schedule
				where `+ownTenant+` and next_run_at <= $1
				order by next_run_at, schedule_id limit 1 for update skip locked`, now))
			if errors.Is(err, pgx.ErrNoRows) {
				idle = true
				return nil
			}
			if err != nil {
				return err
			}
			return runAttempt(ctx, tx, p.TenantID, sc, now, deliver)
		})
		if err != nil || idle {
			return attempts, err
		}
		attempts++
	}
	return attempts, nil
}

// runAttempt delivers one claimed schedule and records the outcome in tx.
func runAttempt(ctx context.Context, tx pgx.Tx, tenantID string, sc domain.Schedule, now time.Time, deliver Deliverer) error {
	attempt := sc.FailureCount + 1
	runID := ""
	if sc.FailureCount > 0 {
		// A retry belongs to the run its first attempt opened.
		err := tx.QueryRow(ctx, `select run_id::text from report_delivery where `+ownTenant+` and schedule_id = $1
			order by run_at desc, attempt desc limit 1`, sc.ID).Scan(&runID)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
	}
	if runID == "" {
		runID, attempt = ids.New(), 1
	}
	got, derr := deliver(ctx, sc)
	outcome, wait := domain.PlanAttempt(attempt, domain.DefaultScheduleRetries, derr == nil)

	var (
		next     time.Time
		failures int
		retryAt  *time.Time
		errText  string
	)
	if outcome == domain.DeliveryRetry {
		next, failures = now.Add(wait), attempt
		retryAt = &next
	} else {
		cron, err := domain.ParseCron(sc.Cron, sc.Timezone)
		if err != nil {
			return err
		}
		if next, err = cron.Next(now); err != nil {
			return err
		}
		next = next.UTC()
	}
	if derr != nil {
		errText = truncate(derr.Error(), maxErrorText)
		got = Delivered{}
	}
	deliveryID := ids.New()
	if _, err := tx.Exec(ctx, `insert into report_delivery (tenant_id, delivery_id, schedule_id, run_id, attempt, status,
		format, destination_type, row_count, bytes, error_text, run_at, retry_at)
		values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)`,
		tenantID, deliveryID, sc.ID, runID, attempt, outcome, sc.Format, sc.Destination.Type,
		got.RowCount, got.Bytes, errText, now, retryAt); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `update report_schedule set last_run_at = $2, next_run_at = $3, failure_count = $4
		where `+ownTenant+` and schedule_id = $1`, sc.ID, now, next, failures); err != nil {
		return err
	}
	if outcome != domain.DeliveryDelivered {
		return nil
	}
	evt, err := events.New(analyticsevents.ReportDelivered, eventSource, sc.ID, tenantID, analyticsevents.ReportDeliveredData{
		ScheduleID: sc.ID, RunID: runID, Attempt: attempt, Format: sc.Format, Destination: sc.Destination.Type,
		RowCount: got.RowCount, Bytes: got.Bytes, DeliveredAt: now.Format(time.RFC3339Nano),
	})
	if err != nil {
		return err
	}
	return outbox.Enqueue(ctx, tx, "report_schedule", sc.ID, evt)
}

// truncate cuts s to at most n bytes without splitting a UTF-8 character.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	s = s[:n]
	for !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s
}

// ListDeliveries returns the attempts of one schedule, newest first, optionally
// of one status (§9.46.14 "Schedule deliveries"). An unknown schedule is ANA-0112.
func (s *Postgres) ListDeliveries(ctx context.Context, scheduleID, status string) ([]domain.Delivery, error) {
	items := []domain.Delivery{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if !ids.Valid(scheduleID) {
			return domain.NotFound("schedule " + scheduleID)
		}
		var found bool
		if err := tx.QueryRow(ctx, `select exists (select 1 from report_schedule where `+ownTenant+` and schedule_id = $1)`,
			scheduleID).Scan(&found); err != nil {
			return err
		}
		if !found {
			return domain.NotFound("schedule " + scheduleID)
		}
		rows, err := tx.Query(ctx, `select `+deliveryColumns+` from report_delivery
			where `+ownTenant+` and schedule_id = $1 and ($2::text = '' or status = $2)
			order by run_at desc, attempt desc limit 200`, scheduleID, status)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			d, err := scanDelivery(rows)
			if err != nil {
				return err
			}
			items = append(items, d)
		}
		return rows.Err()
	})
	return items, err
}
