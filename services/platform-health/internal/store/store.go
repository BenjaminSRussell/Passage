package store

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
)

const listLimit = 100

type Entry struct {
	ID        string    `json:"id"`
	Message   string    `json:"message"`
	CreatedAt time.Time `json:"createdAt"`
}

type Store interface {
	Create(ctx context.Context, message, idempotencyKey string) (Entry, error)
	List(ctx context.Context) ([]Entry, error)
	Ping(ctx context.Context) error
}

type Postgres struct{ pool *pgxpool.Pool }

func New(pool *pgxpool.Pool) *Postgres { return &Postgres{pool: pool} }

func (s *Postgres) Ping(ctx context.Context) error { return s.pool.Ping(ctx) }

func (s *Postgres) Create(ctx context.Context, message, idempotencyKey string) (Entry, error) {
	p, _ := tenant.FromContext(ctx)
	legalEntity := p.LegalEntityID
	if !ids.Valid(legalEntity) {
		legalEntity = ids.Nil
	}
	var key *string
	if idempotencyKey != "" {
		key = &idempotencyKey
	}
	var e Entry
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `insert into hello_entry (tenant_id, id, legal_entity_id, message, idempotency_key)
			values ($1, $2, $3, $4, $5)
			on conflict (tenant_id, idempotency_key) do update set message = hello_entry.message
			returning id, message, created_at`, p.TenantID, ids.New(), legalEntity, message, key).Scan(&e.ID, &e.Message, &e.CreatedAt)
	})
	return e, err
}

func (s *Postgres) List(ctx context.Context) ([]Entry, error) {
	p, _ := tenant.FromContext(ctx)
	entries := []Entry{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select id, message, created_at from hello_entry where tenant_id = $1 order by created_at desc, id desc limit $2`, p.TenantID, listLimit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var e Entry
			if err := rows.Scan(&e.ID, &e.Message, &e.CreatedAt); err != nil {
				return err
			}
			entries = append(entries, e)
		}
		return rows.Err()
	})
	return entries, err
}
