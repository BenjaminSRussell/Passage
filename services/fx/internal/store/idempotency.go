package store

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
)

type IdempotencyRecord struct {
	Key            string
	RequestHash    string
	ResponseStatus int
	ResponseBody   json.RawMessage
}

const lockSQL = `select pg_advisory_lock(hashtextextended($1, 0))`
const unlockSQL = `select pg_advisory_unlock(hashtextextended($1, 0))`

func (s *Postgres) LockKey(ctx context.Context, key string) (func(), error) {
	if key == "" {
		return nil, invalid("idempotency key required")
	}
	p, _ := tenant.FromContext(ctx)
	name := p.TenantID + "/" + key
	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := conn.Exec(ctx, lockSQL, name); err != nil {
		conn.Release()
		return nil, err
	}
	return func() {
		unlockCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if _, err := conn.Exec(unlockCtx, unlockSQL, name); err != nil {
			slog.Error("idempotency unlock failed; closing connection", "err", err)
			_ = conn.Hijack().Close(unlockCtx)
			return
		}
		conn.Release()
	}, nil
}

func (s *Postgres) GetIdempotency(ctx context.Context, key string) (IdempotencyRecord, bool, error) {
	if key == "" {
		return IdempotencyRecord{}, false, invalid("idempotency key required")
	}
	p, _ := tenant.FromContext(ctx)
	rec := IdempotencyRecord{Key: key}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `select request_hash, response_status, response_body from idempotency_keys where tenant_id=$1 and key=$2`,
			p.TenantID, key).Scan(&rec.RequestHash, &rec.ResponseStatus, &rec.ResponseBody)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return IdempotencyRecord{}, false, nil
	}
	return rec, err == nil, err
}

func (s *Postgres) PutIdempotency(ctx context.Context, rec IdempotencyRecord) error {
	if rec.Key == "" {
		return invalid("idempotency key required")
	}
	p, _ := tenant.FromContext(ctx)
	return dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `insert into idempotency_keys (tenant_id, key, request_hash, response_status, response_body)
			values ($1,$2,$3,$4,$5) on conflict (tenant_id, key) do nothing`,
			p.TenantID, rec.Key, rec.RequestHash, rec.ResponseStatus, rec.ResponseBody)
		return err
	})
}
