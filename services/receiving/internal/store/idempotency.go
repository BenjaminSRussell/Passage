package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
)

type ClaimState int

const (
	ClaimNew ClaimState = iota
	ClaimInFlight
	ClaimCompleted
	ClaimMismatch
)

type ClaimResult struct {
	State  ClaimState
	Status int
	Body   []byte
}

func (s *Postgres) ClaimKey(ctx context.Context, key, hash string, lockFor time.Duration) (ClaimResult, error) {
	tenantID, err := tenantOf(ctx)
	if err != nil {
		return ClaimResult{}, err
	}
	var result ClaimResult
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var claimed int
		err := tx.QueryRow(ctx, `INSERT INTO idempotency_keys (tenant_id, key, request_hash, locked_until)
			VALUES ($1, $2, $3, now() + make_interval(secs => $4))
			ON CONFLICT (tenant_id, key) DO UPDATE SET request_hash = $3, response_status = NULL, response_body = NULL,
				locked_until = now() + make_interval(secs => $4), expires_at = now() + interval '24 hours', created_at = now()
			WHERE idempotency_keys.expires_at < now()
				OR (idempotency_keys.response_status IS NULL AND idempotency_keys.locked_until < now())
			RETURNING 1`, tenantID, key, hash, lockFor.Seconds()).Scan(&claimed)
		if err == nil {
			result.State = ClaimNew
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		var storedHash string
		var status *int
		if err := tx.QueryRow(ctx, `SELECT request_hash, response_status, response_body FROM idempotency_keys WHERE tenant_id = $1 AND key = $2`,
			tenantID, key).Scan(&storedHash, &status, &result.Body); err != nil {
			return err
		}
		switch {
		case storedHash != hash:
			result = ClaimResult{State: ClaimMismatch}
		case status != nil:
			result.State, result.Status = ClaimCompleted, *status
		default:
			result = ClaimResult{State: ClaimInFlight}
		}
		return nil
	})
	return result, err
}

func (s *Postgres) CompleteKey(ctx context.Context, key string, status int, body []byte) error {
	tenantID, err := tenantOf(ctx)
	if err != nil {
		return err
	}
	return dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE idempotency_keys SET response_status = $3, response_body = $4, locked_until = NULL WHERE tenant_id = $1 AND key = $2`,
			tenantID, key, status, body)
		return err
	})
}

func (s *Postgres) ReleaseKey(ctx context.Context, key string) error {
	tenantID, err := tenantOf(ctx)
	if err != nil {
		return err
	}
	return dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `DELETE FROM idempotency_keys WHERE tenant_id = $1 AND key = $2`, tenantID, key)
		return err
	})
}
