package store

import (
	"context"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"

	"github.com/BenjaminSRussell/passage/services/finreport/internal/domain"
)

// RunOutput returns a run, the exact definition version it was evaluated with
// and its cells, read in one transaction, so a file rendered from them is
// consistent. Retired versions still resolve: history stays printable (BR-15.8).
func (s *Postgres) RunOutput(ctx context.Context, runID string) (Run, domain.Definition, []domain.Cell, error) {
	var run Run
	var def domain.Definition
	var cells []domain.Cell
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		if run, err = getRunTx(ctx, tx, runID); err != nil {
			return err
		}
		if def, err = runDefinitionTx(ctx, tx, run); err != nil {
			return err
		}
		cells, err = listCellsTx(ctx, tx, runID)
		return err
	})
	return run, def, cells, err
}
