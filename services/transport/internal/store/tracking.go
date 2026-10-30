package store

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/events"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/outbox"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/transport/internal/domain"
	tmsevents "github.com/BenjaminSRussell/passage/services/transport/internal/events"
)

// TimelineLimit caps the events returned for one load's timeline.
const TimelineLimit = 5000

const trackingColumns = `id, load_id, stop_no, code, occurred_at, location, source, ref, created_at`

// TrackingFilter narrows the tracking-event list. With LoadID the result is that
// load's timeline in occurred_at order; without it, the newest reports received.
type TrackingFilter struct {
	LoadID string
}

// RecordTrackingEvent stores one status report (FR-34.6). Source and ref are the
// idempotency key (§6.34.5): the same report again returns the stored event and
// false and writes no outbox row; a different report under the same source and
// ref is refused with idempotency_key_reuse. The tracking.event outbox row is
// written in the same transaction as the insert.
func (s *Postgres) RecordTrackingEvent(ctx context.Context, in domain.TrackingEvent) (domain.TrackingEvent, bool, error) {
	p, _ := tenant.FromContext(ctx)
	var out domain.TrackingEvent
	inserted := false
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := parentEntityTx(ctx, tx, parentLoad, p.TenantID, in.LoadID, ""); err != nil {
			return err
		}
		saved, err := scanTracking(tx.QueryRow(ctx, `insert into tracking_event (tenant_id, id, load_id, source, ref, stop_no, code, occurred_at, location)
			values ($1, $2, $3, $4, $5, $6, $7, $8, $9)
			on conflict (tenant_id, source, ref) do nothing returning `+trackingColumns,
			p.TenantID, ids.New(), in.LoadID, in.Source, in.Ref, in.StopNo, in.Code, in.OccurredAt, in.Location))
		if errors.Is(err, pgx.ErrNoRows) {
			existing, err := scanTracking(tx.QueryRow(ctx, `select `+trackingColumns+` from tracking_event where tenant_id = $1 and source = $2 and ref = $3`,
				p.TenantID, in.Source, in.Ref))
			if err != nil {
				return err
			}
			if !existing.SameReport(in) {
				return httpx.NewProblem(http.StatusUnprocessableEntity, CodeIdempotencyReuse,
					"source "+in.Source+" and ref "+in.Ref+" were already used for a different status report")
			}
			out = existing
			return nil
		}
		if err != nil {
			return err
		}
		out, inserted = saved, true
		evt, err := events.New(tmsevents.TrackingEventRecorded, eventSource, in.LoadID, p.TenantID, tmsevents.TrackingPayload{
			LoadID: in.LoadID, StopNo: in.StopNo, Code: in.Code, OccurredAt: in.OccurredAt.UTC().Format(time.RFC3339Nano),
			Location: in.Location, Source: in.Source,
		})
		if err != nil {
			return err
		}
		return outbox.Enqueue(ctx, tx, "tracking", in.LoadID, evt)
	})
	return out, inserted, err
}

// ListTrackingEvents returns one load's events in occurred_at order (ties by
// source and ref, the order ProjectStops applies), or, without a load, the
// newest reports received across loads.
func (s *Postgres) ListTrackingEvents(ctx context.Context, f TrackingFilter) ([]domain.TrackingEvent, error) {
	p, _ := tenant.FromContext(ctx)
	out := []domain.TrackingEvent{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var rows pgx.Rows
		var err error
		if f.LoadID != "" {
			rows, err = tx.Query(ctx, `select `+trackingColumns+` from tracking_event where tenant_id = $1 and load_id = $2
				order by occurred_at, source, ref limit $3`, p.TenantID, f.LoadID, TimelineLimit)
		} else {
			rows, err = tx.Query(ctx, `select `+trackingColumns+` from tracking_event where tenant_id = $1
				order by created_at desc, id limit $2`, p.TenantID, ListLimit)
		}
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			e, err := scanTracking(rows)
			if err != nil {
				return err
			}
			out = append(out, e)
		}
		return rows.Err()
	})
	return out, err
}

func scanTracking(row pgx.Row) (domain.TrackingEvent, error) {
	var e domain.TrackingEvent
	if err := row.Scan(&e.ID, &e.LoadID, &e.StopNo, &e.Code, &e.OccurredAt, &e.Location, &e.Source, &e.Ref, &e.ReceivedAt); err != nil {
		return domain.TrackingEvent{}, err
	}
	e.OccurredAt, e.ReceivedAt = e.OccurredAt.UTC(), e.ReceivedAt.UTC()
	return e, nil
}
