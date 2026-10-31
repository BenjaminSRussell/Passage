package store

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BenjaminSRussell/passage/platform/fieldcrypt"
)

type Postgres struct {
	pool         *pgxpool.Pool
	beforeCommit func() error
	keys         *fieldcrypt.Keys
}

func New(pool *pgxpool.Pool) *Postgres { return &Postgres{pool: pool} }

func (s *Postgres) Ping(ctx context.Context) error { return s.pool.Ping(ctx) }
