package store

func WithFailpoint(s *Postgres, beforeCommit func() error) *Postgres {
	return &Postgres{pool: s.pool, beforeCommit: beforeCommit}
}
