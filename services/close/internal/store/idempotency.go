package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/services/close/internal/domain"
)

type Outcome struct {
	Status   int
	Body     json.RawMessage
	Replayed bool
}

type ErrInFlight struct{ RetryAfterSeconds int }

func (e *ErrInFlight) Error() string {
	return fmt.Sprintf("idempotency key in flight, retry after %ds", e.RetryAfterSeconds)
}

func HashRequest(method, path string, body []byte) string {
	h := sha256.New()
	for _, part := range [][]byte{[]byte(method), []byte(path), body} {
		h.Write(part)
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

func (s *Postgres) Idempotent(ctx context.Context, key, requestHash string, fn func(tx pgx.Tx) (int, any, error)) (Outcome, error) {
	if key == "" || requestHash == "" {
		return Outcome{}, &domain.Error{Code: domain.CodeValidation, Detail: "idempotency key and request hash are required"}
	}
	var out Outcome
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `delete from idempotency_keys where key = $1 and expires_at <= now()`, key); err != nil {
			return fmt.Errorf("expire idempotency key: %w", err)
		}
		claimed, err := tx.Exec(ctx,
			`insert into idempotency_keys(tenant_id, key, request_hash, locked_until) values (current_setting('passage.tenant_id', true)::uuid, $1, $2, now() + interval '30 seconds') on conflict (tenant_id, key) do nothing`,
			key, requestHash)
		if err != nil {
			return fmt.Errorf("claim idempotency key: %w", err)
		}
		if claimed.RowsAffected() == 0 {
			replay, err := existingOutcome(ctx, tx, key, requestHash)
			if err != nil || replay != nil {
				if replay != nil {
					out = *replay
				}
				return err
			}
		}
		status, body, err := fn(tx)
		if err != nil {
			return err
		}
		encoded, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("encode idempotent response: %w", err)
		}
		if _, err := tx.Exec(ctx, `update idempotency_keys set response_status = $2, response_body = $3::jsonb, locked_until = null where key = $1`, key, status, string(encoded)); err != nil {
			return fmt.Errorf("store idempotent response: %w", err)
		}
		out = Outcome{Status: status, Body: encoded}
		return nil
	})
	if err != nil {
		return Outcome{}, err
	}
	return out, nil
}

func existingOutcome(ctx context.Context, tx pgx.Tx, key, requestHash string) (*Outcome, error) {
	var (
		storedHash string
		status     *int
		body       []byte
		retryAfter int
	)
	err := tx.QueryRow(ctx,
		`select request_hash, response_status, response_body, coalesce(greatest(ceil(extract(epoch from locked_until - now()))::int, 0), 0) from idempotency_keys where key = $1 for update`,
		key).Scan(&storedHash, &status, &body, &retryAfter)
	if err != nil {
		return nil, fmt.Errorf("read idempotency key: %w", err)
	}
	if storedHash != requestHash {
		return nil, &domain.Error{Code: domain.CodeIdempotencyReuse, Detail: "Idempotency-Key was already used with a different request"}
	}
	if status != nil {
		return &Outcome{Status: *status, Body: body, Replayed: true}, nil
	}
	if retryAfter > 0 {
		return nil, &ErrInFlight{RetryAfterSeconds: retryAfter}
	}
	if _, err := tx.Exec(ctx, `update idempotency_keys set locked_until = now() + interval '30 seconds' where key = $1`, key); err != nil {
		return nil, fmt.Errorf("reclaim idempotency key: %w", err)
	}
	return nil, nil
}
