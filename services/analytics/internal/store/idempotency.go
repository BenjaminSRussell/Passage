package store

import (
	"context"
	"encoding/json"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/services/analytics/internal/domain"
)

// Created is the outcome of an idempotent create. Replayed is true when the
// Idempotency-Key was seen before with the same request: Body is the stored
// response and nothing new was written.
type Created struct {
	Status   int
	Body     json.RawMessage
	Replayed bool
}

// claimKey claims an Idempotency-Key inside tx, before the command writes
// anything. Keys are replay-safe for 24 hours (CONVENTIONS); an older key is
// free again. A concurrent duplicate waits on the claimed row and then
// replays. It returns the stored response when the key was used before with
// the same request hash, ANA-0114 when it was used with another request, and
// nil when the caller holds the key and must run the command and storeResponse.
func claimKey(ctx context.Context, tx pgx.Tx, tenantID, key, requestHash string) (*Created, error) {
	if _, err := tx.Exec(ctx, `delete from idempotency_keys where `+ownTenant+` and key = $1
		and created_at < now() - interval '24 hours'`, key); err != nil {
		return nil, err
	}
	claimed, err := tx.Exec(ctx, `insert into idempotency_keys (tenant_id, key, request_hash, response_status)
		values ($1, $2, $3, 0) on conflict (tenant_id, key) do nothing`, tenantID, key, requestHash)
	if err != nil {
		return nil, err
	}
	if claimed.RowsAffected() == 1 {
		return nil, nil
	}
	var (
		storedHash string
		status     int
		body       []byte
	)
	if err := tx.QueryRow(ctx, `select request_hash, response_status, response_body from idempotency_keys
		where `+ownTenant+` and key = $1`, key).Scan(&storedHash, &status, &body); err != nil {
		return nil, err
	}
	if storedHash != requestHash {
		return nil, &domain.Error{Code: domain.CodeIdempotencyReuse, Status: 422, Detail: "Idempotency-Key was already used with a different request"}
	}
	return &Created{Status: status, Body: body, Replayed: true}, nil
}

// storeResponse records the response of a command under its claimed key.
func storeResponse(ctx context.Context, tx pgx.Tx, key string, status int, response any) (Created, error) {
	body, err := json.Marshal(response)
	if err != nil {
		return Created{}, err
	}
	if _, err := tx.Exec(ctx, `update idempotency_keys set response_status = $2, response_body = $3
		where `+ownTenant+` and key = $1`, key, status, body); err != nil {
		return Created{}, err
	}
	return Created{Status: status, Body: body}, nil
}
