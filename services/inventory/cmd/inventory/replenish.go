package main

import (
	"context"
	"log/slog"
	"time"

	"github.com/BenjaminSRussell/passage/services/inventory/internal/domain"
)

const (
	replenishActor           = "system:inventory-truck-replenish"
	defaultReplenishInterval = 24 * time.Hour
)

// truckReplenisher is the store side the nightly run drives.
type truckReplenisher interface {
	TruckLegalEntities(ctx context.Context) ([]string, error)
	ReplenishTrucks(ctx context.Context, legalEntityID, siteID, warehouseID string, shipDate time.Time) ([]domain.TransferOrder, error)
}

// replenishJob creates the draft replenishment transfer orders of every truck below its minimum (BR-45.7) on a schedule, so a
// truck gets restocked even when no completed work order triggered it (a lowered minimum, a transfer that was cancelled). It
// is off until INV_TRUCK_REPLENISH_TENANT_IDS names tenants; INV_TRUCK_REPLENISH_AT ("HH:MM" UTC, the nightly run) or
// INV_TRUCK_REPLENISH_INTERVAL sets when (default once a day). Several replicas may run it: a truck is locked while it is
// replenished and its draft counts as inbound stock, so no truck gets a second draft.
type replenishJob struct {
	sched   schedule
	tenants []string
	store   truckReplenisher
	now     func() time.Time
}

func newReplenishJob(getenv func(string) string, s truckReplenisher) (*replenishJob, error) {
	tenants, err := parseTenants("INV_TRUCK_REPLENISH_TENANT_IDS", getenv("INV_TRUCK_REPLENISH_TENANT_IDS"))
	if err != nil {
		return nil, err
	}
	if len(tenants) == 0 {
		slog.Info("inventory truck replenishment job disabled; INV_TRUCK_REPLENISH_TENANT_IDS is not set")
		return nil, nil
	}
	sched, err := parseSchedule(getenv, "INV_TRUCK_REPLENISH", defaultReplenishInterval)
	if err != nil {
		return nil, err
	}
	return &replenishJob{sched: sched, tenants: tenants, store: s, now: time.Now}, nil
}

func (j *replenishJob) loop(ctx context.Context) { j.sched.run(ctx, j.once) }

func (j *replenishJob) once(ctx context.Context) {
	shipDate := j.now().UTC().Truncate(24 * time.Hour)
	forEachEntity(ctx, replenishActor, j.tenants, j.store.TruckLegalEntities, func(ctx context.Context, tenantID, le string) error {
		orders, err := j.store.ReplenishTrucks(ctx, le, "", "", shipDate)
		if len(orders) > 0 {
			slog.Info("inventory truck replenishment finished", "tenantId", tenantID, "legalEntityId", le, "orders", len(orders))
		}
		return err
	})
}
