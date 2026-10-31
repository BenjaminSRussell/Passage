package store

import (
	"context"
	"net/http"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/warehouse/internal/domain"
	evts "github.com/BenjaminSRussell/passage/services/warehouse/internal/events"
)

// WaveInput is a wave release: the orders to pick and the replenishment rules.
type WaveInput struct {
	WarehouseID string
	Orders      []domain.WaveOrder
	Rules       []domain.ReplenishRule
}

// Released is the work a wave created. WaveID names the release in the answer
// only: waves are not persisted yet.
type Released struct {
	WaveID string `json:"waveId"`
	domain.WaveRelease
}

// ReleaseWave creates the pick work of every order and the replenishment the
// picks need in one transaction, from one read of the directives, locations and
// open lines (BR-31.2) taken under a per-warehouse advisory lock and share locks on the
// warehouse's locations, so concurrent releases never promise the same stock
// or room and no confirmation commits between the reads, and enqueues work.created for each work. It runs once
// per Idempotency-Key.
func (s *Store) ReleaseWave(ctx context.Context, idem Idem, in WaveInput) (Released, bool, error) {
	var (
		out      Released
		replayed bool
	)
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		var err error
		out, replayed, err = once(ctx, tx, idem, http.StatusCreated, func() (Released, error) {
			if _, err := tx.Exec(ctx, `select pg_advisory_xact_lock(hashtext($1))`, tenantID(ctx)+"/wave/"+in.WarehouseID); err != nil {
				return Released{}, err
			}
			if _, err := tx.Exec(ctx, `select id from location where warehouse_id = $1 order by id for share`, in.WarehouseID); err != nil {
				return Released{}, err
			}
			dirs, err := loadDirectives(ctx, tx, in.WarehouseID, "SALES_PICK", "")
			if err != nil {
				return Released{}, err
			}
			locs, err := loadLocations(ctx, tx, in.WarehouseID)
			if err != nil {
				return Released{}, err
			}
			open, err := openLines(ctx, tx, in.WarehouseID)
			if err != nil {
				return Released{}, err
			}
			domain.Reserve(locs, open)
			rel, err := domain.ReleaseWave(in.WarehouseID, ids.New, in.Orders, in.Rules, dirs, locs)
			if err != nil {
				return Released{}, err
			}
			for _, group := range [][]domain.Work{rel.Picks, rel.Replenishment} {
				for i := range group {
					if err := insertWork(ctx, tx, &group[i]); err != nil {
						return Released{}, err
					}
					if err := enqueue(ctx, tx, group[i].ID, evts.WorkCreated, createdData(group[i])); err != nil {
						return Released{}, err
					}
				}
			}
			return Released{WaveID: ids.New(), WaveRelease: rel}, nil
		})
		return err
	})
	return out, replayed, err
}
