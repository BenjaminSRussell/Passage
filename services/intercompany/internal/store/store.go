// Package store persists intercompany trading partners (FR-40.1) in Postgres.
// Every query filters on the caller's tenant_id and runs inside
// dbx.WithTenantTx, so row-level security scopes it to the tenant as well;
// every change writes its event to the outbox in the same transaction.
package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BenjaminSRussell/passage/platform/events"
	"github.com/BenjaminSRussell/passage/platform/outbox"
	"github.com/BenjaminSRussell/passage/services/intercompany/internal/domain"
	icevents "github.com/BenjaminSRussell/passage/services/intercompany/internal/events"
)

const uniqueViolation = "23505"

// Postgres is the pgx-backed repository.
type Postgres struct{ pool *pgxpool.Pool }

// New returns a repository over pool.
func New(pool *pgxpool.Pool) *Postgres { return &Postgres{pool: pool} }

// idempotent runs a command at most once per (tenant, Idempotency-Key). The
// key is claimed first, so a concurrent request with the same key waits on
// the primary key and then replays; the response is stored in the same
// transaction as the change. Reusing a key with a different request fails
// with IC-0841. replayed reports whether out is a stored response.
func idempotent[T any](ctx context.Context, tx pgx.Tx, tenantID, key, scope string, request any, run func() (T, error)) (out T, replayed bool, err error) {
	raw, err := json.Marshal(request)
	if err != nil {
		return out, false, err
	}
	sum := sha256.Sum256(append([]byte(scope+"\n"), raw...))
	hash := hex.EncodeToString(sum[:])
	tag, err := tx.Exec(ctx, `insert into idempotency_keys (tenant_id, key, request_hash, response_status) values ($1, $2, $3, 0)
		on conflict (tenant_id, key) do nothing`, tenantID, key, hash)
	if err != nil {
		return out, false, err
	}
	if tag.RowsAffected() == 0 {
		var stored string
		var body []byte
		if err := tx.QueryRow(ctx, `select request_hash, response_body from idempotency_keys where tenant_id = $1 and key = $2`, tenantID, key).Scan(&stored, &body); err != nil {
			return out, false, err
		}
		if stored != hash {
			return out, false, &domain.Error{Code: domain.CodeIdempotencyReuse, Detail: "Idempotency-Key was already used with a different request"}
		}
		if err := json.Unmarshal(body, &out); err != nil {
			return out, false, fmt.Errorf("replay %s: %w", key, err)
		}
		return out, true, nil
	}
	out, err = run()
	if err != nil {
		return out, false, err
	}
	body, err := json.Marshal(out)
	if err != nil {
		return out, false, err
	}
	_, err = tx.Exec(ctx, `update idempotency_keys set response_status = 201, response_body = $3 where tenant_id = $1 and key = $2`, tenantID, key, body)
	return out, false, err
}

// enqueue writes one event to the outbox inside tx.
func enqueue(ctx context.Context, tx pgx.Tx, aggregateType, aggregateID, eventType, tenantID string, data any) error {
	evt, err := events.New(eventType, icevents.Source, aggregateID, tenantID, data)
	if err != nil {
		return err
	}
	return outbox.Enqueue(ctx, tx, aggregateType, aggregateID, evt)
}
