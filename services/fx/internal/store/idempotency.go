package store

import (
	"context"
	"encoding/json"
	"errors"

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
