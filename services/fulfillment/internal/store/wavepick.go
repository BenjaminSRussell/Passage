package store

import (
	"context"
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/fulfillment/internal/domain"
)

// WavePickStore confirms the picks of a whole wave in one command (FR-28.13).
type WavePickStore interface {
	ConfirmWavePick(ctx context.Context, waveID string, picks []PickInput, pickRest bool, idem Idem) (WavePickResult, error)
}

// WavePickShipment is one shipment of the wave after the confirmation.
type WavePickShipment struct {
	ID             string                `json:"id"`
	ShipmentNumber string                `json:"shipmentNumber"`
	Status         domain.ShipmentStatus `json:"status"`
	Version        int                   `json:"version"`
	LinesConfirmed int                   `json:"linesConfirmed"`
}

// WavePickResult is the answer of a wave pick confirmation: the shipments that
// took picks, with their new status and version.
type WavePickResult struct {
	WaveID         string             `json:"waveId"`
	LinesConfirmed int                `json:"linesConfirmed"`
	ShortLines     int                `json:"shortLines"`
	Shipments      []WavePickShipment `json:"shipments"`
}

// ConfirmWavePick confirms the listed picks across the shipments of a wave in
// one transaction (all or nothing), writing one pick.confirmed per shipment that
// carries the wave id. Every listed line must belong to the wave (FUL-0041).
// With pickRest, every other line of the wave that is not yet confirmed is
// picked in full. Shipments of the wave that cannot be picked (picked, packed,
// cancelled) are left alone unless a listed line is on them, which fails with
// FUL-0001. Each shipment is locked before its lines are read, so what pickRest
// adds reflects the committed state and never overwrites a concurrent pick.
func (s *Postgres) ConfirmWavePick(ctx context.Context, waveID string, picks []PickInput, pickRest bool, idem Idem) (WavePickResult, error) {
	p, _ := tenant.FromContext(ctx)
	var out WavePickResult
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		res, err := runIdempotent(ctx, tx, p.TenantID, idem, func() (WavePickResult, error) {
			return confirmWavePickTx(ctx, tx, p.TenantID, waveID, picks, pickRest)
		})
		out = res
		return err
	})
	return out, err
}

func confirmWavePickTx(ctx context.Context, tx pgx.Tx, tenantID, waveID string, picks []PickInput, pickRest bool) (WavePickResult, error) {
	result := WavePickResult{WaveID: waveID, Shipments: []WavePickShipment{}}
	// FOR SHARE: a concurrent close (FOR UPDATE) waits for these picks, and
	// picks wait for a close in flight and then see the wave CLOSED.
	var number, status string
	if err := tx.QueryRow(ctx, `select wave_number, status from wave where id = $1 for share`, waveID).Scan(&number, &status); errors.Is(err, pgx.ErrNoRows) {
		return result, httpx.NewProblem(http.StatusNotFound, CodeWaveNotFound, "wave not found")
	} else if err != nil {
		return result, err
	}
	// A CLOSED wave is final; a short-picked remainder is re-picked on its
	// shipment, not through the closed wave.
	if status != "OPEN" {
		return result, httpx.NewProblem(http.StatusUnprocessableEntity, CodeStatus, "wave "+number+" is "+status+"; only an OPEN wave takes picks")
	}
	// The wave's lines, grouped by shipment in shipment-number order so that
	// concurrent wave picks lock shipments in the same order.
	rows, err := tx.Query(ctx, `select sl.id::text, sh.id::text
		from wave_line wl
		join shipment_line sl on sl.tenant_id = wl.tenant_id and sl.id = wl.shipment_line_id
		join shipment sh on sh.tenant_id = sl.tenant_id and sh.id = sl.shipment_id
		where wl.wave_id = $1 order by sh.shipment_number, sl.id`, waveID)
	if err != nil {
		return result, err
	}
	var order []string
	waveLines := map[string][]string{}
	shipmentOf := map[string]string{}
	for rows.Next() {
		var lineID, shipmentID string
		if err := rows.Scan(&lineID, &shipmentID); err != nil {
			rows.Close()
			return result, err
		}
		if _, seen := waveLines[shipmentID]; !seen {
			order = append(order, shipmentID)
		}
		waveLines[shipmentID] = append(waveLines[shipmentID], lineID)
		shipmentOf[lineID] = shipmentID
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return result, err
	}
	listed := map[string][]PickInput{}
	for _, pk := range picks {
		sid, ok := shipmentOf[pk.ShipmentLineID]
		if !ok {
			return result, httpx.NewProblem(http.StatusNotFound, CodeLineNotFound, "shipment line "+pk.ShipmentLineID+" is not in this wave")
		}
		listed[sid] = append(listed[sid], pk)
	}
	for _, sid := range order {
		if len(listed[sid]) == 0 && !pickRest {
			continue
		}
		if _, err := tx.Exec(ctx, `select 1 from shipment where id = $1 for update`, sid); err != nil {
			return result, err
		}
		sh, err := loadShipment(ctx, tx, sid)
		if err != nil {
			return result, err
		}
		todo := append([]PickInput(nil), listed[sid]...)
		if pickRest {
			if len(todo) == 0 && !pickable(sh.Status) {
				continue
			}
			named := map[string]bool{}
			for _, pk := range todo {
				named[pk.ShipmentLineID] = true
			}
			for _, lid := range waveLines[sid] {
				idx := lineIndex(sh, lid)
				if named[lid] || idx < 0 || sh.Lines[idx].PickConfirmed {
					continue
				}
				todo = append(todo, PickInput{ShipmentLineID: lid, Picked: sh.Lines[idx].Planned})
			}
		}
		if len(todo) == 0 {
			continue
		}
		if err := confirmPickTx(ctx, tx, tenantID, waveID, &sh, todo); err != nil {
			return result, err
		}
		for _, pk := range todo {
			l := sh.Lines[lineIndex(sh, pk.ShipmentLineID)]
			if l.Picked.LessThan(l.Planned) {
				result.ShortLines++
			}
		}
		result.LinesConfirmed += len(todo)
		result.Shipments = append(result.Shipments, WavePickShipment{ID: sid, ShipmentNumber: sh.ShipmentNumber,
			Status: sh.Status, Version: sh.Version, LinesConfirmed: len(todo)})
	}
	if len(result.Shipments) == 0 {
		return result, httpx.NewProblem(http.StatusUnprocessableEntity, CodeValidation, "the wave has no line left to pick")
	}
	return result, nil
}

// pickable reports whether a shipment in this status takes picks (FR-28.2,
// FR-28.8: a partially shipped shipment re-picks its remainder).
func pickable(st domain.ShipmentStatus) bool {
	return st == domain.StatusReleased || st == domain.StatusPicking || st == domain.StatusPartiallyShipped
}
