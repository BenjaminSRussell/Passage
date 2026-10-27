package store

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
)

// Idempotency problem codes (see README).
const (
	CodeIdempotencyReuse    = "idempotency_key_reuse"
	CodeIdempotencyConflict = "SF-0107"
)

// Idem carries an Idempotency-Key and a fingerprint of the request (method,
// path and body). A replay with a different fingerprint is refused (422).
type Idem struct{ Key, Fingerprint string }

// Replay is a stored command outcome returned for a repeated Idempotency-Key.
type Replay struct {
	Status int
	Body   json.RawMessage
}

// LookupReplay returns the stored outcome for idem, or nil when the key is new.
// A key already used with a different request is SF idempotency_key_reuse.
func (s *Postgres) LookupReplay(ctx context.Context, idem Idem) (*Replay, error) {
	var out *Replay
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		out, err = lookup(ctx, tx, idem)
		return err
	})
	return out, err
}

func lookup(ctx context.Context, tx pgx.Tx, idem Idem) (*Replay, error) {
	var hash string
	var r Replay
	err := tx.QueryRow(ctx, `select request_hash, response_status, response_body from idempotency_keys where key = $1`, idem.Key).
		Scan(&hash, &r.Status, &r.Body)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if hash != idem.Fingerprint {
		return nil, httpx.NewProblem(http.StatusUnprocessableEntity, CodeIdempotencyReuse, "Idempotency-Key was already used with a different request")
	}
	return &r, nil
}

// remember stores a command outcome under its key inside the command's
// transaction, so the record exists only if the change committed. A concurrent
// request holding the same key makes the insert fail with SF-0107.
func remember(ctx context.Context, tx pgx.Tx, idem Idem, status int, body any) error {
	raw, err := json.Marshal(body)
	if err != nil {
		return err
	}
	p, _ := tenant.FromContext(ctx)
	_, err = tx.Exec(ctx, `insert into idempotency_keys (tenant_id, key, request_hash, response_status, response_body) values ($1, $2, $3, $4, $5)`,
		p.TenantID, idem.Key, idem.Fingerprint, status, raw)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation {
		return httpx.NewProblem(http.StatusConflict, CodeIdempotencyConflict, "a concurrent request used the same Idempotency-Key; retry it")
	}
	return err
}
