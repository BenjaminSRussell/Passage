package store

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	fulevents "github.com/BenjaminSRussell/passage/services/fulfillment/internal/events"
)

// CodeWaveOpenLines is returned when a wave is closed while a line of a live
// shipment is not picked yet.
const CodeWaveOpenLines = "FUL-0055"

// MaxPickerLen is the longest picker name (a user name or badge) a wave takes.
const MaxPickerLen = 64

// lockWave reads and locks a wave and checks the If-Match version.
func lockWave(ctx context.Context, tx pgx.Tx, id string, version int) (WaveSummary, error) {
	sum, err := scanWave(tx.QueryRow(ctx, `select `+waveColumns+` from wave where id = $1 for update`, id))
	if err == pgx.ErrNoRows {
		return sum, httpx.NewProblem(http.StatusNotFound, CodeWaveNotFound, "wave not found")
	}
	if err != nil {
		return sum, err
	}
	if sum.Version != version {
		return sum, httpx.NewProblem(http.StatusPreconditionFailed, CodeVersion, fmt.Sprintf("wave is at version %d, request sent %d", sum.Version, version))
	}
	return sum, nil
}

// AssignWave gives an OPEN wave to a picker, or hands it to another one
// (FR-28.13, section 5.28.6). The wave version is checked and bumped and
// wave.assigned carries the previous picker, in the same transaction.
func (s *Postgres) AssignWave(ctx context.Context, id string, version int, picker string, idem Idem) (Wave, error) {
	p, _ := tenant.FromContext(ctx)
	picker = strings.TrimSpace(picker)
	var out Wave
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		res, err := runIdempotent(ctx, tx, p.TenantID, idem, func() (Wave, error) {
			sum, err := lockWave(ctx, tx, id, version)
			if err != nil {
				return Wave{}, err
			}
			if sum.Status != "OPEN" {
				return Wave{}, httpx.NewProblem(http.StatusUnprocessableEntity, CodeStatus, "wave "+sum.WaveNumber+" is "+sum.Status+"; only an OPEN wave can be assigned")
			}
			if sum.Picker != nil && *sum.Picker == picker {
				return Wave{}, httpx.NewProblem(http.StatusUnprocessableEntity, CodeValidation, "wave "+sum.WaveNumber+" is already assigned to "+picker)
			}
			if _, err := tx.Exec(ctx, `update wave set picker = $2, assigned_at = now(), version = version + 1, updated_at = now() where id = $1`, id, picker); err != nil {
				return Wave{}, err
			}
			prev := ""
			if sum.Picker != nil {
				prev = *sum.Picker
			}
			if err := enqueueFor(ctx, tx, "wave", id, fulevents.WaveAssigned, p.TenantID, fulevents.WaveAssignedData{
				WaveID: id, WaveNumber: sum.WaveNumber, LegalEntityID: sum.LegalEntityID, WarehouseID: sum.WarehouseID,
				Picker: picker, PreviousPicker: prev,
			}); err != nil {
				return Wave{}, err
			}
			return getWaveTx(ctx, tx, id)
		})
		out = res
		return err
	})
	return out, err
}

// CloseWave closes an OPEN wave once every line of it is picked or sits on a
// cancelled shipment; otherwise it answers FUL-0055 with the count left.
func (s *Postgres) CloseWave(ctx context.Context, id string, version int, idem Idem) (Wave, error) {
	p, _ := tenant.FromContext(ctx)
	var out Wave
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		res, err := runIdempotent(ctx, tx, p.TenantID, idem, func() (Wave, error) {
			sum, err := lockWave(ctx, tx, id, version)
			if err != nil {
				return Wave{}, err
			}
			if sum.Status != "OPEN" {
				return Wave{}, httpx.NewProblem(http.StatusUnprocessableEntity, CodeStatus, "wave "+sum.WaveNumber+" is already "+sum.Status)
			}
			var open, short int
			if err := tx.QueryRow(ctx, `select
					count(*) filter (where not sl.pick_confirmed and s.status <> 'CANCELLED'),
					count(*) filter (where sl.pick_confirmed and sl.qty_picked < sl.qty_planned and s.status <> 'CANCELLED')
				from wave_line wl
				join shipment_line sl on sl.tenant_id = wl.tenant_id and sl.id = wl.shipment_line_id
				join shipment s on s.tenant_id = sl.tenant_id and s.id = sl.shipment_id
				where wl.wave_id = $1`, id).Scan(&open, &short); err != nil {
				return Wave{}, err
			}
			if open > 0 {
				return Wave{}, httpx.NewProblem(http.StatusUnprocessableEntity, CodeWaveOpenLines,
					fmt.Sprintf("wave %s still has %d line(s) to pick", sum.WaveNumber, open))
			}
			if _, err := tx.Exec(ctx, `update wave set status = 'CLOSED', closed_at = now(), version = version + 1, updated_at = now() where id = $1`, id); err != nil {
				return Wave{}, err
			}
			shipmentIDs := []string{}
			rows, err := tx.Query(ctx, `select shipment_id::text from wave_shipment where wave_id = $1 order by shipment_id`, id)
			if err != nil {
				return Wave{}, err
			}
			for rows.Next() {
				var sid string
				if err := rows.Scan(&sid); err != nil {
					rows.Close()
					return Wave{}, err
				}
				shipmentIDs = append(shipmentIDs, sid)
			}
			rows.Close()
			if err := rows.Err(); err != nil {
				return Wave{}, err
			}
			picker := ""
			if sum.Picker != nil {
				picker = *sum.Picker
			}
			if err := enqueueFor(ctx, tx, "wave", id, fulevents.WaveClosed, p.TenantID, fulevents.WaveClosedData{
				WaveID: id, WaveNumber: sum.WaveNumber, LegalEntityID: sum.LegalEntityID, WarehouseID: sum.WarehouseID,
				Picker: picker, ShipmentIDs: shipmentIDs, LineCount: sum.LineCount, ShortLines: short,
			}); err != nil {
				return Wave{}, err
			}
			return getWaveTx(ctx, tx, id)
		})
		out = res
		return err
	})
	return out, err
}
