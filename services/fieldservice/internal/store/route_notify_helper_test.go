package store

import (
	"context"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
)

// notifyForTest raises the route notification of a technician in its own transaction.
func (s *Postgres) notifyForTest(ctx context.Context, technicianID string) error {
	return dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		return notifyRoute(ctx, tx, tenantOf(ctx), &technicianID)
	})
}
