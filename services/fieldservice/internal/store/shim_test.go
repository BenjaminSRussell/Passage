package store

import (
	"context"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/fieldservice/internal/domain"
)

// moveWorkOrder walks a fixture along the status machine without the guards of the
// public TransitionWorkOrder (SCHEDULED and DISPATCHED by hand, the signature). A move
// to COMPLETED also logs the consumption of every PART line, as a technician's device
// would, so fixtures can post (SVC-WO-002).
func (s *Postgres) moveWorkOrder(ctx context.Context, id string, version int, to domain.WOStatus) (WorkOrder, error) {
	if to == domain.WOCompleted {
		if err := s.fixtureConsumeParts(ctx, id); err != nil {
			return WorkOrder{}, err
		}
	}
	return s.move(ctx, id, version, to, false)
}

func (s *Postgres) fixtureConsumeParts(ctx context.Context, id string) error {
	wo, err := s.GetWorkOrder(ctx, id)
	if err != nil {
		return err
	}
	var parts []domain.WorkLine
	for _, l := range wo.Lines {
		if l.Kind == domain.KindPart {
			parts = append(parts, l)
		}
	}
	if len(parts) == 0 {
		return nil
	}
	tech, _, err := s.CreateTechnician(ctx, Technician{Code: "F" + strings.ToUpper(strings.ReplaceAll(ids.New(), "-", "")[:12]), Name: "Fixture", LegalEntityID: wo.LegalEntityID,
		TerritoryIDs: []string{wo.TerritoryID}, TimeZone: "UTC", ShiftDays: []int{1}, ShiftStartMinute: 480, ShiftEndMinute: 1020, MaxDailyMinutes: 480, Active: true}, "", "")
	if err != nil {
		return err
	}
	return dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		for _, l := range parts {
			if _, err := tx.Exec(ctx, `insert into service_wo_action (tenant_id, id, work_order_id, technician_id, legal_entity_id, kind, device_time, payload, outcome)
				values ($1, $2, $3, $4, $5, 'PART_USED', $6, jsonb_build_object('lineId', $7::text, 'qty', $8::text), 'APPLIED')`,
				tenantOf(ctx), ids.New(), id, tech.ID, wo.LegalEntityID, time.Now().UTC(), l.ID, l.Qty.String()); err != nil {
				return err
			}
		}
		return nil
	})
}
