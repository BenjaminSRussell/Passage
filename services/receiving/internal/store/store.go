package store

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
)

type Store interface {
	Ping(ctx context.Context) error
}

type Postgres struct{ pool *pgxpool.Pool }

func New(pool *pgxpool.Pool) *Postgres { return &Postgres{pool: pool} }

func (s *Postgres) Ping(ctx context.Context) error { return s.pool.Ping(ctx) }
