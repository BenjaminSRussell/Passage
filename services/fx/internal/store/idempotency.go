package store

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
)

type IdempotencyRecord struct {
	Key            string
	RequestHash    string
	ResponseStatus int
	ResponseBody   json.RawMessage
}

const (
	CodeIdempotencyInProgress = "FX_IDEMPOTENCY_IN_PROGRESS"
	CodeIdempotencyConflict   = "COM-0409"
	lockNotAvailable          = "55P03"
	publishedStatus           = http.StatusCreated
	lockTimeout               = "5s"
)

func idempotencyRace(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && (pgErr.Code == lockNotAvailable || (pgErr.Code == uniqueViolation && strings.Contains(pgErr.ConstraintName, "idempotency"))) {
		return httpx.NewProblem(http.StatusConflict, CodeIdempotencyInProgress, "a request with this Idempotency-Key is still in progress; retry shortly")
	}
	return err
}

func (s *Postgres) PublishIdempotent(ctx context.Context, key, requestHash string, in []RateInput, render func([]Posted) (json.RawMessage, error)) (IdempotencyRecord, bool, error) {
	if key == "" {
		return IdempotencyRecord{}, false, invalid("idempotency key required")
	}
	if err := s.validateAll(in); err != nil {
		return IdempotencyRecord{}, false, err
	}
	p, _ := tenant.FromContext(ctx)
	rec := IdempotencyRecord{Key: key, RequestHash: requestHash, ResponseStatus: publishedStatus}
	replayed := false
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `set local lock_timeout = '`+lockTimeout+`'`); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `select pg_advisory_xact_lock(hashtextextended($1, 0))`, p.TenantID+"/"+key); err != nil {
			return err
		}
		var storedHash string
		err := tx.QueryRow(ctx, `select request_hash, response_status, response_body from idempotency_keys where tenant_id=$1 and key=$2`,
			p.TenantID, key).Scan(&storedHash, &rec.ResponseStatus, &rec.ResponseBody)
		if err == nil {
			if storedHash != requestHash {
				return httpx.NewProblem(http.StatusUnprocessableEntity, CodeIdempotencyConflict, "idempotency key reused with a different request")
			}
			replayed = true
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		posted, err := postRatesTx(ctx, tx, in)
		if err != nil {
			return err
		}
		if rec.ResponseBody, err = render(posted); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `insert into idempotency_keys (tenant_id, key, request_hash, response_status, response_body) values ($1,$2,$3,$4,$5)`,
			p.TenantID, key, requestHash, publishedStatus, rec.ResponseBody)
		return err
	})
	if err != nil {
		return IdempotencyRecord{}, false, conflict(idempotencyRace(err))
	}
	return rec, replayed, nil
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
