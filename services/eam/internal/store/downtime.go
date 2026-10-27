package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/eam/internal/domain"
	eamevents "github.com/BenjaminSRussell/passage/services/eam/internal/events"
)

// DowntimeWindow is a stop of a production resource booked on a work order (BR-44.5).
type DowntimeWindow struct {
	ID         string    `json:"id"`
	ResourceID string    `json:"resourceId"`
	From       time.Time `json:"from"`
	To         time.Time `json:"to"`
	Minutes    int       `json:"minutes"`
	RecordedBy *string   `json:"recordedBy,omitempty"`
}

// DowntimeInput is a downtime window to record. ResourceID may be empty: the
// asset's production resource is used then.
type DowntimeInput struct {
	ResourceID string
	From, To   time.Time
	Actor      string
}

// RecordDowntime books a window of production resource downtime on an open
// work order (BR-44.5). The window's whole minutes add to the order's
// downtime, and dev.passage.eam.downtime.recorded.v1 goes to the outbox in the
// same transaction so wave 23 can block the resource's capacity. Windows on
// one order must not overlap (EAM-DT-001). A repeated key returns the work
// order as it is now without a second booking.
func (s *Postgres) RecordDowntime(ctx context.Context, woID string, in DowntimeInput, key string) (WorkOrderDetail, error) {
	var out WorkOrderDetail
	minutes, err := domain.DowntimeMinutes(in.From, in.To)
	if err != nil {
		return out, problem(400, CodeValidation, err.Error())
	}
	err = s.withTx(ctx, func(tx pgx.Tx, t string) error {
		cur, err := getWorkOrder(ctx, tx, t, woID, true)
		if err != nil {
			return err
		}
		if key != "" {
			var owner string
			err := tx.QueryRow(ctx, `select work_order_id::text from downtime_window
			  where tenant_id = $1 and idempotency_key = $2`, t, key).Scan(&owner)
			if err == nil {
				if owner != woID {
					return problem(422, CodeKeyReuse, "Idempotency-Key was already used for downtime on another work order")
				}
				out, err = workOrderDetail(ctx, tx, t, cur)
				return err
			}
			if !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
		}
		if !domain.Status(cur.Status).Open() {
			return problem(422, CodeWOTransition, "downtime is recorded on open work orders; this one is "+cur.Status)
		}
		resource := in.ResourceID
		if resource == "" {
			asset, err := getAsset(ctx, tx, t, cur.AssetID, false)
			if err != nil {
				return err
			}
			if asset.ProductionResourceID == nil {
				return problem(422, CodeDowntime, "the asset has no production resource; give resourceId")
			}
			resource = *asset.ProductionResourceID
		}
		overlap, err := taken(ctx, tx, "downtime_window", `tenant_id = $1 and work_order_id = $2
		    and starts_at < $4 and ends_at > $3`, t, woID, in.From, in.To)
		if err != nil {
			return err
		}
		if overlap {
			return problem(422, CodeDowntime, "the window overlaps downtime already recorded on this work order")
		}
		id := ids.New()
		if _, err := tx.Exec(ctx, `insert into downtime_window (tenant_id, id, work_order_id, resource_id, starts_at,
		    ends_at, minutes, recorded_by, idempotency_key)
		  values ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
			t, id, woID, resource, in.From, in.To, minutes, optStr(in.Actor), optStr(key)); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `update work_order set downtime_minutes = downtime_minutes + $3, updated_at = now() where tenant_id = $1 and id = $2`, t, woID, minutes); err != nil {
			return err
		}
		if err := enqueue(ctx, tx, t, "workorder", woID, eamevents.DowntimeRecorded, eamevents.DowntimeRecordedData{
			DowntimeID: id, ResourceID: resource, WorkOrderID: woID, AssetID: cur.AssetID,
			From: in.From.UTC().Format(time.RFC3339Nano), To: in.To.UTC().Format(time.RFC3339Nano), Minutes: minutes}); err != nil {
			return err
		}
		w, err := getWorkOrder(ctx, tx, t, woID, false)
		if err != nil {
			return err
		}
		out, err = workOrderDetail(ctx, tx, t, w)
		return err
	})
	return out, err
}

// recordedDowntime is the sum of a work order's downtime windows in minutes.
func recordedDowntime(ctx context.Context, tx pgx.Tx, t, woID string) (int, error) {
	var n int
	err := tx.QueryRow(ctx, `select coalesce(sum(minutes), 0) from downtime_window where tenant_id = $1 and work_order_id = $2`,
		t, woID).Scan(&n)
	return n, err
}
