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

// Created is the outcome of an idempotent create. Replayed is true when the
// Idempotency-Key was seen before with the same request: Body is the stored
// response and nothing new was written.
type Created struct {
	Status   int
	Body     json.RawMessage
	Replayed bool
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
		// Keys are replay-safe for 24 hours (CONVENTIONS); an older key is free again.
		if _, err := tx.Exec(ctx, `delete from idempotency_keys where `+ownTenant+` and key = $1
			and created_at < now() - interval '24 hours'`, key); err != nil {
			return err
		}
		claimed, err := tx.Exec(ctx, `insert into idempotency_keys (tenant_id, key, request_hash, response_status)
			values ($1, $2, $3, 0) on conflict (tenant_id, key) do nothing`, p.TenantID, key, requestHash)
		if err != nil {
			return err
		}
		if claimed.RowsAffected() == 0 {
			var (
				storedHash string
				status     int
				body       []byte
			)
			if err := tx.QueryRow(ctx, `select request_hash, response_status, response_body from idempotency_keys
				where `+ownTenant+` and key = $1`, key).Scan(&storedHash, &status, &body); err != nil {
				return err
			}
			if storedHash != requestHash {
				return &domain.Error{Code: domain.CodeIdempotencyReuse, Status: 422, Detail: "Idempotency-Key was already used with a different request"}
			}
			out = Created{Status: status, Body: body, Replayed: true}
			return nil
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
		body, err := json.Marshal(created)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `update idempotency_keys set response_status = 201, response_body = $2
			where `+ownTenant+` and key = $1`, key, body); err != nil {
			return err
		}
		out = Created{Status: 201, Body: body}
		return nil
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
