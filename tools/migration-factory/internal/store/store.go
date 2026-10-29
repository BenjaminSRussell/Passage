// Package store keeps the migration factory's state in Postgres: pack runs with their control
// totals and quarantine, the append-only legacy cross-reference, and cutover plans with their
// steps. Every call runs in a transaction scoped to the caller's tenant (dbx.WithTenantTx), so
// row-level security holds each tenant to its own rows, and every state change other contexts
// care about is written to the outbox in the same transaction.
package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/events"
	"github.com/BenjaminSRussell/passage/platform/outbox"
	"github.com/BenjaminSRussell/passage/platform/tenant"

	mfevents "github.com/BenjaminSRussell/passage/tools/migration-factory/internal/events"
)

var (
	// ErrNotFound is returned when a run or plan does not exist for the caller's tenant.
	ErrNotFound = errors.New("not found")
	// ErrVersion is returned when the caller's expected plan version is not the stored one.
	ErrVersion = errors.New("version conflict")
	// ErrIdempotencyReuse is returned when an Idempotency-Key is replayed with a different request.
	ErrIdempotencyReuse = errors.New("idempotency key reused with a different request")
)

// MaxListLimit caps list pages.
const MaxListLimit = 200

// Postgres is the store.
type Postgres struct{ pool *pgxpool.Pool }

// New returns a store over pool.
func New(pool *pgxpool.Pool) *Postgres { return &Postgres{pool: pool} }

// Ping checks the database connection.
func (s *Postgres) Ping(ctx context.Context) error { return s.pool.Ping(ctx) }

// Idem carries a command's Idempotency-Key header and the hash of its request. An empty Key
// runs the command without replay protection.
type Idem struct {
	Key  string
	Hash string
}

// HashRequest returns the sha256 of method, path and body, the request identity an
// Idempotency-Key is bound to.
func HashRequest(method, path string, body []byte) string {
	h := sha256.New()
	for _, part := range [][]byte{[]byte(method), []byte(path), body} {
		h.Write(part)
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// idempotent runs fn once per (tenant, key). A repeated key with the same request returns the
// stored result and replayed=true; with a different request it fails with ErrIdempotencyReuse.
// The transaction-scoped advisory lock serializes two requests racing on one key, so the
// second waits and then replays the first's committed result.
func idempotent[T any](ctx context.Context, tx pgx.Tx, idem Idem, status int, fn func() (T, error)) (T, bool, error) {
	var zero T
	if idem.Key == "" {
		v, err := fn()
		return v, false, err
	}
	if _, err := tx.Exec(ctx, `select pg_advisory_xact_lock(hashtextextended('idem|' || current_setting('passage.tenant_id', true) || '|' || $1, 0))`, idem.Key); err != nil {
		return zero, false, fmt.Errorf("lock idempotency key: %w", err)
	}
	var storedHash string
	var body []byte
	err := tx.QueryRow(ctx, `select request_hash, response_body from idempotency_keys where tenant_id = current_setting('passage.tenant_id', true)::uuid and key = $1`, idem.Key).Scan(&storedHash, &body)
	switch {
	case err == nil:
		if storedHash != idem.Hash {
			return zero, false, ErrIdempotencyReuse
		}
		var v T
		if err := json.Unmarshal(body, &v); err != nil {
			return zero, false, fmt.Errorf("decode stored response: %w", err)
		}
		return v, true, nil
	case !errors.Is(err, pgx.ErrNoRows):
		return zero, false, fmt.Errorf("read idempotency key: %w", err)
	}
	v, err := fn()
	if err != nil {
		return zero, false, err
	}
	encoded, err := json.Marshal(v)
	if err != nil {
		return zero, false, fmt.Errorf("encode response: %w", err)
	}
	if _, err := tx.Exec(ctx, `insert into idempotency_keys (tenant_id, key, request_hash, response_status, response_body)
		values (current_setting('passage.tenant_id', true)::uuid, $1, $2, $3, $4::jsonb)`, idem.Key, idem.Hash, status, string(encoded)); err != nil {
		return zero, false, fmt.Errorf("store idempotency key: %w", err)
	}
	return v, false, nil
}

// enqueue writes one event for the caller's tenant to the outbox inside tx.
func enqueue(ctx context.Context, tx pgx.Tx, typ, aggregateType, aggregateID string, data any) error {
	p, ok := tenant.FromContext(ctx)
	if !ok {
		return errors.New("store: no principal in context")
	}
	evt, err := events.New(typ, mfevents.Source, aggregateID, p.TenantID, data)
	if err != nil {
		return err
	}
	evt.Actor = p.UserID
	return outbox.Enqueue(ctx, tx, aggregateType, aggregateID, evt)
}

func (s *Postgres) tx(ctx context.Context, fn func(tx pgx.Tx) error) error {
	return dbx.WithTenantTx(ctx, s.pool, fn)
}

func clampLimit(n int) int {
	if n <= 0 {
		return 50
	}
	if n > MaxListLimit {
		return MaxListLimit
	}
	return n
}
