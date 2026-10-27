// Package store persists locations, directives and work in the warehouse
// database. Every method runs in a transaction scoped to the caller's tenant
// (dbx.WithTenantTx), so row-level security applies. Events are written to the
// outbox in the same transaction as the change they describe, and commands
// record their Idempotency-Key and response in that transaction too.
package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	kernelevents "github.com/BenjaminSRussell/passage/platform/events"
	"github.com/BenjaminSRussell/passage/platform/outbox"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	evts "github.com/BenjaminSRussell/passage/services/warehouse/internal/events"
)

// MaxPageSize caps every list page.
const MaxPageSize = 200

var (
	// ErrNotFound is returned when a row does not exist in the caller's tenant.
	ErrNotFound = errors.New("store: not found")
	// ErrConflict is a unique violation: a duplicate location name or a
	// duplicate directive sequence for the same work type and direction.
	ErrConflict = errors.New("store: conflict")
	// ErrIdempotencyReuse is an Idempotency-Key replayed with another request.
	ErrIdempotencyReuse = errors.New("store: idempotency key was used for another request")
	// ErrVersionConflict is an If-Match version that is not the current one.
	ErrVersionConflict = errors.New("store: version conflict")
)

// Idem identifies one command for replay: the caller's Idempotency-Key and a
// hash of the request (method, path, user and body). A key is kept 24 hours.
type Idem struct {
	Key  string
	Hash string
}

// Store is the Postgres repository of the warehouse context.
type Store struct {
	pool *pgxpool.Pool
}

// New returns a store on pool.
func New(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

// Ping checks the database for /readyz.
func (s *Store) Ping(ctx context.Context) error { return s.pool.Ping(ctx) }

// inTx runs fn in a tenant-scoped transaction and maps database errors to the
// store's sentinels. Other errors from fn (domain errors) pass through.
func (s *Store) inTx(ctx context.Context, fn func(tx pgx.Tx) error) error {
	return mapErr(dbx.WithTenantTx(ctx, s.pool, fn))
}

func mapErr(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	var pe *pgconn.PgError
	if errors.As(err, &pe) && pe.Code == "23505" {
		return ErrConflict
	}
	return err
}

// tenantID returns the caller's tenant, which WithTenantTx has already checked.
func tenantID(ctx context.Context) string {
	p, _ := tenant.FromContext(ctx)
	return p.TenantID
}

// once runs fn at most once per Idempotency-Key, inside tx. A first call
// claims the key, runs fn and stores its result; a repeat with the same
// request hash returns the stored result with replayed=true and runs nothing;
// a repeat with another hash is ErrIdempotencyReuse. A concurrent repeat waits
// on the claim until the first transaction ends. When fn fails the whole
// transaction rolls back, the claim with it, so the caller may retry the key.
func once[T any](ctx context.Context, tx pgx.Tx, idem Idem, status int, fn func() (T, error)) (T, bool, error) {
	var zero T
	if _, err := tx.Exec(ctx, `delete from idempotency_keys where tenant_id = $1 and key = $2 and created_at < now() - interval '24 hours'`,
		tenantID(ctx), idem.Key); err != nil {
		return zero, false, err
	}
	tag, err := tx.Exec(ctx, `insert into idempotency_keys (tenant_id, key, request_hash, response_status) values ($1, $2, $3, 0)
		on conflict (tenant_id, key) do nothing`, tenantID(ctx), idem.Key, idem.Hash)
	if err != nil {
		return zero, false, err
	}
	if tag.RowsAffected() == 0 {
		var hash string
		var body []byte
		if err := tx.QueryRow(ctx, `select request_hash, response_body from idempotency_keys where tenant_id = $1 and key = $2`,
			tenantID(ctx), idem.Key).Scan(&hash, &body); err != nil {
			return zero, false, err
		}
		if hash != idem.Hash {
			return zero, false, ErrIdempotencyReuse
		}
		var out T
		if err := json.Unmarshal(body, &out); err != nil {
			return zero, false, fmt.Errorf("decode stored response: %w", err)
		}
		return out, true, nil
	}
	out, err := fn()
	if err != nil {
		return zero, false, err
	}
	body, err := json.Marshal(out)
	if err != nil {
		return zero, false, err
	}
	if _, err := tx.Exec(ctx, `update idempotency_keys set response_status = $3, response_body = $4 where tenant_id = $1 and key = $2`,
		tenantID(ctx), idem.Key, status, body); err != nil {
		return zero, false, err
	}
	return out, false, nil
}

// enqueue writes one work event into the outbox within tx.
func enqueue(ctx context.Context, tx pgx.Tx, workID, typ string, data any) error {
	evt, err := kernelevents.New(typ, evts.Source, workID, tenantID(ctx), data)
	if err != nil {
		return err
	}
	return outbox.Enqueue(ctx, tx, "work", workID, evt)
}

// dec parses a numeric column read as text.
func dec(s string) (decimal.Decimal, error) { return decimal.NewFromString(s) }

// decPtr parses a nullable numeric column read as text.
func decPtr(s *string) (*decimal.Decimal, error) {
	if s == nil {
		return nil, nil
	}
	d, err := dec(*s)
	return &d, err
}

// nullable maps an empty string to SQL NULL.
func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

type scanner interface {
	Scan(dest ...any) error
}
