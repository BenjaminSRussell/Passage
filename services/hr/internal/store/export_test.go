package store

import "github.com/jackc/pgx/v5/pgxpool"

// Pool is the store's connection pool, for external tests that inspect rows directly.
func (s *Postgres) Pool() *pgxpool.Pool { return s.pool }
