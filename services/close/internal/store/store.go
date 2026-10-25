package store

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/services/close/internal/domain"
)

type Postgres struct{ pool *pgxpool.Pool }

func New(pool *pgxpool.Pool) *Postgres { return &Postgres{pool: pool} }

func (s *Postgres) Ping(ctx context.Context) error { return s.pool.Ping(ctx) }

func (s *Postgres) PeriodRows(ctx context.Context, ledgerID, periodID string) ([]domain.Row, error) {
	var rows []domain.Row
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		found, err := tx.Query(ctx, `select module, status, coalesce(allowed_group_id, '') from period_status where ledger_id = $1 and fiscal_period_id = $2 for share`, ledgerID, periodID)
		if err != nil {
			return err
		}
		defer found.Close()
		for found.Next() {
			var module, status, group string
			if err := found.Scan(&module, &status, &group); err != nil {
				return err
			}
			rows = append(rows, domain.Row{Module: domain.Module(module), Status: domain.Status(status), AllowedGroupID: group})
		}
		return found.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("read period status: %w", err)
	}
	return rows, nil
}
