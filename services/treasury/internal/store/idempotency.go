package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
)

// requestHash fingerprints a normalized command so a reused Idempotency-Key
// with a different body is detected.
func requestHash(op string, req any) (string, error) {
	raw, err := json.Marshal(req)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(append([]byte(op+"\n"), raw...))
	return hex.EncodeToString(sum[:]), nil
}

// idempotent runs create once per (tenant, key) inside tx and stores its JSON
// result in idempotency_keys. The key row is claimed first, so a concurrent
// request with the same key blocks on the primary key until the first commits
// and then replays its stored response. A key reused with another body is
// refused with CodeIdempotencyReuse. An empty key runs create without a record.
func idempotent[T any](ctx context.Context, tx pgx.Tx, key, hash string, create func() (T, error)) (T, error) {
	var zero T
	if key == "" {
		return create()
	}
	p, _ := tenant.FromContext(ctx)
	tag, err := tx.Exec(ctx, `insert into idempotency_keys (tenant_id, key, request_hash, response_status) values ($1, $2, $3, 0)
		on conflict (tenant_id, key) do nothing`, p.TenantID, key, hash)
	if err != nil {
		return zero, err
	}
	if tag.RowsAffected() == 0 {
		var storedHash string
		var body []byte
		if err := tx.QueryRow(ctx, `select request_hash, response_body from idempotency_keys where tenant_id = $1 and key = $2`, p.TenantID, key).
			Scan(&storedHash, &body); err != nil {
			return zero, err
		}
		if storedHash != hash {
			return zero, httpx.NewProblem(http.StatusUnprocessableEntity, CodeIdempotencyReuse, "Idempotency-Key was already used with a different request body")
		}
		var out T
		if err := json.Unmarshal(body, &out); err != nil {
			return zero, err
		}
		return out, nil
	}
	out, err := create()
	if err != nil {
		return zero, err
	}
	body, err := json.Marshal(out)
	if err != nil {
		return zero, err
	}
	if _, err := tx.Exec(ctx, `update idempotency_keys set response_status = $3, response_body = $4 where tenant_id = $1 and key = $2`,
		p.TenantID, key, http.StatusCreated, body); err != nil {
		return zero, err
	}
	return out, nil
}
