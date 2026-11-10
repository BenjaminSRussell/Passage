// Package store persists flows, menus, devices, sessions and the offline
// transaction log in the mobile database. Every method runs in a transaction
// scoped to the caller's tenant (dbx.WithTenantTx), so row-level security
// applies. Events are written to the outbox in the same transaction as the
// change they describe, and commands record their Idempotency-Key and
// response in that transaction too.
package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	kernelevents "github.com/BenjaminSRussell/passage/platform/events"
	"github.com/BenjaminSRussell/passage/platform/outbox"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	evts "github.com/BenjaminSRussell/passage/services/mobile/internal/events"
)

const (
	MaxPageSize    = 200
	claimTTL       = time.Minute
	releaseTimeout = 5 * time.Second
)

var (
	ErrInProgress       = errors.New("store: a request with this Idempotency-Key is still running")
	ErrNotFound         = errors.New("store: not found")
	ErrConflict         = errors.New("store: conflict")
	ErrIdempotencyReuse = errors.New("store: idempotency key was used for another request")
	ErrVersionConflict  = errors.New("store: version conflict")
)

// Idem identifies one command for replay: the caller's Idempotency-Key and a
// hash of the request. A key is kept 24 hours.
type Idem struct {
	Key  string
	Hash string
}

type Store struct{ pool *pgxpool.Pool }

func New(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

func (s *Store) Ping(ctx context.Context) error { return s.pool.Ping(ctx) }

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

func tenantID(ctx context.Context) string {
	p, _ := tenant.FromContext(ctx)
	return p.TenantID
}

func userID(ctx context.Context) string {
	p, _ := tenant.FromContext(ctx)
	return p.UserID
}

// once runs fn at most once per Idempotency-Key, inside tx: a first call
// claims the key, runs fn and stores its result; a repeat with the same hash
// returns the stored result with replayed=true; a repeat with another hash is
// ErrIdempotencyReuse. When fn fails the transaction rolls back with the claim.
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

// Idempotent runs fn at most once per Idempotency-Key and stores its result,
// so a repeat returns the first answer without running fn again. fn may open
// transactions and call other services, so it runs holding no connection: the
// key is claimed in a short transaction (an in-progress marker), fn runs, and
// the result is stored in a second short one. A repeat while fn runs is
// ErrInProgress; a claim left by a crash is retryable after claimTTL; when fn
// fails the claim is released and the key can be retried.
func Idempotent[T any](ctx context.Context, s *Store, idem Idem, status int, fn func() (T, error)) (T, bool, error) {
	var zero, out T
	var claimed bool
	if err := s.inTx(ctx, func(tx pgx.Tx) (err error) {
		out, claimed, err = claim[T](ctx, tx, idem)
		return err
	}); err != nil {
		return zero, false, err
	}
	if !claimed {
		return out, true, nil
	}
	out, err := fn()
	if err == nil {
		err = s.storeResult(ctx, idem, status, out)
	}
	if err != nil {
		s.releaseClaim(ctx, idem)
		return zero, false, err
	}
	return out, false, nil
}

func claim[T any](ctx context.Context, tx pgx.Tx, idem Idem) (T, bool, error) {
	var zero T
	tid := tenantID(ctx)
	if _, err := tx.Exec(ctx, `delete from idempotency_keys where tenant_id = $1 and key = $2 and created_at < now() - interval '24 hours'`, tid, idem.Key); err != nil {
		return zero, false, err
	}
	tag, err := tx.Exec(ctx, `insert into idempotency_keys (tenant_id, key, request_hash, response_status) values ($1, $2, $3, 0)
		on conflict (tenant_id, key) do nothing`, tid, idem.Key, idem.Hash)
	if err != nil {
		return zero, false, err
	}
	if tag.RowsAffected() == 1 {
		return zero, true, nil
	}
	var hash string
	var status int
	var body []byte
	if err := tx.QueryRow(ctx, `select request_hash, response_status, response_body from idempotency_keys where tenant_id = $1 and key = $2`,
		tid, idem.Key).Scan(&hash, &status, &body); err != nil {
		return zero, false, err
	}
	if hash != idem.Hash {
		return zero, false, ErrIdempotencyReuse
	}
	if status == 0 {
		taken, err := tx.Exec(ctx, `update idempotency_keys set created_at = now()
			where tenant_id = $1 and key = $2 and response_status = 0 and created_at < now() - $3 * interval '1 second'`, tid, idem.Key, int(claimTTL.Seconds()))
		if err != nil {
			return zero, false, err
		}
		if taken.RowsAffected() == 0 {
			return zero, false, ErrInProgress
		}
		return zero, true, nil
	}
	var out T
	if err := json.Unmarshal(body, &out); err != nil {
		return zero, false, fmt.Errorf("decode stored response: %w", err)
	}
	return out, false, nil
}

func (s *Store) storeResult(ctx context.Context, idem Idem, status int, out any) error {
	body, err := json.Marshal(out)
	if err != nil {
		return err
	}
	return s.inTx(ctx, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `update idempotency_keys set response_status = $3, response_body = $4 where tenant_id = $1 and key = $2`,
			tenantID(ctx), idem.Key, status, body)
		return err
	})
}

func (s *Store) releaseClaim(ctx context.Context, idem Idem) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), releaseTimeout)
	defer cancel()
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `delete from idempotency_keys where tenant_id = $1 and key = $2 and response_status = 0`, tenantID(ctx), idem.Key)
		return err
	})
	if err != nil {
		slog.WarnContext(ctx, "idempotency claim not released; it expires on its own", "key", idem.Key, "err", err)
	}
}

func enqueue(ctx context.Context, tx pgx.Tx, aggregate, id, typ string, data any) error {
	evt, err := kernelevents.New(typ, evts.Source, id, tenantID(ctx), data)
	if err != nil {
		return err
	}
	return outbox.Enqueue(ctx, tx, aggregate, id, evt)
}

type scanner interface {
	Scan(dest ...any) error
}
