package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/analytics/internal/domain"
)

const scheduleColumns = `schedule_id::text, name, target_type, target_id, cron, timezone, format, destination_json,
	last_run_at, next_run_at, failure_count, version`

func scanSchedule(row pgx.Row) (domain.Schedule, error) {
	var (
		s    domain.Schedule
		dest []byte
	)
	if err := row.Scan(&s.ID, &s.Name, &s.TargetType, &s.TargetID, &s.Cron, &s.Timezone, &s.Format, &dest,
		&s.LastRunAt, &s.NextRunAt, &s.FailureCount, &s.Version); err != nil {
		return s, err
	}
	s.LastRunAt, s.NextRunAt = utc(s.LastRunAt), utc(s.NextRunAt)
	return s, json.Unmarshal(dest, &s.Destination)
}

// CreateSchedule validates and stores a schedule under an Idempotency-Key
// (POST /analytics/schedules). The first request claims the key in the same
// transaction as the insert, so a concurrent duplicate waits and then replays
// the stored response. The key reused with another request hash is ANA-0114.
// Keys expire after 24 hours.
func (s *Postgres) CreateSchedule(ctx context.Context, key, requestHash string, in domain.Schedule, now time.Time) (Created, error) {
	prepared, verr := domain.PrepareSchedule(in, now)
	if verr != nil {
		return Created{}, verr
	}
	p, _ := tenant.FromContext(ctx)
	var out Created
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		replay, err := claimKey(ctx, tx, p.TenantID, key, requestHash)
		if err != nil || replay != nil {
			if replay != nil {
				out = *replay
			}
			return err
		}
		dest, err := json.Marshal(prepared.Destination)
		if err != nil {
			return err
		}
		created, err := scanSchedule(tx.QueryRow(ctx, `insert into report_schedule (tenant_id, schedule_id, name, target_type,
			target_id, cron, timezone, format, destination_json, next_run_at, created_by)
			values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11) returning `+scheduleColumns,
			p.TenantID, ids.New(), prepared.Name, prepared.TargetType, prepared.TargetID, prepared.Cron, prepared.Timezone,
			prepared.Format, dest, prepared.NextRunAt, p.UserID))
		if err != nil {
			return err
		}
		out, err = storeResponse(ctx, tx, key, 201, created)
		return err
	})
	return out, err
}

// ListSchedules returns the tenant's schedules by next run.
func (s *Postgres) ListSchedules(ctx context.Context) ([]domain.Schedule, error) {
	items := []domain.Schedule{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+scheduleColumns+` from report_schedule where `+ownTenant+`
			order by next_run_at nulls last, name limit 500`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			sc, err := scanSchedule(rows)
			if err != nil {
				return err
			}
			items = append(items, sc)
		}
		return rows.Err()
	})
	return items, err
}

// GetSchedule returns one schedule or ANA-0112.
func (s *Postgres) GetSchedule(ctx context.Context, id string) (domain.Schedule, error) {
	var out domain.Schedule
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if !ids.Valid(id) {
			return domain.NotFound("schedule " + id)
		}
		sc, err := scanSchedule(tx.QueryRow(ctx, `select `+scheduleColumns+` from report_schedule
			where `+ownTenant+` and schedule_id = $1`, id))
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.NotFound("schedule " + id)
		}
		out = sc
		return err
	})
	return out, err
}
